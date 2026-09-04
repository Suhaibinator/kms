package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// TestInitCheckImportEndToEnd drives the administrative CLI against real
// storage and crypto: it initializes a database with a key file, verifies the
// key, imports secrets, and confirms an imported value decrypts correctly.
func TestInitCheckImportEndToEnd(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")

	// init: create db + key file + bootstrap admin.
	c := newTestCLI()
	if code := c.cmdInit([]string{"--sqlite-path", db, "--kek-file", keyFile, "--admin", "root"}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "token:") {
		t.Fatalf("init should print an admin token: %s", c.stdout())
	}
	if !fileExists(keyFile) {
		t.Fatalf("init should have created the key file")
	}

	// check: database + key verification.
	c2 := newTestCLI()
	if code := c2.cmdCheck([]string{"--sqlite-path", db, "--kek-file", keyFile}); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, c2.stderr())
	}
	if !strings.Contains(c2.stdout(), "Master key OK") {
		t.Fatalf("check stdout = %s", c2.stdout())
	}

	// import (real run): write secrets and a token report.
	src := filepath.Join(dir, "export.json")
	writeFile(t, src, `{"STRIPE_KEY":"sk_live_x","TWILIO_SID":"AC123"}`)
	report := filepath.Join(dir, "report.txt")
	c3 := newTestCLI()
	code := c3.cmdImport([]string{
		"--from", src, "--namespace", "prod/gradethis",
		"--sqlite-path", db, "--kek-file", keyFile, "--report", report,
	})
	if code != 0 {
		t.Fatalf("import exit=%d stderr=%s", code, c3.stderr())
	}
	body := readFileString(t, report)
	if !strings.Contains(body, "STRIPE_KEY -> /prod/gradethis/stripe-key -> kmss_") {
		t.Fatalf("report missing token mapping: %s", body)
	}
	if !strings.Contains(body, "WARNING") {
		t.Fatalf("report missing one-time token warning: %s", body)
	}

	// Verify the imported secret decrypts to the original plaintext.
	store, err := storage.Open(db)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = store.Close() }()
	keyring, err := c3.unseal(context.Background(), store, keyFile, false)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	svc := core.New(store, c3.quietLogger(), "test")
	svc.SetKeyring(keyring)

	pr := core.Principal{Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}}
	stripeRef := domain.Ref{NS: domain.NamespaceRef{Env: "prod", App: "gradethis"}, Key: "stripe-key"}
	val, err := svc.RevealSecret(context.Background(), pr, stripeRef, 0, "", "", "")
	if err != nil {
		t.Fatalf("reveal imported secret: %v", err)
	}
	if string(val.Value) != "sk_live_x" {
		t.Fatalf("imported secret value = %q, want sk_live_x", val.Value)
	}
}

func TestCheckUninitializedDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "fresh.db")
	c := newTestCLI()
	// A freshly opened (migrated) but un-keyed database reports that it needs init.
	if code := c.cmdCheck([]string{"--sqlite-path", db}); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "not yet initialized") {
		t.Fatalf("check stdout = %s", c.stdout())
	}
}

// TestInitHonoursEnvSQLitePath proves the offline commands read the same
// KMS_* variables the server does: init with no flags at all lands on the
// database and key file named by the environment, and says so.
func TestInitHonoursEnvSQLitePath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "from-env.db")
	keyPath := filepath.Join(dir, "master.key")

	c := newTestCLI()
	c.lookupEnv = mapLookup(map[string]string{"KMS_SQLITE_PATH": dbPath, "KMS_KEK_FILE": keyPath})
	if code := c.cmdInit(nil); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	assertDBTarget(t, c.stdout(), dbPath, "env KMS_SQLITE_PATH")
	if !fileExists(dbPath) {
		t.Fatalf("init did not create %s; stdout=%s", dbPath, c.stdout())
	}
}

