package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// --- fixtures ---------------------------------------------------------------

// The namespace every env/exec test injects from, and the values it holds.
const (
	envTestHostValue    = "db.internal"
	envTestGreetValue   = "hello world"
	envTestSessionValue = "session-plaintext"
	envTestStripeValue  = "sk-live-plaintext"
	envTestAPIURLValue  = "https://api.example.com"
)

func envTestRef(env, app, key string) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: env, App: app}, Key: key}
}

func envTestDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func envTestBoundSecret(key string) *kmsv1.SecretMetadata {
	return &kmsv1.SecretMetadata{
		Ref:      envTestRef("prod", "app", key),
		Labels:   map[string]uint64{"current": 1},
		Bound:    true,
		Versions: []*kmsv1.SecretVersionInfo{{Version: 1, State: "enabled", Bound: true}},
	}
}

// --- stubs ------------------------------------------------------------------

// envCall records one RPC a stub answered together with the credentials and
// selectors it carried. Identity is metadata; a per-secret credential is the
// exact GetSecret request field.
type envCall struct {
	method           string
	path             string
	version          uint64
	prefix           string
	auth             string
	schemaVersion    uint64
	schemaVersionSet bool
}

type envRecorder struct {
	mu    sync.Mutex
	calls []envCall
}

func (r *envRecorder) record(ctx context.Context, call envCall) {
	md, _ := metadata.FromIncomingContext(ctx)
	call.auth = strings.Join(md.Get("authorization"), ",")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *envRecorder) snapshot() []envCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]envCall(nil), r.calls...)
}

// count reports how many times method was called.
func (r *envRecorder) count(method string) int {
	n := 0
	for _, call := range r.snapshot() {
		if call.method == method {
			n++
		}
	}
	return n
}

// call returns the single recorded call for method and path.
func (r *envRecorder) call(t *testing.T, method, path string) envCall {
	t.Helper()
	var found []envCall
	for _, call := range r.snapshot() {
		if call.method == method && call.path == path {
			found = append(found, call)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s(%s) called %d times, want exactly 1: %+v", method, path, len(found), r.snapshot())
	}
	return found[0]
}

// envParameterStub answers the ParameterService calls env and exec make.
// ListParameters returns one parameter per page so the client's paging loop is
// exercised on every namespace-mode test.
type envParameterStub struct {
	kmsv1.UnimplementedParameterServiceServer
	rec     *envRecorder
	list    []*kmsv1.Parameter
	get     map[string]*kmsv1.Parameter
	listErr error
	getErr  map[string]error
}

func (s *envParameterStub) ListParameters(ctx context.Context, req *kmsv1.ListParametersRequest) (*kmsv1.ListParametersResponse, error) {
	s.rec.record(ctx, envCall{method: "ListParameters", prefix: req.GetKeyPrefix()})
	if s.listErr != nil {
		return nil, s.listErr
	}
	var matched []*kmsv1.Parameter
	for _, p := range s.list {
		if strings.HasPrefix(p.GetRef().GetKey(), req.GetKeyPrefix()) {
			matched = append(matched, p)
		}
	}
	start := 0
	if token := req.GetPageToken(); token != "" {
		n, err := strconv.Atoi(token)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "bad page token %q", token)
		}
		start = n
	}
	if start >= len(matched) {
		return &kmsv1.ListParametersResponse{}, nil
	}
	next := ""
	if start+1 < len(matched) {
		next = strconv.Itoa(start + 1)
	}
	return &kmsv1.ListParametersResponse{Parameters: matched[start : start+1], NextPageToken: next}, nil
}

func (s *envParameterStub) GetParameter(ctx context.Context, req *kmsv1.GetParameterRequest) (*kmsv1.GetParameterResponse, error) {
	path := displayPath(req.GetRef())
	s.rec.record(ctx, envCall{method: "GetParameter", path: path, version: req.GetVersion()})
	if err := s.getErr[path]; err != nil {
		return nil, err
	}
	p, ok := s.get[path]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no parameter %s", path)
	}
	return &kmsv1.GetParameterResponse{Parameter: p}, nil
}

// envSecretStub answers the SecretService calls.
type envSecretStub struct {
	kmsv1.UnimplementedSecretServiceServer
	rec         *envRecorder
	list        []*kmsv1.SecretMetadata
	metadata    map[string]*kmsv1.SecretMetadata
	get         map[string]*kmsv1.GetSecretResponse
	getErr      map[string]error
	metadataErr map[string]error
	listErr     error
}

func (s *envSecretStub) ListSecrets(ctx context.Context, req *kmsv1.ListSecretsRequest) (*kmsv1.ListSecretsResponse, error) {
	s.rec.record(ctx, envCall{method: "ListSecrets", prefix: req.GetKeyPrefix()})
	if s.listErr != nil {
		return nil, s.listErr
	}
	var matched []*kmsv1.SecretMetadata
	for _, m := range s.list {
		if strings.HasPrefix(m.GetRef().GetKey(), req.GetKeyPrefix()) {
			matched = append(matched, m)
		}
	}
	return &kmsv1.ListSecretsResponse{Secrets: matched}, nil
}

func (s *envSecretStub) GetSecret(ctx context.Context, req *kmsv1.GetSecretRequest) (*kmsv1.GetSecretResponse, error) {
	path := displayPath(req.GetRef())
	s.rec.record(ctx, envCall{method: "GetSecret", path: path, version: req.GetVersion()})
	if err := s.getErr[path]; err != nil {
		return nil, err
	}
	resp, ok := s.get[path]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no secret %s", path)
	}
	return resp, nil
}

func (s *envSecretStub) GetSecretMetadata(ctx context.Context, req *kmsv1.GetSecretMetadataRequest) (*kmsv1.GetSecretMetadataResponse, error) {
	path := displayPath(req.GetRef())
	s.rec.record(ctx, envCall{method: "GetSecretMetadata", path: path})
	if err := s.metadataErr[path]; err != nil {
		return nil, err
	}
	metadata, ok := s.metadata[path]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no secret %s", path)
	}
	return &kmsv1.GetSecretMetadataResponse{Secret: metadata}, nil
}

// envReleaseStub answers GetActiveRelease.
type envReleaseStub struct {
	kmsv1.UnimplementedConfigurationReleaseServiceServer
	rec     *envRecorder
	release *kmsv1.ConfigurationRelease
	err     error
}

func (s *envReleaseStub) GetActiveRelease(ctx context.Context, req *kmsv1.GetActiveReleaseRequest) (*kmsv1.GetActiveReleaseResponse, error) {
	s.rec.record(ctx, envCall{method: "GetActiveRelease", path: req.GetName(), schemaVersion: req.GetSchemaVersion(), schemaVersionSet: req.SchemaVersion != nil})
	if s.err != nil {
		return nil, s.err
	}
	return &kmsv1.GetActiveReleaseResponse{Release: s.release}, nil
}

