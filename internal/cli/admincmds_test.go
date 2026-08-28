package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

func writeCertBundleForTest(c *testCLI, outDir, name string, bundle *kmsv1.CertBundle) error {
	return c.withReservedCertBundle(outDir, name, func(output *reservedCertBundle) error {
		return c.writeCertBundleToOutput(output, bundle)
	})
}

func TestAdminHelpExplainsApplicationCredentialRoles(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"admin", "help"}); code != 0 {
		t.Fatalf("admin help exit = %d", code)
	}
	for _, want := range []string{
		"Create application credentials; mTLS by default",
		"Applications present NAME.crt and prove possession with NAME.key",
		"operator-provided KMS server certificate",
		`"ca show" is NOT that server-trust CA`,
		"built-in client-issuing CA",
		"Admin identities always receive a one-time bearer token",
	} {
		if !strings.Contains(c.stderr(), want) {
			t.Fatalf("admin help missing %q: %s", want, c.stderr())
		}
	}
}

func TestIdentityAndCAFlagHelpUseApplicationCredentialTerms(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "identity create",
			args: []string{"admin", "identity", "create", "-h"},
			want: "directory for one-time application client credentials",
		},
		{
			name: "identity issue-cert",
			args: []string{"admin", "identity", "issue-cert", "-h"},
			want: "directory for one-time application client credentials",
		},
		{
			name: "ca show",
			args: []string{"admin", "ca", "show", "-h"},
			want: "built-in client-issuing CA",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCLI()
			if code := c.Run(tt.args); code != 0 {
				t.Fatalf("help exit = %d, want 0", code)
			}
			if !strings.Contains(c.stderr(), tt.want) {
				t.Fatalf("flag help missing %q: %s", tt.want, c.stderr())
			}
		})
	}
}

