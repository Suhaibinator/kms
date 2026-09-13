package cli

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
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

func TestReleaseCreateRequiresManifestSchemaSelectorBeforeDial(t *testing.T) {
	for _, tc := range []struct {
		name, extension, selector string
	}{
		{name: "yaml omitted", extension: ".yaml"},
		{name: "yaml null", extension: ".yaml", selector: "schema_version: null\n"},
		{name: "json omitted", extension: ".json"},
		{name: "json null", extension: ".json", selector: `"schema_version":null,`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "release"+tc.extension)
			var manifest string
			if tc.extension == ".json" {
				manifest = `{"namespace":"prod/app","name":"runtime",` + tc.selector + `"entries":[{"alias":"settings","kind":"parameter","key":"settings","version":1}]}`
			} else {
				manifest = "namespace: prod/app\nname: runtime\n" + tc.selector + "entries:\n  - alias: settings\n    kind: parameter\n    key: settings\n    version: 1\n"
			}
			if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			cli := newTestCLI()
			dialed := false
			cli.dialOverride = func(*connFlags) (*grpc.ClientConn, error) {
				dialed = true
				return nil, errors.New("unexpected dial")
			}
			if code := cli.cmdReleaseCreate([]string{path}); code != exitError {
				t.Fatalf("exit code = %d, stderr = %q", code, cli.stderr())
			}
			if dialed {
				t.Fatal("invalid manifest dialed the server")
			}
			if !strings.Contains(cli.stderr(), "schema_version is required") {
				t.Fatalf("stderr = %q", cli.stderr())
			}
		})
	}
}

func TestReadReleaseDefinitionPreservesExplicitSchemaSelectors(t *testing.T) {
	for _, tc := range []struct {
		name, manifest string
		want           uint64
	}{
		{name: "yaml zero", manifest: "schema_version: 0\n", want: 0},
		{name: "yaml positive", manifest: "schema_version: 7\n", want: 7},
		{name: "json zero", manifest: `"schema_version":0,`, want: 0},
		{name: "json positive", manifest: `"schema_version":7,`, want: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "release")
			var manifest string
			if strings.HasPrefix(tc.name, "json") {
				manifest = `{"namespace":"prod/app","name":"runtime",` + tc.manifest + `"entries":[{"alias":"settings","kind":"parameter","key":"settings","version":1}]}`
			} else {
				manifest = "namespace: prod/app\nname: runtime\n" + tc.manifest + "entries:\n  - alias: settings\n    kind: parameter\n    key: settings\n    version: 1\n"
			}
			if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			definition, err := (&CLI{}).readReleaseDefinition(path)
			if err != nil {
				t.Fatal(err)
			}
			req, err := releaseCreateRequest(definition)
			if err != nil {
				t.Fatal(err)
			}
			if req.SchemaVersion == nil || req.GetSchemaVersion() != tc.want {
				t.Fatalf("schema selector = %v, want explicit %d", req.SchemaVersion, tc.want)
			}
		})
	}
}

