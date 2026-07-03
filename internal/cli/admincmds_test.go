package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

func TestWriteCertBundleToDir(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	bundle := &kmsv1.CertBundle{
		CertPem:        "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n",
		KeyPem:         "-----BEGIN PRIVATE KEY-----\ndef\n-----END PRIVATE KEY-----\n",
		Serial:         "7f3a",
		NotAfterUnixMs: 1893456000000,
	}
	if err := c.CLI.writeCertBundle(dir, "svc", bundle); err != nil {
		t.Fatalf("writeCertBundle: %v", err)
	}
	certPath := filepath.Join(dir, "svc.crt")
	keyPath := filepath.Join(dir, "svc.key")
	if got := readFileString(t, certPath); got != bundle.CertPem {
		t.Fatalf("cert file = %q", got)
	}
	if got := readFileString(t, keyPath); got != bundle.KeyPem {
		t.Fatalf("key file = %q", got)
	}
	// The private key must not be world-readable (skipped on platforms without
	// POSIX permission bits).
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("stat key: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("key file mode = %o, want 600", perm)
		}
	}
	if !strings.Contains(c.stdout(), "7f3a") {
		t.Fatalf("stdout should mention the serial: %s", c.stdout())
	}
}

func TestWriteCertBundleToStdout(t *testing.T) {
	c := newTestCLI()
	bundle := &kmsv1.CertBundle{CertPem: "CERT\n", KeyPem: "KEY\n", Serial: "s1"}
	if err := c.CLI.writeCertBundle("", "svc", bundle); err != nil {
		t.Fatalf("writeCertBundle: %v", err)
	}
	out := c.stdout()
	if !strings.Contains(out, "CERT") || !strings.Contains(out, "KEY") || !strings.Contains(out, "WARNING") {
		t.Fatalf("stdout = %s", out)
	}
}

func TestParseAuthMethods(t *testing.T) {
	if m, err := parseAuthMethods(""); err != nil || m != nil {
		t.Fatalf("empty = %v, %v", m, err)
	}
	m, err := parseAuthMethods("mtls,token")
	if err != nil || len(m) != 2 || m[0] != "mtls" || m[1] != "token" {
		t.Fatalf("parsed = %v, %v", m, err)
	}
	if _, err := parseAuthMethods("bogus"); err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestAuthFlagToMethods(t *testing.T) {
	cases := map[string][]string{
		"mtls":  {"mtls"},
		"token": {"token"},
		"both":  {"mtls", "token"},
		"":      {"mtls"},
	}
	for in, want := range cases {
		got, err := authFlagToMethods(in)
		if err != nil || len(got) != len(want) {
			t.Fatalf("authFlagToMethods(%q) = %v, %v", in, got, err)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("authFlagToMethods(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
	if _, err := authFlagToMethods("nope"); err == nil {
		t.Fatal("expected error for unknown --auth value")
	}
}

func TestParseTTLSeconds(t *testing.T) {
	cases := map[string]int64{
		"":     0,
		"90d":  90 * 24 * 3600,
		"720h": 720 * 3600,
	}
	for in, want := range cases {
		got, err := parseTTLSeconds(in)
		if err != nil || got != want {
			t.Fatalf("parseTTLSeconds(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"0d", "-5h", "abc"} {
		if _, err := parseTTLSeconds(bad); err == nil {
			t.Fatalf("parseTTLSeconds(%q) should error", bad)
		}
	}
}