func TestWriteCertBundleToDir(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	bundle := &kmsv1.CertBundle{
		CertPem:        "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n",
		KeyPem:         "-----BEGIN PRIVATE KEY-----\ndef\n-----END PRIVATE KEY-----\n",
		Serial:         "7f3a",
		NotAfterUnixMs: 1893456000000,
	}
	if err := writeCertBundleForTest(c, dir, "svc", bundle); err != nil {
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

func TestWriteCertBundleRefusesPreexistingPrivateKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "svc.key")
	if err := os.WriteFile(keyPath, []byte("attacker-controlled"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI()
	bundle := &kmsv1.CertBundle{CertPem: "CERT\n", KeyPem: "PRIVATE KEY\n", Serial: "s1"}
	if err := writeCertBundleForTest(c, dir, "svc", bundle); err == nil {
		t.Fatal("expected pre-existing private-key path to be refused")
	}
	if got := readFileString(t, keyPath); got != "attacker-controlled" {
		t.Fatalf("pre-existing key file was changed: %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Fatalf("pre-existing key mode changed to %o", got)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "svc.crt")); err != nil || info.Size() != 0 {
		t.Fatalf("safe empty certificate reservation = %+v, %v", info, err)
	}
}

func TestWriteCertBundleRefusesPrivateKeySymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "attacker-readable")
	if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "svc.key")
	if err := os.Symlink(target, keyPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	c := newTestCLI()
	bundle := &kmsv1.CertBundle{CertPem: "CERT\n", KeyPem: "PRIVATE KEY\n", Serial: "s1"}
	if err := writeCertBundleForTest(c, dir, "svc", bundle); err == nil {
		t.Fatal("expected private-key symlink to be refused")
	}
	if got := readFileString(t, target); got != "unchanged" {
		t.Fatalf("symlink target received private key: %q", got)
	}
	if info, err := os.Lstat(keyPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("private-key symlink was changed: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "svc.crt")); err != nil || info.Size() != 0 {
		t.Fatalf("safe empty certificate reservation = %+v, %v", info, err)
	}
}

func TestWriteCertBundleRefusesPreexistingCertificateWithoutCreatingKey(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "svc.crt")
	if err := os.WriteFile(certPath, []byte("existing certificate"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI()
	bundle := &kmsv1.CertBundle{CertPem: "NEW CERT\n", KeyPem: "PRIVATE KEY\n", Serial: "s1"}
	if err := writeCertBundleForTest(c, dir, "svc", bundle); err == nil {
		t.Fatal("expected pre-existing certificate path to be refused")
	}
	if got := readFileString(t, certPath); got != "existing certificate" {
		t.Fatalf("pre-existing certificate was changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "svc.key")); !os.IsNotExist(err) {
		t.Fatalf("private key was created despite certificate collision: %v", err)
	}
}

func TestWriteCertBundleToStdout(t *testing.T) {
	c := newTestCLI()
	bundle := &kmsv1.CertBundle{CertPem: "CERT\n", KeyPem: "KEY\n", Serial: "s1"}
	if err := writeCertBundleForTest(c, "", "svc", bundle); err != nil {
		t.Fatalf("writeCertBundle: %v", err)
	}
	out := c.stdout()
	if !strings.Contains(out, "CERT") || !strings.Contains(out, "KEY") || !strings.Contains(out, "WARNING") {
		t.Fatalf("stdout = %s", out)
	}
}

func TestWriteCertBundlePropagatesStdoutFailure(t *testing.T) {
	c := newTestCLI()
	c.Stdout = errorWriter{err: io.ErrClosedPipe}
	if err := writeCertBundleForTest(c, "", "svc", testCertBundle()); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writeCertBundle stdout error = %v, want closed pipe", err)
	}
}

func TestCreatedIdentityPersistsReservedCertBeforeStatusOutputFailure(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	output, err := reserveCertBundle(dir, "svc")
	if err != nil {
		t.Fatalf("reserveCertBundle: %v", err)
	}
	c.Stdout = errorWriter{err: io.ErrClosedPipe}
	bundle := testCertBundle()
	resp := &kmsv1.CreateIdentityResponse{Token: "kms_once", Cert: bundle}
	if err := c.writeCreatedIdentityResult("svc", "client", []string{"token", "mtls"}, output, resp); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writeCreatedIdentityResult error = %v, want closed pipe", err)
	}
	if got := readFileString(t, output.certPath); got != bundle.CertPem {
		t.Fatalf("certificate content = %q", got)
	}
	if got := readFileString(t, output.keyPath); got != bundle.KeyPem {
		t.Fatalf("private key content = %q", got)
	}
}

func TestWithReservedCertBundlePreservesPublishedCredentialsAfterStatusFailure(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	c.Stdout = errorWriter{err: io.ErrClosedPipe}
	bundle := testCertBundle()

	err := c.withReservedCertBundle(dir, "svc", func(output *reservedCertBundle) error {
		return c.writeCreatedIdentityResult("svc", "client", []string{"mtls"}, output, &kmsv1.CreateIdentityResponse{Cert: bundle})
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("withReservedCertBundle error = %v, want closed pipe", err)
	}
	for _, want := range []string{"one-time credentials were fully written", "preserve them", "verify the identity/certificate state on the server before retrying"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("published-credential error missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "remove") {
		t.Fatalf("published-credential error incorrectly recommends removal: %v", err)
	}
	if got := readFileString(t, filepath.Join(dir, "svc.crt")); got != bundle.CertPem {
		t.Fatalf("published certificate = %q", got)
	}
	if got := readFileString(t, filepath.Join(dir, "svc.key")); got != bundle.KeyPem {
		t.Fatalf("published private key = %q", got)
	}
}

func TestWithReservedCertBundlePreservesPublishedCredentialsAfterGuidanceFailure(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	stdout := &substringErrorWriter{substring: "Next steps:", err: io.ErrClosedPipe}
	c.Stdout = stdout
	bundle := testCertBundle()

	err := c.withReservedCertBundle(dir, "svc", func(output *reservedCertBundle) error {
		return c.writeCreatedIdentityResult("svc", "client", []string{"mtls"}, output, &kmsv1.CreateIdentityResponse{Cert: bundle})
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("withReservedCertBundle error = %v, want guidance closed pipe", err)
	}
	if !strings.Contains(err.Error(), "preserve them and verify the identity/certificate state on the server before retrying") {
		t.Fatalf("guidance error lacks published-credential recovery: %v", err)
	}
	if strings.Contains(err.Error(), "remove") {
		t.Fatalf("guidance error incorrectly recommends credential removal: %v", err)
	}
	if !strings.Contains(stdout.written.String(), `Created identity "svc"`) {
		t.Fatalf("test did not reach post-publication guidance: %s", stdout.written.String())
	}
	if got := readFileString(t, filepath.Join(dir, "svc.key")); got != bundle.KeyPem {
		t.Fatalf("published private key = %q", got)
	}
}

func TestWithReservedCertBundleKeepsPartialWriteCleanupGuidance(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	bundle := testCertBundle()

	err := c.withReservedCertBundle(dir, "svc", func(output *reservedCertBundle) error {
		// Force a private-key write failure after the certificate write. This is
		// deliberately not a complete publication and must retain fail-safe
		// reservation cleanup guidance.
		if err := output.keyFile.Close(); err != nil {
			return err
		}
		return c.writeCertBundleToOutput(output, bundle)
	})
	if err == nil {
		t.Fatal("expected partial credential write to fail")
	}
	if !strings.Contains(err.Error(), "inspect and remove") {
		t.Fatalf("partial-write error lacks reservation cleanup guidance: %v", err)
	}
	if strings.Contains(err.Error(), "one-time credentials were fully written") {
		t.Fatalf("partial write incorrectly marked published: %v", err)
	}
	if got := readFileString(t, filepath.Join(dir, "svc.crt")); got != bundle.CertPem {
		t.Fatalf("partially written certificate = %q", got)
	}
	if info, statErr := os.Stat(filepath.Join(dir, "svc.key")); statErr != nil || info.Size() != 0 {
		t.Fatalf("partial private-key reservation = %+v, %v", info, statErr)
	}
}

func TestCreatedIdentityFileOutputExplainsApplicationNextStepsWithoutLeakingPEM(t *testing.T) {
	dir := t.TempDir()
	output, err := reserveCertBundle(dir, "svc")
	if err != nil {
		t.Fatalf("reserveCertBundle: %v", err)
	}
	defer output.cleanup()

	c := newTestCLI()
	bundle := testCertBundle()
	if err := c.writeCreatedIdentityResult("svc", "client", []string{"mtls"}, output, &kmsv1.CreateIdentityResponse{Cert: bundle}); err != nil {
		t.Fatalf("writeCreatedIdentityResult: %v", err)
	}
	out := c.stdout()
	for _, want := range []string{
		output.certPath,
		output.keyPath,
		"Deploy both files securely to the application",
		"CA bundle that trusts the operator-provided KMS server certificate",
		`Do not use "parameter-store admin ca show" for server trust`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("identity output missing %q: %s", want, out)
		}
	}
	for _, secret := range []string{bundle.CertPem, bundle.KeyPem} {
		if strings.Contains(out, secret) {
			t.Fatalf("file-mode status leaked PEM material: %s", out)
		}
	}
}

func TestCreatedIdentityStdoutPrintsOneTimePEMOnceAndExplainsNextSteps(t *testing.T) {
	c := newTestCLI()
	bundle := testCertBundle()
	if err := c.writeCreatedIdentityResult("svc", "client", []string{"mtls"}, nil, &kmsv1.CreateIdentityResponse{Cert: bundle}); err != nil {
		t.Fatalf("writeCreatedIdentityResult: %v", err)
	}
	out := c.stdout()
	if got := strings.Count(out, bundle.CertPem); got != 1 {
		t.Fatalf("certificate PEM count = %d, want 1: %s", got, out)
	}
	if got := strings.Count(out, bundle.KeyPem); got != 1 {
		t.Fatalf("private-key PEM count = %d, want 1: %s", got, out)
	}
	for _, want := range []string{
		"Save the client certificate and private key printed above now",
		"Deploy both credentials securely to the application",
		"operator-provided KMS server certificate",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("identity output missing %q: %s", want, out)
		}
	}
}

func TestIssuedCertificateFileOutputExplainsApplicationNextSteps(t *testing.T) {
	dir := t.TempDir()
	output, err := reserveCertBundle(dir, "svc")
	if err != nil {
		t.Fatalf("reserveCertBundle: %v", err)
	}
	defer output.cleanup()

	c := newTestCLI()
	bundle := testCertBundle()
	if err := c.writeIssuedIdentityCertificateResult("svc", output, bundle); err != nil {
		t.Fatalf("writeIssuedIdentityCertificateResult: %v", err)
	}
	out := c.stdout()
	for _, want := range []string{
		`Issued new mTLS credentials for identity "svc"`,
		output.certPath,
		output.keyPath,
		"Deploy both files securely to the application",
		"operator-provided KMS server certificate",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("issue-cert output missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, bundle.KeyPem) {
		t.Fatalf("file-mode status leaked private-key PEM: %s", out)
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

type substringErrorWriter struct {
	substring string
	err       error
	written   strings.Builder
}

func (w *substringErrorWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.substring) {
		return 0, w.err
	}
	return w.written.Write(p)
}

func TestPrintTokenOncePropagatesOutputFailure(t *testing.T) {
	if err := printTokenOnce(errorWriter{err: io.ErrClosedPipe}, "identity", "svc", "kms_secret"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("printTokenOnce error = %v, want closed pipe", err)
	}
}

func TestWithReservedCertBundleRejectsCollisionBeforeUse(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "svc.key")
	if err := os.WriteFile(keyPath, []byte("existing key"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestCLI()
	uses := 0
	err := c.withReservedCertBundle(dir, "svc", func(*reservedCertBundle) error {
		uses++
		return nil
	})
	if err == nil {
		t.Fatal("reservation succeeded despite existing private-key path")
	}
	if uses != 0 {
		t.Fatalf("protected operation ran %d times before output reservation succeeded", uses)
	}
	if got := readFileString(t, keyPath); got != "existing key" {
		t.Fatalf("existing key changed: %q", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "svc.crt")); err != nil || info.Size() != 0 {
		t.Fatalf("safe empty certificate reservation = %+v, %v", info, err)
	}
}

func TestReserveCertBundleConcurrentCallersExactlyOneOwnsPair(t *testing.T) {
	dir := t.TempDir()
	const callers = 16
	type result struct {
		output *reservedCertBundle
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			output, err := reserveCertBundle(dir, "svc")
			results <- result{output: output, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)

	var winner *reservedCertBundle
	for got := range results {
		switch {
		case got.err == nil && got.output != nil:
			if winner != nil {
				t.Fatal("more than one caller acquired the one-time certificate pair")
			}
			winner = got.output
		case errors.Is(got.err, os.ErrExist) && got.output == nil:
			// Expected: all later callers observe the existing reservation.
		default:
			t.Fatalf("unexpected reservation result: output=%v err=%v", got.output, got.err)
		}
	}
	if winner == nil {
		t.Fatal("no concurrent caller acquired the certificate pair")
		return
	}
	defer winner.cleanup()
	assertReservedFileOwnsPath(t, winner.certFile, winner.certPath)
	assertReservedFileOwnsPath(t, winner.keyFile, winner.keyPath)

	c := newTestCLI()
	bundle := testCertBundle()
	if err := c.writeCertBundleToOutput(winner, bundle); err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, winner.certPath); got != bundle.CertPem {
		t.Fatalf("winning certificate reservation = %q", got)
	}
	if got := readFileString(t, winner.keyPath); got != bundle.KeyPem {
		t.Fatalf("winning private-key reservation = %q", got)
	}
}

func TestWithReservedCertBundleLeavesReservationsAfterUseFailure(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	wantErr := errors.New("mint failed")
	err := c.withReservedCertBundle(dir, "svc", func(output *reservedCertBundle) error {
		if output == nil {
			t.Fatal("file output was not reserved")
			return errors.New("test callback received no reserved file output")
		}
		for _, path := range []string{output.certPath, output.keyPath} {
			if _, statErr := os.Stat(path); statErr != nil {
				t.Fatalf("reserved path %s: %v", path, statErr)
			}
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("withReservedCertBundle error = %v, want %v", err, wantErr)
	}
	for _, suffix := range []string{".crt", ".key"} {
		info, statErr := os.Stat(filepath.Join(dir, "svc"+suffix))
		if statErr != nil || info.Size() != 0 {
			t.Fatalf("%s safe empty reservation = %+v, %v", suffix, info, statErr)
		}
	}
	if !strings.Contains(err.Error(), "inspect and remove") {
		t.Fatalf("failure should explain reservation cleanup: %v", err)
	}
}

func TestReserveCertBundleRejectsPathTraversalName(t *testing.T) {
	parent := t.TempDir()
	outDir := filepath.Join(parent, "certs")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveCertBundle(outDir, "../escape"); err == nil {
		t.Fatal("path-traversal identity name should be rejected locally")
	}
	for _, suffix := range []string{".crt", ".key"} {
		if _, err := os.Stat(filepath.Join(parent, "escape"+suffix)); !os.IsNotExist(err) {
			t.Fatalf("path outside output directory was touched: %v", err)
		}
	}
}

func TestReserveCertBundleUsesCanonicalOutputDirectory(t *testing.T) {
	realDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "certs")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	output, err := reserveCertBundle(alias, "svc")
	if err != nil {
		t.Fatalf("reserveCertBundle: %v", err)
	}
	defer output.cleanup()

	canonicalDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(canonicalDir, "svc.crt"); output.certPath != want {
		t.Fatalf("certificate reservation path = %q, want canonical %q", output.certPath, want)
	}
	if want := filepath.Join(canonicalDir, "svc.key"); output.keyPath != want {
		t.Fatalf("private-key reservation path = %q, want canonical %q", output.keyPath, want)
	}
	assertReservedFileOwnsPath(t, output.certFile, output.certPath)
	assertReservedFileOwnsPath(t, output.keyFile, output.keyPath)
}

func TestReservedCertBundleIgnoresOutputParentSymlinkReplacement(t *testing.T) {
	realDir := t.TempDir()
	replacementDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "certs")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	output, err := reserveCertBundle(alias, "svc")
	if err != nil {
		t.Fatalf("reserveCertBundle: %v", err)
	}
	defer output.cleanup()
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacementDir, alias); err != nil {
		t.Skipf("replace output-directory symlink: %v", err)
	}

	// The stored canonical paths must still name the exact files held open for
	// the one-time bundle, even though the caller's original path now resolves
	// to a different directory.
	assertReservedFileOwnsPath(t, output.certFile, output.certPath)
	assertReservedFileOwnsPath(t, output.keyFile, output.keyPath)
	c := newTestCLI()
	bundle := testCertBundle()
	if err := c.writeCertBundleToOutput(output, bundle); err != nil {
		t.Fatalf("writeCertBundleToOutput: %v", err)
	}
	if got := readFileString(t, filepath.Join(realDir, "svc.crt")); got != bundle.CertPem {
		t.Fatalf("canonical certificate content = %q", got)
	}
	if got := readFileString(t, filepath.Join(realDir, "svc.key")); got != bundle.KeyPem {
		t.Fatalf("canonical private-key content = %q", got)
	}
	for _, suffix := range []string{".crt", ".key"} {
		if _, err := os.Stat(filepath.Join(replacementDir, "svc"+suffix)); !os.IsNotExist(err) {
			t.Fatalf("replacement parent received %s output: %v", suffix, err)
		}
	}
	if !strings.Contains(c.stdout(), output.certPath) || !strings.Contains(c.stdout(), output.keyPath) {
		t.Fatalf("status did not report canonical reserved paths: %s", c.stdout())
	}
	if strings.Contains(c.stdout(), alias) {
		t.Fatalf("status reused mutable output-directory spelling %q: %s", alias, c.stdout())
	}
}

func assertReservedFileOwnsPath(t *testing.T, file *os.File, path string) {
	t.Helper()
	held, err := file.Stat()
	if err != nil {
		t.Fatalf("stat reserved file: %v", err)
	}
	named, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat reserved path %s: %v", path, err)
	}
	if !os.SameFile(held, named) {
		t.Fatalf("reserved handle no longer owns path %s", path)
	}
}

func TestWithReservedCertBundlePublishesBundle(t *testing.T) {
	bundle := testCertBundle()
	dir := t.TempDir()
	c := newTestCLI()
	if err := c.withReservedCertBundle(dir, "svc", func(output *reservedCertBundle) error {
		return c.writeCertBundleToOutput(output, bundle)
	}); err != nil {
		t.Fatalf("withReservedCertBundle: %v", err)
	}
	if got := readFileString(t, filepath.Join(dir, "svc.crt")); got != bundle.CertPem {
		t.Fatalf("certificate = %q", got)
	}
	keyPath := filepath.Join(dir, "svc.key")
	if got := readFileString(t, keyPath); got != bundle.KeyPem {
		t.Fatalf("private key = %q", got)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(keyPath); err != nil {
			t.Fatal(err)
		} else if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("private key mode = %o, want 600", got)
		}
	}
}

func TestWithReservedCertBundlePreservesStdoutOutput(t *testing.T) {
	c := newTestCLI()
	err := c.withReservedCertBundle("", "svc", func(output *reservedCertBundle) error {
		if output != nil {
			t.Fatal("stdout output unexpectedly reserved files")
		}
		return c.writeCertBundleToOutput(output, testCertBundle())
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CERTIFICATE", "PRIVATE KEY", "WARNING"} {
		if !strings.Contains(c.stdout(), want) {
			t.Fatalf("stdout missing %q: %s", want, c.stdout())
		}
	}
}

func testCertBundle() *kmsv1.CertBundle {
	return &kmsv1.CertBundle{
		CertPem:        "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n",
		KeyPem:         "-----BEGIN PRIVATE KEY-----\ndef\n-----END PRIVATE KEY-----\n",
		Serial:         "7f3a",
		NotAfterUnixMs: 1893456000000,
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