// envFixture is a wired CLI plus the stubs behind it.
type envFixture struct {
	*testCLI
	rec      *envRecorder
	params   *envParameterStub
	secrets  *envSecretStub
	releases *envReleaseStub
}

// newEnvFixture builds the standard prod/app namespace: two parameters, a
// two secrets.
func newEnvFixture(t *testing.T) *envFixture {
	t.Helper()
	rec := &envRecorder{}
	f := &envFixture{
		testCLI: newTestCLI(),
		rec:     rec,
		params: &envParameterStub{
			rec: rec,
			list: []*kmsv1.Parameter{
				{Ref: envTestRef("prod", "app", "db/host"), Value: envTestHostValue, ContentType: "string", Version: 3},
				{Ref: envTestRef("prod", "app", "greeting"), Value: envTestGreetValue, ContentType: "string", Version: 1},
			},
			get:    map[string]*kmsv1.Parameter{},
			getErr: map[string]error{},
		},
		secrets: &envSecretStub{
			rec: rec,
			list: []*kmsv1.SecretMetadata{
				{
					Ref: envTestRef("prod", "app", "session-secret"), Labels: map[string]uint64{"current": 4},
					Versions: []*kmsv1.SecretVersionInfo{{Version: 4, State: "enabled"}},
				},
				{
					Ref: envTestRef("prod", "app", "stripe-key"), Labels: map[string]uint64{"current": 9},
					Versions: []*kmsv1.SecretVersionInfo{{Version: 9, State: "enabled"}},
				},
			},
			metadata: map[string]*kmsv1.SecretMetadata{},
			get: map[string]*kmsv1.GetSecretResponse{
				"/prod/app/session-secret": {Ref: envTestRef("prod", "app", "session-secret"), Version: 4, Value: []byte(envTestSessionValue)},
				"/prod/app/stripe-key":     {Ref: envTestRef("prod", "app", "stripe-key"), Version: 9, Value: []byte(envTestStripeValue)},
			},
			getErr:      map[string]error{},
			metadataErr: map[string]error{},
		},
		releases: &envReleaseStub{rec: rec},
	}
	f.dialOverride = startStubGRPC(t, func(s *grpc.Server) {
		kmsv1.RegisterParameterServiceServer(s, f.params)
		kmsv1.RegisterSecretServiceServer(s, f.secrets)
		kmsv1.RegisterConfigurationReleaseServiceServer(s, f.releases)
	})
	return f
}

// run invokes `env prod/app` with the connection flags every test shares.
func (f *envFixture) run(args ...string) int {
	return f.Run(append([]string{"env", "prod/app", "--insecure", "--token", "id-token"}, args...))
}

// envTestRelease builds the standard namespace-local "runtime" release. The
// secret's alias deliberately differs from its key.
func envTestRelease() *kmsv1.ConfigurationRelease {
	release := &kmsv1.ConfigurationRelease{
		Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"},
		Name:      "runtime",
		Version:   4,
		Entries: []*kmsv1.ConfigurationReleaseEntry{
			{
				Alias: "db-host", Kind: "parameter", Ref: envTestRef("prod", "app", "db/host"), Version: 3,
				ContentType: "string", ParameterDigest: envTestDigest(envTestHostValue),
			},
			{
				Alias: "api-url", Kind: "parameter", Ref: envTestRef("prod", "app", "api/url"), Version: 5,
				ContentType: "string", ParameterDigest: strings.ToUpper(envTestDigest(envTestAPIURLValue)),
			},
			{
				Alias: "billing-key", Kind: "secret", Ref: envTestRef("prod", "app", "stripe-key"), Version: 9,
				ContentType: "application/octet-stream",
			},
		},
	}
	setEnvTestReleaseDigest(release)
	return release
}

func setEnvTestReleaseDigest(release *kmsv1.ConfigurationRelease) {
	digest, err := configurationReleaseDigest(release)
	if err != nil {
		panic(err)
	}
	release.Digest = digest
}

func TestConfigurationReleaseDigestMatchesCrossSDKGolden(t *testing.T) {
	t.Parallel()
	release := &kmsv1.ConfigurationRelease{
		Namespace:     &kmsv1.NamespaceRef{Env: "prod", App: "api"},
		Name:          "runtime",
		Version:       42,
		SchemaVersion: 7,
		Entries: []*kmsv1.ConfigurationReleaseEntry{
			{
				Alias: "z-secret", Kind: "secret",
				Ref: envTestRef("prod", "api", "db/password"), Version: 9,
				ContentType: "string", MetadataJson: "{}",
			},
			{
				Alias: "a-policy", Kind: "parameter",
				Ref: envTestRef("prod", "api", "policy"), Version: 3,
				ContentType: "json", MetadataJson: `{"owner":"platform"}`,
				ParameterDigest: envTestDigest(`{"min":14}`),
			},
		},
		Digest:          "ignored",
		MetadataJson:    `{"rollout":"all"}`,
		CreatedBy:       "ignored-creator",
		CreatedAtUnixMs: 1_725_000_000_000,
	}

	got, err := configurationReleaseDigest(release)
	if err != nil {
		t.Fatal(err)
	}
	const want = "0cc9ea54ba0d4903027235ac4ba5604114d7fbc787209919a0633e4e708f26c3"
	if got != want {
		t.Fatalf("digest = %s, want cross-SDK golden %s", got, want)
	}
}

// installRelease wires the release fixture and the parameter versions it pins.
func (f *envFixture) installRelease() {
	f.releases.release = envTestRelease()
	f.params.get["/prod/app/db/host"] = &kmsv1.Parameter{
		Ref: envTestRef("prod", "app", "db/host"), Value: envTestHostValue, ContentType: "string", Version: 3,
	}
	f.params.get["/prod/app/api/url"] = &kmsv1.Parameter{
		Ref: envTestRef("prod", "app", "api/url"), Value: envTestAPIURLValue, Version: 5, ContentType: "string",
	}
	f.secrets.get["/prod/app/stripe-key"] = &kmsv1.GetSecretResponse{
		Ref: envTestRef("prod", "app", "stripe-key"), Version: 9, Value: []byte(envTestStripeValue),
		ContentType: "application/octet-stream",
	}
	f.secrets.metadata["/prod/app/stripe-key"] = &kmsv1.SecretMetadata{
		Ref: envTestRef("prod", "app", "stripe-key"), Labels: map[string]uint64{"current": 9},
		Versions: []*kmsv1.SecretVersionInfo{{Version: 9, State: "enabled"}},
	}
}

// --- namespace mode ---------------------------------------------------------

