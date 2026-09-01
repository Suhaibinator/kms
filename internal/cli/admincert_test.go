package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// initKMS runs `init --admin ops`, the state every offline certificate command
// expects: a migrated database, a master key file, a built-in CA, and one
// enabled admin identity.
func initKMS(t *testing.T, admin string) (db, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	db = filepath.Join(dir, "kms.db")
	keyFile = filepath.Join(dir, "master.key")
	c := newTestCLI()
	args := []string{"--sqlite-path", db, "--kek-file", keyFile}
	if admin != "" {
		args = append(args, "--admin", admin)
	}
	if code := c.Run(append([]string{"init"}, args...)); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	return db, keyFile
}

func openTestStore(t *testing.T, db string) *storage.SQLStore {
	t.Helper()
	store, err := storage.Open(db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func parsePEMCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode([]byte(readFileString(t, path)))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("%s does not hold a certificate PEM block", path)
	}
	if strings.TrimSpace(string(rest)) != "" {
		t.Fatalf("%s holds trailing data after the certificate: %q", path, rest)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate %s: %v", path, err)
	}
	return cert
}

func parsePEMPrivateKey(t *testing.T, path string) any {
	t.Helper()
	block, _ := pem.Decode([]byte(readFileString(t, path)))
	if block == nil {
		t.Fatalf("%s does not hold a PEM block", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse private key %s: %v", path, err)
	}
	return key
}

func auditEvents(t *testing.T, store *storage.SQLStore, eventType string) []domain.AuditEvent {
	t.Helper()
	events, _, err := store.ListAudit(context.Background(),
		domain.AuditFilter{EventType: eventType}, storage.ListPage{Limit: 50})
	if err != nil {
		t.Fatalf("list audit %s: %v", eventType, err)
	}
	return events
}

func identityCerts(t *testing.T, store *storage.SQLStore, name string) []domain.IdentityCert {
	t.Helper()
	certs, err := store.ListIdentityCerts(context.Background(), name)
	if err != nil {
		t.Fatalf("list certs for %s: %v", name, err)
	}
	return certs
}

// dirEntries names the files in dir, so a refusal can be shown to have left no
// certificate or key reservation behind.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestAdminCertIssueEndToEnd drives the offline issuance path against real
// storage, crypto, and the built-in CA: the credential files, the certificate
// itself, the recorded certificate row, and the audit trail.
func TestAdminCertIssueEndToEnd(t *testing.T) {
	db, keyFile := initKMS(t, "ops")
	outDir := t.TempDir()

	c := newTestCLI()
	code := c.Run([]string{"admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir, "--ttl", "30d"})
	if code != 0 {
		t.Fatalf("admin-cert issue exit=%d stderr=%s", code, c.stderr())
	}

	// The CLI prints the canonical reserved paths (fileutil.ResolveStablePath
	// resolves symlinks and short names): /private/var/... on macOS, the long
	// form of RUNNER~1 on Windows. Assert against that same canonical form; the
	// --out argument above deliberately stays the raw t.TempDir() spelling.
	canonicalOut, err := filepath.EvalSymlinks(outDir)
	if err != nil {
		t.Fatalf("canonicalizing %s: %v", outDir, err)
	}
	certPath := filepath.Join(canonicalOut, "ops.crt")
	keyPath := filepath.Join(canonicalOut, "ops.key")
	if runtime.GOOS != "windows" {
		keyInfo, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("stat key: %v", err)
		}
		if perm := keyInfo.Mode().Perm(); perm != 0o600 {
			t.Fatalf("private key mode = %o, want 600", perm)
		}
		certInfo, err := os.Stat(certPath)
		if err != nil {
			t.Fatalf("stat certificate: %v", err)
		}
		// The certificate is created 0644 and then masked by the process umask
		// (the test suite runs under 077), so only the invariant is asserted:
		// nobody but the owner may write it.
		if perm := certInfo.Mode().Perm(); perm&0o022 != 0 || perm&0o400 == 0 {
			t.Fatalf("certificate mode = %o, want owner-readable and not group/world writable", perm)
		}
	}

	cert := parsePEMCertificate(t, certPath)
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "kms://identity/ops" {
		t.Fatalf("certificate URI SANs = %v, want [kms://identity/ops]", cert.URIs)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("certificate public key = %T, want *ecdsa.PublicKey", cert.PublicKey)
	}
	if pub.Curve != elliptic.P256() {
		t.Fatalf("certificate curve = %v, want P-256", pub.Curve.Params().Name)
	}
	if _, ok := parsePEMPrivateKey(t, keyPath).(*ecdsa.PrivateKey); !ok {
		t.Fatalf("private key is not ECDSA")
	}
	if lifetime := time.Until(cert.NotAfter); lifetime > 30*24*time.Hour || lifetime < 29*24*time.Hour {
		t.Fatalf("certificate lifetime = %v, want ~30d (--ttl 30d)", lifetime)
	}

	store := openTestStore(t, db)
	certs := identityCerts(t, store, "ops")
	if len(certs) != 1 {
		t.Fatalf("recorded certificates = %+v, want exactly one", certs)
	}
	serial := certs[0].Serial
	if serial == "" || certs[0].Fingerprint == "" || !certs[0].RevokedAt.IsZero() {
		t.Fatalf("recorded certificate = %+v", certs[0])
	}

	out := c.stdout()
	for _, want := range []string{
		serial,
		certPath,
		keyPath,
		`Issued admin client certificate for identity "ops"`,
		"openssl pkcs12 -export",
		filepath.Join(canonicalOut, "ops.p12"),
		"--cert " + certPath + " --key " + keyPath,
		"KMS_CLIENT_CERT_FILE=" + certPath,
		"still require",
		"parameter-store admin-cert revoke ops --serial " + serial,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("issue stdout missing %q:\n%s", want, out)
		}
	}
	// The one-time private key belongs in the file only.
	if strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("issue stdout leaked private-key material:\n%s", out)
	}

	events := auditEvents(t, store, "identity.cert.issue")
	if len(events) != 1 {
		t.Fatalf("issue audit events = %+v, want exactly one", events)
	}
	ev := events[0]
	if ev.ActorIdentity != "cli" || ev.ActorType != domain.IdentityKindAdmin || ev.Decision != "allow" ||
		ev.ResourceType != domain.ResourceIdentity || ev.ResourceKey != "ops" {
		t.Fatalf("issue audit event = %+v", ev)
	}
	if !strings.Contains(ev.Metadata, `"channel":"local"`) || !strings.Contains(ev.Metadata, serial) {
		t.Fatalf("issue audit metadata = %s", ev.Metadata)
	}
}

