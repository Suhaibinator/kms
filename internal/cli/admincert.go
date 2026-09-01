package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/config"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// cmdAdminCert dispatches the offline admin client-certificate commands. Unlike
// "admin ..." (which talks to a running server over gRPC), these open the
// database and the master key directly on the server host: an admin client
// certificate is the management plane's proof of possession, so minting one
// deliberately requires host access rather than any online credential.
func (c *CLI) cmdAdminCert(args []string) int {
	if len(args) == 0 {
		c.adminCertUsage()
		return 2
	}
	action, rest := args[0], args[1:]
	switch action {
	case "issue":
		return c.cmdAdminCertIssue(rest)
	case "revoke":
		return c.cmdAdminCertRevoke(rest)
	case "list":
		return c.cmdAdminCertList(rest)
	case "help", "-h", "--help":
		c.adminCertUsage()
		return 0
	default:
		_, _ = fmt.Fprintf(c.Stderr, "unknown admin-cert subcommand %q\n\n", action)
		c.adminCertUsage()
		return 2
	}
}

func (c *CLI) adminCertUsage() {
	_, _ = fmt.Fprint(c.Stderr, `parameter-store admin-cert — issue and manage admin client certificates offline

Usage:
  parameter-store admin-cert <action> NAME [flags]

Actions:
  issue NAME --out DIR    Issue a client certificate for an existing admin identity
                          (--ttl, --sqlite-path, --kek-file). The private key is written
                          to DIR/NAME.key and never printed.
  revoke NAME --serial S  Revoke one of that admin's certificates (--sqlite-path).
  list NAME               List the admin's certificates and their state (--sqlite-path).

These commands run on the server host against the database and master key; the
server does not need to be running. Admin client certificates are never issued
over the network: "admin identity issue-cert" refuses admin targets.

The certificate is only half of an admin credential. Console and CLI callers
must also present the admin bearer token.
`)
}

// localAdminPrincipal is the synthesized principal offline commands act as.
// Host access to the database and master key is the authorization; the name
// appears in the audit log as the actor.
func localAdminPrincipal() core.Principal {
	return core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
}

// adminCertName returns the single NAME positional. Extra positionals are
// rejected with the same reasoning as rejectPositionals: an argument the flag
// package would otherwise drop must not be silently ignored by a command that
// mints or revokes a credential.
func (c *CLI) adminCertName(action string) (string, bool) {
	pos := c.args()
	if len(pos) == 0 || pos[0] == "" {
		_, _ = fmt.Fprintf(c.Stderr, "error: admin-cert %s requires a NAME argument\n", action)
		return "", false
	}
	if len(pos) > 1 {
		_, _ = fmt.Fprintf(c.Stderr, "error: unexpected argument %q (boolean flags take the form --flag=false)\n", pos[1])
		return "", false
	}
	return pos[0], true
}

// requireAdminTarget loads name and confirms it is an admin identity. Client
// identities are refused on purpose: their certificates come from the online
// API, which enforces namespace policy the offline path cannot see. Only
// issuance also requires the identity to be enabled — a disabled admin's
// existing certificates stay listable and revocable.
func (c *CLI) requireAdminTarget(ctx context.Context, store storage.Store, name string, mustBeEnabled bool) (domain.Identity, error) {
	id, err := store.GetIdentityByName(ctx, name)
	if err != nil {
		return domain.Identity{}, fmt.Errorf("loading admin identity: %w", err)
	}
	if id.Kind != domain.IdentityKindAdmin {
		return domain.Identity{}, fmt.Errorf(
			"identity %s is not an admin; client certificates are issued with \"admin identity issue-cert\"", name)
	}
	if mustBeEnabled && id.Disabled {
		return domain.Identity{}, fmt.Errorf("admin identity %s is disabled; refusing to issue a certificate for it", name)
	}
	return id, nil
}

