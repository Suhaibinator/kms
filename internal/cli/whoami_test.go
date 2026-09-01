package cli

import (
	"context"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// whoAmIStub answers WhoAmI with a canned identity (or error) and records the
// authorization metadata each call carried, so credential handling is checked
// through the real client transport rather than by inspecting connFlags.
type whoAmIStub struct {
	kmsv1.UnimplementedAdminServiceServer
	mu       sync.Mutex
	auth     []string
	calls    int
	response *kmsv1.WhoAmIResponse
	err      error
}

func (s *whoAmIStub) WhoAmI(ctx context.Context, _ *kmsv1.WhoAmIRequest) (*kmsv1.WhoAmIResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	md, _ := metadata.FromIncomingContext(ctx)
	s.auth = append(s.auth, strings.Join(md.Get("authorization"), ","))
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

func (s *whoAmIStub) authorization(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

// newWhoAmICLI wires a CLI to a WhoAmI stub over the in-memory transport.
func newWhoAmICLI(t *testing.T, stub *whoAmIStub) *testCLI {
	t.Helper()
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterAdminServiceServer(s, stub) })
	return c
}

func TestWhoAmITablePrintsResolvedIdentity(t *testing.T) {
	stub := &whoAmIStub{response: &kmsv1.WhoAmIResponse{
		Name:       "gradethis-api",
		Kind:       "client",
		Namespace:  &kmsv1.NamespaceRef{Env: "prod", App: "gradethis"},
		AuthMethod: "mtls",
	}}
	c := newWhoAmICLI(t, stub)
	if code := c.Run([]string{"whoami", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("whoami exit = %d, stderr=%s", code, c.stderr())
	}
	want := "name: gradethis-api\nkind: client\nnamespace: prod/gradethis\nauth_method: mtls\n"
	if c.stdout() != want {
		t.Fatalf("whoami stdout = %q, want %q", c.stdout(), want)
	}
}

// An identity with no namespace binding is a real state (every admin is one),
// so the table says so rather than printing an empty field.
func TestWhoAmITableReportsUnboundNamespace(t *testing.T) {
	stub := &whoAmIStub{response: &kmsv1.WhoAmIResponse{Name: "root", Kind: "admin", AuthMethod: "token"}}
	c := newWhoAmICLI(t, stub)
	if code := c.Run([]string{"whoami", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("whoami exit = %d, stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "namespace: (unbound)\n") {
		t.Fatalf("whoami stdout = %q", c.stdout())
	}
}

func TestWhoAmIJSON(t *testing.T) {
	stub := &whoAmIStub{response: &kmsv1.WhoAmIResponse{
		Name:       "gradethis-api",
		Kind:       "client",
		Namespace:  &kmsv1.NamespaceRef{Env: "prod", App: "gradethis"},
		AuthMethod: "mtls",
	}}
	c := newWhoAmICLI(t, stub)
	if code := c.Run([]string{"-o", "json", "whoami", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("whoami exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "name", "kind", "namespace", "auth_method")
	if document["name"] != "gradethis-api" || document["kind"] != "client" || document["auth_method"] != "mtls" {
		t.Fatalf("whoami document = %v", document)
	}
	namespace, ok := document["namespace"].(map[string]any)
	if !ok {
		t.Fatalf("namespace = %#v, want an object", document["namespace"])
	}
	if namespace["env"] != "prod" || namespace["app"] != "gradethis" {
		t.Fatalf("namespace = %v", namespace)
	}
}

// An unbound identity must be null, not {"env":"","app":""}: a caller checking
// "am I namespace-scoped?" should not have to compare empty strings.
func TestWhoAmIJSONUnboundNamespaceIsNull(t *testing.T) {
	stub := &whoAmIStub{response: &kmsv1.WhoAmIResponse{Name: "root", Kind: "admin", AuthMethod: "token"}}
	c := newWhoAmICLI(t, stub)
	if code := c.Run([]string{"-o", "json", "whoami", "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("whoami exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	if namespace, present := document["namespace"]; !present || namespace != nil {
		t.Fatalf("namespace = %#v (present=%v), want null", namespace, present)
	}
}

func TestWhoAmIRejectsPositionalArgument(t *testing.T) {
	c := newWhoAmICLI(t, &whoAmIStub{response: &kmsv1.WhoAmIResponse{Name: "root"}})
	if code := c.Run([]string{"whoami", "extra"}); code != 2 {
		t.Fatalf("whoami exit = %d, want 2; stderr=%s", code, c.stderr())
	}
}

// The server's status code is the CLI's exit code: a rejected credential is 3,
// and an unreachable server is 8, so a script can tell "fix your token" from
// "retry later" without reading the message.
func TestWhoAmIExitCodesMirrorTheStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "unauthenticated", err: status.Error(codes.Unauthenticated, "invalid token"), wantCode: exitUnauthenticated},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "nope"), wantCode: exitPermissionDenied},
		{name: "unavailable", err: status.Error(codes.Unavailable, "not ready"), wantCode: exitUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newWhoAmICLI(t, &whoAmIStub{err: test.err})
			if code := c.Run([]string{"whoami", "--insecure", "--token", "t"}); code != test.wantCode {
				t.Fatalf("whoami exit = %d, want %d; stderr=%s", code, test.wantCode, c.stderr())
			}
			if c.stdout() != "" {
				t.Fatalf("failed whoami wrote to stdout: %q", c.stdout())
			}
		})
	}
}

// A transport that never comes up fails before any RPC. The dial error carries
// no status of its own, so exitCodeFor must still classify it as unavailable.
func TestWhoAmIDialFailureIsUnavailable(t *testing.T) {
	c := newTestCLI()
	c.dialOverride = func(*connFlags) (*grpc.ClientConn, error) {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	if code := c.Run([]string{"whoami", "--insecure"}); code != exitUnavailable {
		t.Fatalf("whoami exit = %d, want %d; stderr=%s", code, exitUnavailable, c.stderr())
	}
	if !strings.Contains(c.stderr(), "error: ") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

func TestUsageListsWhoAmI(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"help"}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(c.stderr(), "whoami                        Print the identity the server sees for this credential.") {
		t.Fatalf("usage does not list whoami:\n%s", c.stderr())
	}
}