func TestAdminCertIssueRequiresOutputDirectory(t *testing.T) {
	db, keyFile := initKMS(t, "ops")

	c := newTestCLI()
	code := c.Run([]string{"admin-cert", "issue", "ops", "--sqlite-path", db, "--kek-file", keyFile})
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "--out is required") || !strings.Contains(c.stderr(), "never to stdout") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if c.stdout() != "" {
		t.Fatalf("refused issuance produced stdout: %q", c.stdout())
	}
	if certs := identityCerts(t, openTestStore(t, db), "ops"); len(certs) != 0 {
		t.Fatalf("refused issuance recorded certificates: %+v", certs)
	}
}

// TestAdminCertIssueRefusesWithoutCertificateAuthority pins the rule that the
// offline command never creates a CA: InsertCAKey retires every other CA row,
// so a CLI that generated one next to a starting server would invalidate the
// certificates the loser had already issued.
func TestAdminCertIssueRefusesWithoutCertificateAuthority(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")
	outDir := t.TempDir()

	// A database with an admin identity but no master key and no CA: only
	// storage is touched, never init.
	store, err := storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateIdentity(context.Background(), storage.CreateIdentityParams{
		Name: "ops", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash("kms_seed"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI()
	code := c.Run([]string{"admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir})
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", code, c.stdout(), c.stderr())
	}
	for _, want := range []string{"certificate authority", `parameter-store init`} {
		if !strings.Contains(c.stderr(), want) {
			t.Fatalf("stderr missing %q: %s", want, c.stderr())
		}
	}
	if names := dirEntries(t, outDir); len(names) != 0 {
		t.Fatalf("refused issuance left files behind: %v", names)
	}
	if fileExists(keyFile) {
		t.Fatal("refused issuance created a master key file")
	}
	reopened := openTestStore(t, db)
	if certs := identityCerts(t, reopened, "ops"); len(certs) != 0 {
		t.Fatalf("refused issuance recorded certificates: %+v", certs)
	}
	if events := auditEvents(t, reopened, "identity.cert.issue"); len(events) != 0 {
		t.Fatalf("refused issuance wrote audit events: %+v", events)
	}
}

// TestAdminCertIssueRejectsInvalidTargetsWithoutMutation covers every refusal
// that precedes issuance. None of them may write a file, record a certificate,
// or leave an audit trail.
func TestAdminCertIssueRejectsInvalidTargetsWithoutMutation(t *testing.T) {
	tests := []struct {
		name     string
		args     func(db, keyFile, outDir string) []string
		wantExit int
		wantErr  string
	}{
		{
			name: "unknown identity",
			args: func(db, key, out string) []string {
				return []string{"ghost", "--sqlite-path", db, "--kek-file", key, "--out", out}
			},
			wantExit: 1,
			wantErr:  "identity ghost",
		},
		{
			name: "client identity",
			args: func(db, key, out string) []string {
				return []string{"app", "--sqlite-path", db, "--kek-file", key, "--out", out}
			},
			wantExit: 1,
			wantErr:  "is not an admin",
		},
		{
			name: "disabled admin",
			args: func(db, key, out string) []string {
				return []string{"retired", "--sqlite-path", db, "--kek-file", key, "--out", out}
			},
			wantExit: 1,
			wantErr:  "is disabled",
		},
		{
			name: "missing name",
			args: func(db, key, out string) []string {
				return []string{"--sqlite-path", db, "--kek-file", key, "--out", out}
			},
			wantExit: 2,
			wantErr:  "requires a NAME argument",
		},
		{
			name: "stray positional",
			args: func(db, key, out string) []string {
				return []string{"ops", "extra", "--sqlite-path", db, "--kek-file", key, "--out", out}
			},
			wantExit: 2,
			wantErr:  `unexpected argument "extra"`,
		},
		{
			name: "invalid ttl",
			args: func(db, key, out string) []string {
				return []string{"ops", "--sqlite-path", db, "--kek-file", key, "--out", out, "--ttl", "0d"}
			},
			wantExit: 1,
			wantErr:  "invalid --ttl",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, keyFile := initKMS(t, "ops")
			outDir := t.TempDir()
			ctx := context.Background()
			seed := openTestStore(t, db)
			if _, err := seed.CreateIdentity(ctx, storage.CreateIdentityParams{
				Name: "app", Kind: domain.IdentityKindClient, TokenHash: crypto.TokenHash("kms_app"),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := seed.CreateIdentity(ctx, storage.CreateIdentityParams{
				Name: "retired", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash("kms_retired"),
			}); err != nil {
				t.Fatal(err)
			}
			if err := seed.SetIdentityDisabled(ctx, "retired", true); err != nil {
				t.Fatal(err)
			}

			c := newTestCLI()
			if code := c.Run(append([]string{"admin-cert", "issue"}, test.args(db, keyFile, outDir)...)); code != test.wantExit {
				t.Fatalf("exit=%d, want %d; stdout=%s stderr=%s", code, test.wantExit, c.stdout(), c.stderr())
			}
			if !strings.Contains(c.stderr(), test.wantErr) {
				t.Fatalf("stderr = %q, want %q", c.stderr(), test.wantErr)
			}
			if c.stdout() != "" {
				t.Fatalf("refused issuance produced stdout: %q", c.stdout())
			}
			if names := dirEntries(t, outDir); len(names) != 0 {
				t.Fatalf("refused issuance left files behind: %v", names)
			}
			for _, name := range []string{"ops", "app", "retired"} {
				if certs := identityCerts(t, seed, name); len(certs) != 0 {
					t.Fatalf("refused issuance recorded certificates for %s: %+v", name, certs)
				}
			}
			if events := auditEvents(t, seed, "identity.cert.issue"); len(events) != 0 {
				t.Fatalf("refused issuance wrote audit events: %+v", events)
			}
		})
	}
}

func TestAdminCertRevokeBySerial(t *testing.T) {
	db, keyFile := initKMS(t, "ops")
	outDir := t.TempDir()

	issue := newTestCLI()
	if code := issue.Run([]string{"admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir}); code != 0 {
		t.Fatalf("issue exit=%d stderr=%s", code, issue.stderr())
	}
	store := openTestStore(t, db)
	serial := identityCerts(t, store, "ops")[0].Serial

	c := newTestCLI()
	if code := c.Run([]string{"admin-cert", "revoke", "ops", "--sqlite-path", db, "--serial", serial}); code != 0 {
		t.Fatalf("revoke exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "Revoked certificate "+serial) ||
		!strings.Contains(c.stdout(), "sees the change immediately") {
		t.Fatalf("revoke stdout = %s", c.stdout())
	}

	rec, err := store.GetIdentityCertBySerial(context.Background(), serial)
	if err != nil {
		t.Fatalf("lookup revoked certificate: %v", err)
	}
	if rec.Cert.RevokedAt.IsZero() {
		t.Fatalf("certificate %s was not revoked: %+v", serial, rec.Cert)
	}
	events := auditEvents(t, store, "identity.cert.revoke")
	if len(events) != 1 || events[0].ActorIdentity != "cli" || events[0].Decision != "allow" ||
		events[0].ResourceKey != "ops" || !strings.Contains(events[0].Metadata, serial) {
		t.Fatalf("revoke audit events = %+v", events)
	}
}

func TestAdminCertRevokeRejectsInvalidTargets(t *testing.T) {
	db, keyFile := initKMS(t, "ops")
	outDir := t.TempDir()
	ctx := context.Background()
	store := openTestStore(t, db)
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "app", Kind: domain.IdentityKindClient, TokenHash: crypto.TokenHash("kms_app"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "other", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash("kms_other"),
	}); err != nil {
		t.Fatal(err)
	}
	issue := newTestCLI()
	if code := issue.Run([]string{"admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir}); code != 0 {
		t.Fatalf("issue exit=%d stderr=%s", code, issue.stderr())
	}
	serial := identityCerts(t, store, "ops")[0].Serial

	tests := []struct {
		name     string
		args     []string
		wantExit int
		wantErr  string
	}{
		{name: "missing serial", args: []string{"ops", "--sqlite-path", db}, wantExit: 1, wantErr: "--serial is required"},
		{name: "missing name", args: []string{"--sqlite-path", db, "--serial", serial}, wantExit: 2, wantErr: "requires a NAME argument"},
		{name: "client identity", args: []string{"app", "--sqlite-path", db, "--serial", serial}, wantExit: 1, wantErr: "is not an admin"},
		{name: "wrong identity", args: []string{"other", "--sqlite-path", db, "--serial", serial}, wantExit: 1, wantErr: serial},
		{name: "unknown serial", args: []string{"ops", "--sqlite-path", db, "--serial", "deadbeef"}, wantExit: 1, wantErr: "deadbeef"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := newTestCLI()
			if code := c.Run(append([]string{"admin-cert", "revoke"}, test.args...)); code != test.wantExit {
				t.Fatalf("exit=%d, want %d; stdout=%s stderr=%s", code, test.wantExit, c.stdout(), c.stderr())
			}
			if !strings.Contains(c.stderr(), test.wantErr) {
				t.Fatalf("stderr = %q, want %q", c.stderr(), test.wantErr)
			}
			rec, err := store.GetIdentityCertBySerial(context.Background(), serial)
			if err != nil {
				t.Fatalf("lookup certificate: %v", err)
			}
			if !rec.Cert.RevokedAt.IsZero() {
				t.Fatalf("refused revocation still revoked %s", serial)
			}
		})
	}
}

func TestAdminCertListShowsState(t *testing.T) {
	db, keyFile := initKMS(t, "ops")
	outDir := t.TempDir()

	issue := newTestCLI()
	if code := issue.Run([]string{"admin-cert", "issue", "ops",
		"--sqlite-path", db, "--kek-file", keyFile, "--out", outDir}); code != 0 {
		t.Fatalf("issue exit=%d stderr=%s", code, issue.stderr())
	}
	store := openTestStore(t, db)
	cert := identityCerts(t, store, "ops")[0]

	list := newTestCLI()
	if code := list.Run([]string{"admin-cert", "list", "ops", "--sqlite-path", db}); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, list.stderr())
	}
	for _, want := range []string{"SERIAL", "FINGERPRINT", "STATE", "EXPIRES", "ISSUED", cert.Serial, cert.Fingerprint, "valid"} {
		if !strings.Contains(list.stdout(), want) {
			t.Fatalf("list stdout missing %q:\n%s", want, list.stdout())
		}
	}
	if strings.Contains(list.stdout(), "revoked") {
		t.Fatalf("fresh certificate listed as revoked:\n%s", list.stdout())
	}
	// No read is audited; only issuance and revocation are.
	if events := auditEvents(t, store, "identity.cert.list"); len(events) != 0 {
		t.Fatalf("list wrote audit events: %+v", events)
	}

	revoke := newTestCLI()
	if code := revoke.Run([]string{"admin-cert", "revoke", "ops", "--sqlite-path", db, "--serial", cert.Serial}); code != 0 {
		t.Fatalf("revoke exit=%d stderr=%s", code, revoke.stderr())
	}
	after := newTestCLI()
	if code := after.Run([]string{"admin-cert", "list", "ops", "--sqlite-path", db}); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, after.stderr())
	}
	if !strings.Contains(after.stdout(), "revoked") {
		t.Fatalf("revoked certificate not shown as revoked:\n%s", after.stdout())
	}
}

func TestAdminCertListRejectsNonAdminTarget(t *testing.T) {
	db, _ := initKMS(t, "ops")
	store := openTestStore(t, db)
	if _, err := store.CreateIdentity(context.Background(), storage.CreateIdentityParams{
		Name: "app", Kind: domain.IdentityKindClient, TokenHash: crypto.TokenHash("kms_app"),
	}); err != nil {
		t.Fatal(err)
	}
	c := newTestCLI()
	if code := c.Run([]string{"admin-cert", "list", "app", "--sqlite-path", db}); code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "is not an admin") {
		t.Fatalf("stderr = %s", c.stderr())
	}
}

func TestCertState(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		cert domain.IdentityCert
		want string
	}{
		{name: "valid", cert: domain.IdentityCert{NotAfter: now.Add(time.Hour)}, want: "valid"},
		{name: "no expiry", cert: domain.IdentityCert{}, want: "valid"},
		{name: "expired", cert: domain.IdentityCert{NotAfter: now.Add(-time.Hour)}, want: "expired"},
		{name: "revoked", cert: domain.IdentityCert{NotAfter: now.Add(time.Hour), RevokedAt: now.Add(-time.Minute)}, want: "revoked"},
		{name: "revoked and expired", cert: domain.IdentityCert{NotAfter: now.Add(-time.Hour), RevokedAt: now.Add(-time.Minute)}, want: "revoked"},
	}
	for _, test := range tests {
		if got := certState(test.cert, now); got != test.want {
			t.Errorf("%s: certState = %q, want %q", test.name, got, test.want)
		}
	}
}

// TestInitBootstrapsCertificateAuthority proves init is the one command that
// creates the CA, and that re-running it adopts the existing one rather than
// retiring it (InsertCAKey would otherwise invalidate every issued cert).
func TestInitBootstrapsCertificateAuthority(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")

	first := newTestCLI()
	if code := first.Run([]string{"init", "--sqlite-path", db, "--kek-file", keyFile}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, first.stderr())
	}
	if !strings.Contains(first.stdout(), "Built-in CA: ready") {
		t.Fatalf("init stdout = %s", first.stdout())
	}
	store := openTestStore(t, db)
	ca, err := store.ActiveCAKey(context.Background())
	if err != nil {
		t.Fatalf("init did not create a CA: %v", err)
	}

	second := newTestCLI()
	if code := second.Run([]string{"init", "--sqlite-path", db, "--kek-file", keyFile}); code != 0 {
		t.Fatalf("second init exit=%d stderr=%s", code, second.stderr())
	}
	again, err := store.ActiveCAKey(context.Background())
	if err != nil {
		t.Fatalf("second init lost the CA: %v", err)
	}
	if again.ID != ca.ID || again.CertPEM != ca.CertPEM {
		t.Fatalf("second init replaced the CA: %s -> %s", ca.ID, again.ID)
	}
}

