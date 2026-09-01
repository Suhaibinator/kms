package cli

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// --- shared JSON assertions ------------------------------------------------

// decodeJSONStdout parses the command's stdout as the single JSON document
// JSON mode promises. It fails the test when stdout holds anything else, which
// is what makes "nothing but the document reaches stdout" checkable.
func decodeJSONStdout(t *testing.T, c *testCLI) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(c.stdout()), &document); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\nstdout=%q\nstderr=%s", err, c.stdout(), c.stderr())
	}
	return document
}

// assertJSONFields checks that the document carries exactly the named keys, so
// a field renamed or dropped by accident breaks the contract loudly.
func assertJSONFields(t *testing.T, document map[string]any, want ...string) {
	t.Helper()
	if len(document) != len(want) {
		t.Fatalf("document keys = %v, want exactly %v", sortedKeys(document), want)
	}
	for _, key := range want {
		if _, ok := document[key]; !ok {
			t.Fatalf("document is missing %q: keys = %v", key, sortedKeys(document))
		}
	}
}

func sortedKeys(document map[string]any) []string {
	keys := make([]string, 0, len(document))
	for k := range document {
		keys = append(keys, k)
	}
	return keys
}

// --- stubs ------------------------------------------------------------------

// parameterStub answers the two ParameterService calls the convenience
// commands make. ListParameters deliberately pages so the client's paging loop
// is exercised.
type parameterStub struct {
	kmsv1.UnimplementedParameterServiceServer
	mu         sync.Mutex
	auth       []string
	parameters []*kmsv1.Parameter
	pages      int
	putResp    *kmsv1.PutParameterResponse
	err        error
}

func (s *parameterStub) record(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.auth = append(s.auth, strings.Join(md.Get("authorization"), ","))
}

func (s *parameterStub) ListParameters(ctx context.Context, req *kmsv1.ListParametersRequest) (*kmsv1.ListParametersResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(ctx)
	s.pages++
	if s.err != nil {
		return nil, s.err
	}
	// Two pages: the first carries one parameter and a token, the second the rest.
	if req.GetPageToken() == "" && len(s.parameters) > 1 {
		return &kmsv1.ListParametersResponse{Parameters: s.parameters[:1], NextPageToken: "page-2"}, nil
	}
	if req.GetPageToken() == "page-2" {
		return &kmsv1.ListParametersResponse{Parameters: s.parameters[1:]}, nil
	}
	return &kmsv1.ListParametersResponse{Parameters: s.parameters}, nil
}

func (s *parameterStub) PutParameter(ctx context.Context, _ *kmsv1.PutParameterRequest) (*kmsv1.PutParameterResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return s.putResp, nil
}

// secretStub answers the SecretService calls the convenience commands make.
type secretStub struct {
	kmsv1.UnimplementedSecretServiceServer
	mu      sync.Mutex
	auth    []string
	secrets []*kmsv1.SecretMetadata
	getResp *kmsv1.GetSecretResponse
	putResp *kmsv1.PutSecretResponse
	err     error
	getErr  error
}

func (s *secretStub) record(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.auth = append(s.auth, strings.Join(md.Get("authorization"), ","))
}

func (s *secretStub) ListSecrets(ctx context.Context, _ *kmsv1.ListSecretsRequest) (*kmsv1.ListSecretsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return &kmsv1.ListSecretsResponse{Secrets: s.secrets}, nil
}

func (s *secretStub) GetSecret(ctx context.Context, _ *kmsv1.GetSecretRequest) (*kmsv1.GetSecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(ctx)
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getResp, nil
}

func (s *secretStub) PutSecret(ctx context.Context, _ *kmsv1.PutSecretRequest) (*kmsv1.PutSecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(ctx)
	if s.err != nil {
		return nil, s.err
	}
	return s.putResp, nil
}