// TestInitFlagBeatsEnvBeatsFile pins the resolution order for the database
// path across all three layers, checking both the file that gets created and
// the source the command reports.
func TestInitFlagBeatsEnvBeatsFile(t *testing.T) {
	dir := t.TempDir()
	fileDB := filepath.Join(dir, "file.db")
	envDB := filepath.Join(dir, "env.db")
	flagDB := filepath.Join(dir, "flag.db")
	keyPath := filepath.Join(dir, "master.key")
	configPath := filepath.Join(dir, "kms.yaml")
	writeFile(t, configPath, "storage:\n  sqlite_path: "+fileDB+"\n")
	env := mapLookup(map[string]string{"KMS_SQLITE_PATH": envDB})

	// The flag outranks both the environment and the file.
	withFlag := newTestCLI()
	withFlag.lookupEnv = env
	if code := withFlag.cmdInit([]string{"--config", configPath, "--sqlite-path", flagDB, "--kek-file", keyPath}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, withFlag.stderr())
	}
	assertDBTarget(t, withFlag.stdout(), flagDB, "flag --sqlite-path")
	if !fileExists(flagDB) {
		t.Fatalf("--sqlite-path did not create %s", flagDB)
	}

	// Without the flag, the environment outranks the file.
	withEnv := newTestCLI()
	withEnv.lookupEnv = env
	if code := withEnv.cmdInit([]string{"--config", configPath, "--kek-file", keyPath}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, withEnv.stderr())
	}
	assertDBTarget(t, withEnv.stdout(), envDB, "env KMS_SQLITE_PATH")
	if !fileExists(envDB) {
		t.Fatalf("KMS_SQLITE_PATH did not create %s", envDB)
	}

	// With neither, the config file supplies the path.
	fromFile := newTestCLI() // newTestCLI's environment is empty
	if code := fromFile.cmdInit([]string{"--config", configPath, "--kek-file", keyPath}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, fromFile.stderr())
	}
	assertDBTarget(t, fromFile.stdout(), fileDB, "file storage.sqlite_path")
	if !fileExists(fileDB) {
		t.Fatalf("config file did not create %s", fileDB)
	}
}

// TestInitRejectsStrayPositional guards the case that motivates
// rejectPositionals: an argument the flag package would otherwise drop on the
// floor must not be silently ignored by a command that creates a database.
func TestInitRejectsStrayPositional(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	c := newTestCLI()
	if code := c.cmdInit([]string{"--kek-file", keyPath, "extra"}); code != 2 {
		t.Fatalf("init exit=%d, want 2; stdout=%s stderr=%s", code, c.stdout(), c.stderr())
	}
	if !strings.Contains(c.stderr(), `unexpected argument "extra"`) {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if fileExists(keyPath) {
		t.Fatal("a rejected invocation created the master key file")
	}
}

// TestCheckVerifiesKeyFromEnv checks that the master key is verified from
// KMS_KEK_FILE alone, with no flags on the command line.
func TestCheckVerifiesKeyFromEnv(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kms.db")
	keyPath := filepath.Join(dir, "master.key")

	init := newTestCLI()
	if code := init.cmdInit([]string{"--sqlite-path", dbPath, "--kek-file", keyPath}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, init.stderr())
	}

	c := newTestCLI()
	c.lookupEnv = mapLookup(map[string]string{"KMS_SQLITE_PATH": dbPath, "KMS_KEK_FILE": keyPath})
	if code := c.cmdCheck(nil); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "Master key OK.") {
		t.Fatalf("check stdout = %s", c.stdout())
	}
}

