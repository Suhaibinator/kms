package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// quietLogger builds a warn-level logger for administrative commands so normal
// runs keep stdout/stderr clean while still surfacing problems.
func (c *CLI) quietLogger() *slog.Logger {
	return newLogger(c.Stderr, slog.LevelWarn)
}

// --- init ------------------------------------------------------------------

func (c *CLI) cmdInit(args []string) int {
	fs := c.newFlags("init")
	db := fs.String("db", "./kms.db", "database file path (created if absent)")
	keyFile := fs.String("master-key-file", "", "master key file path (generated if absent); omit to use a passphrase")
	admin := fs.String("admin", "", "also create a bootstrap admin identity with this name")
	if !c.parseFlags(fs, args) {
		return 2
	}
	ctx := context.Background()

	store, err := storage.Open(*db)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// CreateKeyFileIfMissing generates the key file on first init; without a
	// key file the prompter asks for a passphrase (twice, with confirmation).
	keyring, err := c.unseal(ctx, store, *keyFile, true)
	if err != nil {
		return c.fail("initializing master key: %v", err)
	}
	keyring.Active().Destroy()

	fmt.Fprintf(c.Stdout, "Initialized database at %s\n", *db)
	if *keyFile != "" {
		fmt.Fprintf(c.Stdout, "Master key file: %s (back this up separately from the database)\n", *keyFile)
	} else {
		fmt.Fprintln(c.Stdout, "Master key derived from passphrase. You must supply it on every start.")
	}

	if *admin != "" {
		token, hash, err := crypto.GenerateToken("kms")
		if err != nil {
			return c.fail("generating admin token: %v", err)
		}
		if _, err := store.CreateIdentity(ctx, *admin, domain.IdentityKindAdmin, hash); err != nil {
			return c.fail("creating admin identity: %v", err)
		}
		printTokenOnce(c.Stdout, "admin identity", *admin, token)
	}
	return 0
}

// --- migrate ---------------------------------------------------------------

func (c *CLI) cmdMigrate(args []string) int {
	fs := c.newFlags("migrate")
	db := fs.String("db", "./kms.db", "database file path")
	if !c.parseFlags(fs, args) {
		return 2
	}
	// Open runs migrations inside the transaction and refuses a newer schema.
	store, err := storage.Open(*db)
	if err != nil {
		return c.fail("migrating database: %v", err)
	}
	_ = store.Close()
	fmt.Fprintf(c.Stdout, "Migrations applied; %s is up to date.\n", *db)
	return 0
}

// --- check -----------------------------------------------------------------

func (c *CLI) cmdCheck(args []string) int {
	fs := c.newFlags("check")
	db := fs.String("db", "./kms.db", "database file path")
	keyFile := fs.String("key-file", "", "master key file to verify (optional)")
	if !c.parseFlags(fs, args) {
		return 2
	}
	ctx := context.Background()

	store, err := storage.Open(*db)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Ping(ctx); err != nil {
		return c.fail("database unreachable: %v", err)
	}
	fmt.Fprintf(c.Stdout, "Database OK: %s (schema up to date)\n", *db)

	// Verify the master key only if the database has been initialized and some
	// key source is available. Never print key material.
	if _, err := store.ActiveKeyMetadata(ctx); errors.Is(err, domain.ErrNotFound) {
		fmt.Fprintln(c.Stdout, "Master key: database not yet initialized (run init).")
		return 0
	}
	if !c.keySourceAvailable(*keyFile) {
		fmt.Fprintln(c.Stdout, "Master key: not checked (no key file, passphrase, or TTY available).")
		return 0
	}
	keyring, err := c.unseal(ctx, store, *keyFile, false)
	if err != nil {
		return c.fail("master key verification failed: %v", err)
	}
	keyring.Active().Destroy()
	fmt.Fprintln(c.Stdout, "Master key OK.")
	return 0
}

func (c *CLI) keySourceAvailable(keyFile string) bool {
	if keyFile != "" {
		return true
	}
	if os.Getenv("KMS_MASTER_PASSPHRASE") != "" {
		return true
	}
	return c.newPrompter() != nil
}

// --- backup ----------------------------------------------------------------

