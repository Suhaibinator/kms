package cli

import (
	"context"
	"encoding/json/v2"
	"strings"
	"sync"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// adminStub serves the AdminService RPCs the JSON, exit-code, and confirmation
// tests exercise. Every mutating call is recorded so a refused confirmation can
// be shown to have reached the server not at all.
type adminStub struct {
	kmsv1.UnimplementedAdminServiceServer
	mu         sync.Mutex
	namespaces []*kmsv1.Namespace
	identities []*kmsv1.Identity
	// err, when set, is returned by every RPC below, so one stub covers each
	// status code the exit-code table asserts.
	err error

	deletedNamespaces []string
	createdNamespaces []string
	revokedIdentities []string
	revokedCerts      []string
	rotated           []string
}

func (s *adminStub) calls() (deletedNamespaces, revokedIdentities, revokedCerts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deletedNamespaces...),
		append([]string(nil), s.revokedIdentities...),
		append([]string(nil), s.revokedCerts...)
}

func (s *adminStub) ListNamespaces(context.Context, *kmsv1.ListNamespacesRequest) (*kmsv1.ListNamespacesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return &kmsv1.ListNamespacesResponse{Namespaces: s.namespaces}, nil
}

func (s *adminStub) ListIdentities(context.Context, *kmsv1.ListIdentitiesRequest) (*kmsv1.ListIdentitiesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return &kmsv1.ListIdentitiesResponse{Identities: s.identities}, nil
}

func (s *adminStub) CreateNamespace(_ context.Context, req *kmsv1.CreateNamespaceRequest) (*kmsv1.CreateNamespaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	ref := req.GetRef()
	s.createdNamespaces = append(s.createdNamespaces, ref.GetEnv()+"/"+ref.GetApp())
	methods := req.GetAllowedAuthMethods()
	if methods == nil {
		methods = []string{"mtls"}
	}
	return &kmsv1.CreateNamespaceResponse{Namespace: &kmsv1.Namespace{
		Ref: ref, Description: req.GetDescription(), AllowedAuthMethods: methods,
	}}, nil
}

func (s *adminStub) DeleteNamespace(_ context.Context, req *kmsv1.DeleteNamespaceRequest) (*kmsv1.DeleteNamespaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.deletedNamespaces = append(s.deletedNamespaces, req.GetRef().GetEnv()+"/"+req.GetRef().GetApp())
	return &kmsv1.DeleteNamespaceResponse{}, nil
}

func (s *adminStub) RevokeIdentity(_ context.Context, req *kmsv1.RevokeIdentityRequest) (*kmsv1.RevokeIdentityResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.revokedIdentities = append(s.revokedIdentities, req.GetName())
	return &kmsv1.RevokeIdentityResponse{}, nil
}

func (s *adminStub) RevokeIdentityCertificate(_ context.Context, req *kmsv1.RevokeIdentityCertificateRequest) (*kmsv1.RevokeIdentityCertificateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.revokedCerts = append(s.revokedCerts, req.GetName()+"/"+req.GetSerial())
	return &kmsv1.RevokeIdentityCertificateResponse{}, nil
}

func (s *adminStub) RotateIdentityToken(_ context.Context, req *kmsv1.RotateIdentityTokenRequest) (*kmsv1.RotateIdentityTokenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.rotated = append(s.rotated, req.GetName())
	return &kmsv1.RotateIdentityTokenResponse{Token: "kms_rotated"}, nil
}

// runAdminJSON runs one admin command against stub and returns its stdout.
func runAdminJSON(t *testing.T, stub kmsv1.AdminServiceServer, args ...string) (*testCLI, int) {
	t.Helper()
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterAdminServiceServer(s, stub) })
	return c, c.Run(args)
}

// assertJSONDocument fails unless stdout holds exactly the expected document:
// JSON mode promises one document and nothing else on stdout.
func assertJSONDocument(t *testing.T, c *testCLI, want string) {
	t.Helper()
	if got := c.stdout(); got != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", got, want)
	}
}