func TestInitWithAdminCertDir(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")
	certDir := t.TempDir()

	c := newTestCLI()
	code := c.Run([]string{"init", "--sqlite-path", db, "--kek-file", keyFile,
		"--admin", "ops", "--cert-dir", certDir})
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	token := tokenFromCLIOutput(t, c.stdout())
	if !strings.HasPrefix(token, "kms_") {
		t.Fatalf("admin token = %q", token)
	}
	cert := parsePEMCertificate(t, filepath.Join(certDir, "ops.crt"))
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "kms://identity/ops" {
		t.Fatalf("certificate URI SANs = %v", cert.URIs)
	}
	if _, ok := parsePEMPrivateKey(t, filepath.Join(certDir, "ops.key")).(*ecdsa.PrivateKey); !ok {
		t.Fatal("bootstrap admin key is not ECDSA")
	}
	if !strings.Contains(c.stdout(), "openssl pkcs12 -export") {
		t.Fatalf("init stdout missing browser guidance:\n%s", c.stdout())
	}

	store := openTestStore(t, db)
	if certs := identityCerts(t, store, "ops"); len(certs) != 1 {
		t.Fatalf("recorded certificates = %+v, want one", certs)
	}
	// The token still authenticates: the certificate is an addition, not a
	// replacement.
	id, err := store.GetIdentityByTokenHash(context.Background(), crypto.TokenHash(token))
	if err != nil || id.Name != "ops" || id.Kind != domain.IdentityKindAdmin {
		t.Fatalf("bootstrap admin token lookup = %+v, %v", id, err)
	}
}