func (c *CLI) cmdBackup(args []string) int {
	fs := c.newFlags("backup")
	db := fs.String("db", "./kms.db", "source database file path")
	out := fs.String("out", "", "backup output file path (must not exist)")
	if !c.parseFlags(fs, args) {
		return 2
	}
	if *out == "" {
		return c.fail("--out is required")
	}
	if fileExists(*out) {
		return c.fail("output file %s already exists; refusing to overwrite", *out)
	}
	store, err := storage.Open(*db)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Backup(context.Background(), *out); err != nil {
		return c.fail("backup failed: %v", err)
	}
	fmt.Fprintf(c.Stdout, "Backup written to %s\n", *out)
	fmt.Fprintln(c.Stdout, "Note: the master key is NOT included; back it up separately.")
	return 0
}

// --- restore ---------------------------------------------------------------

func (c *CLI) cmdRestore(args []string) int {
	fs := c.newFlags("restore")
	db := fs.String("db", "./kms.db", "destination database file path")
	in := fs.String("in", "", "backup input file path")
	force := fs.Bool("force", false, "overwrite an existing destination database")
	if !c.parseFlags(fs, args) {
		return 2
	}
	if *in == "" {
		return c.fail("--in is required")
	}
	if err := restoreFile(*in, *db, *force); err != nil {
		return c.fail("%v", err)
	}
	// Validate by opening (and migrating) the restored copy.
	store, err := storage.Open(*db)
	if err != nil {
		return c.fail("restored database failed to open: %v", err)
	}
	_ = store.Close()

	fmt.Fprintf(c.Stdout, "Restored %s from %s\n", *db, *in)
	fmt.Fprintln(c.Stdout, "Next steps: ensure the matching master key (file or passphrase) is available before starting the server.")
	return 0
}

// --- create-admin ----------------------------------------------------------

func (c *CLI) cmdCreateAdmin(args []string) int {
	fs := c.newFlags("create-admin")
	db := fs.String("db", "./kms.db", "database file path")
	name := fs.String("name", "", "admin identity name")
	if !c.parseFlags(fs, args) {
		return 2
	}
	if *name == "" {
		return c.fail("--name is required")
	}
	// Direct store access. WAL mode allows this concurrently with a running
	// server, but the caller is responsible for that coordination.
	store, err := storage.Open(*db)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	token, hash, err := crypto.GenerateToken("kms")
	if err != nil {
		return c.fail("generating token: %v", err)
	}
	if _, err := store.CreateIdentity(context.Background(), *name, domain.IdentityKindAdmin, hash); err != nil {
		return c.fail("creating admin identity: %v", err)
	}
	printTokenOnce(c.Stdout, "admin identity", *name, token)
	return 0
}

// --- rotate-kek ------------------------------------------------------------

func (c *CLI) cmdRotateKEK(args []string) int {
	fs := c.newFlags("rotate-kek")
	db := fs.String("db", "./kms.db", "database file path")
	keyFile := fs.String("key-file", "", "current master key file (omit to use the current passphrase)")
	newKeyFile := fs.String("new-key-file", "", "new master key file (generated if absent); omit to enter a new passphrase")
	if !c.parseFlags(fs, args) {
		return 2
	}
	ctx := context.Background()

	store, err := storage.Open(*db)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Unseal the current keyring exactly as serve does.
	current, err := c.unseal(ctx, store, *keyFile, false)
	if err != nil {
		return c.fail("unsealing current master key: %v", err)
	}
	svc := core.New(store, c.quietLogger(), Version)
	svc.SetKeyring(current)

	newKM, material, err := c.buildNewKEK(*newKeyFile)
	if err != nil {
		return c.fail("preparing new master key: %v", err)
	}

	pr := core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
	count, err := svc.RotateKEK(ctx, pr, newKM, material)
	if err != nil {
		return c.fail("rotating KEK: %v", err)
	}
	fmt.Fprintf(c.Stdout, "KEK rotated: %d secret versions rewrapped under %s\n", count, newKM.ID)
	if *newKeyFile != "" {
		fmt.Fprintf(c.Stdout, "New master key file: %s (back it up; the old key is no longer sufficient after retirement).\n", *newKeyFile)
	} else {
		fmt.Fprintln(c.Stdout, "New master key derived from the entered passphrase; use it on the next start.")
	}
	// A running server holds the OLD keyring in memory and will fail to decrypt
	// the rewrapped rows until it is restarted with the new key. Rotate with the
	// server stopped, or update its master-key configuration and restart it.
	fmt.Fprintln(c.Stderr, "IMPORTANT: point any running server at the new master key and restart it; "+
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
func printTokenOnce(w io.Writer, kind, name, token string) {
	fmt.Fprintf(w, "Created %s %q.\n", kind, name)
	fmt.Fprintf(w, "  token: %s\n", token)
	fmt.Fprintln(w, "  WARNING: this token is shown once and cannot be recovered. Store it securely.")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