func TestAdminNamespaceListJSON(t *testing.T) {
	stub := &adminStub{namespaces: []*kmsv1.Namespace{
		{
			Ref: &kmsv1.NamespaceRef{Env: "prod", App: "gradethis"}, Description: "grading",
			AllowedAuthMethods: []string{"mtls", "token"}, ParameterCount: 3, SecretCount: 1,
		},
		{Ref: &kmsv1.NamespaceRef{Env: "dev", App: "sandbox"}},
	}}
	c, code := runAdminJSON(t, stub, "-o", "json", "admin", "namespace", "list", "--insecure", "--token", "admin-token")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	assertJSONDocument(t, c, `{
  "items": [
    {
      "env": "prod",
      "app": "gradethis",
      "auth_methods": [
        "mtls",
        "token"
      ],
      "parameter_count": 3,
      "secret_count": 1,
      "description": "grading"
    },
    {
      "env": "dev",
      "app": "sandbox",
      "auth_methods": [],
      "parameter_count": 0,
      "secret_count": 0,
      "description": ""
    }
  ]
}
`)
}

// TestAdminNamespaceListJSONEmpty pins the rule that a list is never null: a
// script may range over items without a nil check.
func TestAdminNamespaceListJSONEmpty(t *testing.T) {
	c, code := runAdminJSON(t, &adminStub{}, "admin", "namespace", "list", "--output", "json", "--insecure")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	assertJSONDocument(t, c, "{\n  \"items\": []\n}\n")
}

func TestAdminIdentityListJSON(t *testing.T) {
	stub := &adminStub{identities: []*kmsv1.Identity{
		{
			Name: "svc", Kind: "client", HasToken: true,
			Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "gradethis"},
			Certs:     []*kmsv1.IdentityCertInfo{{Serial: "s1"}, {Serial: "s2"}},
		},
		{Name: "ops", Kind: "admin", Disabled: true},
	}}
	c, code := runAdminJSON(t, stub, "-o", "json", "admin", "identity", "list", "--insecure", "--token", "admin-token")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	assertJSONDocument(t, c, `{
  "items": [
    {
      "name": "svc",
      "kind": "client",
      "namespace": {
        "env": "prod",
        "app": "gradethis"
      },
      "has_token": true,
      "cert_count": 2,
      "disabled": false
    },
    {
      "name": "ops",
      "kind": "admin",
      "namespace": null,
      "has_token": false,
      "cert_count": 0,
      "disabled": true
    }
  ]
}
`)
}

func TestAdminPolicyListJSON(t *testing.T) {
	stub := &policyAdminStub{policies: []*kmsv1.Policy{
		{Name: "ci-verify", Subject: "ci", Allow: []*kmsv1.PolicyRule{
			{Operation: "configuration-release:verify-defaults", Env: "prod", App: "gradethis"},
		}},
		{Name: "lockdown", Subject: "*", Deny: []*kmsv1.PolicyRule{{Operation: "secret:*", Env: "prod", App: "*"}}},
	}}
	c, code := runAdminJSON(t, stub, "-o", "json", "admin", "policy", "list", "--insecure", "--token", "admin-token")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	assertJSONDocument(t, c, `{
  "items": [
    {
      "name": "ci-verify",
      "subject": "ci",
      "allow": [
        "configuration-release:verify-defaults@prod/gradethis"
      ],
      "deny": []
    },
    {
      "name": "lockdown",
      "subject": "*",
      "allow": [],
      "deny": [
        "secret:*@prod/*"
      ]
    }
  ]
}
`)
}

