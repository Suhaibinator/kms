package cli

import (
	"slices"
	"strings"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/config"
	"google.golang.org/grpc"
)

// connEnvLookup builds the CLI's environment lookup from a map, so a client
// test sees exactly the named variables and never the developer's shell.
func connEnvLookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

// TestConnFlagsEnvFallback runs a command end to end with KMS_TOKEN set but no
// --token, then with both, to pin the precedence the flags promise: flag beats
// environment. admin policy list is used because it dials through dialConn, so
// the stub transport is honoured while authCtx still resolves the credential.
func TestConnFlagsEnvFallback(t *testing.T) {
	stub := &policyAdminStub{}
	dial := startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterAdminServiceServer(s, stub) })
	env := map[string]string{
		"KMS_TOKEN":    "env-token",
		"KMS_ENDPOINT": "ignored-by-override:1",
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "environment", args: []string{"admin", "policy", "list", "--insecure"}},
		{name: "flag", args: []string{"admin", "policy", "list", "--insecure", "--token", "flag-token"}},
	} {
		c := newTestCLI()
		c.lookupEnv = connEnvLookup(env)
		c.dialOverride = dial
		if code := c.Run(tc.args); code != 0 {
			t.Fatalf("%s: exit = %d, stderr=%s", tc.name, code, c.stderr())
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if want := []string{"Bearer env-token", "Bearer flag-token"}; !slices.Equal(stub.auth, want) {
		t.Fatalf("authorization metadata = %q, want %q", stub.auth, want)
	}
}

// TestConnFlagsEnvFillsEveryField covers each connection setting's fallback, so
// a field added to connFlags without a matching entry in envFallbacks is caught.
func TestConnFlagsEnvFillsEveryField(t *testing.T) {
	c := newTestCLI()
	c.lookupEnv = connEnvLookup(map[string]string{
		"KMS_ENDPOINT":         "kms.internal:9443",
		"KMS_TOKEN":            "env-token",
		"KMS_CA_FILE":          "/etc/kms/ca.pem",
		"KMS_CLIENT_CERT_FILE": "/etc/kms/client.crt",
		"KMS_CLIENT_KEY_FILE":  "/etc/kms/client.key",
	})
	fs := c.newFlags("test")
	cf := addConnFlags(&c.CLI, fs)
	if !c.parseFlags(fs, []string{"--ca", "/flag/ca.pem"}) {
		t.Fatalf("parseFlags failed: %s", c.stderr())
	}
	cf.finalize()

	if cf.endpoint != "kms.internal:9443" || cf.token != "env-token" {
		t.Errorf("endpoint = %q, token = %q", cf.endpoint, cf.token)
	}
	// The flag wins for --ca even though KMS_CA_FILE is also set.
	if cf.ca != "/flag/ca.pem" {
		t.Errorf("ca = %q, want the flag value", cf.ca)
	}
	if cf.cert != "/etc/kms/client.crt" || cf.key != "/etc/kms/client.key" {
		t.Errorf("cert = %q, key = %q", cf.cert, cf.key)
	}
}

// TestHelpDoesNotLeakToken is the reason --token carries a literal empty
// default: flag help prints non-empty string defaults, so deriving the default
// from KMS_TOKEN wrote the caller's bearer token to stderr on any -h.
func TestHelpDoesNotLeakToken(t *testing.T) {
	for _, args := range [][]string{
		{"put-secret", "-h"},
		{"list", "-h"},
		{"admin", "identity", "create", "-h"},
		{"release", "activate", "-h"},
		{"defaults", "apply", "-h"},
	} {
		c := newTestCLI()
		c.lookupEnv = connEnvLookup(map[string]string{"KMS_TOKEN": "supersecret"})
		if code := c.Run(args); code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr=%s)", args, code, c.stderr())
		}
		help := c.stderr()
		if strings.Contains(help, "supersecret") {
			t.Fatalf("%v help leaked the token value:\n%s", args, help)
		}
		if !strings.Contains(help, "KMS_TOKEN") {
			t.Fatalf("%v help does not name KMS_TOKEN:\n%s", args, help)
		}
	}
}

func TestConnFlagsDefaultEndpoint(t *testing.T) {
	c := newTestCLI()
	c.lookupEnv = connEnvLookup(nil)
	fs := c.newFlags("test")
	cf := addConnFlags(&c.CLI, fs)
	if !c.parseFlags(fs, nil) {
		t.Fatalf("parseFlags failed: %s", c.stderr())
	}
	cf.finalize()
	if cf.endpoint != "localhost:8443" {
		t.Errorf("endpoint = %q, want localhost:8443", cf.endpoint)
	}
	if cf.token != "" || cf.ca != "" || cf.cert != "" || cf.key != "" || cf.insecure {
		t.Errorf("empty environment produced %+v", cf)
	}
}

// TestClientEnvNamesDisjointFromServerSettings keeps the two KMS_* namespaces
// apart: config.EnvNames() configures a server process, while these five tell a
// client which server to talk to and as whom. A name in both would make one
// variable mean two things depending on the subcommand.
func TestClientEnvNamesDisjointFromServerSettings(t *testing.T) {
	server := make(map[string]bool)
	for _, name := range config.EnvNames() {
		server[name] = true
	}
	fallbacks := (&connFlags{}).envFallbacks()
	if len(fallbacks) != 5 {
		t.Fatalf("connection fallbacks = %d, want 5", len(fallbacks))
	}
	for _, fallback := range fallbacks {
		if !strings.HasPrefix(fallback.env, "KMS_") {
			t.Errorf("%q is not in the KMS_ namespace", fallback.env)
		}
		if server[fallback.env] {
			t.Errorf("%q is both a client connection variable and a server setting", fallback.env)
		}
	}
}