// requireLocalCA prepares a Service that can issue from the built-in CA. It
// refuses when no CA exists yet rather than creating one: storage.InsertCAKey
// retires every other CA row, so a CLI that generated a CA next to a starting
// server would silently invalidate whichever CA lost the race. The returned
// cleanup destroys the unsealed key material.
func (c *CLI) requireLocalCA(ctx context.Context, store storage.Store, cfg config.Config, prov config.Provenance) (*core.Service, func(), error) {
	if _, err := store.ActiveCAKey(ctx); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil, fmt.Errorf(
				"no certificate authority in %s; run \"parameter-store init\" (or start serve once) to create it, then retry",
				dbTarget(cfg, prov))
		}
		return nil, nil, fmt.Errorf("loading certificate authority: %w", err)
	}
	keyring, err := c.unseal(ctx, store, cfg.Encryption.KEKFile, false)
	if err != nil {
		return nil, nil, fmt.Errorf("unsealing master key: %w", err)
	}
	svc := core.New(store, c.quietLogger(), Version)
	svc.SetKeyring(keyring)
	// The CA row exists, so BootstrapCA only loads and decrypts it here.
	if err := svc.BootstrapCA(ctx); err != nil {
		keyring.Active().Destroy()
		return nil, nil, fmt.Errorf("loading certificate authority: %w", err)
	}
	return svc, func() { keyring.Active().Destroy() }, nil
}

// --- admin-cert issue ------------------------------------------------------