// TestEnvNamespaceRendersEveryFormat: the default selection is the namespace's
// current values, and each --format renders the same sorted variables in the
// syntax its consumer expects.
func TestEnvNamespaceRendersEveryFormat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "default is dotenv",
			want: "DB_HOST=db.internal\nGREETING=\"hello world\"\nSESSION_SECRET=session-plaintext\nSTRIPE_KEY=sk-live-plaintext\n",
		},
		{
			name: "dotenv",
			args: []string{"--format", "dotenv"},
			want: "DB_HOST=db.internal\nGREETING=\"hello world\"\nSESSION_SECRET=session-plaintext\nSTRIPE_KEY=sk-live-plaintext\n",
		},
		{
			name: "export",
			args: []string{"--format", "export"},
			want: "export DB_HOST='db.internal'\nexport GREETING='hello world'\nexport SESSION_SECRET='session-plaintext'\nexport STRIPE_KEY='sk-live-plaintext'\n",
		},
		{
			name: "yaml",
			args: []string{"--format", "yaml"},
			want: "DB_HOST: \"db.internal\"\nGREETING: \"hello world\"\nSESSION_SECRET: \"session-plaintext\"\nSTRIPE_KEY: \"sk-live-plaintext\"\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			args := append([]string{}, tc.args...)
			if code := f.run(args...); code != 0 {
				t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
			}
			if got := f.stdout(); got != tc.want {
				t.Fatalf("stdout = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEnvJSONFormats: --format json and the global -o json produce the same
// single object, whose keys stay in sorted order.
func TestEnvJSONFormats(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "--format json", args: []string{"--format", "json"}},
		{name: "-o json implies it", args: []string{"-o", "json"}},
		{name: "-o json with --format json", args: []string{"-o", "json", "--format", "json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			args := append([]string{}, tc.args...)
			if code := f.run(args...); code != 0 {
				t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
			}
			var document map[string]string
			if err := json.Unmarshal([]byte(f.stdout()), &document); err != nil {
				t.Fatalf("stdout is not one JSON object: %v\nstdout=%q", err, f.stdout())
			}
			want := map[string]string{
				"DB_HOST":        envTestHostValue,
				"GREETING":       envTestGreetValue,
				"SESSION_SECRET": envTestSessionValue,
				"STRIPE_KEY":     envTestStripeValue,
			}
			if len(document) != len(want) {
				t.Fatalf("document = %v, want %v", document, want)
			}
			for name, value := range want {
				if document[name] != value {
					t.Fatalf("%s = %q, want %q", name, document[name], value)
				}
			}
			// Sorted, so a diff of two runs is stable.
			raw := f.stdout()
			for i, name := range []string{"DB_HOST", "GREETING", "SESSION_SECRET", "STRIPE_KEY"} {
				if i == 0 {
					continue
				}
				previous := []string{"DB_HOST", "GREETING", "SESSION_SECRET", "STRIPE_KEY"}[i-1]
				if strings.Index(raw, name) < strings.Index(raw, previous) {
					t.Fatalf("%s precedes %s: %s", name, previous, raw)
				}
			}
		})
	}
}

// TestEnvPrefixSelectsASubtree: --prefix is a server-side selector, so it must
// reach both list calls rather than being filtered locally.
func TestEnvPrefixSelectsASubtree(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	if code := f.run("--prefix", "db/"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	if got := f.stdout(); got != "DB_HOST=db.internal\n" {
		t.Fatalf("stdout = %q", got)
	}
	for _, call := range f.rec.snapshot() {
		if call.method == "ListParameters" || call.method == "ListSecrets" {
			if call.prefix != "db/" {
				t.Fatalf("%s carried prefix %q, want %q", call.method, call.prefix, "db/")
			}
		}
	}
}

// TestEnvEnvPrefixIsPrependedVerbatim: --env-prefix is applied to every
// variable, including the _B64 form, and is the escape hatch for a key that
// would otherwise start with a digit.
func TestEnvEnvPrefixIsPrependedVerbatim(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	f.params.list = append(f.params.list, &kmsv1.Parameter{
		Ref: envTestRef("prod", "app", "2fa-issuer"), Value: "acme", ContentType: "string",
	})
	if code := f.run("--env-prefix", "APP_", "--no-secrets"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	want := "APP_2FA_ISSUER=acme\nAPP_DB_HOST=db.internal\nAPP_GREETING=\"hello world\"\n"
	if got := f.stdout(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

// TestEnvRejectsAKeyThatCannotBeAVariable: without --env-prefix a leading
// digit has no legal environment name, and the error must point at the flag
// that fixes it rather than silently dropping the entry.
func TestEnvRejectsAKeyThatCannotBeAVariable(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	f.params.list = append(f.params.list, &kmsv1.Parameter{
		Ref: envTestRef("prod", "app", "2fa-issuer"), Value: "acme", ContentType: "string",
	})
	if code := f.run("--no-secrets"); code != exitError {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
	}
	if !strings.Contains(f.stderr(), "--env-prefix") {
		t.Fatalf("stderr = %s, want it to name --env-prefix", f.stderr())
	}
	if f.stdout() != "" {
		t.Fatalf("stdout = %q, want nothing written", f.stdout())
	}
}

// TestEnvNoSecretsNeverCallsSecretService: --no-secrets is a promise that the
// invocation selects parameters only, so it must not list metadata or read
// secret material even when secrets exist in the namespace.
func TestEnvNoSecretsNeverCallsSecretService(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	// This would fail the invocation if parameter-only mode touched secrets.
	f.secrets.listErr = status.Error(codes.PermissionDenied, "secret listing denied")
	if code := f.run("--no-secrets"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	if got := f.stdout(); got != "DB_HOST=db.internal\nGREETING=\"hello world\"\n" {
		t.Fatalf("stdout = %q", got)
	}
	if n := f.rec.count("ListSecrets") + f.rec.count("GetSecretMetadata") + f.rec.count("GetSecret"); n != 0 {
		t.Fatalf("secret service called %d times under --no-secrets", n)
	}
	// No secret was selected, so nothing was skipped either.
	if strings.Contains(f.stderr(), "warning:") {
		t.Fatalf("stderr = %s, want no skip warning", f.stderr())
	}
}

func TestEnvBoundNamespaceSecretFailsClosedOrIsExplicitlyOmitted(t *testing.T) {
	configure := func(f *envFixture) {
		f.secrets.list[0].Bound = true
		f.secrets.list[0].Versions[0].Bound = true
	}

	t.Run("default fails without output", func(t *testing.T) {
		f := newEnvFixture(t)
		configure(f)
		if code := f.run(); code != exitError {
			t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
		}
		if f.stdout() != "" {
			t.Fatalf("stdout = %q, want no partial output", f.stdout())
		}
		if !strings.Contains(f.stderr(), "secret /prod/app/session-secret cannot be materialized: it is bound") {
			t.Fatalf("stderr = %s, want bound-secret failure", f.stderr())
		}
		if n := f.rec.count("GetSecret"); n != 0 {
			t.Fatalf("GetSecret called %d times before fail-closed rejection", n)
		}
	})

	t.Run("explicit incomplete mode omits with warning", func(t *testing.T) {
		f := newEnvFixture(t)
		configure(f)
		if code := f.run("--allow-incomplete-secrets", "--quiet"); code != exitOK {
			t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
		}
		if strings.Contains(f.stdout(), "SESSION_SECRET") {
			t.Fatalf("stdout = %q, want bound secret omitted", f.stdout())
		}
		if !strings.Contains(f.stdout(), "STRIPE_KEY="+envTestStripeValue) {
			t.Fatalf("stdout = %q, want resolvable secret preserved", f.stdout())
		}
		if !strings.Contains(f.stderr(), "warning: omitted unavailable secret /prod/app/session-secret: it is bound") {
			t.Fatalf("stderr = %s, want unsuppressible omission warning", f.stderr())
		}
	})
}

func TestEnvReleaseBoundPinFailsClosed(t *testing.T) {
	f := newEnvFixture(t)
	f.installRelease()
	f.secrets.metadata["/prod/app/stripe-key"].Versions = []*kmsv1.SecretVersionInfo{
		{Version: 8, State: "enabled"},
		{Version: 9, State: "enabled", Bound: true},
	}
	if code := f.run("--release", "runtime", "--schema-version", "0"); code != exitError {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
	}
	if f.stdout() != "" {
		t.Fatalf("stdout = %q, want no release output", f.stdout())
	}
	if !strings.Contains(f.stderr(), "secret /prod/app/stripe-key cannot be materialized: it is bound") {
		t.Fatalf("stderr = %s, want bound release rejection", f.stderr())
	}
	if n := f.rec.count("GetSecretMetadata"); n != 1 {
		t.Fatalf("GetSecretMetadata called %d times, want exact pinned-version lookup", n)
	}
	if n := f.rec.count("GetSecret"); n != 0 {
		t.Fatalf("GetSecret called %d times for a bound release version", n)
	}
}

func TestEnvReleaseRejectsUnavailableBoundMetadataBeforeEmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		versions []*kmsv1.SecretVersionInfo
	}{
		{
			name:     "disabled",
			versions: []*kmsv1.SecretVersionInfo{{Version: 9, State: "disabled", Bound: true}},
		},
		{
			name:     "contradictory destroyed timestamp",
			versions: []*kmsv1.SecretVersionInfo{{Version: 9, State: "enabled", DestroyedAtUnixMs: 1, Bound: true}},
		},
		{
			name:     "expired",
			versions: []*kmsv1.SecretVersionInfo{{Version: 9, State: "enabled", ExpiresAtUnixMs: 1, Bound: true}},
		},
		{
			name: "duplicate",
			versions: []*kmsv1.SecretVersionInfo{
				{Version: 9, State: "enabled", Bound: true},
				{Version: 9, State: "enabled", Bound: true},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			f.installRelease()
			f.secrets.metadata["/prod/app/stripe-key"].Versions = tc.versions
			if code := f.run("--release", "runtime", "--schema-version", "0"); code != exitError {
				t.Fatalf("exit = %d, want %d (stdout=%q stderr=%s)", code, exitError, f.stdout(), f.stderr())
			}
			if !strings.Contains(f.stderr(), "metadata") {
				t.Fatalf("stderr = %s, want metadata rejection", f.stderr())
			}
			if f.stdout() != "" {
				t.Fatalf("stdout = %q, want no candidate output", f.stdout())
			}
			if n := f.rec.count("GetSecret"); n != 0 {
				t.Fatalf("GetSecret called %d times for unavailable metadata", n)
			}
		})
	}
}

func TestEnvIncompleteModeValidatesOmittedSecretNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		params  []*kmsv1.Parameter
		secrets []*kmsv1.SecretMetadata
		want    string
	}{
		{
			name:    "unavailable key needs env prefix",
			secrets: []*kmsv1.SecretMetadata{envTestBoundSecret("2fa-secret")},
			want:    "mapping unavailable secret output: \"2fa-secret\" maps to \"2FA_SECRET\", which starts with a digit: use --env-prefix",
		},
		{
			name: "possible binary name collides with resolved value",
			params: []*kmsv1.Parameter{
				{Ref: envTestRef("prod", "app", "api-b64"), Value: "resolved", ContentType: "string"},
			},
			secrets: []*kmsv1.SecretMetadata{envTestBoundSecret("api")},
			want:    "unavailable secret /prod/app/api and another selected value both map to environment variable API_B64",
		},
		{
			name: "two omitted possible names collide",
			secrets: []*kmsv1.SecretMetadata{
				envTestBoundSecret("api"),
				envTestBoundSecret("api-b64"),
			},
			want: "unavailable secrets /prod/app/api and /prod/app/api-b64 may both map to environment variable API_B64",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			f.params.list = tc.params
			f.secrets.list = tc.secrets
			if code := f.run("--allow-incomplete-secrets"); code != exitError {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
			}
			if !strings.Contains(f.stderr(), tc.want) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), tc.want)
			}
			if f.stdout() != "" {
				t.Fatalf("stdout = %q, want no output after mapping failure", f.stdout())
			}
			if n := f.rec.count("GetSecret"); n != 0 {
				t.Fatalf("GetSecret called %d times for unavailable-only selection", n)
			}
		})
	}
}