// newConvenienceCLI wires a CLI to both data-plane stubs over the in-memory
// transport.
func newConvenienceCLI(t *testing.T, params *parameterStub, secrets *secretStub) *testCLI {
	t.Helper()
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(s *grpc.Server) {
		kmsv1.RegisterParameterServiceServer(s, params)
		kmsv1.RegisterSecretServiceServer(s, secrets)
	})
	return c
}

func ref(env, app, key string) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: env, App: app}, Key: key}
}

// --- list -------------------------------------------------------------------

func listStubs() (*parameterStub, *secretStub) {
	return &parameterStub{parameters: []*kmsv1.Parameter{
		{Ref: ref("prod", "gradethis", "greeting"), Version: 3, ContentType: "string"},
		{Ref: ref("prod", "gradethis", "settings"), Version: 1, ContentType: "json"},
	}}, &secretStub{secrets: []*kmsv1.SecretMetadata{
		{Ref: ref("prod", "gradethis", "db-password"), Labels: map[string]uint64{"current": 7}},
		{Ref: ref("prod", "gradethis", "webhook"), ClientBound: true, Labels: map[string]uint64{"current": 2}},
	}}
}

// The table is a published interface: scripts parse these columns, so its
// bytes must not drift when the JSON branch is added beside it.
func TestListTableIsUnchanged(t *testing.T) {
	params, secrets := listStubs()
	c := newConvenienceCLI(t, params, secrets)
	if code := c.Run([]string{"list", "prod/gradethis", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, c.stderr())
	}
	want := strings.Join([]string{
		"TYPE       PATH                         CURRENT  NOTE",
		"parameter  /prod/gradethis/greeting     3        string",
		"parameter  /prod/gradethis/settings     1        json",
		"secret     /prod/gradethis/db-password  7        standard",
		"secret     /prod/gradethis/webhook      2        client-bound",
		"",
	}, "\n")
	if c.stdout() != want {
		t.Fatalf("list stdout =\n%q\nwant\n%q", c.stdout(), want)
	}
}