func TestInitCertDirRequiresAdmin(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")
	certDir := t.TempDir()

	c := newTestCLI()
	if code := c.Run([]string{"init", "--sqlite-path", db, "--kek-file", keyFile, "--cert-dir", certDir}); code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "--cert-dir requires --admin") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if fileExists(db) || fileExists(keyFile) {
		t.Fatal("a rejected init created the database or master key file")
	}
	if names := dirEntries(t, certDir); len(names) != 0 {
		t.Fatalf("a rejected init left files behind: %v", names)
	}
}

func TestCreateAdminWithCertDir(t *testing.T) {
	db, keyFile := initKMS(t, "")
	certDir := t.TempDir()

	c := newTestCLI()
	code := c.Run([]string{"create-admin", "--sqlite-path", db, "--kek-file", keyFile,
		"--name", "ops2", "--cert-dir", certDir})
	if code != 0 {
		t.Fatalf("create-admin exit=%d stderr=%s", code, c.stderr())
	}
	if token := tokenFromCLIOutput(t, c.stdout()); !strings.HasPrefix(token, "kms_") {
		t.Fatalf("admin token = %q", token)
	}
	cert := parsePEMCertificate(t, filepath.Join(certDir, "ops2.crt"))
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "kms://identity/ops2" {
		t.Fatalf("certificate URI SANs = %v", cert.URIs)
	}
	store := openTestStore(t, db)
	if certs := identityCerts(t, store, "ops2"); len(certs) != 1 {
		t.Fatalf("recorded certificates = %+v, want one", certs)
	}
}