// TestEnvServerErrorsKeepTheirExitCode covers the rest of the mapping scripts
// branch on.
func TestEnvServerErrorsKeepTheirExitCode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(*envFixture)
		args []string
		want int
	}{
		{
			name: "failed precondition on GetSecret",
			set: func(f *envFixture) {
				f.secrets.getErr["/prod/app/session-secret"] = status.Error(codes.FailedPrecondition, "secret is disabled")
			},
			want: exitFailedPrecondition,
		},
		{
			name: "not found on the release",
			set: func(f *envFixture) {
				f.releases.err = status.Error(codes.NotFound, "no such release")
			},
			args: []string{"--release", "runtime", "--schema-version", "0"},
			want: exitNotFound,
		},
		{
			name: "unauthenticated on ListParameters",
			set: func(f *envFixture) {
				f.params.listErr = status.Error(codes.Unauthenticated, "no credential")
			},
			want: exitUnauthenticated,
		},
		{
			name: "permission denied on ListSecrets",
			set: func(f *envFixture) {
				f.secrets.listErr = status.Error(codes.PermissionDenied, "denied")
			},
			want: exitPermissionDenied,
		},
		{
			name: "resource exhausted on GetParameter",
			set: func(f *envFixture) {
				f.installRelease()
				f.params.getErr["/prod/app/db/host"] = status.Error(codes.ResourceExhausted, "slow down")
			},
			args: []string{"--release", "runtime", "--schema-version", "0"},
			want: exitResourceExhausted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			tc.set(f)
			args := append([]string{}, tc.args...)
			if code := f.run(args...); code != tc.want {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, tc.want, f.stderr())
			}
			if f.stdout() != "" {
				t.Fatalf("stdout = %q, want nothing on a fatal error", f.stdout())
			}
		})
	}
}

// --- release mode -----------------------------------------------------------

