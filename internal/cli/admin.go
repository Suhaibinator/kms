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
// It deliberately bypasses info: --quiet must not hide the target of an
// irreversible command.
func (c *CLI) warnDestructiveTarget(cfg config.Config, prov config.Provenance) {
	_, _ = fmt.Fprintf(c.Stderr, "Target database: %s\n", dbTarget(cfg, prov))
}

// bootstrapCertJSON names the files a locally issued client certificate was
// written to. Offline issuance never emits a separate CA file, so only the
// pair appears.
type bootstrapCertJSON struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// initAdminJSON is the bootstrap admin created by init or create-admin. The
// token is minted once and appears nowhere else in the output.
type initAdminJSON struct {
	Name  string             `json:"name"`
	Token string             `json:"token"`
	Cert  *bootstrapCertJSON `json:"cert"`
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *certDir != "" && *admin == "" {
		return c.failUsage("--cert-dir requires --admin")
	}
	ctx := context.Background()

	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	// CreateKeyFileIfMissing generates the key file on first init; without a
	// key file the prompter asks for a passphrase (twice, with confirmation).
	keyring, err := c.unseal(ctx, store, cfg.Encryption.KEKFile, true)
	if err != nil {
		return c.failErr("initializing master key", err)
	}
	defer keyring.Active().Destroy()

	// init is the one place that creates the built-in CA: it runs once, before
	// any server, so it cannot race a concurrent generator (InsertCAKey retires
	// every other CA row). BootstrapCA is get-or-create, so re-running init on
	// an existing database keeps the CA it already has.
	svc := core.New(store, c.quietLogger(), Version)
	svc.SetKeyring(keyring)
	if err := svc.BootstrapCA(ctx); err != nil {
		return c.failErr("preparing built-in CA", err)
	}

	c.resultLine("Initialized database at %s", dbTarget(cfg, prov))
	if cfg.Encryption.KEKFile != "" {
		c.resultLine("Master key file: %s (back this up separately from the database)", cfg.Encryption.KEKFile)
	} else {
		c.resultLine("Master key derived from passphrase. You must supply it on every start.")
	}
	c.resultLine("Built-in CA: ready")

	document := initJSON{
		SQLitePath:       absPath(cfg.Storage.SQLitePath),
		SQLitePathSource: prov["storage.sqlite_path"].String(),
		MasterKey:        "passphrase",
		KEKFile:          cfg.Encryption.KEKFile,
		CA:               "ready",
	}
	if cfg.Encryption.KEKFile != "" {
		document.MasterKey = "file"
	}
	if *admin != "" {
		if err := c.requireNoIdentity(ctx, store, *admin); err != nil {
			return c.failErr("", err)
		}
		if err := c.withReservedCertBundle(*certDir, *admin, func(output *reservedCertBundle) error {
			created, err := c.createBootstrapAdmin(ctx, store, svc, *admin, output)
			document.Admin = created
			return err
		}); err != nil {
			return c.failErr("", err)
		}
	}
	if c.jsonOutput() {
		return c.printJSON(document)
	}
	return 0
}

// initJSON is the JSON form of init. admin is null unless --admin created one;
// kek_file is absent when the master key comes from a passphrase.
type initJSON struct {
	SQLitePath       string         `json:"sqlite_path"`
	SQLitePathSource string         `json:"sqlite_path_source"`
	MasterKey        string         `json:"master_key"` // file | passphrase
	KEKFile          string         `json:"kek_file,omitempty"`
	CA               string         `json:"ca"` // ready
	Admin            *initAdminJSON `json:"admin"`
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

// createBootstrapAdmin creates an admin identity directly in the store and —
// when output reserves a destination — issues and writes its client
// certificate. In table mode both credentials are printed here, exactly once;
// in JSON mode they are returned so the caller places them in the single
// result document instead.
func (c *CLI) createBootstrapAdmin(ctx context.Context, store storage.Store, svc *core.Service, name string, output *reservedCertBundle) (*initAdminJSON, error) {
	token, hash, err := crypto.GenerateToken("kms")
	if err != nil {
		return nil, fmt.Errorf("generating admin token: %w", err)
	}
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name:      name,
		Kind:      domain.IdentityKindAdmin,
		TokenHash: hash,
	}); err != nil {
		return nil, fmt.Errorf("creating admin identity: %w", err)
	}
	created := &initAdminJSON{Name: name, Token: token}
	if c.jsonOutput() {
		// The token is still unrecoverable, so the warning is printed even
		// though the value itself now travels in the result document.
		_, _ = fmt.Fprintln(c.Stderr, "WARNING: this token is shown once and cannot be recovered. Store it securely.")
	} else if err := printTokenOnce(c.Stdout, "admin identity", name, token); err != nil {
		return nil, fmt.Errorf("writing one-time admin token: %w", err)
	}
	if output == nil {
		return created, nil
	}
	bundle, err := svc.IssueLocalAdminCertificate(ctx, localAdminPrincipal(), name, 0)
	if err != nil {
		return nil, c.bootstrapAdminCertFailure(name, fmt.Errorf("issuing admin client certificate: %w", err))
	}
	if err := c.publishBootstrapCert(ctx, svc, output, name, bundle); err != nil {
		return nil, c.bootstrapAdminCertFailure(name, err)
	}
	created.Cert = &bootstrapCertJSON{CertFile: output.certPath, KeyFile: output.keyPath}
	return created, nil
}