// TestServeHelpIsEnvIndependent keeps help output stable: the --config default
// must be the empty string, never whatever KMS_CONFIG happens to hold, so
// `serve -h` documents the flag rather than the current shell.
func TestServeHelpIsEnvIndependent(t *testing.T) {
	c := newTestCLI()
	c.lookupEnv = mapLookup(map[string]string{"KMS_CONFIG": "/nope"})
	if code := c.Run([]string{"serve", "-h"}); code != 0 {
		t.Fatalf("serve -h exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "--config FILE") {
		t.Fatalf("help missing the --config flag: %s", c.stderr())
	}
	if strings.Contains(c.stderr(), "/nope") {
		t.Fatalf("help leaked KMS_CONFIG: %s", c.stderr())
	}
}

func TestRotateAdminDirectDatabaseRecovery(t *testing.T) {
	db := filepath.Join(t.TempDir(), "kms.db")
	ctx := context.Background()
	oldToken := "kms_old_admin_token"
	store, err := storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(oldToken),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI()
	if code := c.Run([]string{"rotate-admin", "--sqlite-path", db, "--name", "admin"}); code != 0 {
		t.Fatalf("rotate-admin exit=%d stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), `Rotated admin identity "admin".`) {
		t.Fatalf("rotate-admin stdout = %s", c.stdout())
	}
	newToken := tokenFromCLIOutput(t, c.stdout())
	if newToken == oldToken || !strings.HasPrefix(newToken, "kms_") {
		t.Fatalf("replacement token = %q", newToken)
	}

	store, err = storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.GetIdentityByTokenHash(ctx, crypto.TokenHash(oldToken)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old token lookup error = %v, want not found", err)
	}
	rotated, err := store.GetIdentityByTokenHash(ctx, crypto.TokenHash(newToken))
	if err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	if rotated.ID != created.ID || rotated.Name != created.Name || rotated.Kind != domain.IdentityKindAdmin {
		t.Fatalf("rotated identity = %+v, created = %+v", rotated, created)
	}

	svc := core.New(store, c.quietLogger(), "test")
	if _, err := svc.Authenticate(ctx, oldToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("old token authentication error = %v, want unauthenticated", err)
	}
	if authenticated, err := svc.Authenticate(ctx, newToken, "", ""); err != nil || authenticated.ID != created.ID {
		t.Fatalf("new token authentication = %+v, %v", authenticated, err)
	}

	events, _, err := store.ListAudit(ctx, domain.AuditFilter{ActorIdentity: "cli", EventType: "identity.write"}, storage.ListPage{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ResourceType != domain.ResourceIdentity || events[0].ResourceKey != "admin" || events[0].Decision != "allow" || !strings.Contains(events[0].Metadata, `"action":"rotate-token"`) {
		t.Fatalf("rotation audit events = %+v", events)
	}
}

func TestRotateAdminRejectsInvalidTargetsWithoutMutation(t *testing.T) {
	// wantCode is 1 for the checks the CLI composes itself; the store's
	// not-found sentinel surfaces as the documented exit code 5 instead.
	tests := []struct {
		name      string
		target    string
		kind      string
		disabled  bool
		hasToken  bool
		args      func(db string) []string
		wantError string
		wantCode  int
	}{
		{name: "missing name", target: "admin", kind: domain.IdentityKindAdmin, hasToken: true, args: func(db string) []string { return []string{"--sqlite-path", db} }, wantError: "--name is required", wantCode: 2},
		{name: "unknown identity", target: "admin", kind: domain.IdentityKindAdmin, hasToken: true, args: func(db string) []string { return []string{"--sqlite-path", db, "--name", "missing"} }, wantError: "identity missing", wantCode: exitNotFound},
		{name: "client identity", target: "client", kind: domain.IdentityKindClient, hasToken: true, args: func(db string) []string { return []string{"--sqlite-path", db, "--name", "client"} }, wantError: "is not an admin", wantCode: 1},
		{name: "disabled admin", target: "admin", kind: domain.IdentityKindAdmin, disabled: true, hasToken: true, args: func(db string) []string { return []string{"--sqlite-path", db, "--name", "admin"} }, wantError: "is disabled", wantCode: 1},
		{name: "admin without token", target: "admin", kind: domain.IdentityKindAdmin, args: func(db string) []string { return []string{"--sqlite-path", db, "--name", "admin"} }, wantError: "has no token to rotate", wantCode: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "kms.db")
			ctx := context.Background()
			oldToken := "kms_preserved_" + strings.ReplaceAll(test.name, " ", "_")
			var oldHash []byte
			if test.hasToken {
				oldHash = crypto.TokenHash(oldToken)
			}
			store, err := storage.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{Name: test.target, Kind: test.kind, TokenHash: oldHash}); err != nil {
				t.Fatal(err)
			}
			if test.disabled {
				if err := store.SetIdentityDisabled(ctx, test.target, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			c := newTestCLI()
			if code := c.cmdRotateAdmin(test.args(db)); code != test.wantCode {
				t.Fatalf("rotate-admin exit=%d, want %d; stdout=%s stderr=%s", code, test.wantCode, c.stdout(), c.stderr())
			}
			if !strings.Contains(c.stderr(), test.wantError) {
				t.Fatalf("stderr = %q, want %q", c.stderr(), test.wantError)
			}
			if c.stdout() != "" {
				t.Fatalf("failed rotation disclosed output: %q", c.stdout())
			}

			store, err = storage.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			identity, err := store.GetIdentityByName(ctx, test.target)
			if err != nil {
				t.Fatal(err)
			}
			if identity.Disabled != test.disabled || identity.HasToken != test.hasToken {
				t.Fatalf("identity changed after refusal: %+v", identity)
			}
			if test.hasToken {
				preserved, err := store.GetIdentityByTokenHash(ctx, oldHash)
				if err != nil || preserved.Name != test.target {
					t.Fatalf("old token was not preserved: %+v, %v", preserved, err)
				}
			}
			events, _, err := store.ListAudit(ctx, domain.AuditFilter{EventType: "identity.write"}, storage.ListPage{Limit: 10})
			if err != nil || len(events) != 0 {
				t.Fatalf("refused rotation audit events = %+v, %v", events, err)
			}
		})
	}
}

// --- JSON output ------------------------------------------------------------

// initDB creates a database and master key file, returning both paths.
func initDB(t *testing.T) (db, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	db = filepath.Join(dir, "kms.db")
	keyFile = filepath.Join(dir, "master.key")
	c := newTestCLI()
	if code := c.cmdInit([]string{"--sqlite-path", db, "--kek-file", keyFile}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	return db, keyFile
}

func TestInitJSONCarriesTheAdminTokenOnce(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "init", "--sqlite-path", db, "--kek-file", keyFile, "--admin", "root"})
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "sqlite_path", "sqlite_path_source", "master_key", "kek_file", "ca", "admin")
	if document["master_key"] != "file" || document["ca"] != "ready" || document["kek_file"] != keyFile {
		t.Fatalf("document = %v", document)
	}
	if document["sqlite_path"] != absPath(db) || document["sqlite_path_source"] != "flag --sqlite-path" {
		t.Fatalf("sqlite_path = %v (%v)", document["sqlite_path"], document["sqlite_path_source"])
	}
	admin, ok := document["admin"].(map[string]any)
	if !ok {
		t.Fatalf("admin = %#v, want an object", document["admin"])
	}
	assertJSONFields(t, admin, "name", "token", "cert")
	token, _ := admin["token"].(string)
	if !strings.HasPrefix(token, "kms_") {
		t.Fatalf("admin token = %q", token)
	}
	if strings.Count(c.stdout(), token) != 1 {
		t.Fatalf("the one-time token appears more than once on stdout: %s", c.stdout())
	}
	// No --cert-dir: there is no certificate, and the field says so explicitly.
	if admin["cert"] != nil {
		t.Fatalf("cert = %#v, want null", admin["cert"])
	}
	// The one-time warning is a security notice, so it stays on stderr where
	// it cannot corrupt the document.
	if !strings.Contains(c.stderr(), "WARNING: this token is shown once") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

// Without --admin there is no bootstrap identity, and admin is null rather
// than an object of empty strings.
func TestInitJSONWithoutAdminIsNull(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "init",
		"--sqlite-path", filepath.Join(dir, "kms.db"), "--kek-file", filepath.Join(dir, "master.key")})
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, c.stderr())
	}
	if admin, present := decodeJSONStdout(t, c)["admin"]; !present || admin != nil {
		t.Fatalf("admin = %#v (present=%v), want null", admin, present)
	}
}