// TestEnvReleaseInjectsVerifiedPins: --release names entries by alias, pins the
// exact version of each, stays in its home namespace, and verifies the parameter digest
// (case-insensitively, since the hex casing is not normalised on the wire).
func TestEnvReleaseInjectsVerifiedPins(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	f.installRelease()
	code := f.run("--release", "runtime", "--schema-version", "0")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	want := "API_URL=https://api.example.com\nBILLING_KEY=sk-live-plaintext\nDB_HOST=db.internal\n"
	if got := f.stdout(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	// Namespace listing is not consulted at all: a release is its own selection.
	if n := f.rec.count("ListParameters") + f.rec.count("ListSecrets"); n != 0 {
		t.Fatalf("release mode made %d list calls", n)
	}
	active := f.rec.call(t, "GetActiveRelease", "runtime")
	if !active.schemaVersionSet || active.schemaVersion != 0 {
		t.Fatalf("GetActiveRelease schema selector = (%d, set=%t), want explicit 0", active.schemaVersion, active.schemaVersionSet)
	}
	if got := f.rec.call(t, "GetParameter", "/prod/app/db/host").version; got != 3 {
		t.Fatalf("GetParameter version = %d, want the pinned 3", got)
	}
	if got := f.rec.call(t, "GetParameter", "/prod/app/api/url").version; got != 5 {
		t.Fatalf("GetParameter version = %d, want 5", got)
	}
	secret := f.rec.call(t, "GetSecret", "/prod/app/stripe-key")
	if secret.version != 9 {
		t.Fatalf("GetSecret version = %d, want the pinned 9", secret.version)
	}
}

// TestEnvReleaseWithoutSecrets: --no-secrets narrows a release too, so a job
// that needs only configuration never reads a secret entry.
func TestEnvReleaseWithoutSecrets(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	f.installRelease()
	if code := f.run("--release", "runtime", "--schema-version", "0", "--no-secrets"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	want := "API_URL=" + envTestAPIURLValue + "\nDB_HOST=" + envTestHostValue + "\n"
	if got := f.stdout(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if n := f.rec.count("GetSecretMetadata") + f.rec.count("GetSecret"); n != 0 {
		t.Fatalf("secret service read called %d times under release --no-secrets", n)
	}
	if strings.Contains(f.stderr(), "warning:") {
		t.Fatalf("stderr = %s, want no skip warning", f.stderr())
	}
}

// TestEnvReleaseVerificationFailuresAreFatal: every mismatch between what the
// release recorded and what the server returned aborts the whole invocation,
// so a process is never started with a mix of pinned and drifted values.
func TestEnvReleaseVerificationFailuresAreFatal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(*envFixture)
		want string
	}{
		{
			name: "parameter digest mismatch",
			set: func(f *envFixture) {
				f.params.get["/prod/app/db/host"].Value = "db.tampered"
			},
			want: "value does not match the release digest",
		},
		{
			name: "empty recorded digest",
			set: func(f *envFixture) {
				f.releases.release.Entries[0].ParameterDigest = ""
				setEnvTestReleaseDigest(f.releases.release)
			},
			want: "value does not match the release digest",
		},
		{
			name: "parameter version mismatch",
			set: func(f *envFixture) {
				f.params.get["/prod/app/db/host"].Version = 4
			},
			want: "server returned version 4, release pins 3",
		},
		{
			name: "parameter ref mismatch",
			set: func(f *envFixture) {
				f.params.get["/prod/app/db/host"].Ref = envTestRef("prod", "app", "db/other")
			},
			want: "server returned a different resource",
		},
		{
			name: "parameter namespace mismatch",
			set: func(f *envFixture) {
				f.params.get["/prod/app/db/host"].Ref = envTestRef("staging", "app", "db/host")
			},
			want: "server returned a different resource",
		},
		{
			name: "missing parameter payload",
			set: func(f *envFixture) {
				f.params.get["/prod/app/db/host"] = nil
			},
			want: "server returned a different resource",
		},
		{
			name: "parameter content type mismatch",
			set: func(f *envFixture) {
				f.params.get["/prod/app/db/host"].ContentType = "json"
			},
			want: "content type \"json\" does not match the release's \"string\"",
		},
		{
			name: "empty parameter content type mismatch",
			set: func(f *envFixture) {
				f.releases.release.Entries[0].ContentType = ""
				setEnvTestReleaseDigest(f.releases.release)
			},
			want: "content type \"string\" does not match the release's \"\"",
		},
		{
			name: "secret version mismatch",
			set: func(f *envFixture) {
				f.secrets.get["/prod/app/stripe-key"].Version = 8
			},
			want: "server returned version 8, release pins 9",
		},
		{
			name: "secret ref mismatch",
			set: func(f *envFixture) {
				f.secrets.get["/prod/app/stripe-key"].Ref = envTestRef("prod", "app", "other-key")
			},
			want: "server returned a different resource",
		},
		{
			name: "secret content type mismatch",
			set: func(f *envFixture) {
				f.secrets.get["/prod/app/stripe-key"].ContentType = "text/plain"
			},
			want: "content type \"text/plain\" does not match the release's \"application/octet-stream\"",
		},
		{
			name: "empty secret content type mismatch",
			set: func(f *envFixture) {
				f.releases.release.Entries[2].ContentType = ""
				setEnvTestReleaseDigest(f.releases.release)
			},
			want: "content type \"application/octet-stream\" does not match the release's \"\"",
		},
		{
			name: "unknown entry kind",
			set: func(f *envFixture) {
				f.releases.release.Entries[0].Kind = "keypair"
			},
			want: "release entry db-host has unknown kind \"keypair\"",
		},
		{
			name: "no active version",
			set: func(f *envFixture) {
				f.releases.release = nil
			},
			want: "release runtime has no active version",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			f.installRelease()
			tc.set(f)
			code := f.run("--release", "runtime", "--schema-version", "0")
			if code != exitError {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
			}
			if !strings.Contains(f.stderr(), tc.want) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), tc.want)
			}
			if f.stdout() != "" {
				t.Fatalf("stdout = %q, want nothing written when verification fails", f.stdout())
			}
			if strings.Contains(f.stderr(), envTestStripeValue) {
				t.Fatalf("stderr leaked secret material: %s", f.stderr())
			}
		})
	}
}

func TestEnvReleaseRejectsTamperedManifestBeforeReads(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	f.installRelease()
	f.releases.release.MetadataJson = `{"tampered":true}`

	if code := f.run("--release", "runtime", "--schema-version", "0"); code != exitError {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
	}
	if !strings.Contains(f.stderr(), "release runtime: manifest digest mismatch") {
		t.Fatalf("stderr = %s", f.stderr())
	}
	if got := f.rec.count("GetParameter") + f.rec.count("GetSecretMetadata") + f.rec.count("GetSecret"); got != 0 {
		t.Fatalf("tampered release caused %d resource reads: %+v", got, f.rec.snapshot())
	}
	if f.stdout() != "" {
		t.Fatalf("stdout = %q, want no partial candidate", f.stdout())
	}
}