// TestCreateAdminTokenOnlyNeedsNoMasterKey keeps the pre-existing behaviour:
// without --cert-dir the command never unseals, so it never prompts.
func TestCreateAdminTokenOnlyNeedsNoMasterKey(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")

	c := newTestCLI()
	if code := c.Run([]string{"create-admin", "--sqlite-path", db, "--name", "ops"}); code != 0 {
		t.Fatalf("create-admin exit=%d stderr=%s", code, c.stderr())
	}
	if token := tokenFromCLIOutput(t, c.stdout()); !strings.HasPrefix(token, "kms_") {
		t.Fatalf("admin token = %q", token)
	}
}

func TestCreateAdminCertDirRefusesWithoutCertificateAuthority(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")
	certDir := t.TempDir()

	// Migrated database, but no init: no master key and no CA.
	store, err := storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI()
	code := c.Run([]string{"create-admin", "--sqlite-path", db, "--kek-file", keyFile,
		"--name", "ops", "--cert-dir", certDir})
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", code, c.stdout(), c.stderr())
	}
	if !strings.Contains(c.stderr(), "certificate authority") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if c.stdout() != "" {
		t.Fatalf("refused create-admin produced stdout: %q", c.stdout())
	}
	if names := dirEntries(t, certDir); len(names) != 0 {
		t.Fatalf("refused create-admin left files behind: %v", names)
	}
	// The identity must not exist: a token printed before the refusal would be
	// an admin credential the operator believes was never created.
	if _, err := openTestStore(t, db).GetIdentityByName(context.Background(), "ops"); err == nil {
		t.Fatal("refused create-admin created the identity anyway")
	}
	if fileExists(keyFile) {
		t.Fatal("refused create-admin created a master key file")
	}
}