func TestCheckJSONReportsEachVerdict(t *testing.T) {
	db, keyFile := initDB(t)

	// A database that opens and whose master key unseals.
	verified := newTestCLI()
	if code := verified.Run([]string{"-o", "json", "check", "--sqlite-path", db, "--kek-file", keyFile}); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, verified.stderr())
	}
	document := decodeJSONStdout(t, verified)
	assertJSONFields(t, document, "database", "master_key", "sqlite_path", "sqlite_path_source")
	if document["database"] != "ok" || document["master_key"] != "ok" || document["sqlite_path"] != absPath(db) {
		t.Fatalf("document = %v", document)
	}

	// The same database with no key source available: the schema is still
	// verified, the key is reported as unchecked, and the exit code stays 0.
	unchecked := newTestCLI()
	if code := unchecked.Run([]string{"-o", "json", "check", "--sqlite-path", db}); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, unchecked.stderr())
	}
	if got := decodeJSONStdout(t, unchecked)["master_key"]; got != "not_checked" {
		t.Fatalf("master_key = %v, want not_checked", got)
	}

	// A migrated but un-keyed database needs init, and says so.
	fresh := newTestCLI()
	if code := fresh.Run([]string{"-o", "json", "check", "--sqlite-path", filepath.Join(t.TempDir(), "fresh.db")}); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, fresh.stderr())
	}
	if got := decodeJSONStdout(t, fresh)["master_key"]; got != "not_initialized" {
		t.Fatalf("master_key = %v, want not_initialized", got)
	}
}

