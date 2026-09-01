package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Suhaibinator/kms/internal/config"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// quietLogger builds a warn-level logger for administrative commands so normal
// runs keep stdout/stderr clean while still surfacing problems.
func (c *CLI) quietLogger() *zap.Logger {
	return newLogger(c.Stderr, zapcore.WarnLevel)
}

// dbTarget renders the database a command is about to act on as an absolute
// path plus the layer that supplied it, e.g.
// "/data/kms.db (source: env KMS_SQLITE_PATH)". Offline commands print it so an
// operator can see at a glance which file was chosen and why — the flag, an
// environment variable, the config file, or the built-in default.
func dbTarget(cfg config.Config, prov config.Provenance) string {
	return fmt.Sprintf("%s (source: %s)", absPath(cfg.Storage.SQLitePath), prov["storage.sqlite_path"])
}

// absPath resolves path against the working directory, falling back to the
// original string when that is not possible.
func absPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// requireSQLitePath rejects an empty storage.sqlite_path. Offline commands
// cannot call Config.Validate (it stats TLS material they never use), so they
// check the one setting they depend on themselves.
func (c *CLI) requireSQLitePath(cfg config.Config) error {
	if cfg.Storage.SQLitePath == "" {
		return errors.New("storage.sqlite_path must not be empty")
	}
	return nil
}

// warnDestructiveTarget prints the database a destructive command is about to
// overwrite to stderr, before it acts, so an operator who pointed the command
// at the wrong file by way of a stale environment variable can still stop it.
func (c *CLI) warnDestructiveTarget(cfg config.Config, prov config.Provenance) {
	_, _ = fmt.Fprintf(c.Stderr, "Target database: %s\n", dbTarget(cfg, prov))
}

// --- init ------------------------------------------------------------------

func (c *CLI) cmdInit(args []string) int {
	fs := c.newFlags("init")
	r := c.serverSettings(fs, "storage.sqlite_path", "encryption.kek_file")
	admin := fs.String("admin", "", "also create a bootstrap admin identity with this `name`")
	certDir := fs.String("cert-dir", "", "`directory` for the bootstrap admin's client certificate (NAME.crt/NAME.key); requires --admin")
	c.setUsage(fs, "init [flags]",
		"Create or migrate the database, establish its master key and built-in CA, and optionally create a bootstrap admin identity with its client certificate.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	if *certDir != "" && *admin == "" {
		return c.fail("--cert-dir requires --admin")
	}
	ctx := context.Background()

	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// CreateKeyFileIfMissing generates the key file on first init; without a
	// key file the prompter asks for a passphrase (twice, with confirmation).
	keyring, err := c.unseal(ctx, store, cfg.Encryption.KEKFile, true)
	if err != nil {
		return c.fail("initializing master key: %v", err)
	}
	defer keyring.Active().Destroy()

	// init is the one place that creates the built-in CA: it runs once, before
	// any server, so it cannot race a concurrent generator (InsertCAKey retires
	// every other CA row). BootstrapCA is get-or-create, so re-running init on
	// an existing database keeps the CA it already has.
	svc := core.New(store, c.quietLogger(), Version)
	svc.SetKeyring(keyring)
	if err := svc.BootstrapCA(ctx); err != nil {
		return c.fail("preparing built-in CA: %v", err)
	}

	_, _ = fmt.Fprintf(c.Stdout, "Initialized database at %s\n", dbTarget(cfg, prov))
	if cfg.Encryption.KEKFile != "" {
		_, _ = fmt.Fprintf(c.Stdout, "Master key file: %s (back this up separately from the database)\n", cfg.Encryption.KEKFile)
	} else {
		_, _ = fmt.Fprintln(c.Stdout, "Master key derived from passphrase. You must supply it on every start.")
	}
	_, _ = fmt.Fprintln(c.Stdout, "Built-in CA: ready")

	if *admin != "" {
		if err := c.requireNoIdentity(ctx, store, *admin); err != nil {
			return c.fail("%v", err)
		}
		if err := c.withReservedCertBundle(*certDir, *admin, func(output *reservedCertBundle) error {
			return c.createBootstrapAdmin(ctx, store, svc, *admin, output)
		}); err != nil {
			return c.fail("%v", err)
		}
	}
	return 0
}