func TestAdminCertUsageAndUnknownSubcommand(t *testing.T) {
	help := newTestCLI()
	if code := help.Run([]string{"admin-cert", "help"}); code != 0 {
		t.Fatalf("admin-cert help exit = %d", code)
	}
	for _, want := range []string{
		"issue NAME --out DIR",
		"revoke NAME --serial S",
		"list NAME",
		"admin bearer token",
	} {
		if !strings.Contains(help.stderr(), want) {
			t.Fatalf("admin-cert help missing %q: %s", want, help.stderr())
		}
	}

	none := newTestCLI()
	if code := none.Run([]string{"admin-cert"}); code != 2 {
		t.Fatalf("bare admin-cert exit = %d, want 2", code)
	}

	unknown := newTestCLI()
	if code := unknown.Run([]string{"admin-cert", "frobnicate"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if !strings.Contains(unknown.stderr(), `unknown admin-cert subcommand "frobnicate"`) {
		t.Fatalf("stderr = %s", unknown.stderr())
	}
}

// --- online identity create: admin kind is token-only ----------------------

type identityAdminStub struct {
	kmsv1.UnimplementedAdminServiceServer
	mu      sync.Mutex
	created []*kmsv1.CreateIdentityRequest
}

func (s *identityAdminStub) CreateIdentity(_ context.Context, req *kmsv1.CreateIdentityRequest) (*kmsv1.CreateIdentityResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, proto.Clone(req).(*kmsv1.CreateIdentityRequest))
	resp := &kmsv1.CreateIdentityResponse{
		Identity: &kmsv1.Identity{Name: req.GetName(), Kind: req.GetKind()},
		Token:    "kms_stub_token",
	}
	if hasAuthMethod(req.GetAuthMethods(), "mtls") {
		resp.Cert = &kmsv1.CertBundle{
			CertPem: "-----BEGIN CERTIFICATE-----\nstub\n-----END CERTIFICATE-----\n",
			KeyPem:  "-----BEGIN PRIVATE KEY-----\nstub\n-----END PRIVATE KEY-----\n",
			Serial:  "s1",
		}
	}
	return resp, nil
}

func (s *identityAdminStub) requests() []*kmsv1.CreateIdentityRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*kmsv1.CreateIdentityRequest(nil), s.created...)
}