// TestAdminNamespaceTableOutputUnchanged keeps the historical table byte for
// byte: scripts parse it, and the JSON mode exists so they do not have to.
func TestAdminNamespaceListTableUnchanged(t *testing.T) {
	stub := &adminStub{namespaces: []*kmsv1.Namespace{
		{Ref: &kmsv1.NamespaceRef{Env: "prod", App: "gradethis"}, Description: "grading",
			AllowedAuthMethods: []string{"mtls"}, ParameterCount: 3, SecretCount: 1},
	}}
	c, code := runAdminJSON(t, stub, "admin", "namespace", "list", "--insecure")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	want := "NAMESPACE       AUTH  PARAMS  SECRETS  DESCRIPTION\nprod/gradethis  mtls  3       1        grading\n"
	if got := c.stdout(); got != want {
		t.Fatalf("table stdout = %q, want %q", got, want)
	}
}

// TestIdentityCreateJSONCarriesTokenOnce: the one-time token appears in the
// document and nowhere else on stdout, while the warning that makes it
// actionable stays on stderr, where --quiet cannot reach it.
func TestIdentityCreateJSONCarriesTokenOnce(t *testing.T) {
	outDir := t.TempDir()
	stub := &identityAdminStub{}
	c, code := runAdminJSON(t, stub, "-o", "json", "admin", "identity", "create", "svc",
		"--namespace", "prod/gradethis", "--auth", "both", "--out", outDir,
		"--insecure", "--token", "admin-token")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	out := c.stdout()
	if got := strings.Count(out, "kms_stub_token"); got != 1 {
		t.Fatalf("token occurrences on stdout = %d, want 1:\n%s", got, out)
	}
	if strings.Contains(out, "PRIVATE KEY") || strings.Contains(out, "BEGIN CERTIFICATE") {
		t.Fatalf("JSON document carried PEM material:\n%s", out)
	}
	if !strings.Contains(c.stderr(), "WARNING: the token is shown once") {
		t.Fatalf("stderr missing the one-time token warning: %s", c.stderr())
	}

	var doc struct {
		Name        string   `json:"name"`
		Kind        string   `json:"kind"`
		AuthMethods []string `json:"auth_methods"`
		Token       string   `json:"token"`
		Cert        struct {
			CertFile string `json:"cert_file"`
			KeyFile  string `json:"key_file"`
			Serial   string `json:"serial"`
		} `json:"cert"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if doc.Name != "svc" || doc.Kind != "client" || doc.Token != "kms_stub_token" || doc.Cert.Serial != "s1" {
		t.Fatalf("document = %+v", doc)
	}
	if strings.Join(doc.AuthMethods, ",") != "mtls,token" {
		t.Fatalf("auth_methods = %v", doc.AuthMethods)
	}
	// The paths in the document are the files that were actually written.
	if got := readFileString(t, doc.Cert.CertFile); !strings.Contains(got, "BEGIN CERTIFICATE") {
		t.Fatalf("cert_file %s = %q", doc.Cert.CertFile, got)
	}
	if got := readFileString(t, doc.Cert.KeyFile); !strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("key_file %s = %q", doc.Cert.KeyFile, got)
	}
}

// TestIdentityCreateJSONRefusesToDropThePrivateKey: without --out the table
// mode prints the one-time key to stdout, which JSON mode must not do. Dropping
// it silently would enroll a certificate whose key nobody holds, so the command
// refuses before the RPC.
func TestIdentityCreateJSONRefusesToDropThePrivateKey(t *testing.T) {
	stub := &identityAdminStub{}
	c, code := runAdminJSON(t, stub, "-o", "json", "admin", "identity", "create", "svc", "--insecure")
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "--out is required with --output json") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if c.stdout() != "" {
		t.Fatalf("refused command wrote stdout: %q", c.stdout())
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("refused command reached the server: %+v", reqs)
	}
}

func TestIdentityRotateJSON(t *testing.T) {
	c, code := runAdminJSON(t, &adminStub{}, "-o", "json", "admin", "identity", "rotate", "svc", "--insecure")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	assertJSONDocument(t, c, "{\n  \"name\": \"svc\",\n  \"token\": \"kms_rotated\"\n}\n")
	if !strings.Contains(c.stderr(), "WARNING: this token is shown once") {
		t.Fatalf("stderr missing the one-time token warning: %s", c.stderr())
	}
}

func TestAdminNamespaceWriteAndDeleteJSON(t *testing.T) {
	stub := &adminStub{}
	c, code := runAdminJSON(t, stub, "-o", "json", "admin", "namespace", "create",
		"--env", "prod", "--app", "gradethis", "--auth-methods", "mtls,token", "--insecure")
	if code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, c.stderr())
	}
	assertJSONDocument(t, c, `{
  "env": "prod",
  "app": "gradethis",
  "auth_methods": [
    "mtls",
    "token"
  ]
}
`)

	c, code = runAdminJSON(t, stub, "-o", "json", "-y", "admin", "namespace", "delete",
		"--env", "prod", "--app", "gradethis", "--insecure")
	if code != 0 {
		t.Fatalf("delete exit=%d stderr=%s", code, c.stderr())
	}
	assertJSONDocument(t, c, "{\n  \"env\": \"prod\",\n  \"app\": \"gradethis\",\n  \"deleted\": true\n}\n")
	if !strings.Contains(c.stderr(), "Deleted namespace prod/gradethis") {
		t.Fatalf("stderr missing the status line: %s", c.stderr())
	}
}

// TestAdminQuietSilencesStatusButNotWarnings pins rule 6: --quiet drops advice,
// never a one-time-credential warning.
func TestAdminQuietSilencesStatusButNotWarnings(t *testing.T) {
	c, code := runAdminJSON(t, &adminStub{}, "-o", "json", "-q", "-y", "admin", "namespace", "delete",
		"--env", "prod", "--app", "gradethis", "--insecure")
	if code != 0 {
		t.Fatalf("delete exit=%d stderr=%s", code, c.stderr())
	}
	if c.stderr() != "" {
		t.Fatalf("--quiet left informational stderr: %q", c.stderr())
	}

	c, code = runAdminJSON(t, &adminStub{}, "-o", "json", "-q", "admin", "identity", "rotate", "svc", "--insecure")
	if code != 0 {
		t.Fatalf("rotate exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "shown once") {
		t.Fatalf("--quiet silenced a one-time token warning: %q", c.stderr())
	}
}

// TestAdminExitCodesMirrorTheServerStatus: scripts branch on the exit code, so
// each gRPC status the server can return has to arrive as its own number.
func TestAdminExitCodesMirrorTheServerStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		args []string
		want int
	}{
		{
			name: "not found",
			err:  status.Error(codes.NotFound, "identity ghost: not found"),
			args: []string{"admin", "identity", "revoke", "ghost", "--yes", "--insecure"},
			want: exitNotFound,
		},
		{
			name: "already exists",
			err:  status.Error(codes.AlreadyExists, "namespace prod/gradethis already exists"),
			args: []string{"admin", "namespace", "create", "--env", "prod", "--app", "gradethis", "--insecure"},
			want: exitConflict,
		},
		{
			name: "permission denied",
			err:  status.Error(codes.PermissionDenied, "identity ci may not list namespaces"),
			args: []string{"admin", "namespace", "list", "--insecure"},
			want: exitPermissionDenied,
		},
		{
			name: "unauthenticated",
			err:  status.Error(codes.Unauthenticated, "no credential"),
			args: []string{"admin", "identity", "list", "--insecure"},
			want: exitUnauthenticated,
		},
		{
			name: "invalid argument stays a generic error",
			err:  status.Error(codes.InvalidArgument, "bad namespace"),
			args: []string{"admin", "namespace", "list", "--insecure"},
			want: exitError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, code := runAdminJSON(t, &adminStub{err: test.err}, test.args...)
			if code != test.want {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, test.want, c.stderr())
			}
			if !strings.HasPrefix(c.stderr(), "error: ") {
				t.Fatalf("stderr = %q, want an error line", c.stderr())
			}
		})
	}
}

// TestAdminDestructiveCommandsRequireConfirmation: on a non-interactive stdin
// the three online destructive commands refuse with the usage code and reach
// the server not at all; --yes is the only way to run them from a script.
func TestAdminDestructiveCommandsRequireConfirmation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, stub *adminStub, ran bool)
	}{
		{
			name:    "namespace delete",
			args:    []string{"admin", "namespace", "delete", "--env", "prod", "--app", "gradethis", "--insecure"},
			wantErr: "refusing to delete namespace prod/gradethis without --yes",
			check: func(t *testing.T, stub *adminStub, ran bool) {
				t.Helper()
				deleted, _, _ := stub.calls()
				if ran != (len(deleted) == 1) {
					t.Fatalf("deleted namespaces = %v, ran = %t", deleted, ran)
				}
			},
		},
		{
			name:    "identity revoke",
			args:    []string{"admin", "identity", "revoke", "svc", "--insecure"},
			wantErr: "refusing to revoke identity svc without --yes",
			check: func(t *testing.T, stub *adminStub, ran bool) {
				t.Helper()
				_, revoked, _ := stub.calls()
				if ran != (len(revoked) == 1) {
					t.Fatalf("revoked identities = %v, ran = %t", revoked, ran)
				}
			},
		},
		{
			name:    "identity revoke-cert",
			args:    []string{"admin", "identity", "revoke-cert", "svc", "--serial", "7f3a", "--insecure"},
			wantErr: "refusing to revoke certificate 7f3a of identity svc without --yes",
			check: func(t *testing.T, stub *adminStub, ran bool) {
				t.Helper()
				_, _, certs := stub.calls()
				if ran != (len(certs) == 1) {
					t.Fatalf("revoked certificates = %v, ran = %t", certs, ran)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &adminStub{}
			c, code := runAdminJSON(t, stub, test.args...)
			if code != exitUsage {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, exitUsage, c.stderr())
			}
			if !strings.Contains(c.stderr(), test.wantErr) {
				t.Fatalf("stderr = %q, want %q", c.stderr(), test.wantErr)
			}
			if c.stdout() != "" {
				t.Fatalf("refused command wrote stdout: %q", c.stdout())
			}
			test.check(t, stub, false)

			stub = &adminStub{}
			c, code = runAdminJSON(t, stub, append(append([]string(nil), test.args...), "--yes")...)
			if code != 0 {
				t.Fatalf("--yes exit=%d stderr=%s", code, c.stderr())
			}
			test.check(t, stub, true)
		})
	}
}

// TestAdminNamespaceDeleteTypedConfirmation covers the interactive path: the
// operator retypes the namespace to proceed, and a mistyped answer aborts
// without touching the server.
func TestAdminNamespaceDeleteTypedConfirmation(t *testing.T) {
	for _, test := range []struct {
		name     string
		typed    string
		wantExit int
		wantRun  bool
	}{
		{name: "match", typed: "prod/gradethis\n", wantExit: 0, wantRun: true},
		{name: "mismatch", typed: "prod/other\n", wantExit: exitUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &adminStub{}
			c := newTestCLI()
			c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterAdminServiceServer(s, stub) })
			interactive(t, c, test.typed)
			code := c.Run([]string{"admin", "namespace", "delete", "--env", "prod", "--app", "gradethis", "--insecure"})
			if code != test.wantExit {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, test.wantExit, c.stderr())
			}
			if !strings.Contains(c.stderr(), `Type "prod/gradethis" to confirm`) {
				t.Fatalf("stderr missing the prompt: %s", c.stderr())
			}
			deleted, _, _ := stub.calls()
			if test.wantRun != (len(deleted) == 1) {
				t.Fatalf("deleted namespaces = %v, want run = %t", deleted, test.wantRun)
			}
			if !test.wantRun && !strings.Contains(c.stderr(), "does not match") {
				t.Fatalf("mismatch was not reported: %s", c.stderr())
			}
		})
	}
}