// publishBootstrapCert writes a freshly issued admin certificate to its
// reserved files. Table mode is publishAdminCert. JSON mode writes the files
// and the stderr guidance only: stdout carries nothing but the caller's
// document, and the file paths appear inside it.
func (c *CLI) publishBootstrapCert(ctx context.Context, svc *core.Service, output *reservedCertBundle, name string, bundle *core.CertBundle) error {
	if !c.jsonOutput() {
		return c.publishAdminCert(ctx, svc, output, name, bundle)
	}
	_, err := c.publishAdminCertFiles(ctx, svc, output, name, bundle)
	return err
}

// bootstrapAdminCertFailure annotates a certificate failure that happens after
// the admin identity and its one-time token already exist. Table mode has
// printed the token by then; JSON mode never will, because the document is
// only written on success — so name the command that mints a replacement
// rather than leaving behind an admin whose token nobody holds.
func (c *CLI) bootstrapAdminCertFailure(name string, err error) error {
	if !c.jsonOutput() {
		return err
	}
	return fmt.Errorf("%w; admin identity %q was created but its token was not printed — mint a replacement with: parameter-store rotate-admin --name %s", err, name, name)
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	// Open runs migrations inside the transaction and refuses a newer schema.
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("migrating database", err)
	}
	_ = store.Close()
	if c.jsonOutput() {
		return c.printJSON(migrateJSON{
			SQLitePath:       absPath(cfg.Storage.SQLitePath),
			SQLitePathSource: prov["storage.sqlite_path"].String(),
			Migrated:         true,
		})
	}
	_, _ = fmt.Fprintf(c.Stdout, "Migrations applied to %s\n", dbTarget(cfg, prov))
	return 0
}

// migrateJSON is the JSON form of migrate. migrated is always true: a failed
// migration exits non-zero with an error instead of reporting false.
type migrateJSON struct {
	SQLitePath       string `json:"sqlite_path"`
	SQLitePathSource string `json:"sqlite_path_source"`
	Migrated         bool   `json:"migrated"`
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	ctx := context.Background()

	// check is a health probe: it keeps its documented 0/1 exit codes rather
	// than classifying failures the way the other offline commands do, so a
	// monitoring script can branch on "healthy" alone.
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Ping(ctx); err != nil {
		return c.fail("database unreachable: %v", err)
	}
	document := checkJSON{
		Database:         "ok",
		SQLitePath:       absPath(cfg.Storage.SQLitePath),
		SQLitePathSource: prov["storage.sqlite_path"].String(),
	}
	c.resultLine("Database OK (schema up to date): %s", dbTarget(cfg, prov))

	// Verify the master key only if the database has been initialized and some
	// key source is available. Never print key material.
	if _, err := store.ActiveKeyMetadata(ctx); errors.Is(err, domain.ErrNotFound) {
		document.MasterKey = "not_initialized"
		c.resultLine("Master key: database not yet initialized (run init).")
		return c.finishCheck(document)
	}
	// Build the options once and reuse them: Unseal zeroes the passphrase it is
	// given, so a second unsealOptions call would be needed to retry.
	opts := c.unsealOptions(cfg.Encryption.KEKFile, false)
	if !opts.HasKeySource() {
		document.MasterKey = "not_checked"
		c.resultLine("Master key: not checked (no key file, passphrase, or TTY available).")
		return c.finishCheck(document)
	}
	keyring, err := crypto.Unseal(ctx, store, opts)
	if err != nil {
		return c.fail("master key verification failed: %v", err)
	}
	keyring.Active().Destroy()
	document.MasterKey = "ok"
	c.resultLine("Master key OK.")
	return c.finishCheck(document)
}