// requireNoIdentity refuses to create an identity that already exists before
// any certificate output files are reserved, so a retried init/create-admin
// does not leave empty NAME.crt/NAME.key placeholders behind and points at the
// command that adds a certificate to an existing admin instead.
func (c *CLI) requireNoIdentity(ctx context.Context, store storage.Store, name string) error {
	_, err := store.GetIdentityByName(ctx, name)
	switch {
	case err == nil:
		return fmt.Errorf("identity %q already exists; to issue it a client certificate run: parameter-store admin-cert issue %s --out <dir>", name, name)
	case errors.Is(err, domain.ErrNotFound):
		return nil
	default:
		return fmt.Errorf("checking identity %q: %w", name, err)
	}
}

// createBootstrapAdmin creates an admin identity directly in the store, prints
// its one-time token, and — when output reserves a destination — issues and
// writes its client certificate. Both credentials are shown exactly once.
func (c *CLI) createBootstrapAdmin(ctx context.Context, store storage.Store, svc *core.Service, name string, output *reservedCertBundle) error {
	token, hash, err := crypto.GenerateToken("kms")
	if err != nil {
		return fmt.Errorf("generating admin token: %w", err)
	}
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name:      name,
		Kind:      domain.IdentityKindAdmin,
		TokenHash: hash,
	}); err != nil {
		return fmt.Errorf("creating admin identity: %w", err)
	}
	if err := printTokenOnce(c.Stdout, "admin identity", name, token); err != nil {
		return fmt.Errorf("writing one-time admin token: %w", err)
	}
	if output == nil {
		return nil
	}
	bundle, err := svc.IssueLocalAdminCertificate(ctx, localAdminPrincipal(), name, 0)
	if err != nil {
		return fmt.Errorf("issuing admin client certificate: %w", err)
	}
	return c.publishAdminCert(ctx, svc, output, name, bundle)
}

// --- migrate ---------------------------------------------------------------

func (c *CLI) cmdMigrate(args []string) int {
	fs := c.newFlags("migrate")
	r := c.serverSettings(fs, "storage.sqlite_path")
	c.setUsage(fs, "migrate [flags]", "Apply any pending database migrations.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	// Open runs migrations inside the transaction and refuses a newer schema.
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("migrating database: %v", err)
	}
	_ = store.Close()
	_, _ = fmt.Fprintf(c.Stdout, "Migrations applied to %s\n", dbTarget(cfg, prov))
	return 0
}

// --- check -----------------------------------------------------------------