func TestEnvReleaseValidatesReturnedSchemaBeforeResourceReads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command       string
		requested, returned uint64
		wantSuccess         bool
	}{
		{name: "env explicit zero rejects schema seven", command: "env", requested: 0, returned: 7},
		{name: "env explicit seven rejects schema zero", command: "env", requested: 7, returned: 0},
		{name: "exec explicit zero rejects schema seven", command: "exec", requested: 0, returned: 7},
		{name: "exec explicit seven rejects schema zero", command: "exec", requested: 7, returned: 0},
		{name: "env matching positive schema", command: "env", requested: 7, returned: 7, wantSuccess: true},
		{name: "exec matching positive schema", command: "exec", requested: 7, returned: 7, wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var f *envFixture
			var launched func() bool
			if tc.command == "exec" {
				execFixture := newExecFixture(t, exitOK, nil)
				f = execFixture.envFixture
				launched = func() bool { return execFixture.launched.called }
			} else {
				f = newEnvFixture(t)
				launched = func() bool { return false }
			}
			f.installRelease()
			f.releases.release.SchemaVersion = tc.returned
			setEnvTestReleaseDigest(f.releases.release)
			selector := strconv.FormatUint(tc.requested, 10)
			var code int
			if tc.command == "exec" {
				code = f.runExec([]string{"--release", "runtime", "--schema-version", selector, "--no-secrets"}, "workload")
			} else {
				code = f.run("--release", "runtime", "--schema-version", selector, "--no-secrets")
			}
			if tc.wantSuccess {
				if code != exitOK {
					t.Fatalf("exit=%d stderr=%s", code, f.stderr())
				}
				if tc.command == "exec" && !launched() {
					t.Fatal("matching release schema did not launch child")
				}
				return
			}
			if code != exitError {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, exitError, f.stderr())
			}
			want := fmt.Sprintf("server returned schema %d, requested %d", tc.returned, tc.requested)
			if !strings.Contains(f.stderr(), want) {
				t.Fatalf("stderr=%q, want %q", f.stderr(), want)
			}
			if f.stdout() != "" {
				t.Fatalf("stdout=%q, want no values", f.stdout())
			}
			if got := f.rec.count("GetParameter") + f.rec.count("GetSecretMetadata") + f.rec.count("GetSecret"); got != 0 {
				t.Fatalf("foreign schema caused %d resource reads: %+v", got, f.rec.snapshot())
			}
			if launched() {
				t.Fatal("foreign schema launched child")
			}
		})
	}
}

// TestEnvReleaseRejectsForeignPinsBeforeReads proves the namespace boundary is
// checked for the complete manifest before any pinned resource is fetched.
// This avoids both partial candidate resolution and a foreign-resource oracle.
func TestEnvReleaseRejectsForeignPinsBeforeReads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(*kmsv1.ConfigurationRelease)
		want string
	}{
		{
			name: "release namespace",
			set: func(release *kmsv1.ConfigurationRelease) {
				release.Namespace = &kmsv1.NamespaceRef{Env: "staging", App: "app"}
			},
			want: "server returned a different namespace",
		},
		{
			name: "foreign entry",
			set: func(release *kmsv1.ConfigurationRelease) {
				release.Entries[1].Ref = envTestRef("shared", "data", "api/url")
			},
			want: "release entry api-url must reference its home namespace",
		},
		{
			name: "missing entry ref",
			set: func(release *kmsv1.ConfigurationRelease) {
				release.Entries[1].Ref = nil
			},
			want: "release entry api-url must reference its home namespace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			f.installRelease()
			tc.set(f.releases.release)
			if code := f.run("--release", "runtime", "--schema-version", "0"); code != exitError {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
			}
			if !strings.Contains(f.stderr(), tc.want) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), tc.want)
			}
			if got := f.rec.count("GetParameter") + f.rec.count("GetSecretMetadata") + f.rec.count("GetSecret"); got != 0 {
				t.Fatalf("malformed release caused %d resource reads: %+v", got, f.rec.snapshot())
			}
			if f.stdout() != "" {
				t.Fatalf("stdout = %q, want no partial candidate", f.stdout())
			}
		})
	}
}

// TestEnvPrefixAndReleaseConflict: a release fixes its own entries, so
// narrowing it with a key prefix is a category error rather than a filter.
func TestEnvPrefixAndReleaseConflict(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	if code := f.run("--release", "runtime", "--schema-version", "0", "--prefix", "db/"); code != exitUsage {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitUsage, f.stderr())
	}
	if !strings.Contains(f.stderr(), "--prefix and --release are mutually exclusive") {
		t.Fatalf("stderr = %s", f.stderr())
	}
	if n := f.rec.count("GetActiveRelease"); n != 0 {
		t.Fatalf("a usage error still called the server")
	}
}

func TestEnvIncompleteModeFlagConflicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "release is atomic",
			args: []string{"--release", "runtime", "--schema-version", "0", "--allow-incomplete-secrets"},
			want: "--allow-incomplete-secrets cannot be used with --release",
		},
		{
			name: "parameter-only mode is already complete",
			args: []string{"--no-secrets", "--allow-incomplete-secrets"},
			want: "--allow-incomplete-secrets and --no-secrets are mutually exclusive",
		},
		{
			name: "obsolete strict flag",
			args: []string{"--strict"},
			want: "flag provided but not defined: -strict",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			if code := f.run(tc.args...); code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitUsage, f.stderr())
			}
			if !strings.Contains(f.stderr(), tc.want) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), tc.want)
			}
			if len(f.rec.snapshot()) != 0 {
				t.Fatalf("usage error made RPCs: %+v", f.rec.snapshot())
			}
		})
	}
}

// --- output flags -----------------------------------------------------------

// TestEnvOutputFlagConflicts: the invalid combinations are refused before the
// command dials, so a mistyped invocation reads no secret.
func TestEnvOutputFlagConflicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "-o json with another format",
			args: []string{"-o", "json", "--format", "yaml"},
			want: "--output json and --format yaml conflict",
		},
		{
			name: "unknown format",
			args: []string{"--format", "toml"},
			want: "unknown --format \"toml\"",
		},
		{
			name: "--force without --out",
			args: []string{"--force"},
			want: "--force requires --out",
		},
		{
			name: "invalid --env-prefix",
			args: []string{"--env-prefix", "1app-"},
			want: "invalid --env-prefix",
		},
		{
			name: "invalid --prefix",
			args: []string{"--prefix", "../etc"},
			want: "invalid --prefix",
		},
		{
			name: "invalid --release",
			args: []string{"--release", "not a name", "--schema-version", "0"},
			want: "invalid --release",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			if code := f.run(tc.args...); code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitUsage, f.stderr())
			}
			if !strings.Contains(f.stderr(), tc.want) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), tc.want)
			}
			if len(f.rec.snapshot()) != 0 {
				t.Fatalf("a usage error still made RPCs: %+v", f.rec.snapshot())
			}
		})
	}
}

// TestEnvRequiresANamespace covers the positional argument contract.
func TestEnvRequiresANamespace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no namespace", args: []string{"env"}, want: "env requires an env/app namespace argument"},
		{name: "invalid namespace", args: []string{"env", "prod"}, want: "invalid namespace"},
		{name: "extra positional", args: []string{"env", "prod/app", "extra"}, want: "unexpected argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			if code := f.Run(tc.args); code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitUsage, f.stderr())
			}
			if !strings.Contains(f.stderr(), tc.want) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), tc.want)
			}
		})
	}
}