// TestIdentityCreateAdminKindRequestsTokenOnly pins the client half of the
// offline-only rule: an admin created over gRPC asks for a token and nothing
// else, and an explicit --auth mtls never reaches the server.
func TestIdentityCreateAdminKindRequestsTokenOnly(t *testing.T) {
	stub := &identityAdminStub{}
	dial := startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterAdminServiceServer(s, stub) })

	c := newTestCLI()
	c.dialOverride = dial
	if code := c.Run([]string{"admin", "identity", "create", "boss", "--kind", "admin",
		"--insecure", "--token", "admin-token"}); code != 0 {
		t.Fatalf("identity create exit=%d stderr=%s", code, c.stderr())
	}
	reqs := stub.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %+v, want one", reqs)
	}
	if got := reqs[0].GetAuthMethods(); len(got) != 1 || got[0] != "token" {
		t.Fatalf("admin auth_methods = %v, want [token]", got)
	}
	if !strings.Contains(c.stdout(), "kms_stub_token") {
		t.Fatalf("stdout = %s", c.stdout())
	}

	// The client default is unchanged: mTLS.
	client := newTestCLI()
	client.dialOverride = dial
	if code := client.Run([]string{"admin", "identity", "create", "svc", "--insecure", "--token", "admin-token"}); code != 0 {
		t.Fatalf("client identity create exit=%d stderr=%s", code, client.stderr())
	}
	reqs = stub.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %+v, want two", reqs)
	}
	if got := reqs[1].GetAuthMethods(); len(got) != 1 || got[0] != "mtls" {
		t.Fatalf("client auth_methods = %v, want [mtls]", got)
	}
}