func TestMigrateAndBackupJSON(t *testing.T) {
	db, _ := initDB(t)

	migrated := newTestCLI()
	if code := migrated.Run([]string{"-o", "json", "migrate", "--sqlite-path", db}); code != 0 {
		t.Fatalf("migrate exit=%d stderr=%s", code, migrated.stderr())
	}
	document := decodeJSONStdout(t, migrated)
	assertJSONFields(t, document, "sqlite_path", "sqlite_path_source", "migrated")
	if document["migrated"] != true || document["sqlite_path"] != absPath(db) {
		t.Fatalf("migrate document = %v", document)
	}

	out := filepath.Join(t.TempDir(), "backup.db")
	backup := newTestCLI()
	if code := backup.Run([]string{"-o", "json", "backup", "--sqlite-path", db, "--out", out}); code != 0 {
		t.Fatalf("backup exit=%d stderr=%s", code, backup.stderr())
	}
	document = decodeJSONStdout(t, backup)
	assertJSONFields(t, document, "backup_file", "sqlite_path")
	if document["backup_file"] != out {
		t.Fatalf("backup document = %v", document)
	}
	// The "master key not included" note is advice, not the result: in JSON
	// mode it belongs on stderr.
	if !strings.Contains(backup.stderr(), "the master key is NOT included") {
		t.Fatalf("stderr = %q", backup.stderr())
	}
}

func TestCreateAdminAndRotateAdminJSON(t *testing.T) {
	db, _ := initDB(t)

	created := newTestCLI()
	if code := created.Run([]string{"-o", "json", "create-admin", "--sqlite-path", db, "--name", "ops"}); code != 0 {
		t.Fatalf("create-admin exit=%d stderr=%s", code, created.stderr())
	}
	document := decodeJSONStdout(t, created)
	assertJSONFields(t, document, "name", "token", "cert")
	firstToken, _ := document["token"].(string)
	if document["name"] != "ops" || !strings.HasPrefix(firstToken, "kms_") {
		t.Fatalf("create-admin document = %v", document)
	}

	rotated := newTestCLI()
	if code := rotated.Run([]string{"-o", "json", "rotate-admin", "--sqlite-path", db, "--name", "ops"}); code != 0 {
		t.Fatalf("rotate-admin exit=%d stderr=%s", code, rotated.stderr())
	}
	document = decodeJSONStdout(t, rotated)
	assertJSONFields(t, document, "name", "token")
	secondToken, _ := document["token"].(string)
	if secondToken == firstToken || !strings.HasPrefix(secondToken, "kms_") {
		t.Fatalf("rotate-admin token = %q (first = %q)", secondToken, firstToken)
	}
	if strings.Count(rotated.stdout(), secondToken) != 1 {
		t.Fatalf("the replacement token appears more than once: %s", rotated.stdout())
	}
}