// checkJSON is the JSON form of check. Every verdict the command can reach
// with exit code 0 is named here; a failure exits 1 with an error instead.
type checkJSON struct {
	Database         string `json:"database"`   // ok
	MasterKey        string `json:"master_key"` // ok | not_initialized | not_checked
	SQLitePath       string `json:"sqlite_path"`
	SQLitePathSource string `json:"sqlite_path_source"`
}

// finishCheck renders the verdict in JSON mode; table mode has already printed
// its lines. printJSON's own failure exit is 1, which check documents.
func (c *CLI) finishCheck(document checkJSON) int {
	if c.jsonOutput() {
		return c.printJSON(document)
	}
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *out == "" {
		return c.failUsage("--out is required")
	}
	if fileExists(*out) {
		return c.failErr("", fmt.Errorf("output file %s: %w; refusing to overwrite", *out, os.ErrExist))
	}
	if err := storage.ValidateKMSDatabase(cfg.Storage.SQLitePath); err != nil {
		return c.failErr("invalid backup source", err)
	}
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Backup(context.Background(), *out); err != nil {
		return c.failErr("backup failed", err)
	}
	c.resultLine("Backup written to %s", *out)
	c.resultLine("Note: the master key is NOT included; back it up separately.")
	if c.jsonOutput() {
		return c.printJSON(backupJSON{BackupFile: *out, SQLitePath: absPath(cfg.Storage.SQLitePath)})
	}
	return 0
}

// backupJSON is the JSON form of backup. The master key is deliberately not
// part of the backup, and so has no field here.
type backupJSON struct {
	BackupFile string `json:"backup_file"`
	SQLitePath string `json:"sqlite_path"`
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *in == "" {
		return c.failUsage("--in is required")
	}
	// Restore overwrites the destination: name it before touching anything,
	// then make the operator say yes to that exact pair of paths. --force
	// keeps its separate meaning (replace an existing destination at all), so
	// a scripted restore over a live database needs both flags.
	sqlitePath := absPath(cfg.Storage.SQLitePath)
	if !*force {
		// Refuse before the prompt: an operator should not answer "y" only to
		// learn the destination needed --force. restoreFile re-checks under
		// the atomic publish, so a file appearing in between is still refused.
		if _, err := os.Lstat(sqlitePath); err == nil {
			return c.failUsage("destination %s already exists; pass --force to overwrite", sqlitePath)
		}
	}
	c.warnDestructiveTarget(cfg, prov)
	if ok, code := c.confirmYesNo(fmt.Sprintf("restore %s from %s", sqlitePath, *in)); !ok {
		return code
	}
	if err := restoreFile(*in, cfg.Storage.SQLitePath, *force); err != nil {
		return c.failErr("", err)
	}

	c.resultLine("Restored %s from %s", dbTarget(cfg, prov), *in)
	c.resultLine("Next steps: ensure the matching master key (file or passphrase) is available before starting the server.")
	if c.jsonOutput() {
		return c.printJSON(restoreJSON{SQLitePath: sqlitePath, BackupFile: *in})
	}
	return 0
}