func TestIdentityCreateAdminKindRejectsMTLSLocally(t *testing.T) {
	stub := &identityAdminStub{}
	dial := startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterAdminServiceServer(s, stub) })

	for _, auth := range []string{"mtls", "both"} {
		c := newTestCLI()
		c.dialOverride = dial
		code := c.Run([]string{"admin", "identity", "create", "boss", "--kind", "admin",
			"--auth", auth, "--insecure", "--token", "admin-token"})
		if code != 1 {
			t.Fatalf("--auth %s exit=%d, want 1; stderr=%s", auth, code, c.stderr())
		}
		if !strings.Contains(c.stderr(), "admin-cert issue") {
			t.Fatalf("--auth %s stderr = %s", auth, c.stderr())
		}
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Fatalf("refused invocations reached the server: %+v", reqs)
	}
}

func TestIdentityAuthMethods(t *testing.T) {
	tests := []struct {
		kind, auth string
		want       []string
		wantErr    bool
	}{
		{kind: "client", auth: "", want: []string{"mtls"}},
		{kind: "client", auth: "token", want: []string{"token"}},
		{kind: "client", auth: "both", want: []string{"mtls", "token"}},
		{kind: "admin", auth: "", want: []string{"token"}},
		{kind: "admin", auth: "token", want: []string{"token"}},
		{kind: "admin", auth: "mtls", wantErr: true},
		{kind: "admin", auth: "both", wantErr: true},
		{kind: "admin", auth: "bogus", wantErr: true},
		{kind: "client", auth: "bogus", wantErr: true},
	}
	for _, test := range tests {
		got, err := identityAuthMethods(test.kind, test.auth)
		if test.wantErr {
			if err == nil {
				t.Errorf("identityAuthMethods(%q, %q) = %v, want error", test.kind, test.auth, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("identityAuthMethods(%q, %q): %v", test.kind, test.auth, err)
			continue
		}
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Errorf("identityAuthMethods(%q, %q) = %v, want %v", test.kind, test.auth, got, test.want)
		}
	}
}