func TestReleaseCreateRequestRejectsAmbiguousAndDuplicateEntries(t *testing.T) {
	schemaVersion := uint64(1)
	_, err := releaseCreateRequest(releaseDefinition{
		Namespace: "prod/app", Name: "runtime", SchemaVersion: &schemaVersion,
		Entries: []releaseEntryDefinition{
			{Alias: "x", Kind: "parameter", Key: "a", Version: 1, Label: "current"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "version or label") {
		t.Fatalf("ambiguous selector error = %v", err)
	}
	_, err = releaseCreateRequest(releaseDefinition{
		Namespace: "prod/app", Name: "runtime", SchemaVersion: &schemaVersion,
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
	diff := computeReleaseDiff(from, to)
	var output bytes.Buffer
	writeReleaseDiff(&output, diff)
	text := output.String()
	if strings.Contains(text, "secret-plaintext") || strings.Contains(text, "do_not_print") {
		t.Fatalf("diff leaked secret metadata/value: %s", text)
	}
	for _, want := range []string{"password", "1 -> 2", "digest-one -> digest-two"} {
		if !strings.Contains(text, want) {
			t.Fatalf("diff missing %q: %s", want, text)
		}
	}
	// The JSON rendering shares the same computation, so it must be just as
	// free of secret material.
	var encoded bytes.Buffer
	if err := writeJSON(&encoded, diff); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded.String(), "secret-plaintext") || strings.Contains(encoded.String(), "do_not_print") {
		t.Fatalf("diff json leaked secret metadata/value: %s", encoded.String())
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

type releaseSubscriberAdminStub struct {
	kmsv1.UnimplementedAdminServiceServer
	response *kmsv1.ListReleaseSubscribersResponse
	pages    map[string]*kmsv1.ListReleaseSubscribersResponse
	err      error
	calls    []*kmsv1.ListReleaseSubscribersRequest
}

func (s *releaseSubscriberAdminStub) ListReleaseSubscribers(_ context.Context, req *kmsv1.ListReleaseSubscribersRequest) (*kmsv1.ListReleaseSubscribersResponse, error) {
	s.calls = append(s.calls, proto.Clone(req).(*kmsv1.ListReleaseSubscribersRequest))
	if s.err != nil {
		return nil, s.err
	}
	if s.pages != nil {
		return s.pages[req.GetPageToken()], nil
	}
	return s.response, nil
}

func TestReleaseSubscribersRequiresCoherentPages(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			revision := "snapshot"
			if changed {
				revision = "new-snapshot"
			}
			admin := &releaseSubscriberAdminStub{pages: map[string]*kmsv1.ListReleaseSubscribersResponse{
				"":       {ProjectionRevision: "snapshot", Summary: &kmsv1.ReleaseSubscriberSummary{Complete: true}, NextPageToken: "second", Instances: []*kmsv1.ReleaseSubscriberState{{InstanceId: "one", Classification: "applied"}}},
				"second": {ProjectionRevision: revision, Summary: &kmsv1.ReleaseSubscriberSummary{Complete: true}, Instances: []*kmsv1.ReleaseSubscriberState{{InstanceId: "two", Classification: "pending"}}},
			}}
			c := newTestCLI()
			c.dialOverride = startStubGRPC(t, func(server *grpc.Server) { kmsv1.RegisterAdminServiceServer(server, admin) })
			code := c.Run([]string{"release", "subscribers", "dev/app", "runtime", "--schema-version", "4", "--output", "json", "--insecure"})
			if changed {
				if code != exitError || c.stdout() != "" {
					t.Fatalf("mixed projection output: %d %s", code, c.stdout())
				}
			} else if code != exitOK || !strings.Contains(c.stdout(), "two") {
				t.Fatalf("pagination failed: %d %s", code, c.stderr())
			}
			if len(admin.calls) != 2 || admin.calls[1].GetSchemaVersion() != 4 {
				t.Fatalf("scope/pagination lost: %+v", admin.calls)
			}
		})
	}
}

func TestReleaseSubscribersUsesAuthoritativeInstances(t *testing.T) {
	admin := &releaseSubscriberAdminStub{response: &kmsv1.ListReleaseSubscribersResponse{
		ProjectionRevision: "snapshot", Summary: &kmsv1.ReleaseSubscriberSummary{Complete: true},
		Subscribers: []*kmsv1.ReleaseSubscriberState{{InstanceId: "obsolete", State: "rejected"}},
		Instances: []*kmsv1.ReleaseSubscriberState{
			{Identity: "client", ClientName: "worker", InstanceId: "same", SessionId: "a", SchemaVersion: 1, State: "received", Classification: "applied", Reason: "desired_applied", LastAppliedVersion: 4},
			{Identity: "client", ClientName: "worker", InstanceId: "same", SessionId: "b", SchemaVersion: 2, State: "applied", Classification: "stale", LastAppliedVersion: 3},
		},
	}}
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(server *grpc.Server) { kmsv1.RegisterAdminServiceServer(server, admin) })
	if code := c.Run([]string{"release", "subscribers", "dev/app", "runtime", "--output", "json", "--insecure"}); code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	var page struct {
		Items []*kmsv1.ReleaseSubscriberState `json:"items"`
	}
	if err := json.Unmarshal([]byte(c.stdout()), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].GetClassification() != "applied" || page.Items[1].GetClassification() != "stale" {
		t.Fatalf("output=%s", c.stdout())
	}
	if strings.Contains(c.stdout(), "obsolete") {
		t.Fatalf("raw history leaked into effective output: %s", c.stdout())
	}
	var out bytes.Buffer
	writeEffectiveReleaseSubscribers(&out, page.Items)
	if !strings.Contains(out.String(), "STATUS") || !strings.Contains(out.String(), "stale") || strings.Contains(out.String(), "REVISION LAG") {
		t.Fatalf("table=%s", out.String())
	}
}
func TestReleaseSubscribersFailsClosedWithoutProjection(t *testing.T) {
	admin := &releaseSubscriberAdminStub{response: &kmsv1.ListReleaseSubscribersResponse{Subscribers: []*kmsv1.ReleaseSubscriberState{{State: "applied"}}}}
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(server *grpc.Server) { kmsv1.RegisterAdminServiceServer(server, admin) })
	if code := c.Run([]string{"release", "subscribers", "dev/app", "runtime", "--insecure"}); code != exitError {
		t.Fatalf("exit=%d", code)
	}
	if c.stdout() != "" || !strings.Contains(c.stderr(), "unavailable") {
		t.Fatalf("stdout=%s stderr=%s", c.stdout(), c.stderr())
	}
}
func TestReleaseSubscribersPropagatesProjectionError(t *testing.T) {
	admin := &releaseSubscriberAdminStub{err: status.Error(codes.PermissionDenied, "cannot read projection")}
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(server *grpc.Server) { kmsv1.RegisterAdminServiceServer(server, admin) })
	if code := c.Run([]string{"release", "subscribers", "dev/app", "runtime", "--insecure"}); code != exitPermissionDenied {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	if c.stdout() != "" {
		t.Fatalf("partial output=%s", c.stdout())
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

func writeSchemaFreeVerifyArtifact(t *testing.T) string {
	t.Helper()
	raw, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev",
		Contract:   []configstore.ContractEntry{{Alias: "greeting", Kind: configstore.ContractKindParameter, ContentType: "string"}},
		Parameters: []configstore.DefaultsParameter{{Alias: "greeting", ContentType: "string", Value: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "schema-free-defaults.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReleaseVerifyDefaultsHashesLocallyAndReportsVerdicts(t *testing.T) {
	stub := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", Version: 3, ActivationRevision: 42, SchemaMatches: true, SchemaVersion: 7,
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
	for _, want := range []string{"ALIAS", "VERDICT", "greeting  match", "settings  match", "Release runtime version 3 (revision 42): 2 match, 0 differs", "1 unverified", "schema match (version 7)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestVerifyDefaultsRequestSchemaSelectors(t *testing.T) {
	ns := &kmsv1.NamespaceRef{Env: "prod", App: "app"}
	artifact := configstore.DefaultsArtifact{Profile: "dev"}
	if _, err := verifyDefaultsRequest(ns, "runtime", artifact, optionalUint64{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing selector error = %v", err)
	}
	explicitZero := optionalUint64{set: true, value: 0}
	request, err := verifyDefaultsRequest(ns, "runtime", artifact, explicitZero)
	if err != nil {
		t.Fatal(err)
	}
	if request.SchemaVersion == nil || request.GetSchemaVersion() != 0 || request.GetSchemaSha256() != "" {
		t.Fatalf("explicit-zero request = %+v", request)
	}
	artifact.SchemaSHA256 = verifyTestSchemaSHA
	if _, err := verifyDefaultsRequest(ns, "runtime", artifact, explicitZero); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("dual selector error = %v", err)
	}
}

func TestReleaseVerifyDefaultsExplicitSchemaZero(t *testing.T) {
	stub := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", Version: 1, SchemaVersion: 0, SchemaMatches: true,
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "match"}}, MatchCount: 1,
	}}
	run := func(args ...string) (int, *testCLI) {
		c := newTestCLI()
		c.dialOverride = startStubGRPC(t, func(server *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(server, stub) })
		return c.Run(append([]string{"release", "verify-defaults"}, args...)), c
	}
	artifact := writeSchemaFreeVerifyArtifact(t)
	if code, c := run("prod/app", "--artifact", artifact, "--insecure"); code != exitUsage || !strings.Contains(c.stderr(), "requires --schema-version") {
		t.Fatalf("missing selector = exit %d stderr %q", code, c.stderr())
	}
	code, c := run("prod/app", "--artifact", artifact, "--schema-version", "0", "--insecure")
	if code != exitOK {
		t.Fatalf("explicit schema zero = exit %d stderr %q", code, c.stderr())
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 1 || stub.calls[0].SchemaVersion == nil || stub.calls[0].GetSchemaVersion() != 0 || stub.calls[0].GetSchemaSha256() != "" {
		t.Fatalf("verify calls = %+v", stub.calls)
	}
	if !strings.Contains(c.stdout(), "schema match (version 0)") {
		t.Fatalf("stdout = %q", c.stdout())
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
			{"prod/gradethis"},                                                  // no --artifact
			{"--artifact", artifact},                                            // no namespace
			{"not-a-namespace", "--artifact", artifact},                         // bad namespace
			{"prod/gradethis", "extra", "--artifact", artifact},                 // too many positionals
			{"prod/gradethis", "--artifact", artifact, "--bogus"},               // unknown flag
			{"prod/gradethis", "--artifact", artifact, "--schema-version", "0"}, // embedded digest conflicts
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