// --- --out ------------------------------------------------------------------

// TestEnvOutWritesAPrivateFile: --out is the recipe behind systemd's
// EnvironmentFile=, so the file must be owner-only and the staging entry the
// writer uses must never survive.
func TestEnvOutWritesAPrivateFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.env")
	f := newEnvFixture(t)
	code := f.run("--out", path)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	want := "DB_HOST=db.internal\nGREETING=\"hello world\"\nSESSION_SECRET=session-plaintext\nSTRIPE_KEY=sk-live-plaintext\n"
	if string(raw) != want {
		t.Fatalf("file = %q, want %q", raw, want)
	}
	if f.stdout() != "" {
		t.Fatalf("stdout = %q, want the values only in the file", f.stdout())
	}
	if !strings.Contains(f.stderr(), fmt.Sprintf("Wrote 4 variables to %s", path)) {
		t.Fatalf("stderr = %s", f.stderr())
	}
	assertNoStagingFiles(t, dir)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

// TestEnvOutJSONReportsTheFile: with --out the values live in the file, so
// the one JSON document stdout still owes names the file and the count --
// the same shape get-secret uses -- and never the assignments.
func TestEnvOutJSONReportsTheFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "app.env")
	f := newEnvFixture(t)
	code := f.run("--out", path, "--output", "json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	var document struct {
		OutFile   string `json:"out_file"`
		Variables int    `json:"variables"`
	}
	if err := json.Unmarshal([]byte(f.stdout()), &document); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, f.stdout())
	}
	if document.OutFile != path || document.Variables != 4 {
		t.Fatalf("document = %+v, want out_file=%s variables=4", document, path)
	}
	for _, forbidden := range []string{envTestSessionValue, envTestStripeValue, "STRIPE_KEY="} {
		if strings.Contains(f.stdout(), forbidden) {
			t.Fatalf("stdout carries %q alongside the file: %s", forbidden, f.stdout())
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// --output json selects the json format for the file as well.
	var vars map[string]string
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatalf("file is not JSON: %v\n%s", err, raw)
	}
	if vars["STRIPE_KEY"] != envTestStripeValue {
		t.Fatalf("file = %s", raw)
	}
}

// TestEnvOutRefusesToReplaceWithoutForce: overwriting an environment file is
// the kind of thing a scheduled job does by accident, so it is a conflict
// (exit 6) until --force says otherwise.
func TestEnvOutRefusesToReplaceWithoutForce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.env")
	if err := os.WriteFile(path, []byte("PREVIOUS=1\n"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}

	f := newEnvFixture(t)
	if code := f.run("--no-secrets", "--out", path); code != exitConflict {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitConflict, f.stderr())
	}
	if !strings.Contains(f.stderr(), "already exists (pass --force to replace it)") {
		t.Fatalf("stderr = %s", f.stderr())
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "PREVIOUS=1\n" {
		t.Fatalf("file = %q (err %v), want it untouched", raw, err)
	}
	assertNoStagingFiles(t, dir)

	forced := newEnvFixture(t)
	if code := forced.run("--no-secrets", "--out", path, "--force"); code != 0 {
		t.Fatalf("--force exit = %d, stderr=%s", code, forced.stderr())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != "DB_HOST=db.internal\nGREETING=\"hello world\"\n" {
		t.Fatalf("file = %q", raw)
	}
	assertNoStagingFiles(t, dir)
}

// TestEnvQuietSuppressesProgressOnly: --quiet is for the progress line and the
// encoding note, never for a warning that the environment is incomplete.
func TestEnvQuietSuppressesProgressOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.env")
	f := newEnvFixture(t)
	f.secrets.get["/prod/app/session-secret"].Value = []byte{0x00, 0x01, 0x02}
	f.secrets.list[1].Bound = true
	f.secrets.list[1].Versions[0].Bound = true
	if code := f.run("--allow-incomplete-secrets", "--quiet", "--out", path); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	if strings.Contains(f.stderr(), "Wrote") {
		t.Fatalf("--quiet kept the progress line: %s", f.stderr())
	}
	if strings.Contains(f.stderr(), "note:") {
		t.Fatalf("--quiet kept the encoding note: %s", f.stderr())
	}
	if !strings.Contains(f.stderr(), "warning: omitted unavailable secret /prod/app/stripe-key") {
		t.Fatalf("--quiet suppressed the skip warning: %s", f.stderr())
	}
}

// assertNoStagingFiles fails when the private-file writer left its staging
// entry behind: a .kms-env-* file would hold the same secrets at an
// unpredictable name.
func assertNoStagingFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".kms-env-") {
			t.Fatalf("staging file %s survived in %s", entry.Name(), dir)
		}
	}
}

// --- binary values and size caps --------------------------------------------

// TestEnvBinaryValueIsBase64WithANote: a value with a NUL byte cannot survive
// the environment, so it is renamed and encoded, and the operator is told.
func TestEnvBinaryValueIsBase64WithANote(t *testing.T) {
	t.Parallel()
	f := newEnvFixture(t)
	f.secrets.get["/prod/app/session-secret"].Value = []byte{0x00, 0xff, 0x10}
	if code := f.run(); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	if !strings.Contains(f.stdout(), "SESSION_SECRET_B64=AP8Q\n") {
		t.Fatalf("stdout = %q, want the base64 form", f.stdout())
	}
	if strings.Contains(f.stdout(), "SESSION_SECRET=") {
		t.Fatalf("stdout = %q, want only the _B64 name", f.stdout())
	}
	want := "note: session-secret is not text; injected base64-encoded as SESSION_SECRET_B64"
	if !strings.Contains(f.stderr(), want) {
		t.Fatalf("stderr = %s, want %q", f.stderr(), want)
	}
}

// TestEnvRejectsOversizedEntries: the kernel would refuse the exec with a bare
// E2BIG, so the CLI names the variable instead. The per-entry cap differs by
// platform; the total cap does not.
func TestEnvRejectsOversizedEntries(t *testing.T) {
	t.Parallel()

	t.Run("one variable over the entry cap", func(t *testing.T) {
		t.Parallel()
		f := newEnvFixture(t)
		f.params.list[0].Value = strings.Repeat("x", maxEnvEntryBytes+1)
		if code := f.run("--no-secrets"); code != exitError {
			t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
		}
		if !strings.Contains(f.stderr(), "environment variable DB_HOST is") {
			t.Fatalf("stderr = %s, want it to name DB_HOST", f.stderr())
		}
		if strings.Contains(f.stderr(), "xxxxxxxx") {
			t.Fatalf("stderr echoed the value: %s", f.stderr())
		}
	})

	t.Run("the whole environment over the total cap", func(t *testing.T) {
		t.Parallel()
		f := newEnvFixture(t)
		f.params.list = nil
		chunk := strings.Repeat("y", maxEnvEntryBytes/2)
		for i := range 2*maxEnvTotalBytes/maxEnvEntryBytes + 2 {
			f.params.list = append(f.params.list, &kmsv1.Parameter{
				Ref: envTestRef("prod", "app", fmt.Sprintf("bulk/item-%03d", i)), Value: chunk, ContentType: "string",
			})
		}
		if code := f.run("--no-secrets"); code != exitError {
			t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
		}
		if !strings.Contains(f.stderr(), "over the 2097152-byte total limit") {
			t.Fatalf("stderr = %s", f.stderr())
		}
	})
}