func (c *CLI) cmdCheck(args []string) int {
	fs := c.newFlags("check")
	r := c.serverSettings(fs, "storage.sqlite_path", "encryption.kek_file")
	c.setUsage(fs, "check [flags]",
		"Verify that the database opens with an up-to-date schema and, when a key source is available, that the master key unseals it.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	ctx := context.Background()

	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Ping(ctx); err != nil {
		return c.fail("database unreachable: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Database OK (schema up to date): %s\n", dbTarget(cfg, prov))

	// Verify the master key only if the database has been initialized and some
	// key source is available. Never print key material.
	if _, err := store.ActiveKeyMetadata(ctx); errors.Is(err, domain.ErrNotFound) {
		_, _ = fmt.Fprintln(c.Stdout, "Master key: database not yet initialized (run init).")
		return 0
	}
	// Build the options once and reuse them: Unseal zeroes the passphrase it is
	// given, so a second unsealOptions call would be needed to retry.
	opts := c.unsealOptions(cfg.Encryption.KEKFile, false)
	if !opts.HasKeySource() {
		_, _ = fmt.Fprintln(c.Stdout, "Master key: not checked (no key file, passphrase, or TTY available).")
		return 0
	}
	keyring, err := crypto.Unseal(ctx, store, opts)
	if err != nil {
		return c.fail("master key verification failed: %v", err)
	}
	keyring.Active().Destroy()
	_, _ = fmt.Fprintln(c.Stdout, "Master key OK.")
	return 0
}

// --- backup ----------------------------------------------------------------

func (c *CLI) cmdBackup(args []string) int {
	fs := c.newFlags("backup")
	r := c.serverSettings(fs, "storage.sqlite_path")
	out := fs.String("out", "", "backup output `file` (must not exist)")
	c.setUsage(fs, "backup [flags]",
		"Write a consistent online backup of the database to --out; the master key is not included.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, _, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	if *out == "" {
		return c.fail("--out is required")
	}
	if fileExists(*out) {
		return c.fail("output file %s already exists; refusing to overwrite", *out)
	}
	if err := storage.ValidateKMSDatabase(cfg.Storage.SQLitePath); err != nil {
		return c.fail("invalid backup source: %v", err)
	}
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Backup(context.Background(), *out); err != nil {
		return c.fail("backup failed: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Backup written to %s\n", *out)
	_, _ = fmt.Fprintln(c.Stdout, "Note: the master key is NOT included; back it up separately.")
	return 0
}

// --- restore ---------------------------------------------------------------

func (c *CLI) cmdRestore(args []string) int {
	fs := c.newFlags("restore")
	r := c.serverSettings(fs, "storage.sqlite_path")
	in := fs.String("in", "", "backup input `file`")
	force := fs.Bool("force", false, "overwrite an existing destination database")
	c.setUsage(fs, "restore [flags]",
		"Restore the database from a backup file. The server must be stopped; an existing destination is replaced only with --force.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	if *in == "" {
		return c.fail("--in is required")
	}
	// Restore overwrites the destination: name it before touching anything.
	c.warnDestructiveTarget(cfg, prov)
	if err := restoreFile(*in, cfg.Storage.SQLitePath, *force); err != nil {
		return c.fail("%v", err)
	}

	_, _ = fmt.Fprintf(c.Stdout, "Restored %s from %s\n", dbTarget(cfg, prov), *in)
	_, _ = fmt.Fprintln(c.Stdout, "Next steps: ensure the matching master key (file or passphrase) is available before starting the server.")
	return 0
}

// --- create-admin ----------------------------------------------------------

func (c *CLI) cmdCreateAdmin(args []string) int {
	fs := c.newFlags("create-admin")
	r := c.serverSettings(fs, "storage.sqlite_path", "encryption.kek_file")
	name := fs.String("name", "", "admin identity `name`")
	certDir := fs.String("cert-dir", "", "`directory` for the admin's client certificate (NAME.crt/NAME.key); omit for a token-only admin")
	c.setUsage(fs, "create-admin [flags]",
		"Create an admin identity directly in the database and print its token once; with --cert-dir also issue its client certificate.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	if *name == "" {
		return c.fail("--name is required")
	}
	ctx := context.Background()
	// Direct store access. WAL mode allows this concurrently with a running
	// server, but the caller is responsible for that coordination.
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// The master key is only needed to issue a certificate: a token-only admin
	// is created without unsealing (and so without a passphrase prompt).
	var svc *core.Service
	if *certDir != "" {
		issuer, closeCA, err := c.requireLocalCA(ctx, store, cfg, prov)
		if err != nil {
			return c.fail("%v", err)
		}
		defer closeCA()
		svc = issuer
	}

	if err := c.requireNoIdentity(ctx, store, *name); err != nil {
		return c.fail("%v", err)
	}
	if err := c.withReservedCertBundle(*certDir, *name, func(output *reservedCertBundle) error {
		return c.createBootstrapAdmin(ctx, store, svc, *name, output)
	}); err != nil {
		return c.fail("%v", err)
	}
	return 0
}

// --- rotate-admin ----------------------------------------------------------

func (c *CLI) cmdRotateAdmin(args []string) int {
	fs := c.newFlags("rotate-admin")
	r := c.serverSettings(fs, "storage.sqlite_path")
	name := fs.String("name", "", "admin identity `name`")
	c.setUsage(fs, "rotate-admin [flags]",
		"Recover an existing admin by rotating its token directly in the database, printing the replacement once.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, _, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	if *name == "" {
		return c.fail("--name is required")
	}

	// Direct store access makes this the recovery path when no usable admin
	// credential remains. WAL mode allows a running server to observe the new
	// hash immediately, but the operator must coordinate concurrent identity
	// administration.
	ctx := context.Background()
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	identity, err := store.GetIdentityByName(ctx, *name)
	if err != nil {
		return c.fail("loading admin identity: %v", err)
	}
	if identity.Kind != domain.IdentityKindAdmin {
		return c.fail("identity %s is not an admin", *name)
	}
	if identity.Disabled {
		return c.fail("admin identity %s is disabled; refusing to re-enable it", *name)
	}
	if !identity.HasToken {
		return c.fail("admin identity %s has no token to rotate", *name)
	}

	svc := core.New(store, c.quietLogger(), Version)
	token, err := svc.RotateIdentityToken(ctx, localAdminPrincipal(), *name)
	if err != nil {
		return c.fail("rotating admin token: %v", err)
	}
	if err := printRotatedTokenOnce(c.Stdout, "admin identity", *name, token); err != nil {
		return c.fail("writing one-time admin token: %v", err)
	}
	return 0
}

// --- rotate-kek ------------------------------------------------------------

func (c *CLI) cmdRotateKEK(args []string) int {
	fs := c.newFlags("rotate-kek")
	r := c.serverSettings(fs, "storage.sqlite_path", "encryption.kek_file")
	newKeyFile := fs.String("new-key-file", "", "new master key `file` (generated if absent); omit to enter a new passphrase")
	c.setUsage(fs, "rotate-kek [flags]",
		"Rotate the master key, rewrapping every secret version and CA key under the new key.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return c.fail("%v", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.fail("%v", err)
	}
	ctx := context.Background()

	// Rotation rewrites every wrapped row in place: name the database first.
	c.warnDestructiveTarget(cfg, prov)
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Unseal the current keyring exactly as serve does.
	current, err := c.unseal(ctx, store, cfg.Encryption.KEKFile, false)
	if err != nil {
		return c.fail("unsealing current master key: %v", err)
	}
	svc := core.New(store, c.quietLogger(), Version)
	svc.SetKeyring(current)

	newKM, material, err := c.buildNewKEK(*newKeyFile)
	if err != nil {
		return c.fail("preparing new master key: %v", err)
	}

	secretsRewrapped, caRewrapped, err := svc.RotateKEK(ctx, localAdminPrincipal(), newKM, material)
	if err != nil {
		return c.fail("rotating KEK: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "KEK rotated: %d secret versions and %d CA keys rewrapped under %s\n",
		secretsRewrapped, caRewrapped, newKM.ID)
	if *newKeyFile != "" {
		_, _ = fmt.Fprintf(c.Stdout, "New master key file: %s (back it up; the old key is no longer sufficient after retirement).\n", *newKeyFile)
	} else {
		_, _ = fmt.Fprintln(c.Stdout, "New master key derived from the entered passphrase; use it on the next start.")
	}
	// A running server holds the OLD keyring in memory and will fail to decrypt
	// the rewrapped rows until it is restarted with the new key. Rotate with the
	// server stopped, or update its master-key configuration and restart it.
	_, _ = fmt.Fprintln(c.Stderr, "IMPORTANT: point any running server at the new master key and restart it; "+
		"the old key can no longer decrypt secrets after this rotation.")
	return 0
}

// buildNewKEK produces the new key metadata and 32-byte material for rotation.
func (c *CLI) buildNewKEK(newKeyFile string) (domain.KeyMetadata, []byte, error) {
	id, err := crypto.NewKEKID()
	if err != nil {
		return domain.KeyMetadata{}, nil, err
	}
	km := domain.KeyMetadata{ID: id}

	if newKeyFile != "" {
		var material []byte
		if fileExists(newKeyFile) {
			material, err = crypto.LoadKEKMaterialFromFile(newKeyFile)
		} else {
			material, err = crypto.WriteKEKMaterialFile(newKeyFile)
		}
		if err != nil {
			return domain.KeyMetadata{}, nil, err
		}
		km.Source = domain.KeySourceFile
		return km, material, nil
	}

	passphrase, err := c.readNewPassphrase()
	if err != nil {
		return domain.KeyMetadata{}, nil, err
	}
	defer crypto.Zero(passphrase)
	params := crypto.DefaultArgon2Params()
	salt, err := crypto.NewKDFSalt()
	if err != nil {
		return domain.KeyMetadata{}, nil, err
	}
	material, err := crypto.DeriveKEKMaterialFromPassphrase(passphrase, salt, params)
	if err != nil {
		return domain.KeyMetadata{}, nil, err
	}
	km.Source = domain.KeySourcePassphrase
	km.KDF = crypto.MarshalParams(params)
	km.KDFSalt = salt
	return km, material, nil
}

// --- shared helpers --------------------------------------------------------

// printTokenOnce prints a freshly minted token with a clear one-time warning.
func printTokenOnce(w io.Writer, kind, name, token string) error {
	if _, err := fmt.Fprintf(w, "Created %s %q.\n", kind, name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  token: %s\n", token); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "  WARNING: this token is shown once and cannot be recovered. Store it securely.")
	return err
}

// printRotatedTokenOnce prints a replacement token without implying that the
// identity itself was recreated.
func printRotatedTokenOnce(w io.Writer, kind, name, token string) error {
	if _, err := fmt.Fprintf(w, "Rotated %s %q.\n", kind, name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  token: %s\n", token); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "  WARNING: this token is shown once and cannot be recovered. Store it securely.")
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
