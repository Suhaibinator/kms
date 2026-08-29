package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func TestReadReleaseDefinitionIsStrictAndBuildsExactSelectors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.yaml")
	definition := `
namespace: prod/app
name: runtime
schema_id: app/runtime
schema_version: 2
entries:
  - alias: settings
    kind: parameter
    key: config/settings
    label: current
  - alias: password
    kind: secret
    key: /shared/data/db-password
    version: 7
`
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	cli := &CLI{}
	parsed, err := cli.readReleaseDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	req, err := releaseCreateRequest(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetNamespace().GetEnv() != "prod" || req.GetNamespace().GetApp() != "app" || req.GetSchemaVersion() != 2 {
		t.Fatalf("request identity = %#v", req)
	}
	if got := displayPath(req.GetEntries()[0].GetRef()); got != "/prod/app/config/settings" {
		t.Fatalf("relative entry path = %q", got)
	}
	if req.GetEntries()[0].GetLabel() != "current" || req.GetEntries()[0].GetVersion() != 0 {
		t.Fatalf("label selector = %#v", req.GetEntries()[0])
	}
	if got := displayPath(req.GetEntries()[1].GetRef()); got != "/shared/data/db-password" {
		t.Fatalf("absolute entry path = %q", got)
	}

	unknown := filepath.Join(dir, "unknown.yaml")
	if err := os.WriteFile(unknown, []byte("namespace: prod/app\nname: runtime\nunknown_field: true\nentries: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.readReleaseDefinition(unknown); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestReleaseCreateRequestRejectsAmbiguousAndDuplicateEntries(t *testing.T) {
	_, err := releaseCreateRequest(releaseDefinition{
		Namespace: "prod/app", Name: "runtime",
		Entries: []releaseEntryDefinition{
			{Alias: "x", Kind: "parameter", Key: "a", Version: 1, Label: "current"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "version or label") {
		t.Fatalf("ambiguous selector error = %v", err)
	}
	_, err = releaseCreateRequest(releaseDefinition{
		Namespace: "prod/app", Name: "runtime",
		Entries: []releaseEntryDefinition{
			{Alias: "x", Kind: "parameter", Key: "a", Version: 1},
			{Alias: "x", Kind: "secret", Key: "b", Version: 2},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("duplicate alias error = %v", err)
	}
}

func TestPrintReleaseDiffNeverRendersSecretMaterial(t *testing.T) {
	secretRef := &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "password"}
	parameterRef := &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "settings"}
	from := &kmsv1.ConfigurationRelease{Entries: []*kmsv1.ConfigurationReleaseEntry{
		{Alias: "password", Kind: "secret", Ref: secretRef, Version: 1, MetadataJson: `{"do_not_print":"secret-plaintext"}`},
		{Alias: "settings", Kind: "parameter", Ref: parameterRef, Version: 1, ParameterDigest: "digest-one"},
	}}
	to := &kmsv1.ConfigurationRelease{Entries: []*kmsv1.ConfigurationReleaseEntry{
		{Alias: "password", Kind: "secret", Ref: secretRef, Version: 2, MetadataJson: `{"do_not_print":"new-secret-plaintext"}`},
		{Alias: "settings", Kind: "parameter", Ref: parameterRef, Version: 2, ParameterDigest: "digest-two"},
	}}
	var output bytes.Buffer
	printReleaseDiff(&output, from, to)
	text := output.String()
	if strings.Contains(text, "secret-plaintext") || strings.Contains(text, "do_not_print") {
		t.Fatalf("diff leaked secret metadata/value: %s", text)
	}
	for _, want := range []string{"password", "1 -> 2", "digest-one -> digest-two"} {
		if !strings.Contains(text, want) {
			t.Fatalf("diff missing %q: %s", want, text)
		}
	}
}

func TestOptionalExpectedCurrentVersionTracksPresenceOfZero(t *testing.T) {
	var value optionalUint64
	if value.set {
		t.Fatal("zero value should be absent")
	}
	if err := value.Set("0"); err != nil {
		t.Fatal(err)
	}
	if !value.set || value.value != 0 {
		t.Fatalf("optional value = %+v", value)
	}
}

func TestReleaseSubscriberPresentationSeparatesIdentitiesSharingClientInstance(t *testing.T) {
	instances := map[releaseSubscriberInstanceKey]*releaseSubscriberInstanceStatus{}
	mergeReleaseSubscriberStates(instances, []*kmsv1.ReleaseSubscriberState{
		{
			Identity:           "identity-a",
			ClientName:         "shared-client",
			InstanceId:         "same-instance",
			State:              "received",
			ReleaseVersion:     1,
			ActivationRevision: 1,
		},
		{
			Identity:           "identity-b",
			ClientName:         "shared-client",
			InstanceId:         "same-instance",
			State:              "applied",
			ReleaseVersion:     2,
			ActivationRevision: 2,
			Connected:          true,
		},
	})
	if len(instances) != 2 {
		t.Fatalf("grouped instances = %d, want 2", len(instances))
	}

	var output bytes.Buffer
	writeReleaseSubscriberInstances(&output, instances, 2)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("output rows = %d, want header plus 2 identities:\n%s", len(lines), output.String())
	}
	if got := strings.Join(strings.Fields(lines[0]), "|"); got != "IDENTITY|CLIENT|INSTANCE|RECEIVED|PREPARED|APPLIED|REJECTED|LAG|CONNECTED" {
		t.Fatalf("header = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[1]), "|"); got != "identity-a|shared-client|same-instance|v1/r1|-|-|-|1|false" {
		t.Fatalf("identity-a row = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[2]), "|"); got != "identity-b|shared-client|same-instance|-|-|v2/r2|-|0|true" {
		t.Fatalf("identity-b row = %q", got)
	}
}

// startStubGRPC serves the registered stub services on an in-memory listener
// and returns the dial override command tests install on the CLI.
func startStubGRPC(t *testing.T, register func(*grpc.Server)) dialFunc {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return func(*connFlags) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///cli-stub",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		)
	}
}

type verifyReleaseStub struct {
	kmsv1.UnimplementedConfigurationReleaseServiceServer
	mu       sync.Mutex
	calls    []*kmsv1.VerifyReleaseDefaultsRequest
	auth     []string
	response *kmsv1.VerifyReleaseDefaultsResponse
	err      error
}

func (s *verifyReleaseStub) VerifyReleaseDefaults(ctx context.Context, req *kmsv1.VerifyReleaseDefaultsRequest) (*kmsv1.VerifyReleaseDefaultsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, proto.Clone(req).(*kmsv1.VerifyReleaseDefaultsRequest))
	md, _ := metadata.FromIncomingContext(ctx)
	s.auth = append(s.auth, strings.Join(md.Get("authorization"), ","))
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

const verifyTestSchemaSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// writeVerifyArtifact encodes a valid defaults artifact whose json parameter is
// deliberately non-canonical (pretty-printed, keys unsorted).
func writeVerifyArtifact(t *testing.T) string {
	t.Helper()
	raw, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: verifyTestSchemaSHA,
		Contract: []configstore.ContractEntry{
			{Alias: "db_password", Kind: configstore.ContractKindSecret},
			{Alias: "greeting", Kind: configstore.ContractKindParameter, ContentType: "string"},
			{Alias: "settings", Kind: configstore.ContractKindParameter, ContentType: "json"},
		},
		Parameters: []configstore.DefaultsParameter{
			{Alias: "greeting", ContentType: "string", Value: "hello"},
			{Alias: "settings", ContentType: "json", Value: "{\n  \"b\": 1,\n  \"a\": 2\n}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "defaults.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReleaseVerifyDefaultsHashesLocallyAndReportsVerdicts(t *testing.T) {
	stub := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", Version: 3, ActivationRevision: 42, SchemaMatches: true,
		Entries:    []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "match"}, {Alias: "settings", Verdict: "match"}},
		MatchCount: 2, UnverifiedCount: 1,
	}}
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(s, stub) })
	code := c.Run([]string{"release", "verify-defaults", "prod/gradethis", "--artifact", writeVerifyArtifact(t), "--release", "runtime", "--insecure", "--token", "ci-token"})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 1 || stub.auth[0] != "Bearer ci-token" {
		t.Fatalf("calls=%d auth=%v", len(stub.calls), stub.auth)
	}
	req := stub.calls[0]
	if req.GetNamespace().GetEnv() != "prod" || req.GetNamespace().GetApp() != "gradethis" || req.GetName() != "runtime" || req.GetProfile() != "dev" || req.GetSchemaSha256() != verifyTestSchemaSHA {
		t.Fatalf("request identity = %+v", req)
	}
	if len(req.GetEntries()) != 2 {
		t.Fatalf("entries = %+v", req.GetEntries())
	}
	wantJSON, _ := configstore.ParameterHash("json", []byte(`{"a":2,"b":1}`))
	wantText, _ := configstore.ParameterHash("string", []byte("hello"))
	for _, e := range req.GetEntries() {
		switch e.GetAlias() {
		case "settings":
			if e.GetContentType() != "json" || e.GetSha256() != wantJSON {
				t.Fatalf("settings entry = %+v, want canonical json hash %s", e, wantJSON)
			}
		case "greeting":
			if e.GetContentType() != "string" || e.GetSha256() != wantText {
				t.Fatalf("greeting entry = %+v", e)
			}
		default:
			t.Fatalf("unexpected entry %+v", e)
		}
		if strings.Contains(e.String(), "hello") {
			t.Fatalf("parameter value leaked onto the wire: %s", e.String())
		}
	}
	out := c.stdout()
	for _, want := range []string{"ALIAS", "VERDICT", "greeting  match", "settings  match", "Release runtime version 3 (revision 42): 2 match, 0 differs", "1 unverified", "schema match"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestReleaseVerifyDefaultsExitCodes(t *testing.T) {
	artifact := writeVerifyArtifact(t)
	run := func(t *testing.T, stub *verifyReleaseStub, args ...string) (int, *testCLI) {
		t.Helper()
		c := newTestCLI()
		c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(s, stub) })
		return c.Run(append([]string{"release", "verify-defaults"}, args...)), c
	}
	t.Run("differs fails", func(t *testing.T) {
		stub := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{Name: "runtime", Version: 1, SchemaMatches: true,
			Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "differs"}, {Alias: "settings", Verdict: "match"}}, MatchCount: 1, DiffersCount: 1}}
		code, c := run(t, stub, "prod/gradethis", "--artifact", artifact, "--insecure")
		if code != 1 || !strings.Contains(c.stdout(), "greeting  differs") {
			t.Fatalf("exit=%d stdout=%s", code, c.stdout())
		}
	})
	t.Run("schema mismatch fails", func(t *testing.T) {
		stub := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{Name: "runtime", Version: 1, SchemaMatches: false,
			Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "match"}, {Alias: "settings", Verdict: "match"}}, MatchCount: 2}}
		code, c := run(t, stub, "prod/gradethis", "--artifact", artifact, "--insecure")
		if code != 1 || !strings.Contains(c.stdout(), "schema mismatch") {
			t.Fatalf("exit=%d stdout=%s", code, c.stdout())
		}
	})
	t.Run("rpc failure", func(t *testing.T) {
		stub := &verifyReleaseStub{err: status.Error(codes.ResourceExhausted, "verify-defaults request budget exhausted for identity")}
		code, c := run(t, stub, "prod/gradethis", "--artifact", artifact, "--insecure")
		if code != 1 || !strings.Contains(c.stderr(), "ResourceExhausted") {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
	})
	t.Run("usage errors", func(t *testing.T) {
		stub := &verifyReleaseStub{}
		for _, args := range [][]string{
			{"prod/gradethis"},                                    // no --artifact
			{"--artifact", artifact},                              // no namespace
			{"not-a-namespace", "--artifact", artifact},           // bad namespace
			{"prod/gradethis", "extra", "--artifact", artifact},   // too many positionals
			{"prod/gradethis", "--artifact", artifact, "--bogus"}, // unknown flag
		} {
			code, _ := run(t, stub, args...)
			if code != 2 {
				t.Fatalf("args %v exit = %d, want 2", args, code)
			}
		}
		stub.mu.Lock()
		defer stub.mu.Unlock()
		if len(stub.calls) != 0 {
			t.Fatalf("usage errors must not reach the server: %d calls", len(stub.calls))
		}
	})
	t.Run("invalid artifact", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(bad, []byte(`{"format":"nope"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		code, c := run(t, &verifyReleaseStub{}, "prod/gradethis", "--artifact", bad, "--insecure")
		if code != 1 || !strings.Contains(c.stderr(), "invalid defaults artifact") {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
	})
}

func TestVerifyDefaultsCleanRequiresMatchesAndSchema(t *testing.T) {
	clean := &kmsv1.VerifyReleaseDefaultsResponse{SchemaMatches: true, Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "a", Verdict: "match"}}, MatchCount: 1, UnverifiedCount: 5}
	if !verifyDefaultsClean(true, clean) {
		t.Fatal("all-match with unverified extras should be clean")
	}
	if verifyDefaultsClean(true, &kmsv1.VerifyReleaseDefaultsResponse{SchemaMatches: false, MatchCount: 1, Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "a", Verdict: "match"}}}) {
		t.Fatal("schema mismatch should not be clean when the artifact carries a schema")
	}
	if !verifyDefaultsClean(false, &kmsv1.VerifyReleaseDefaultsResponse{SchemaMatches: false, MatchCount: 1, Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "a", Verdict: "match"}}}) {
		t.Fatal("schema is ignored when not checked")
	}
	for _, verdict := range []string{"differs", "missing_in_release", "unknown_alias", "secret_alias", "unsupported_content_type"} {
		if verifyDefaultsClean(true, &kmsv1.VerifyReleaseDefaultsResponse{SchemaMatches: true, Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "a", Verdict: verdict}}}) {
			t.Fatalf("%s should not be clean", verdict)
		}
	}
}