func (c *CLI) cmdAdminCertIssue(args []string) int {
	fs := c.newFlags("admin-cert issue")
	r := c.serverSettings(fs, "storage.sqlite_path", "encryption.kek_file")
	out := fs.String("out", "", "`directory` for the one-time admin credentials (NAME.crt/NAME.key); required")
	ttl := fs.String("ttl", "", "certificate `lifetime` (e.g. 90d, 720h); default 90d")
	c.setUsage(fs, "admin-cert issue NAME --out DIR [flags]",
		"Issue a client certificate for an existing admin identity, offline, from the database and master key on this host. The admin still needs its bearer token to sign in.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	name, ok := c.adminCertName("issue")
	if !ok {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *out == "" {
		return c.failUsage("--out is required: the admin private key is written only to an owner-only file, never to stdout")
	}
	ttlSeconds, err := parseTTLSeconds(*ttl)
	if err != nil {
		return c.failUsage("%v", err)
	}
	ctx := context.Background()

	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	// Validate the target before anything is prompted, unsealed, or reserved: a
	// refusal must leave no key-file placeholders and no passphrase prompt.
	if _, err := c.requireAdminTarget(ctx, store, name, true); err != nil {
		return c.failErr("", err)
	}
	svc, closeCA, err := c.requireLocalCA(ctx, store, cfg, prov)
	if err != nil {
		return c.failErr("", err)
	}
	defer closeCA()

	err = c.withReservedCertBundle(*out, name, func(output *reservedCertBundle) error {
		bundle, issueErr := svc.IssueLocalAdminCertificate(ctx, localAdminPrincipal(), name, time.Duration(ttlSeconds)*time.Second)
		if issueErr != nil {
			return fmt.Errorf("issuing admin client certificate: %w", issueErr)
		}
		return c.publishAdminCert(ctx, svc, output, name, bundle)
	})
	if err != nil {
		return c.failErr("", err)
	}
	return 0
}

// publishAdminCert writes a freshly issued admin certificate bundle to its
// reserved files and prints the follow-up guidance. IssueLocalAdminCertificate
// has already committed the certificate row, so this is the point of no
// return for the one-time private key: the key file is written before any
// informational output, so a broken stdout cannot discard a recorded
// certificate, and a failure before the files are fully written revokes the
// serial rather than leaving a valid certificate whose key nobody holds.
func (c *CLI) publishAdminCert(ctx context.Context, svc *core.Service, output *reservedCertBundle, name string, bundle *core.CertBundle) error {
	if c.jsonOutput() {
		return c.publishAdminCertJSON(ctx, svc, output, name, bundle)
	}
	if err := c.writeCertBundleToOutput(output, toProtoCertBundle(bundle)); err != nil {
		return c.orphanedAdminCert(ctx, svc, output, name, bundle.Serial, err)
	}
	if _, err := fmt.Fprintf(c.Stdout, "Issued admin client certificate for identity %q (expires %s).\n",
		name, bundle.NotAfter.UTC().Format(time.RFC3339)); err != nil {
		return c.orphanedAdminCert(ctx, svc, output, name, bundle.Serial, fmt.Errorf("writing certificate output: %w", err))
	}
	if err := c.writeAdminCertNextSteps(output, name, bundle.Serial); err != nil {
		return c.orphanedAdminCert(ctx, svc, output, name, bundle.Serial, err)
	}
	return nil
}

// publishAdminCertJSON is the JSON half of publishAdminCert: the credential
// files are written by publishAdminCertFiles and stdout receives one object
// naming them. Every failure still routes through orphanedAdminCert, so an
// unusable certificate is revoked here too.
func (c *CLI) publishAdminCertJSON(ctx context.Context, svc *core.Service, output *reservedCertBundle, name string, bundle *core.CertBundle) error {
	cert, err := c.publishAdminCertFiles(ctx, svc, output, name, bundle)
	if err != nil {
		return err
	}
	if err := writeJSON(c.Stdout, issuedCertJSON{Name: name, Serial: bundle.Serial, Cert: cert}); err != nil {
		return c.orphanedAdminCert(ctx, svc, output, name, bundle.Serial, fmt.Errorf("writing certificate output: %w", err))
	}
	return nil
}

// publishAdminCertFiles writes the credential files exactly as in table mode
// (the private key never enters a document) and moves the status line and the
// CLI/browser guidance to stderr, leaving stdout to the caller's document. The
// one-time-key warning is not an info line: --quiet must not hide that the key
// on disk is the only copy. Failures are already wrapped by orphanedAdminCert.
func (c *CLI) publishAdminCertFiles(ctx context.Context, svc *core.Service, output *reservedCertBundle, name string, bundle *core.CertBundle) (*certFilesJSON, error) {
	cert, err := writeCertBundleFiles(output, toProtoCertBundle(bundle))
	if err != nil {
		return nil, c.orphanedAdminCert(ctx, svc, output, name, bundle.Serial, err)
	}
	c.info("Issued admin client certificate for identity %q: wrote %s and %s (serial %s, expires %s).",
		name, output.certPath, output.keyPath, bundle.Serial, bundle.NotAfter.UTC().Format(time.RFC3339))
	if _, err := fmt.Fprintln(c.Stderr, "WARNING: the private key is written once and is never stored server-side."); err != nil {
		return nil, c.orphanedAdminCert(ctx, svc, output, name, bundle.Serial, fmt.Errorf("writing certificate output: %w", err))
	}
	if err := c.writeAdminCertNextSteps(output, name, bundle.Serial); err != nil {
		return nil, c.orphanedAdminCert(ctx, svc, output, name, bundle.Serial, err)
	}
	return cert, nil
}

// orphanedAdminCert turns a publish failure into an actionable error. When the
// certificate and key files were fully written (output.published) the
// credential is intact and only the guidance was lost, so the certificate is
// left valid and named. Otherwise the key is gone for good: revoke the serial
// (best effort, host-local) so the identity is not reported as certified by a
// certificate nobody can use, and print the manual revoke command if that
// fails.
func (c *CLI) orphanedAdminCert(ctx context.Context, svc *core.Service, output *reservedCertBundle, name, serial string, cause error) error {
	if output != nil && output.published {
		return fmt.Errorf("%w (certificate %s was written and remains valid)", cause, serial)
	}
	if rerr := svc.RevokeIdentityCertificate(ctx, localAdminPrincipal(), name, serial); rerr != nil {
		return fmt.Errorf("%w; certificate %s was recorded but its private key was not written, and revoking it failed (%v) — run: parameter-store admin-cert revoke %s --serial %s",
			cause, serial, rerr, name, serial)
	}
	return fmt.Errorf("%w; certificate %s was recorded but its private key was not written, so it has been revoked — issue a replacement with: parameter-store admin-cert issue %s --out <dir>", cause, serial, name)
}

// toProtoCertBundle renders a locally issued bundle in the shape the gRPC path
// returns, so offline issuance reuses writeCertBundleToOutput (and with it the
// exclusive-reservation and 0600 discipline) instead of writing files itself.
func toProtoCertBundle(b *core.CertBundle) *kmsv1.CertBundle {
	if b == nil {
		return nil
	}
	var notAfter int64
	if !b.NotAfter.IsZero() {
		notAfter = b.NotAfter.UnixMilli()
	}
	return &kmsv1.CertBundle{
		CertPem:        b.CertPEM,
		KeyPem:         b.KeyPEM,
		Serial:         b.Serial,
		NotAfterUnixMs: notAfter,
	}
}

// writeAdminCertNextSteps explains how to use an admin certificate from the CLI
// and from a browser. Unlike the application guidance, both consumers are
// operated by a person: the browser needs a PKCS#12 import, and the credential
// is only half of the admin's authentication.
func (c *CLI) writeAdminCertNextSteps(output *reservedCertBundle, name, serial string) error {
	if output == nil {
		return nil
	}
	p12Path := filepath.Join(filepath.Dir(output.certPath), name+".p12")
	guidance := fmt.Sprintf(`
Admin client credentials:
  client certificate: %[1]s
  client private key: %[2]s

Next steps:
  1. CLI: add these flags to every "parameter-store admin ..." command, alongside --token:
       --cert %[1]s --key %[2]s
     Or export them once:
       export KMS_CLIENT_CERT_FILE=%[1]s
       export KMS_CLIENT_KEY_FILE=%[2]s
       export KMS_TOKEN=<the admin bearer token>
  2. Browser (admin console): convert the pair to PKCS#12, then import it.
       openssl pkcs12 -export -inkey %[2]s -in %[1]s -name "parameter-store %[3]s" -out %[5]s
     Chrome/Edge: import into the operating system store (macOS Keychain Access,
     Windows certmgr); on Linux use chrome://settings/certificates, tab
     "Your certificates", Import.
     Firefox: Settings > Privacy & Security > View Certificates > Your Certificates > Import.
     Reload the console afterwards. The browser picks the certificate per TLS
     connection and cannot sign out of it; close the browser to stop presenting it.
  3. The certificate alone never signs you in: the console and the CLI still require
     the admin bearer token.
  4. Revoke this certificate later with:
       parameter-store admin-cert revoke %[3]s --serial %[4]s
`, output.certPath, output.keyPath, name, serial, p12Path)
	// JSON mode keeps stdout to the document alone; the guidance is advice, so
	// it goes through info and --quiet may drop it.
	if c.jsonOutput() {
		c.info("%s", strings.TrimRight(guidance, "\n"))
		return nil
	}
	if _, err := fmt.Fprint(c.Stdout, guidance); err != nil {
		return fmt.Errorf("writing certificate guidance: %w", err)
	}
	return nil
}

// --- admin-cert revoke -----------------------------------------------------

func (c *CLI) cmdAdminCertRevoke(args []string) int {
	fs := c.newFlags("admin-cert revoke")
	r := c.serverSettings(fs, "storage.sqlite_path")
	serial := fs.String("serial", "", "certificate `serial` to revoke")
	c.setUsage(fs, "admin-cert revoke NAME --serial SERIAL [flags]",
		"Revoke one admin client certificate directly in the database. The master key is not needed.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	name, ok := c.adminCertName("revoke")
	if !ok {
		return 2
	}
	cfg, _, _, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *serial == "" {
		return c.failUsage("--serial is required")
	}
	// Revocation is immediate and irreversible for that credential, so the
	// operator retypes the admin it belongs to before the database is opened.
	if ok, code := c.confirmDestructive("revoke admin certificate "+*serial+" of identity", name); !ok {
		return code
	}
	ctx := context.Background()

	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	// A disabled admin's certificates stay revocable; only the kind is checked,
	// for symmetry with issue.
	if _, err := c.requireAdminTarget(ctx, store, name, false); err != nil {
		return c.failErr("", err)
	}
	svc := core.New(store, c.quietLogger(), Version)
	if err := svc.RevokeIdentityCertificate(ctx, localAdminPrincipal(), name, *serial); err != nil {
		return c.failErr("revoking certificate", err)
	}
	if c.jsonOutput() {
		c.info("Revoked certificate %s for admin identity %q. It stops authenticating on the next request; a running server sees the change immediately.",
			*serial, name)
		return c.printJSON(revokedCertJSON{Name: name, Serial: *serial, Revoked: true})
	}
	_, _ = fmt.Fprintf(c.Stdout,
		"Revoked certificate %s for admin identity %q. It stops authenticating on the next request; a running server sees the change immediately.\n",
		*serial, name)
	return 0
}

// --- admin-cert list -------------------------------------------------------

func (c *CLI) cmdAdminCertList(args []string) int {
	fs := c.newFlags("admin-cert list")
	r := c.serverSettings(fs, "storage.sqlite_path")
	c.setUsage(fs, "admin-cert list NAME [flags]",
		"List an admin identity's client certificates with their serial, fingerprint, and state.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	name, ok := c.adminCertName("list")
	if !ok {
		return 2
	}
	cfg, _, _, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	ctx := context.Background()

	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	if _, err := c.requireAdminTarget(ctx, store, name, false); err != nil {
		return c.failErr("", err)
	}
	certs, err := store.ListIdentityCerts(ctx, name)
	if err != nil {
		return c.failErr("listing certificates", err)
	}

	now := time.Now()
	if c.jsonOutput() {
		items := make([]adminCertJSON, 0, len(certs))
		for _, cert := range certs {
			items = append(items, adminCertJSON{
				Serial:      cert.Serial,
				Fingerprint: cert.Fingerprint,
				State:       certState(cert, now),
				ExpiresAt:   jsonTimeOf(cert.NotAfter),
				IssuedAt:    jsonTimeOf(cert.CreatedAt),
			})
		}
		return c.printList(items, "")
	}
	rows := make([][]string, 0, len(certs))
	for _, cert := range certs {
		rows = append(rows, []string{
			cert.Serial, cert.Fingerprint, certState(cert, now),
			formatCertTime(cert.NotAfter), formatCertTime(cert.CreatedAt),
		})
	}
	c.printTable([]string{"SERIAL", "FINGERPRINT", "STATE", "EXPIRES", "ISSUED"}, rows)
	return 0
}

// adminCertJSON is the JSON view of one admin client certificate: every column
// the table shows, with the two timestamps in RFC 3339 (null when unset, which
// the table spells "-").
type adminCertJSON struct {
	Serial      string  `json:"serial"`
	Fingerprint string  `json:"fingerprint"`
	State       string  `json:"state"`
	ExpiresAt   *string `json:"expires_at"`
	IssuedAt    *string `json:"issued_at"`
}

// certState reports how a certificate would be treated at verification time.
// Revocation is an explicit administrative act, so it outranks expiry in the
// display even when both apply.
func certState(cert domain.IdentityCert, now time.Time) string {
	switch {
	case !cert.RevokedAt.IsZero():
		return "revoked"
	case !cert.NotAfter.IsZero() && !now.Before(cert.NotAfter):
		return "expired"
	default:
		return "valid"
	}
}

func formatCertTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