// --- stderr hygiene ---------------------------------------------------------

// TestEnvNeverPrintsSecretMaterialOnStderr: every diagnostic path names keys
// and variables; none of them may carry a value or a token.
func TestEnvNeverPrintsSecretMaterialOnStderr(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(*envFixture)
		args []string
	}{
		{name: "omitted secret", set: func(*envFixture) {}, args: []string{"--allow-incomplete-secrets"}},
		{
			name: "binary note",
			set: func(f *envFixture) {
				f.secrets.get["/prod/app/session-secret"].Value = append([]byte(envTestSessionValue), 0x00)
			},
			args: []string{},
		},
		{
			name: "server error",
			set: func(f *envFixture) {
				f.secrets.getErr["/prod/app/session-secret"] = status.Error(codes.Internal, "boom")
			},
			args: []string{},
		},
		{
			name: "oversized value",
			set: func(f *envFixture) {
				f.secrets.get["/prod/app/session-secret"].Value = []byte(strings.Repeat(envTestSessionValue, maxEnvEntryBytes))
			},
			args: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEnvFixture(t)
			tc.set(f)
			_ = f.run(tc.args...)
			for _, forbidden := range []string{envTestSessionValue, envTestStripeValue} {
				if strings.Contains(f.stderr(), forbidden) {
					t.Fatalf("stderr leaked %q: %s", forbidden, f.stderr())
				}
			}
		})
	}
}

// TestEnvHelpNeverPrintsTokenValues: flag help renders every default, so
// secretTokenList must print its keys and nothing else.

// --- unit-level checks ------------------------------------------------------

// TestEnvOutputAllowed is the terminal guard's whole truth table. It is
// consulted before anything is fetched, so a refused invocation reads no
// secret and leaves no audit rows.
func TestEnvOutputAllowed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		isTTY          bool
		mayHaveSecrets bool
		show           bool
		out            string
		want           bool
	}{
		// Not a terminal: piping is always fine.
		{isTTY: false, mayHaveSecrets: true, want: true},
		{isTTY: false, mayHaveSecrets: false, want: true},
		{isTTY: false, mayHaveSecrets: true, show: true, want: true},
		{isTTY: false, mayHaveSecrets: true, out: "f", want: true},
		// A terminal with secrets in the selection needs an explicit sink.
		{isTTY: true, mayHaveSecrets: true, want: false},
		{isTTY: true, mayHaveSecrets: true, show: true, want: true},
		{isTTY: true, mayHaveSecrets: true, out: "f", want: true},
		{isTTY: true, mayHaveSecrets: true, show: true, out: "f", want: true},
		// Parameters alone never need one.
		{isTTY: true, mayHaveSecrets: false, want: true},
		{isTTY: true, mayHaveSecrets: false, show: true, want: true},
		{isTTY: true, mayHaveSecrets: false, out: "f", want: true},
	} {
		got := envOutputAllowed(tc.isTTY, tc.mayHaveSecrets, tc.show, tc.out)
		if got != tc.want {
			t.Fatalf("envOutputAllowed(isTTY=%v, mayHaveSecrets=%v, show=%v, out=%q) = %v, want %v",
				tc.isTTY, tc.mayHaveSecrets, tc.show, tc.out, got, tc.want)
		}
	}
}

// TestEnvTerminalGuardRefusesBeforeAnyRPC drives the guard through the real
// command with a pseudo-terminal on stdout, which is the only way to reach
// stdoutIsTTY. Everything else is covered by the truth table above.
func TestEnvTerminalGuardRefusesBeforeAnyRPC(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/ptmx")
	}
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open a pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = pty.Close() })
	// Only the master side is opened, and only Linux answers isatty for it.
	// On Darwin the master is a cloning device that is not a terminal (the
	// slave would be), so the guard has nothing to refuse there — and a write
	// to a master whose slave was never opened blocks forever.
	if !term.IsTerminal(int(pty.Fd())) {
		t.Skip("the pseudo-terminal master is not a tty on this platform")
	}

	f := newEnvFixture(t)
	f.Stdout = pty
	code := f.Run([]string{"env", "prod/app", "--insecure", "--token", "id-token"})
	if code != exitError {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitError, f.stderr())
	}
	want := "refusing to print secrets to a terminal; pass --show to print, --out FILE to save, or --no-secrets"
	if !strings.Contains(f.stderr(), want) {
		t.Fatalf("stderr = %s, want %q", f.stderr(), want)
	}
	// The guard runs before the connection is opened, so nothing was read.
	if calls := f.rec.snapshot(); len(calls) != 0 {
		t.Fatalf("the refused invocation still made RPCs: %+v", calls)
	}

	// The three escape hatches all pass the guard on the same terminal.
	for _, args := range [][]string{
		{"--show"},
		{"--no-secrets"},
		{"--out", filepath.Join(t.TempDir(), "app.env")},
	} {
		allowed := newEnvFixture(t)
		allowed.Stdout = pty
		full := append([]string{"env", "prod/app", "--insecure", "--token", "id-token"}, args...)
		if code := allowed.Run(full); code != 0 {
			t.Fatalf("env %v exit = %d, stderr=%s", args, code, allowed.stderr())
		}
	}
}

// TestSecretItemNamesAcceptsEverySpelling checks the accepted token keys for a
// secret, including the rule that a cross-namespace release entry is not
// addressable by its bare relative key.

// TestSecretItemTokenEnvName: the variable follows the alias in release mode
// and the key otherwise, and carries neither --env-prefix nor _B64.

// TestSameRefComparesNamespaceAndKey guards the verification helper the
// release path depends on.
func TestSameRefComparesNamespaceAndKey(t *testing.T) {
	t.Parallel()
	base := envTestRef("prod", "app", "db/host")
	for _, tc := range []struct {
		name string
		a, b *kmsv1.ResourceRef
		want bool
	}{
		{name: "identical", a: base, b: envTestRef("prod", "app", "db/host"), want: true},
		{name: "different key", a: base, b: envTestRef("prod", "app", "db/port")},
		{name: "different env", a: base, b: envTestRef("staging", "app", "db/host")},
		{name: "different app", a: base, b: envTestRef("prod", "other", "db/host")},
		{name: "nil left", a: nil, b: base},
		{name: "nil right", a: base, b: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sameRef(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameRef = %v, want %v", got, tc.want)
			}
		})
	}
}