// With --cert-dir the credential files are still written, but their guidance
// text would corrupt the document, so JSON mode names the paths instead.
func TestCreateAdminWithCertDirJSONNamesTheCredentialFiles(t *testing.T) {
	db, keyFile := initDB(t)
	certDir := t.TempDir()

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "create-admin",
		"--sqlite-path", db, "--kek-file", keyFile, "--name", "ops", "--cert-dir", certDir})
	if code != 0 {
		t.Fatalf("create-admin exit=%d stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	cert, ok := document["cert"].(map[string]any)
	if !ok {
		t.Fatalf("cert = %#v, want an object", document["cert"])
	}
	assertJSONFields(t, cert, "cert_file", "key_file")
	certFile, _ := cert["cert_file"].(string)
	keyPath, _ := cert["key_file"].(string)
	if !strings.HasSuffix(certFile, "ops.crt") || !strings.HasSuffix(keyPath, "ops.key") {
		t.Fatalf("cert = %v", cert)
	}
	if !strings.Contains(readFileString(t, certFile), "BEGIN CERTIFICATE") {
		t.Fatalf("%s does not hold a certificate", certFile)
	}
	if !fileExists(keyPath) {
		t.Fatalf("%s was not written", keyPath)
	}
	// The PKCS#12/next-steps guidance is human help: it must not reach stdout
	// in JSON mode, and the private key must never appear there at all.
	if strings.Contains(c.stdout(), "Next steps") || strings.Contains(c.stdout(), "PRIVATE KEY") {
		t.Fatalf("JSON stdout carried certificate guidance or key material: %s", c.stdout())
	}
	// The unrecoverable-key warning survives the move to stderr.
	if !strings.Contains(c.stderr(), "WARNING: the private key is written once") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

// --- rotate-kek confirmation ------------------------------------------------

// activeKEKID reads the master key identifier recorded in the database, so a
// test can prove a refused rotation left it alone.
func activeKEKID(t *testing.T, db string) string {
	t.Helper()
	store, err := storage.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	km, err := store.ActiveKeyMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return km.ID
}

// rotate-kek rewrites every wrapped row: a script that forgot --yes must fail
// loudly rather than rotating the master key of whatever database the
// environment happened to name.
func TestRotateKEKRefusedOnNonInteractiveStdinWithoutYes(t *testing.T) {
	db, keyFile := initDB(t)
	before := activeKEKID(t, db)
	newKey := filepath.Join(t.TempDir(), "new-master.key")

	c := newTestCLI() // newTestCLI has no stdin, so it is never a terminal
	code := c.Run([]string{"rotate-kek", "--sqlite-path", db, "--kek-file", keyFile, "--new-key-file", newKey})
	if code != exitUsage {
		t.Fatalf("rotate-kek exit=%d, want %d; stderr=%s", code, exitUsage, c.stderr())
	}
	if !strings.Contains(c.stderr(), "refusing to rotate the master key of "+absPath(db)) {
		t.Fatalf("stderr = %q", c.stderr())
	}
	// The refusal comes before the database is opened or a key is generated.
	if fileExists(newKey) {
		t.Fatal("a refused rotation generated the replacement key file")
	}
	if after := activeKEKID(t, db); after != before {
		t.Fatalf("a refused rotation changed the active key: %q -> %q", before, after)
	}
	if c.stdout() != "" {
		t.Fatalf("a refused rotation wrote to stdout: %q", c.stdout())
	}
}

func TestRotateKEKProceedsWithYesAndReportsJSON(t *testing.T) {
	db, keyFile := initDB(t)
	before := activeKEKID(t, db)
	newKey := filepath.Join(t.TempDir(), "new-master.key")

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "--yes", "rotate-kek",
		"--sqlite-path", db, "--kek-file", keyFile, "--new-key-file", newKey})
	if code != 0 {
		t.Fatalf("rotate-kek exit=%d stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "kek_id", "secret_versions_rewrapped", "ca_keys_rewrapped", "new_key_file")
	if document["new_key_file"] != newKey {
		t.Fatalf("document = %v", document)
	}
	kekID, _ := document["kek_id"].(string)
	if kekID == "" || kekID == before {
		t.Fatalf("kek_id = %q, want a new identifier (was %q)", kekID, before)
	}
	if after := activeKEKID(t, db); after != kekID {
		t.Fatalf("database active key = %q, want the reported %q", after, kekID)
	}
	// The restart notice is a safety warning: it stays on stderr and is never
	// routed through info, so --quiet cannot hide it.
	if !strings.Contains(c.stderr(), "IMPORTANT: point any running server at the new master key") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

// --quiet silences progress but never the destructive-target warning or the
// restart notice.
func TestRotateKEKQuietKeepsTheSafetyWarnings(t *testing.T) {
	db, keyFile := initDB(t)
	newKey := filepath.Join(t.TempDir(), "new-master.key")

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "--yes", "--quiet", "rotate-kek",
		"--sqlite-path", db, "--kek-file", keyFile, "--new-key-file", newKey})
	if code != 0 {
		t.Fatalf("rotate-kek exit=%d stderr=%s", code, c.stderr())
	}
	for _, want := range []string{"Target database: ", "IMPORTANT: point any running server"} {
		if !strings.Contains(c.stderr(), want) {
			t.Fatalf("--quiet silenced %q: %s", want, c.stderr())
		}
	}
	if strings.Contains(c.stderr(), "KEK rotated:") {
		t.Fatalf("--quiet did not silence the progress line: %s", c.stderr())
	}
}

// mapLookup builds an environment lookup backed by m, so a test declares
// exactly which KMS_* variables the command under test can see.
func mapLookup(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// assertDBTarget checks that output names the absolute form of path together
// with the provenance the command should have reported for it.
func assertDBTarget(t *testing.T, output, path, source string) {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs %s: %v", path, err)
	}
	if !strings.Contains(output, abs) {
		t.Fatalf("output does not name %s: %s", abs, output)
	}
	if want := "(source: " + source + ")"; !strings.Contains(output, want) {
		t.Fatalf("output does not report %s: %s", want, output)
	}
}

func tokenFromCLIOutput(t *testing.T, output string) string {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		if token, ok := strings.CutPrefix(strings.TrimSpace(line), "token: "); ok && token != "" {
			return token
		}
	}
	t.Fatalf("output contains no token: %s", output)
	return ""
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