// restoreJSON is the JSON form of restore: the destination that now holds the
// backup, and the file it came from.
type restoreJSON struct {
	SQLitePath string `json:"sqlite_path"`
	BackupFile string `json:"backup_file"`
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *name == "" {
		return c.failUsage("--name is required")
	}
	ctx := context.Background()
	// Direct store access. WAL mode allows this concurrently with a running
	// server, but the caller is responsible for that coordination.
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	// The master key is only needed to issue a certificate: a token-only admin
	// is created without unsealing (and so without a passphrase prompt).
	var svc *core.Service
	if *certDir != "" {
		issuer, closeCA, err := c.requireLocalCA(ctx, store, cfg, prov)
		if err != nil {
			return c.failErr("", err)
		}
		defer closeCA()
		svc = issuer
	}

	if err := c.requireNoIdentity(ctx, store, *name); err != nil {
		return c.failErr("", err)
	}
	var created *initAdminJSON
	if err := c.withReservedCertBundle(*certDir, *name, func(output *reservedCertBundle) error {
		admin, err := c.createBootstrapAdmin(ctx, store, svc, *name, output)
		created = admin
		return err
	}); err != nil {
		return c.failErr("", err)
	}
	if c.jsonOutput() {
		return c.printJSON(created)
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	if *name == "" {
		return c.failUsage("--name is required")
	}

	// Direct store access makes this the recovery path when no usable admin
	// credential remains. WAL mode allows a running server to observe the new
	// hash immediately, but the operator must coordinate concurrent identity
	// administration.
	ctx := context.Background()
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	identity, err := store.GetIdentityByName(ctx, *name)
	if err != nil {
		return c.failErr("loading admin identity", err)
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
		return c.failErr("rotating admin token", err)
	}
	if c.jsonOutput() {
		// The replacement token is unrecoverable too, so the warning is kept
		// on stderr while the value itself travels in the document.
		_, _ = fmt.Fprintln(c.Stderr, "WARNING: this token is shown once and cannot be recovered. Store it securely.")
		return c.printJSON(rotatedTokenJSON{Name: *name, Token: token})
	}
	if err := printRotatedTokenOnce(c.Stdout, "admin identity", *name, token); err != nil {
		return c.fail("writing one-time admin token: %v", err)
	}
	return 0
}

// rotatedTokenJSON is the JSON form of rotate-admin: the identity and its
// replacement token, which appears here and nowhere else.
type rotatedTokenJSON struct {
	Name  string `json:"name"`
	Token string `json:"token"`
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
		return c.failErr("", err)
	}
	if err := c.requireSQLitePath(cfg); err != nil {
		return c.failUsage("%v", err)
	}
	ctx := context.Background()

	// Rotation rewrites every wrapped row in place: name the database first,
	// then make the operator retype it. Confirming before the database is even
	// opened means a refusal cannot have prompted for a passphrase or touched
	// the file.
	sqlitePath := absPath(cfg.Storage.SQLitePath)
	c.warnDestructiveTarget(cfg, prov)
	if ok, code := c.confirmDestructive("rotate the master key of", sqlitePath); !ok {
		return code
	}
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.failErr("opening database", err)
	}
	defer func() { _ = store.Close() }()

	// Unseal the current keyring exactly as serve does.
	current, err := c.unseal(ctx, store, cfg.Encryption.KEKFile, false)
	if err != nil {
		return c.failErr("unsealing current master key", err)
	}
	svc := core.New(store, c.quietLogger(), Version)
	svc.SetKeyring(current)

	newKM, material, err := c.buildNewKEK(*newKeyFile)
	if err != nil {
		return c.failErr("preparing new master key", err)
	}

	secretsRewrapped, caRewrapped, err := svc.RotateKEK(ctx, localAdminPrincipal(), newKM, material)
	if err != nil {
		return c.failErr("rotating KEK", err)
	}
	c.resultLine("KEK rotated: %d secret versions and %d CA keys rewrapped under %s",
		secretsRewrapped, caRewrapped, newKM.ID)
	if *newKeyFile != "" {
		c.resultLine("New master key file: %s (back it up; the old key is no longer sufficient after retirement).", *newKeyFile)
	} else {
		c.resultLine("New master key derived from the entered passphrase; use it on the next start.")
	}
	// A running server holds the OLD keyring in memory and will fail to decrypt
	// the rewrapped rows until it is restarted with the new key. Rotate with the
	// server stopped, or update its master-key configuration and restart it.
	// This is a safety warning, not progress: --quiet must not hide it.
	_, _ = fmt.Fprintln(c.Stderr, "IMPORTANT: point any running server at the new master key and restart it; "+
		"the old key can no longer decrypt secrets after this rotation.")
	if c.jsonOutput() {
		return c.printJSON(rotateKEKJSON{
			KEKID:                   newKM.ID,
			SecretVersionsRewrapped: secretsRewrapped,
			CAKeysRewrapped:         caRewrapped,
			NewKeyFile:              *newKeyFile,
		})
	}
	return 0
}

// rotateKEKJSON is the JSON form of rotate-kek. new_key_file is absent when the
// replacement key was derived from a passphrase, which has no file to name.
type rotateKEKJSON struct {
	KEKID                   string `json:"kek_id"`
	SecretVersionsRewrapped int    `json:"secret_versions_rewrapped"`
	CAKeysRewrapped         int    `json:"ca_keys_rewrapped"`
	NewKeyFile              string `json:"new_key_file,omitempty"`
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