func TestListJSONShape(t *testing.T) {
	params, secrets := listStubs()
	c := newConvenienceCLI(t, params, secrets)
	if code := c.Run([]string{"-o", "json", "list", "prod/gradethis", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	// list drains every page itself, so the envelope reports no continuation:
	// the items array is always the complete result.
	assertJSONFields(t, document, "items")
	if _, present := document["next_page_token"]; present {
		t.Fatalf("a fully drained listing carries next_page_token: %v", document)
	}
	items, ok := document["items"].([]any)
	if !ok || len(items) != 4 {
		t.Fatalf("items = %#v, want 4 rows", document["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("items[0] = %#v", items[0])
	}
	assertJSONFields(t, first, "type", "path", "current", "note", "client_bound")
	if first["type"] != "parameter" || first["path"] != "/prod/gradethis/greeting" || first["note"] != "string" {
		t.Fatalf("items[0] = %v", first)
	}
	if current, _ := first["current"].(float64); current != 3 {
		t.Fatalf("items[0].current = %v, want 3", first["current"])
	}
	// The paging loop ran: the second parameter page only arrives when the
	// client follows the token.
	if params.pages != 2 {
		t.Fatalf("ListParameters called %d times, want 2 (paged)", params.pages)
	}
	clientBound, ok := items[3].(map[string]any)
	if !ok || clientBound["client_bound"] != true || clientBound["note"] != "client-bound" {
		t.Fatalf("items[3] = %#v, want the client-bound secret", items[3])
	}
	if items[2].(map[string]any)["client_bound"] != false {
		t.Fatalf("a standard secret reported client_bound: %v", items[2])
	}
}

// An empty namespace must render as [], never null: a consumer should be able
// to range over items without a nil check.
func TestListJSONEmptyNamespaceIsAnEmptyArray(t *testing.T) {
	c := newConvenienceCLI(t, &parameterStub{}, &secretStub{})
	if code := c.Run([]string{"-o", "json", "list", "prod/gradethis", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("list exit = %d, stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), `"items": []`) {
		t.Fatalf("empty listing = %q", c.stdout())
	}
}

func TestListSurfacesTheServerStatusAsTheExitCode(t *testing.T) {
	c := newConvenienceCLI(t, &parameterStub{err: status.Error(codes.PermissionDenied, "nope")}, &secretStub{})
	if code := c.Run([]string{"list", "prod/gradethis", "--insecure", "--token", "t"}); code != exitPermissionDenied {
		t.Fatalf("list exit = %d, want %d; stderr=%s", code, exitPermissionDenied, c.stderr())
	}
}

// --- get-secret -------------------------------------------------------------

func TestGetSecretNotFoundExitsFive(t *testing.T) {
	secrets := &secretStub{getErr: status.Error(codes.NotFound, "no such secret")}
	c := newConvenienceCLI(t, &parameterStub{}, secrets)
	if code := c.Run([]string{"get-secret", "/prod/gradethis/missing", "--insecure", "--token", "t"}); code != exitNotFound {
		t.Fatalf("get-secret exit = %d, want %d; stderr=%s", code, exitNotFound, c.stderr())
	}
	if c.stdout() != "" {
		t.Fatalf("failed get-secret wrote to stdout: %q", c.stdout())
	}
	if !strings.Contains(c.stderr(), "error: get-secret: ") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

func TestGetSecretJSONCarriesTheValueOnce(t *testing.T) {
	secrets := &secretStub{getResp: &kmsv1.GetSecretResponse{
		Ref: ref("prod", "gradethis", "db-password"), Version: 7,
		Value: []byte("hunter2"), ContentType: "text/plain", CreatedAtUnixMs: 1_700_000_000_000,
	}}
	c := newConvenienceCLI(t, &parameterStub{}, secrets)
	if code := c.Run([]string{"-o", "json", "get-secret", "/prod/gradethis/db-password", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("get-secret exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "key", "version", "value", "content_type", "created_at")
	if document["key"] != "/prod/gradethis/db-password" || document["value"] != "hunter2" {
		t.Fatalf("document = %v", document)
	}
	if document["created_at"] != "2023-11-14T22:13:20Z" {
		t.Fatalf("created_at = %v", document["created_at"])
	}
	if strings.Count(c.stdout(), "hunter2") != 1 {
		t.Fatalf("secret value appears more than once: %q", c.stdout())
	}
}

// --out wins over stdout in both modes: the bytes go to the file and the
// document names it instead of repeating the secret.
func TestGetSecretJSONWithOutNamesTheFileAndOmitsTheValue(t *testing.T) {
	out := filepath.Join(t.TempDir(), "secret.txt")
	secrets := &secretStub{getResp: &kmsv1.GetSecretResponse{
		Ref: ref("prod", "gradethis", "db-password"), Version: 7, Value: []byte("hunter2"), ContentType: "text/plain",
	}}
	c := newConvenienceCLI(t, &parameterStub{}, secrets)
	if code := c.Run([]string{"-o", "json", "get-secret", "/prod/gradethis/db-password", "--out", out, "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("get-secret exit = %d, stderr=%s", code, c.stderr())
	}
	if got := readFileString(t, out); got != "hunter2" {
		t.Fatalf("--out file = %q", got)
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "key", "version", "value", "content_type", "created_at", "out_file")
	if document["value"] != nil {
		t.Fatalf("value = %#v, want null when the bytes went to --out", document["value"])
	}
	if document["out_file"] != out {
		t.Fatalf("out_file = %v, want %s", document["out_file"], out)
	}
	if strings.Contains(c.stdout(), "hunter2") {
		t.Fatalf("--out mode leaked the secret to stdout: %q", c.stdout())
	}
	// The "Wrote N bytes" progress line is informational and belongs on stderr.
	if !strings.Contains(c.stderr(), "Wrote 7 bytes to "+out) {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

// --quiet silences progress, never the result: the document still arrives.
func TestGetSecretQuietSilencesOnlyTheProgressLine(t *testing.T) {
	out := filepath.Join(t.TempDir(), "secret.txt")
	secrets := &secretStub{getResp: &kmsv1.GetSecretResponse{
		Ref: ref("prod", "gradethis", "db-password"), Version: 7, Value: []byte("hunter2"),
	}}
	c := newConvenienceCLI(t, &parameterStub{}, secrets)
	if code := c.Run([]string{"-o", "json", "-q", "get-secret", "/prod/gradethis/db-password", "--out", out, "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("get-secret exit = %d, stderr=%s", code, c.stderr())
	}
	if strings.Contains(c.stderr(), "Wrote") {
		t.Fatalf("--quiet did not silence the progress line: %q", c.stderr())
	}
	if _, ok := decodeJSONStdout(t, c)["key"]; !ok {
		t.Fatalf("--quiet suppressed the result document: %q", c.stdout())
	}
}

// --- put-secret / put-parameter --------------------------------------------

func TestPutSecretJSONCarriesTheAccessTokenOnceWithTheWarningOnStderr(t *testing.T) {
	secrets := &secretStub{putResp: &kmsv1.PutSecretResponse{Version: 4, Revision: 11, AccessToken: "kmss_generated"}}
	c := newConvenienceCLI(t, &parameterStub{}, secrets)
	c.Stdin = nil
	valueFile := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(valueFile, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := c.Run([]string{"-o", "json", "put-secret", "/prod/gradethis/db-password",
		"--value-file", valueFile, "--generate-token", "--insecure", "--token", "t"})
	if code != 0 {
		t.Fatalf("put-secret exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "key", "version", "revision", "access_token")
	if document["access_token"] != "kmss_generated" || document["key"] != "/prod/gradethis/db-password" {
		t.Fatalf("document = %v", document)
	}
	if strings.Count(c.stdout(), "kmss_generated") != 1 {
		t.Fatalf("access token appears more than once on stdout: %q", c.stdout())
	}
	// The one-time warning is security-relevant, so it is on stderr and is
	// never routed through info.
	if !strings.Contains(c.stderr(), "WARNING: the access token is shown once") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

// Without --generate-token there is no token, and the field is absent rather
// than an empty string a script might mistake for a credential.
func TestPutSecretJSONOmitsAnAbsentAccessToken(t *testing.T) {
	secrets := &secretStub{putResp: &kmsv1.PutSecretResponse{Version: 4, Revision: 11}}
	c := newConvenienceCLI(t, &parameterStub{}, secrets)
	valueFile := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(valueFile, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := c.Run([]string{"-o", "json", "put-secret", "/prod/gradethis/db-password",
		"--value-file", valueFile, "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("put-secret exit = %d, stderr=%s", code, c.stderr())
	}
	assertJSONFields(t, decodeJSONStdout(t, c), "key", "version", "revision")
}

func TestPutParameterJSON(t *testing.T) {
	params := &parameterStub{putResp: &kmsv1.PutParameterResponse{Version: 2, Revision: 9}}
	c := newConvenienceCLI(t, params, &secretStub{})
	if code := c.Run([]string{"-o", "json", "put-parameter", "/prod/gradethis/greeting", "hello",
		"--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("put-parameter exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "key", "version", "revision")
	if document["key"] != "/prod/gradethis/greeting" {
		t.Fatalf("document = %v", document)
	}
}

func TestPutParameterSurfacesAlreadyExistsAsConflict(t *testing.T) {
	params := &parameterStub{err: status.Error(codes.AlreadyExists, "exists")}
	c := newConvenienceCLI(t, params, &secretStub{})
	if code := c.Run([]string{"put-parameter", "/prod/gradethis/greeting", "hello",
		"--insecure", "--token", "t"}); code != exitConflict {
		t.Fatalf("put-parameter exit = %d, want %d; stderr=%s", code, exitConflict, c.stderr())
	}
}
