package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// ---- helpers --------------------------------------------------------------

func newStore(t *testing.T) *SQLStore {
	t.Helper()
	return newStoreWithOptions(t, Options{})
}

func newStoreWithOptions(t *testing.T, opts Options) *SQLStore {
	t.Helper()
	st, err := OpenWithOptions(filepath.Join(t.TempDir(), "kms.db"), opts)
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func nsRef(env, app string) domain.NamespaceRef { return domain.NamespaceRef{Env: env, App: app} }

func ref(env, app, key string) domain.Ref { return domain.Ref{NS: nsRef(env, app), Key: key} }

// seedNS creates a namespace (default auth methods) and returns it.
func seedNS(t *testing.T, st *SQLStore, env, app string) domain.Namespace {
	t.Helper()
	if _, err := st.ActiveKeyMetadata(context.Background()); errors.Is(err, domain.ErrNotFound) {
		if err := st.InsertKeyMetadata(context.Background(), domain.KeyMetadata{
			ID: "kek-a", Source: domain.KeySourceFile, KeyCheck: []byte("test"), State: domain.KeyStateActive,
		}); err != nil {
			t.Fatalf("InsertKeyMetadata: %v", err)
		}
	} else if err != nil {
		t.Fatalf("ActiveKeyMetadata: %v", err)
	}
	ns, err := st.CreateNamespace(context.Background(), domain.Namespace{NamespaceRef: nsRef(env, app), CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("CreateNamespace(%s/%s): %v", env, app, err)
	}
	return ns
}

// encryptStub returns a deterministic Encrypt function whose payload embeds the
// version number, and records the version it was invoked with.
func encryptStub(gotVersion *uint64) func(uint64) (EncryptedPayload, error) {
	return func(v uint64) (EncryptedPayload, error) {
		if gotVersion != nil {
			*gotVersion = v
		}
		s := strconv.FormatUint(v, 10)
		return EncryptedPayload{
			Ciphertext:   []byte("ct-" + s),
			EncryptedDEK: []byte("dek-" + s),
			KEKID:        "kek-a",
			WrapMode:     domain.WrapModeStandard,
			Algorithm:    "AES-256-GCM",
			Nonce:        []byte("nonce-" + s),
			AAD:          "aad-" + s,
		}, nil
	}
}

func putSecret(t *testing.T, st *SQLStore, r domain.Ref, clientBound bool) (uint64, uint64) {
	t.Helper()
	v, rev, err := st.CreateSecretVersion(context.Background(), CreateSecretParams{
		Ref:         r,
		CreatedBy:   "tester",
		ClientBound: clientBound,
		Encrypt:     encryptStub(nil),
	})
	if err != nil {
		t.Fatalf("CreateSecretVersion(%s): %v", r, err)
	}
	return v, rev
}

func mustErrIs(t *testing.T, err, target error, ctx string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got err %v, want errors.Is(_, %v)", ctx, err, target)
	}
}

// ---- schema / lifecycle ---------------------------------------------------

func TestSchemaDDL(t *testing.T) {
	st := newStore(t)
	var rows []struct {
		Name string
		SQL  string
	}
	if err := st.db.Raw("SELECT name, sql FROM sqlite_master WHERE type='table' AND sql IS NOT NULL").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	ddl := map[string]string{}
	for _, r := range rows {
		ddl[r.Name] = r.SQL
	}
	for _, tbl := range []string{
		"key_metadata", "namespaces", "parameters", "parameter_versions", "parameter_labels",
		"secrets", "secret_versions", "secret_labels", "identities", "ca_keys", "identity_certs",
		"policies", "audit_events", "change_log", "schema_migrations",
	} {
		if _, ok := ddl[tbl]; !ok {
			t.Errorf("missing table %s", tbl)
		}
	}
	if !strings.Contains(strings.ToUpper(ddl["change_log"]), "AUTOINCREMENT") {
		t.Errorf("change_log missing AUTOINCREMENT:\n%s", ddl["change_log"])
	}
	for _, tbl := range []string{"parameter_versions", "secret_versions", "identity_certs"} {
		if !strings.Contains(strings.ToUpper(ddl[tbl]), "ON DELETE CASCADE") {
			t.Errorf("%s missing FK cascade:\n%s", tbl, ddl[tbl])
		}
	}
}

func TestPragmasApplied(t *testing.T) {
	st := newStore(t)
	check := func(pragma, want string) {
		var got string
		if err := st.db.Raw("PRAGMA " + pragma).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(got, want) {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
	check("journal_mode", "wal")
	check("foreign_keys", "1")
	check("busy_timeout", "5000")
}

func TestOpenCreatesDatabaseWithRestrictedPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits")
	}
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("new database mode = %o, want 600", got)
	}
}

func TestOpenRejectsBroadExistingDatabasePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows security is asserted through DACL-specific tests")
	}
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("broadly accessible existing database was accepted")
	}
}

func TestOpenRejectsSharedMutableDatabaseParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows DACL rejection is covered by fileutil platform tests")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "shared")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "kms.db")
	st, err := Open(path)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("database open accepted a parent mutable by another account")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unsafe database path was created: %v", err)
	}
}

func TestOpenRefusesDanglingDatabaseSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "attacker-target.db")
	path := filepath.Join(dir, "kms.db")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	st, err := Open(path)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("dangling database symlink was accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("database open followed dangling symlink: %v", err)
	}
}

func TestOpenRefusesExistingDatabaseSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	targetStore, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := targetStore.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "kms.db")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	st, err := Open(path)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("existing database symlink was accepted")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("database symlink changed: info=%v err=%v", info, err)
	}
}

func TestOpenUsesTheLiteralSpecialCharacterPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames do not support every character covered here")
	}
	for _, name := range []string{
		"kms?tenant=other.db",
		"kms#fragment.db",
		"file:kms.db",
		"kms%3Fencoded.db",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			st, err := Open(path)
			if err != nil {
				t.Fatalf("Open(%q): %v", path, err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if err := ValidateKMSDatabase(path); err != nil {
				t.Fatalf("literal path is not the migrated database: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() == 0 {
				t.Fatal("literal path remained the empty reservation file")
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("literal database mode = %o, want 600", got)
			}
		})
	}
}

func TestPingAndClose(t *testing.T) {
	st := newStore(t)
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestReopenAndVersionGuard(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.db")
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	seedNS(t, st, "prod", "app")
	if _, _, err := st.PutParameter(context.Background(), ref("prod", "app", "a"), "v1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen must succeed and preserve data.
	st2, err := Open(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := st2.GetParameter(context.Background(), ref("prod", "app", "a"), 0, "")
	if err != nil || got.Value != "v1" {
		t.Fatalf("after reopen got %+v err %v", got, err)
	}
	// Stamp a newer-than-supported version and confirm Open refuses.
	if err := st2.db.Exec("UPDATE schema_migrations SET version = ?", schemaVersion+5).Error; err != nil {
		t.Fatal(err)
	}
	_ = st2.Close()
	if _, err := Open(p); err == nil {
		t.Fatal("expected Open to refuse a newer schema version")
	}
}

func TestMigrationRepairsCurrentStampedChangeLogNamespaceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ns := seedNS(t, st, "prod", "app")
	ctx := context.Background()
	if _, _, err := st.PutParameter(ctx, ref("prod", "app", "before"), "legacy", "", "", "admin"); err != nil {
		t.Fatal(err)
	}

	// Model an interrupted/current-stamped database whose physical change-log
	// table is still the pre-incarnation shape. Open must inspect the table, not
	// trust the schema stamp alone, and legacy rows must remain unattributed (0).
	for _, statement := range []string{
		`DROP INDEX IF EXISTS idx_change_log_namespace_revision`,
		`DROP INDEX IF EXISTS idx_change_log_ns`,
		`ALTER TABLE change_log RENAME TO change_log_pre_namespace_id`,
		`CREATE TABLE change_log (
			revision INTEGER PRIMARY KEY AUTOINCREMENT,
			resource_type TEXT NOT NULL,
			env TEXT NOT NULL,
			app TEXT NOT NULL,
			key TEXT NOT NULL,
			change_type TEXT NOT NULL,
			value TEXT,
			content_type TEXT NOT NULL DEFAULT '',
			version_number INTEGER NOT NULL DEFAULT 0,
			label TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`INSERT INTO change_log (
			revision, resource_type, env, app, key, change_type, value,
			content_type, version_number, label, created_at
		) SELECT revision, resource_type, env, app, key, change_type, value,
			content_type, version_number, label, created_at
		FROM change_log_pre_namespace_id`,
		`DROP TABLE change_log_pre_namespace_id`,
		`CREATE INDEX idx_change_log_ns ON change_log(env, app)`,
	} {
		if err := st.db.Exec(statement).Error; err != nil {
			_ = st.Close()
			t.Fatalf("prepare current-stamped legacy change_log: %v", err)
		}
	}
	var stamped schemaMigrationModel
	if err := st.db.Order("version DESC").First(&stamped).Error; err != nil {
		t.Fatal(err)
	}
	if stamped.Version != schemaVersion {
		t.Fatalf("schema stamp = %d, want current %d", stamped.Version, schemaVersion)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := Open(path)
	if err != nil {
		t.Fatalf("repair current-stamped legacy change_log: %v", err)
	}
	defer func() { _ = repaired.Close() }()
	if !repaired.db.Migrator().HasColumn(&changeLogModel{}, "NamespaceID") {
		t.Fatal("namespace_id column was not repaired")
	}
	entries, err := repaired.ListChangesSince(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].NamespaceID != 0 {
		t.Fatalf("legacy entries = %+v, want one fail-closed namespace_id=0 row", entries)
	}

	if _, _, err := repaired.PutParameter(ctx, ref("prod", "app", "after"), "current", "", "", "admin"); err != nil {
		t.Fatal(err)
	}
	entries, err = repaired.ListChangesSince(ctx, entries[0].Revision, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].NamespaceID != ns.ID {
		t.Fatalf("new entries = %+v, want namespace_id=%d", entries, ns.ID)
	}
}

func TestMigrationRepairsCurrentStampedReleaseEntryNamespaceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := seedNS(t, st, "prod", "source")
	target := seedNS(t, st, "prod", "target")
	resource := ref("prod", "source", "config")
	if _, _, err := st.PutParameter(ctx, resource, "value", "string", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{
		Namespace: target.NamespaceRef, Name: "runtime", Digest: "digest", Metadata: "{}",
		Entries: []domain.ConfigurationReleaseEntry{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: resource, Version: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP INDEX IF EXISTS idx_release_entry_resource`,
		`ALTER TABLE configuration_release_entries DROP COLUMN resource_namespace_id`,
	} {
		if err := st.db.Exec(statement).Error; err != nil {
			_ = st.Close()
			t.Fatalf("prepare current-stamped legacy release entries: %v", err)
		}
	}
	var stamped schemaMigrationModel
	if err := st.db.Order("version DESC").First(&stamped).Error; err != nil {
		t.Fatal(err)
	}
	if stamped.Version != schemaVersion {
		t.Fatalf("schema stamp = %d, want current %d", stamped.Version, schemaVersion)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := Open(path)
	if err != nil {
		t.Fatalf("repair current-stamped legacy release entries: %v", err)
	}
	defer func() { _ = repaired.Close() }()
	if !repaired.db.Migrator().HasColumn(&configurationReleaseEntryModel{}, "ResourceNamespaceID") {
		t.Fatal("resource_namespace_id column was not repaired")
	}
	var entry configurationReleaseEntryModel
	if err := repaired.db.First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.ResourceNamespaceID != 0 {
		t.Fatalf("legacy resource namespace ID = %d, want fail-closed 0", entry.ResourceNamespaceID)
	}
	if _, _, err := repaired.ActivateConfigurationRelease(ctx, target.NamespaceRef, release.Name, release.Version, nil); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("activate migrated legacy release err = %v, want ErrFailedPrecondition", err)
	}
	if source.ID <= 0 {
		t.Fatal("source namespace setup failed")
	}
}

// ---- parameters -----------------------------------------------------------

func TestPutGetParameter(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "db/url")

	v, rev, err := st.PutParameter(ctx, r, "postgres://a", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("first version = %d, want 1", v)
	}
	if rev == 0 {
		t.Fatal("revision should be non-zero")
	}

	got, err := st.GetParameter(ctx, r, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "postgres://a" || got.Version != 1 || got.ContentType != "string" {
		t.Fatalf("got %+v", got)
	}
	if got.Ref != r {
		t.Fatalf("ref = %+v, want %+v", got.Ref, r)
	}
	if got.Metadata != "{}" {
		t.Fatalf("default metadata = %q, want {}", got.Metadata)
	}
	if got.Labels[domain.LabelCurrent] != 1 {
		t.Fatalf("current label = %d, want 1", got.Labels[domain.LabelCurrent])
	}
	if _, ok := got.Labels[domain.LabelPrevious]; ok {
		t.Fatal("first version must not have a previous label")
	}

	// Second put moves current->2, previous->1.
	v2, _, err := st.PutParameter(ctx, r, "postgres://b", "text/plain", `{"k":"x"}`, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if v2 != 2 {
		t.Fatalf("second version = %d, want 2", v2)
	}
	cur, err := st.GetParameter(ctx, r, 0, domain.LabelCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Value != "postgres://b" || cur.Version != 2 || cur.ContentType != "text/plain" || cur.Metadata != `{"k":"x"}` {
		t.Fatalf("current = %+v", cur)
	}
	if cur.Labels[domain.LabelCurrent] != 2 || cur.Labels[domain.LabelPrevious] != 1 {
		t.Fatalf("labels = %v", cur.Labels)
	}
	// Explicit old version still resolves.
	old, err := st.GetParameter(ctx, r, 1, "")
	if err != nil || old.Value != "postgres://a" {
		t.Fatalf("v1 = %+v err %v", old, err)
	}
	// Previous label resolves to v1.
	prev, err := st.GetParameter(ctx, r, 0, domain.LabelPrevious)
	if err != nil || prev.Version != 1 {
		t.Fatalf("previous = %+v err %v", prev, err)
	}
}

func TestPutParameterMissingNamespace(t *testing.T) {
	st := newStore(t)
	// No namespace created: PutParameter must fail rather than orphan a row.
	_, _, err := st.PutParameter(context.Background(), ref("prod", "app", "k"), "v", "", "", "u")
	mustErrIs(t, err, domain.ErrNotFound, "put into missing namespace")
}

func TestGetParameterNotFound(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	_, err := st.GetParameter(ctx, ref("prod", "app", "missing"), 0, "")
	mustErrIs(t, err, domain.ErrNotFound, "missing parameter")

	if _, _, err := st.PutParameter(ctx, ref("prod", "app", "exists"), "v", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	_, err = st.GetParameter(ctx, ref("prod", "app", "exists"), 99, "")
	mustErrIs(t, err, domain.ErrNotFound, "missing version")
	_, err = st.GetParameter(ctx, ref("prod", "app", "exists"), 0, "nope")
	mustErrIs(t, err, domain.ErrNotFound, "missing label")
}

func TestParameterInfo(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "p")
	if _, _, err := st.PutParameter(ctx, r, "a", "", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, r, "b", "", "", "u2"); err != nil {
		t.Fatal(err)
	}
	info, err := st.GetParameterInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if info.Ref != r {
		t.Fatalf("ref = %+v", info.Ref)
	}
	if len(info.Versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(info.Versions))
	}
	if info.Versions[0].Version != 1 || info.Versions[1].Version != 2 {
		t.Fatalf("version order = %+v", info.Versions)
	}
	if info.Labels[domain.LabelCurrent] != 2 {
		t.Fatalf("labels = %v", info.Labels)
	}
}

func TestListParametersOrderingAndPagination(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns := nsRef("prod", "app")
	seedNS(t, st, "prod", "app")
	keys := []string{"a", "c", "b", "d/e", "d/f"}
	for _, k := range keys {
		if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: k}, "v", "", "", "u"); err != nil {
			t.Fatal(err)
		}
	}
	// Page through with limit 2 and assert sorted, complete, non-overlapping.
	var seen []string
	token := ""
	for {
		list, next, err := st.ListParameters(ctx, ns, "", ListPage{Limit: 2, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			seen = append(seen, p.Ref.Key)
		}
		if next == "" {
			break
		}
		token = next
	}
	want := []string{"a", "b", "c", "d/e", "d/f"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("paginated = %v, want %v", seen, want)
	}
}

func TestListParametersNamespaceScoped(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	seedNS(t, st, "stage", "app")
	if _, _, err := st.PutParameter(ctx, ref("prod", "app", "k"), "prod", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, ref("stage", "app", "k"), "stage", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	list, _, err := st.ListParameters(ctx, nsRef("prod", "app"), "", ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Value != "prod" {
		t.Fatalf("prod list = %+v, want just the prod value", list)
	}
	// Listing a namespace that does not exist is ErrNotFound.
	_, _, err = st.ListParameters(ctx, nsRef("nope", "app"), "", ListPage{Limit: 100})
	mustErrIs(t, err, domain.ErrNotFound, "list missing namespace")
}

// TestListParametersPrefixEscaping verifies the list browsing filter is a plain
// opaque byte prefix (LIKE 'prefix%') that still escapes LIKE metacharacters, so
// an underscore in the prefix matches only a literal underscore, never a wildcard
// character. It is a non-authz browsing convenience: "a_b" matches "a_b",
// "a_b/child", and "a_bc" alike (opaque prefix, not segment-aware), but never
// "aXb" (the '_' is escaped).
func TestListParametersPrefixEscaping(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns := nsRef("prod", "app")
	seedNS(t, st, "prod", "app")
	for _, k := range []string{"a_b", "a_b/child", "aXb", "aXb/child", "a_bc"} {
		if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: k}, "v", "", "", "u"); err != nil {
			t.Fatal(err)
		}
	}
	list, _, err := st.ListParameters(ctx, ns, "a_b", ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range list {
		got[p.Ref.Key] = true
	}
	// Opaque prefix: itself, children, and the "a_bc" sibling all begin with "a_b".
	if !got["a_b"] || !got["a_b/child"] || !got["a_bc"] {
		t.Fatalf("opaque prefix a_b should include a_b, a_b/child, a_bc: %v", got)
	}
	// The escaped underscore must not act as a LIKE wildcard.
	if got["aXb"] || got["aXb/child"] {
		t.Fatalf("prefix a_b must not match aXb via LIKE wildcard: %v", got)
	}
}

func TestDeleteParameterCascades(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "p")
	if _, _, err := st.PutParameter(ctx, r, "a", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, r, "b", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	var pid int64
	if err := st.db.Raw("SELECT id FROM parameters WHERE name='p'").Scan(&pid).Error; err != nil {
		t.Fatal(err)
	}
	rev, err := st.DeleteParameter(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if rev == 0 {
		t.Fatal("delete revision should be non-zero")
	}
	var nVers, nLabels int64
	st.db.Raw("SELECT COUNT(*) FROM parameter_versions WHERE parameter_id=?", pid).Scan(&nVers)
	st.db.Raw("SELECT COUNT(*) FROM parameter_labels WHERE parameter_id=?", pid).Scan(&nLabels)
	if nVers != 0 || nLabels != 0 {
		t.Fatalf("cascade left rows: versions=%d labels=%d", nVers, nLabels)
	}
	_, err = st.GetParameter(ctx, r, 0, "")
	mustErrIs(t, err, domain.ErrNotFound, "after delete")

	// Delete of a missing parameter is ErrNotFound.
	_, err = st.DeleteParameter(ctx, ref("prod", "app", "nope"))
	mustErrIs(t, err, domain.ErrNotFound, "delete missing")
}

// ---- secrets --------------------------------------------------------------

func TestCreateSecretVersion(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "svc/key")
	var gotVer uint64
	v, rev, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref:         r,
		ContentType: "application/json",
		Metadata:    `{"team":"x"}`,
		CreatedBy:   "alice",
		ClientBound: false,
		Encrypt:     encryptStub(&gotVer),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 || rev == 0 {
		t.Fatalf("v=%d r=%d", v, rev)
	}
	if gotVer != 1 {
		t.Fatalf("Encrypt got version %d, want 1", gotVer)
	}
	rec, ver, err := st.GetSecretVersion(ctx, r, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Ref != r || rec.ContentType != "application/json" || rec.Metadata != `{"team":"x"}` || rec.ClientBound {
		t.Fatalf("secret rec = %+v", rec)
	}
	if string(ver.Ciphertext) != "ct-1" || string(ver.EncryptedDEK) != "dek-1" || ver.AAD != "aad-1" {
		t.Fatalf("version payload = %+v", ver)
	}
	if ver.State != domain.StateEnabled || ver.KEKID != "kek-a" {
		t.Fatalf("version meta = %+v", ver)
	}
	if rec.Labels[domain.LabelCurrent] != 1 {
		t.Fatalf("labels = %v", rec.Labels)
	}

	// change-log entry for a secret carries no value but carries the ref.
	changes, err := st.ListChangesSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := changes[len(changes)-1]
	if last.ResourceType != domain.ResourceSecret || last.Value != "" || last.ChangeType != domain.ChangePut {
		t.Fatalf("secret change entry = %+v", last)
	}
	if last.Ref != r {
		t.Fatalf("change ref = %+v, want %+v", last.Ref, r)
	}
}

func TestCreateSecretVersionRejectsRetiredKEKPayload(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	if err := st.SetKeyState(ctx, "kek-a", domain.KeyStateRetired); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-new", State: domain.KeyStateActive, KeyCheck: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	r := ref("prod", "app", "stale")
	_, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{Ref: r, Encrypt: encryptStub(nil)}) // payload uses kek-a
	mustErrIs(t, err, domain.ErrFailedPrecondition, "stale KEK payload")
	if _, err := st.GetSecretRecord(ctx, r); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stale-key write left a secret row: %v", err)
	}
}

func TestCreateSecretVersionModeMismatch(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false)
	_, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ClientBound: true, Encrypt: encryptStub(nil),
	})
	mustErrIs(t, err, domain.ErrFailedPrecondition, "mode mismatch")
	// Nothing new should be written.
	info, err := st.GetSecretInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != 1 {
		t.Fatalf("versions after failed create = %d, want 1", len(info.Versions))
	}
}

func TestCreateSecretVersionMissingNamespace(t *testing.T) {
	st := newStore(t)
	_, _, err := st.CreateSecretVersion(context.Background(), CreateSecretParams{
		Ref: ref("prod", "app", "s"), Encrypt: encryptStub(nil),
	})
	mustErrIs(t, err, domain.ErrNotFound, "create secret in missing namespace")
}

func TestCreateSecretVersionKeepsTokenHash(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	hash := []byte("tokenhash")
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, AccessTokenHash: hash, Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	// New version without a hash must keep the existing one.
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := st.GetSecretRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.AccessTokenHash) != "tokenhash" {
		t.Fatalf("token hash = %q, want kept", rec.AccessTokenHash)
	}
}

func TestCreateSecretVersionRejectsStaleExpectation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "guarded")

	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ClientBound: true, AccessTokenHash: []byte("hash-v1"), Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	recV1, err := st.GetSecretRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	encrypted := false
	_, _, err = st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ClientBound: true,
		Expected: &SecretWriteExpectation{Exists: false},
		Encrypt: func(version uint64) (EncryptedPayload, error) {
			encrypted = true
			return encryptStub(nil)(version)
		},
	})
	if !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale create err = %v, want ErrAborted", err)
	}
	if encrypted {
		t.Fatal("stale create invoked Encrypt before rejecting the changed state")
	}

	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ClientBound: true, AccessTokenHash: []byte("hash-v2"),
		Expected: &SecretWriteExpectation{Exists: true, ID: recV1.ID, AccessTokenHash: []byte("hash-v1")},
		Encrypt:  encryptStub(nil),
	}); err != nil {
		t.Fatalf("current token rotation: %v", err)
	}

	encrypted = false
	_, _, err = st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ClientBound: true,
		Expected: &SecretWriteExpectation{Exists: true, ID: recV1.ID, AccessTokenHash: []byte("hash-v1")},
		Encrypt: func(version uint64) (EncryptedPayload, error) {
			encrypted = true
			return encryptStub(nil)(version)
		},
	})
	if !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale token write err = %v, want ErrAborted", err)
	}
	if encrypted {
		t.Fatal("stale token write encrypted a version before rejecting the rotated token")
	}

	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ClientBound: true,
		Expected: &SecretWriteExpectation{Exists: true, ID: recV1.ID, AccessTokenHash: []byte("hash-v2")},
		Encrypt:  encryptStub(nil),
	}); err != nil {
		t.Fatalf("write with current expectation: %v", err)
	}
	info, err := st.GetSecretInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != 3 {
		t.Fatalf("version count = %d, want 3 successful writes only", len(info.Versions))
	}
}

func TestCreateSecretVersionConcurrentExpectedAbsentOnlyOneWins(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "concurrent-create")

	type result struct {
		version uint64
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			version, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
				Ref:         r,
				ClientBound: true,
				Expected:    &SecretWriteExpectation{Exists: false},
				Encrypt:     encryptStub(nil),
			})
			results <- result{version: version, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, conflicted int
	for got := range results {
		switch {
		case got.err == nil && got.version == 1:
			succeeded++
		case errors.Is(got.err, domain.ErrAborted):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent create result: version=%d err=%v", got.version, got.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent creates succeeded=%d conflicted=%d, want 1/1", succeeded, conflicted)
	}
	info, err := st.GetSecretInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != 1 || info.Labels[domain.LabelCurrent] != 1 {
		t.Fatalf("concurrent create persisted %+v, want exactly version 1", info)
	}
}

func TestCreateSecretVersionConcurrentTokenRotationsOnlyOneWins(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "concurrent-rotation")
	oldHash := []byte("hash-v1")
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ClientBound: true, AccessTokenHash: oldHash, Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := st.GetSecretRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		hash []byte
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		newHash := fmt.Appendf(nil, "hash-v%d", i+2)
		wg.Add(1)
		go func(newHash []byte) {
			defer wg.Done()
			<-start
			_, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
				Ref: r, ClientBound: true, AccessTokenHash: newHash,
				Expected: &SecretWriteExpectation{Exists: true, ID: rec.ID, AccessTokenHash: oldHash},
				Encrypt:  encryptStub(nil),
			})
			results <- result{hash: newHash, err: err}
		}(newHash)
	}
	close(start)
	wg.Wait()
	close(results)

	var winnerHash []byte
	var succeeded, conflicted int
	for got := range results {
		switch {
		case got.err == nil:
			succeeded++
			winnerHash = got.hash
		case errors.Is(got.err, domain.ErrAborted):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent rotation error: %v", got.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent rotations succeeded=%d conflicted=%d, want 1/1", succeeded, conflicted)
	}
	latest, err := st.GetSecretRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(latest.AccessTokenHash, winnerHash) {
		t.Fatalf("persisted token hash = %q, want winning hash %q", latest.AccessTokenHash, winnerHash)
	}
	info, err := st.GetSecretInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != 2 || info.Labels[domain.LabelCurrent] != 2 {
		t.Fatalf("concurrent rotations persisted %+v, want versions 1 and 2 only", info)
	}
}

func TestCreateSecretVersionRejectsDeleteRecreateABA(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "aba")
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{Ref: r, Encrypt: encryptStub(nil)}); err != nil {
		t.Fatal(err)
	}
	original, err := st.GetSecretRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteSecret(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{Ref: r, Encrypt: encryptStub(nil)}); err != nil {
		t.Fatal(err)
	}
	replacement, err := st.GetSecretRecord(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == original.ID {
		t.Fatalf("replacement reused secret row ID %d", original.ID)
	}

	encrypted := false
	_, _, err = st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref:      r,
		Expected: &SecretWriteExpectation{Exists: true, ID: original.ID},
		Encrypt: func(version uint64) (EncryptedPayload, error) {
			encrypted = true
			return encryptStub(nil)(version)
		},
	})
	if !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("delete/recreate ABA err = %v, want ErrAborted", err)
	}
	if encrypted {
		t.Fatal("delete/recreate ABA invoked Encrypt before rejecting stale row identity")
	}
}

func TestGetSecretVersionReadsOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms.db")
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	ctx := context.Background()
	seedNS(t, reader, "prod", "app")
	r := ref("prod", "app", "snapshot")
	if _, _, err := reader.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, AccessTokenHash: []byte("hash-v1"), Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}

	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	firstRead := make(chan struct{})
	resumeRead := make(chan struct{})
	var pause sync.Once
	if err := reader.db.Callback().Query().After("gorm:query").Register("test:pause_secret_snapshot", func(db *gorm.DB) {
		if db.Statement.Table == "secrets" {
			pause.Do(func() {
				close(firstRead)
				<-resumeRead
			})
		}
	}); err != nil {
		t.Fatal(err)
	}

	type readResult struct {
		rec SecretRecord
		ver SecretVersionRecord
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		rec, ver, err := reader.GetSecretVersion(ctx, r, 0, "")
		readDone <- readResult{rec: rec, ver: ver, err: err}
	}()
	select {
	case <-firstRead:
	case <-time.After(2 * time.Second):
		t.Fatal("secret read did not reach the snapshot hook")
	}

	writeDone := make(chan error, 1)
	go func() {
		_, _, err := writer.CreateSecretVersion(ctx, CreateSecretParams{
			Ref: r, AccessTokenHash: []byte("hash-v2"), Encrypt: encryptStub(nil),
		})
		writeDone <- err
	}()
	var concurrentWriteErr error
	writerCompleted := false
	select {
	case concurrentWriteErr = <-writeDone:
		writerCompleted = true
	case <-time.After(2 * time.Second):
	}
	close(resumeRead)

	got := <-readDone
	if got.err != nil {
		t.Fatalf("GetSecretVersion: %v", got.err)
	}
	if string(got.rec.AccessTokenHash) != "hash-v1" || got.ver.Version != 1 {
		t.Fatalf("read mixed snapshots: hash=%q version=%d, want hash-v1/version 1", got.rec.AccessTokenHash, got.ver.Version)
	}
	if !writerCompleted {
		concurrentWriteErr = <-writeDone
		t.Fatalf("read-only snapshot blocked the concurrent writer: %v", concurrentWriteErr)
	}
	if concurrentWriteErr != nil {
		t.Fatalf("concurrent write inside read snapshot: %v", concurrentWriteErr)
	}
	latest, latestVersion, err := reader.GetSecretVersion(ctx, r, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(latest.AccessTokenHash) != "hash-v2" || latestVersion.Version != 2 {
		t.Fatalf("post-write read = hash %q/version %d, want hash-v2/version 2", latest.AccessTokenHash, latestVersion.Version)
	}
}

func TestGetParameterReadsOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms.db")
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	ctx := context.Background()
	seedNS(t, reader, "prod", "app")
	r := ref("prod", "app", "snapshot-parameter")
	if _, _, err := reader.PutParameter(ctx, r, "v1", "string", "{}", "test"); err != nil {
		t.Fatal(err)
	}

	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	firstRead := make(chan struct{})
	resumeRead := make(chan struct{})
	var pause sync.Once
	if err := reader.db.Callback().Query().After("gorm:query").Register("test:pause_parameter_snapshot", func(db *gorm.DB) {
		if db.Statement.Table == "parameters" {
			pause.Do(func() {
				close(firstRead)
				<-resumeRead
			})
		}
	}); err != nil {
		t.Fatal(err)
	}

	type readResult struct {
		parameter domain.Parameter
		err       error
	}
	readDone := make(chan readResult, 1)
	go func() {
		parameter, err := reader.GetParameter(ctx, r, 0, "")
		readDone <- readResult{parameter: parameter, err: err}
	}()
	select {
	case <-firstRead:
	case <-time.After(2 * time.Second):
		t.Fatal("parameter read did not reach the snapshot hook")
	}

	writeDone := make(chan error, 1)
	go func() {
		_, _, err := writer.PutParameter(ctx, r, "v2", "string", "{}", "test")
		writeDone <- err
	}()
	var concurrentWriteErr error
	writerCompleted := false
	select {
	case concurrentWriteErr = <-writeDone:
		writerCompleted = true
	case <-time.After(2 * time.Second):
	}
	close(resumeRead)

	got := <-readDone
	if got.err != nil {
		t.Fatalf("GetParameter: %v", got.err)
	}
	if got.parameter.Value != "v1" || got.parameter.Version != 1 {
		t.Fatalf("read mixed snapshots: value=%q version=%d, want v1/version 1", got.parameter.Value, got.parameter.Version)
	}
	if !writerCompleted {
		concurrentWriteErr = <-writeDone
		t.Fatalf("read-only snapshot blocked the concurrent writer: %v", concurrentWriteErr)
	}
	if concurrentWriteErr != nil {
		t.Fatalf("concurrent write inside read snapshot: %v", concurrentWriteErr)
	}
	latest, err := reader.GetParameter(ctx, r, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Value != "v2" || latest.Version != 2 {
		t.Fatalf("post-write read = %q/version %d, want v2/version 2", latest.Value, latest.Version)
	}
}

func TestSecretVersionPinsContentAndProtectionAttributes(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")

	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ContentType: "text/plain", Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ContentType: "application/json", AccessTokenHash: []byte("token-v2"), Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	// Omitting a hash preserves the per-secret token and records that the new
	// version is protected without minting or rotating it.
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ContentType: "application/yaml", Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}

	rec, v1, err := st.GetSecretVersion(ctx, r, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ContentType != "application/yaml" || len(rec.AccessTokenHash) == 0 {
		t.Fatalf("latest secret metadata = %+v, want yaml with token", rec)
	}
	if v1.ContentType != "text/plain" || v1.ClientBound || v1.HasAccessToken {
		t.Fatalf("v1 attributes = %+v, want original unprotected text version", v1)
	}
	_, v2, err := st.GetSecretVersion(ctx, r, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if v2.ContentType != "application/json" || v2.ClientBound || !v2.HasAccessToken {
		t.Fatalf("v2 attributes = %+v, want protected json version", v2)
	}
	_, v3, err := st.GetSecretVersion(ctx, r, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if v3.ContentType != "application/yaml" || !v3.HasAccessToken {
		t.Fatalf("v3 attributes = %+v, want inherited token protection", v3)
	}

	bound := ref("prod", "app", "bound")
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: bound, ClientBound: true, AccessTokenHash: []byte("bound-token"), Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	_, boundV1, err := st.GetSecretVersion(ctx, bound, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if !boundV1.ClientBound || !boundV1.HasAccessToken {
		t.Fatalf("client-bound v1 attributes = %+v", boundV1)
	}
}

func TestSchemaV2BackfillsSecretVersionAttributes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, ContentType: "ignored", Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	// Emulate a v1 database after AutoMigrate has introduced the new columns:
	// historical versions have no independent values, while the shared secret
	// row contains the only attributes that v2 retained.
	if err := st.db.Exec(`UPDATE secrets SET content_type = ?, client_bound = 1, access_token_hash = ? WHERE name = ?`, "application/custom", []byte("token-hash"), "s").Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Exec(`UPDATE secret_versions SET content_type = '', client_bound = 0, has_access_token = 0`).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Exec(`DELETE FROM schema_migrations`).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Create(&schemaMigrationModel{Version: 1, AppliedAt: fmtTime(time.Now())}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, ver, err := st.GetSecretVersion(ctx, r, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver.ContentType != "application/custom" || !ver.ClientBound || !ver.HasAccessToken {
		t.Fatalf("backfilled attributes = %+v", ver)
	}
	var latest schemaMigrationModel
	if err := st.db.Order("version DESC").First(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if latest.Version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", latest.Version, schemaVersion)
	}
}

func TestSecretEncryptErrorAborts(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	boom := errors.New("boom")
	_, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: r, Encrypt: func(uint64) (EncryptedPayload, error) { return EncryptedPayload{}, boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	// Secret row must not have been created.
	_, err = st.GetSecretRecord(ctx, r)
	mustErrIs(t, err, domain.ErrNotFound, "no secret after encrypt error")
}

func TestSecretVersionNumberIncludesDestroyed(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false) // v1
	putSecret(t, st, r, false) // v2
	if _, err := st.DestroySecretVersion(ctx, r, 2); err != nil {
		t.Fatal(err)
	}
	v, _ := putSecret(t, st, r, false) // must be v3, not reuse 2
	if v != 3 {
		t.Fatalf("next version after destroy = %d, want 3", v)
	}
}

func TestGetSecretVersionDoesNotFilterState(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false) // v1
	putSecret(t, st, r, false) // v2
	if _, err := st.SetSecretVersionState(ctx, r, 1, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	// disabled version still returned, with its state.
	_, ver, err := st.GetSecretVersion(ctx, r, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver.State != domain.StateDisabled {
		t.Fatalf("state = %q, want disabled", ver.State)
	}
}

func TestSetSecretVersionState(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false)
	putSecret(t, st, r, false)
	putSecret(t, st, r, false)

	// invalid state.
	_, err := st.SetSecretVersionState(ctx, r, 1, "bogus")
	mustErrIs(t, err, domain.ErrInvalidArgument, "bad state")

	// disable all (version 0).
	if _, err := st.SetSecretVersionState(ctx, r, 0, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	info, _ := st.GetSecretInfo(ctx, r)
	for _, v := range info.Versions {
		if v.State != domain.StateDisabled {
			t.Fatalf("version %d state = %q, want disabled", v.Version, v.State)
		}
	}
	// re-enable v2 only.
	if _, err := st.SetSecretVersionState(ctx, r, 2, domain.StateEnabled); err != nil {
		t.Fatal(err)
	}
	_, ver, _ := st.GetSecretVersion(ctx, r, 2, "")
	if ver.State != domain.StateEnabled {
		t.Fatalf("v2 state = %q, want enabled", ver.State)
	}

	// destroyed version cannot be resurrected via state change.
	if _, err := st.DestroySecretVersion(ctx, r, 3); err != nil {
		t.Fatal(err)
	}
	_, err = st.SetSecretVersionState(ctx, r, 3, domain.StateEnabled)
	mustErrIs(t, err, domain.ErrFailedPrecondition, "resurrect destroyed")

	// "disable all" must skip destroyed versions.
	if _, err := st.SetSecretVersionState(ctx, r, 0, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	_, ver3, _ := st.GetSecretVersion(ctx, r, 3, "")
	if ver3.State != domain.StateDestroyed {
		t.Fatalf("v3 state = %q, want still destroyed", ver3.State)
	}
}

func TestDestroySecretVersion(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false)
	rev, err := st.DestroySecretVersion(ctx, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rev == 0 {
		t.Fatal("destroy revision should be non-zero")
	}
	_, ver, err := st.GetSecretVersion(ctx, r, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver.Ciphertext != nil || ver.EncryptedDEK != nil || ver.Nonce != nil {
		t.Fatalf("destroyed version still has material: %+v", ver)
	}
	if ver.State != domain.StateDestroyed || ver.DestroyedAt.IsZero() {
		t.Fatalf("destroyed meta = state %q destroyedAt %v", ver.State, ver.DestroyedAt)
	}
	// re-destroy is a failed precondition.
	_, err = st.DestroySecretVersion(ctx, r, 1)
	mustErrIs(t, err, domain.ErrFailedPrecondition, "re-destroy")
	// version 0 is invalid.
	_, err = st.DestroySecretVersion(ctx, r, 0)
	mustErrIs(t, err, domain.ErrInvalidArgument, "destroy v0")
}

func TestPromoteSecretVersion(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false) // v1
	putSecret(t, st, r, false) // v2 current, previous 1
	putSecret(t, st, r, false) // v3 current, previous 2

	cur, prev, rev, err := st.PromoteSecretVersion(ctx, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cur != 1 || prev != 3 || rev == 0 {
		t.Fatalf("promote returned cur=%d prev=%d rev=%d", cur, prev, rev)
	}
	rec, _, _ := st.GetSecretVersion(ctx, r, 0, domain.LabelCurrent)
	if rec.Labels[domain.LabelCurrent] != 1 || rec.Labels[domain.LabelPrevious] != 3 {
		t.Fatalf("labels after promote = %v", rec.Labels)
	}

	// Promote requires an enabled, existing target.
	if _, err := st.SetSecretVersionState(ctx, r, 2, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = st.PromoteSecretVersion(ctx, r, 2)
	mustErrIs(t, err, domain.ErrFailedPrecondition, "promote disabled")
	_, _, _, err = st.PromoteSecretVersion(ctx, r, 99)
	mustErrIs(t, err, domain.ErrNotFound, "promote missing")
}

func TestSecretInfoAndList(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns := nsRef("prod", "app")
	seedNS(t, st, "prod", "app")
	if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
		Ref: domain.Ref{NS: ns, Key: "a/one"}, AccessTokenHash: []byte("h"), Encrypt: encryptStub(nil),
	}); err != nil {
		t.Fatal(err)
	}
	putSecret(t, st, domain.Ref{NS: ns, Key: "a/two"}, true)
	putSecret(t, st, domain.Ref{NS: ns, Key: "b/three"}, false)

	info, err := st.GetSecretInfo(ctx, domain.Ref{NS: ns, Key: "a/one"})
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasAccessToken {
		t.Fatal("HasAccessToken should be true")
	}

	list, _, err := st.ListSecrets(ctx, ns, "a", ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Ref.Key != "a/one" || list[1].Ref.Key != "a/two" {
		t.Fatalf("list a = %+v", list)
	}
	if !list[1].ClientBound {
		t.Fatal("a/two should be client-bound")
	}
}

func TestDeleteSecret(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false)
	putSecret(t, st, r, false)
	var sid int64
	st.db.Raw("SELECT id FROM secrets WHERE name='s'").Scan(&sid)
	if _, err := st.DeleteSecret(ctx, r); err != nil {
		t.Fatal(err)
	}
	var nVers, nLabels int64
	st.db.Raw("SELECT COUNT(*) FROM secret_versions WHERE secret_id=?", sid).Scan(&nVers)
	st.db.Raw("SELECT COUNT(*) FROM secret_labels WHERE secret_id=?", sid).Scan(&nLabels)
	if nVers != 0 || nLabels != 0 {
		t.Fatalf("cascade left rows versions=%d labels=%d", nVers, nLabels)
	}
	_, err := st.GetSecretRecord(ctx, r)
	mustErrIs(t, err, domain.ErrNotFound, "after delete")
}

func TestUpdateSecretAccessTokenHash(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "s")
	putSecret(t, st, r, false)
	if err := st.UpdateSecretAccessTokenHash(ctx, r, []byte("newhash")); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.GetSecretRecord(ctx, r)
	if string(rec.AccessTokenHash) != "newhash" {
		t.Fatalf("hash = %q", rec.AccessTokenHash)
	}
	// clearing to nil.
	if err := st.UpdateSecretAccessTokenHash(ctx, r, nil); err != nil {
		t.Fatal(err)
	}
	rec, _ = st.GetSecretRecord(ctx, r)
	if rec.AccessTokenHash != nil {
		t.Fatalf("hash = %v, want nil", rec.AccessTokenHash)
	}
	err := st.UpdateSecretAccessTokenHash(ctx, ref("prod", "app", "missing"), []byte("x"))
	mustErrIs(t, err, domain.ErrNotFound, "update missing secret")
}

// ---- key metadata / rotation ---------------------------------------------

func TestKeyMetadata(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	km := domain.KeyMetadata{ID: "kek-a", Source: domain.KeySourceFile, KeyCheck: []byte("kc"), State: domain.KeyStateActive}
	if err := st.InsertKeyMetadata(ctx, km); err != nil {
		t.Fatal(err)
	}
	// duplicate.
	err := st.InsertKeyMetadata(ctx, km)
	mustErrIs(t, err, domain.ErrAlreadyExists, "duplicate key")

	got, err := st.GetKeyMetadata(ctx, "kek-a")
	if err != nil || got.Source != domain.KeySourceFile || string(got.KeyCheck) != "kc" {
		t.Fatalf("got %+v err %v", got, err)
	}
	active, err := st.ActiveKeyMetadata(ctx)
	if err != nil || active.ID != "kek-a" {
		t.Fatalf("active = %+v err %v", active, err)
	}
	if err := st.SetKeyState(ctx, "kek-a", domain.KeyStateRetired); err != nil {
		t.Fatal(err)
	}
	_, err = st.ActiveKeyMetadata(ctx)
	mustErrIs(t, err, domain.ErrNotFound, "no active key")

	err = st.SetKeyState(ctx, "missing", domain.KeyStateActive)
	mustErrIs(t, err, domain.ErrNotFound, "set state missing")
}

func TestActiveKeyMetadataFreshDB(t *testing.T) {
	st := newStore(t)
	_, err := st.ActiveKeyMetadata(context.Background())
	mustErrIs(t, err, domain.ErrNotFound, "fresh db active key")
}

// noCARewrap is a rewrap callback for tests without CA keys; it must never fire.
func noCARewrap(t *testing.T) func(CAKeyRecord) ([]byte, error) {
	return func(CAKeyRecord) ([]byte, error) {
		t.Fatal("CA rewrap called with no CA keys present")
		return nil, nil
	}
}

func TestRotateKEK(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	s1 := ref("prod", "app", "s1")
	s2 := ref("prod", "app", "s2")
	putSecret(t, st, s1, false) // v1
	putSecret(t, st, s1, false) // v2
	putSecret(t, st, s2, false) // v1
	// destroy one; it must be skipped by rotation.
	if _, err := st.DestroySecretVersion(ctx, s1, 1); err != nil {
		t.Fatal(err)
	}

	secrets, caCount, err := st.RotateKEK(ctx, domain.KeyMetadata{ID: "kek-b", Source: "file", KeyCheck: []byte("b")},
		func(rec SecretVersionRecord) ([]byte, error) {
			if rec.KEKID != "kek-a" {
				return nil, fmt.Errorf("unexpected kek %s", rec.KEKID)
			}
			return append([]byte("rw-"), rec.EncryptedDEK...), nil
		}, noCARewrap(t))
	if err != nil {
		t.Fatal(err)
	}
	if secrets != 2 { // s1/v2 and s2/v1; s1/v1 destroyed
		t.Fatalf("rewrapped %d secrets, want 2", secrets)
	}
	if caCount != 0 {
		t.Fatalf("CA rewrapped %d, want 0", caCount)
	}
	// new key active, old retired.
	active, _ := st.ActiveKeyMetadata(ctx)
	if active.ID != "kek-b" {
		t.Fatalf("active = %s, want kek-b", active.ID)
	}
	old, _ := st.GetKeyMetadata(ctx, "kek-a")
	if old.State != domain.KeyStateRetired {
		t.Fatalf("kek-a state = %s, want retired", old.State)
	}
	// non-destroyed versions rewrapped to kek-b; destroyed untouched.
	_, v2, _ := st.GetSecretVersion(ctx, s1, 2, "")
	if v2.KEKID != "kek-b" || !strings.HasPrefix(string(v2.EncryptedDEK), "rw-") {
		t.Fatalf("s1/v2 not rewrapped: %+v", v2)
	}
	_, v1, _ := st.GetSecretVersion(ctx, s1, 1, "")
	if v1.State != domain.StateDestroyed || v1.EncryptedDEK != nil {
		t.Fatalf("destroyed version altered: %+v", v1)
	}
}

func TestRotateKEKRewrapsCA(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-a", Source: "file", KeyCheck: []byte("a"), State: domain.KeyStateActive}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertCAKey(ctx, CAKeyRecord{
		ID: "ca-1", CertPEM: "cert", EncryptedKey: []byte("k"), EncryptedDEK: []byte("dek-a"), KEKID: "kek-a",
	}); err != nil {
		t.Fatal(err)
	}

	secrets, caCount, err := st.RotateKEK(ctx, domain.KeyMetadata{ID: "kek-b", Source: "file", KeyCheck: []byte("b")},
		func(SecretVersionRecord) ([]byte, error) {
			t.Fatal("secret rewrap called with no secrets present")
			return nil, nil
		},
		func(rec CAKeyRecord) ([]byte, error) {
			if rec.KEKID != "kek-a" {
				return nil, fmt.Errorf("unexpected kek %s", rec.KEKID)
			}
			return append([]byte("rw-"), rec.EncryptedDEK...), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if secrets != 0 || caCount != 1 {
		t.Fatalf("rewrapped secrets=%d ca=%d, want 0 and 1", secrets, caCount)
	}
	ca, err := st.ActiveCAKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ca.KEKID != "kek-b" || string(ca.EncryptedDEK) != "rw-dek-a" {
		t.Fatalf("ca not rewrapped: %+v", ca)
	}
}

func TestRotateKEKRollback(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	s1 := ref("prod", "app", "s1")
	putSecret(t, st, s1, false)
	putSecret(t, st, ref("prod", "app", "s2"), false)

	// Fail on the second version so the first has already been rewrapped: the
	// whole transaction (including that successful rewrap and the key swap) must
	// roll back.
	var calls int
	_, _, err := st.RotateKEK(ctx, domain.KeyMetadata{ID: "kek-b", KeyCheck: []byte("b")},
		func(rec SecretVersionRecord) ([]byte, error) {
			calls++
			if calls >= 2 {
				return nil, errors.New("rewrap failed partway")
			}
			return []byte("rw"), nil
		}, noCARewrap(t))
	if err == nil {
		t.Fatal("expected rotation error")
	}
	if calls < 2 {
		t.Fatalf("rewrap called %d times, expected to fail on the second", calls)
	}
	// Everything unchanged: kek-b absent, kek-a still active, versions on kek-a.
	if _, err := st.GetKeyMetadata(ctx, "kek-b"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("kek-b should not exist: %v", err)
	}
	active, _ := st.ActiveKeyMetadata(ctx)
	if active.ID != "kek-a" {
		t.Fatalf("active = %s, want kek-a (rollback)", active.ID)
	}
	_, v, _ := st.GetSecretVersion(ctx, s1, 1, "")
	if v.KEKID != "kek-a" {
		t.Fatalf("version kek = %s, want kek-a (rollback)", v.KEKID)
	}
}

// ---- built-in CA / client certificates ------------------------------------

func TestCAKeyInsertAndRetire(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-a", Source: "file", KeyCheck: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	// No CA yet.
	_, err := st.ActiveCAKey(ctx)
	mustErrIs(t, err, domain.ErrNotFound, "no ca yet")

	if err := st.InsertCAKey(ctx, CAKeyRecord{ID: "ca-1", CertPEM: "c1", EncryptedKey: []byte("k1"), EncryptedDEK: []byte("d1"), KEKID: "kek-a"}); err != nil {
		t.Fatal(err)
	}
	ca, err := st.ActiveCAKey(ctx)
	if err != nil || ca.ID != "ca-1" || ca.State != domain.KeyStateActive {
		t.Fatalf("active ca = %+v err %v", ca, err)
	}
	// Inserting a new CA retires the previous active one.
	if err := st.InsertCAKey(ctx, CAKeyRecord{ID: "ca-2", CertPEM: "c2", EncryptedKey: []byte("k2"), EncryptedDEK: []byte("d2"), KEKID: "kek-a"}); err != nil {
		t.Fatal(err)
	}
	ca, err = st.ActiveCAKey(ctx)
	if err != nil || ca.ID != "ca-2" {
		t.Fatalf("active ca after rotate = %+v err %v", ca, err)
	}
	var oldState string
	st.db.Raw("SELECT state FROM ca_keys WHERE id='ca-1'").Scan(&oldState)
	if oldState != domain.KeyStateRetired {
		t.Fatalf("ca-1 state = %q, want retired", oldState)
	}
}

func TestIdentityCertCRUD(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "svc", Kind: domain.IdentityKindClient}); err != nil {
		t.Fatal(err)
	}
	notAfter := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cert := domain.IdentityCert{Serial: "01", Fingerprint: "fp1", NotAfter: notAfter}
	if err := st.InsertIdentityCert(ctx, "svc", cert); err != nil {
		t.Fatal(err)
	}
	// Duplicate serial is a conflict.
	err := st.InsertIdentityCert(ctx, "svc", cert)
	mustErrIs(t, err, domain.ErrAlreadyExists, "dup serial")
	// Issue against a missing identity.
	err = st.InsertIdentityCert(ctx, "ghost", domain.IdentityCert{Serial: "02"})
	mustErrIs(t, err, domain.ErrNotFound, "cert for missing identity")

	// A second concurrent cert (overlap rollover).
	if err := st.InsertIdentityCert(ctx, "svc", domain.IdentityCert{Serial: "02", Fingerprint: "fp2", NotAfter: notAfter}); err != nil {
		t.Fatal(err)
	}
	certs, err := st.ListIdentityCerts(ctx, "svc")
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("certs = %d, want 2", len(certs))
	}
	if !certs[0].NotAfter.Equal(notAfter) {
		t.Fatalf("not_after round-trip = %v, want %v", certs[0].NotAfter, notAfter)
	}

	// GetIdentityCertBySerial joins the owning identity.
	rec, err := st.GetIdentityCertBySerial(ctx, "01")
	if err != nil {
		t.Fatal(err)
	}
	if rec.IdentityName != "svc" || rec.IdentityDisabled || rec.Cert.Serial != "01" {
		t.Fatalf("cert record = %+v", rec)
	}
	if !rec.Cert.RevokedAt.IsZero() {
		t.Fatal("fresh cert must not be revoked")
	}
	_, err = st.GetIdentityCertBySerial(ctx, "nope")
	mustErrIs(t, err, domain.ErrNotFound, "missing serial")

	// Revoke is reflected and idempotent.
	if err := st.RevokeIdentityCert(ctx, "01"); err != nil {
		t.Fatal(err)
	}
	rec, _ = st.GetIdentityCertBySerial(ctx, "01")
	if rec.Cert.RevokedAt.IsZero() {
		t.Fatal("cert should be revoked")
	}
	if err := st.RevokeIdentityCert(ctx, "01"); err != nil {
		t.Fatalf("re-revoke should be a no-op: %v", err)
	}
	err = st.RevokeIdentityCert(ctx, "nope")
	mustErrIs(t, err, domain.ErrNotFound, "revoke missing")

	// Disabling the identity kills its certs at the DB level (interceptor reads
	// IdentityDisabled).
	if err := st.SetIdentityDisabled(ctx, "svc", true); err != nil {
		t.Fatal(err)
	}
	rec, _ = st.GetIdentityCertBySerial(ctx, "02")
	if !rec.IdentityDisabled {
		t.Fatal("cert record should report identity disabled")
	}

	// Deleting the identity cascades to its certs.
	// (identity_certs.identity_id has ON DELETE CASCADE.)
	if err := st.db.Exec("DELETE FROM identities WHERE name = 'svc'").Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	st.db.Raw("SELECT COUNT(*) FROM identity_certs").Scan(&n)
	if n != 0 {
		t.Fatalf("certs after identity delete = %d, want 0 (cascade)", n)
	}
}

func TestCreateIdentityWithCertIsAtomic(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "owner", Kind: domain.IdentityKindClient}); err != nil {
		t.Fatal(err)
	}
	cert := domain.IdentityCert{Serial: "shared", Fingerprint: "fp", NotAfter: time.Now().Add(time.Hour), CreatedAt: time.Now()}
	if err := st.InsertIdentityCert(ctx, "owner", cert); err != nil {
		t.Fatal(err)
	}

	_, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "retryable", Kind: domain.IdentityKindClient, Cert: &cert})
	mustErrIs(t, err, domain.ErrAlreadyExists, "duplicate initial cert")
	if _, err := st.GetIdentityByName(ctx, "retryable"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed cert insert left identity behind: %v", err)
	}

	cert.Serial = "unique"
	id, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "retryable", Kind: domain.IdentityKindClient, Cert: &cert})
	if err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	full, err := st.GetIdentityByName(ctx, id.Name)
	if err != nil || len(full.Certs) != 1 || full.Certs[0].Serial != "unique" {
		t.Fatalf("atomic identity/cert result = %+v err %v", full, err)
	}
}

// ---- namespaces / identities / policies -----------------------------------

func TestNamespaces(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: nsRef("prod", "a"), Description: "A", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if ns.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be set")
	}
	// Default auth methods are mtls-only.
	if len(ns.AllowedAuthMethods) != 1 || ns.AllowedAuthMethods[0] != domain.AuthMethodMTLS {
		t.Fatalf("default auth methods = %v, want [mtls]", ns.AllowedAuthMethods)
	}
	_, err = st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: nsRef("prod", "a")})
	mustErrIs(t, err, domain.ErrAlreadyExists, "dup namespace")

	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: nsRef("prod", "b")}); err != nil {
		t.Fatal(err)
	}
	list, _, err := st.ListNamespaces(ctx, ListPage{Limit: 100})
	if err != nil || len(list) != 2 || list[0].App != "a" {
		t.Fatalf("list = %+v err %v", list, err)
	}

	got, err := st.GetNamespace(ctx, nsRef("prod", "a"))
	if err != nil || got.Description != "A" {
		t.Fatalf("GetNamespace = %+v err %v", got, err)
	}
	_, err = st.GetNamespace(ctx, nsRef("prod", "missing"))
	mustErrIs(t, err, domain.ErrNotFound, "get missing namespace")
}

func TestNamespaceAuthMethodPersistence(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	// Explicit both-methods on create.
	ns, err := st.CreateNamespace(ctx, domain.Namespace{
		NamespaceRef:       nsRef("prod", "a"),
		AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ns.AllowedAuthMethods) != 2 {
		t.Fatalf("create methods = %v", ns.AllowedAuthMethods)
	}
	// UpdateNamespace fully replaces the method set and description.
	upd, err := st.UpdateNamespace(ctx, nsRef("prod", "a"), "new desc", []domain.AuthMethod{domain.AuthMethodToken})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Description != "new desc" || len(upd.AllowedAuthMethods) != 1 || upd.AllowedAuthMethods[0] != domain.AuthMethodToken {
		t.Fatalf("updated = %+v", upd)
	}
	// Round-trips from storage.
	got, _ := st.GetNamespace(ctx, nsRef("prod", "a"))
	if len(got.AllowedAuthMethods) != 1 || got.AllowedAuthMethods[0] != domain.AuthMethodToken {
		t.Fatalf("persisted methods = %v", got.AllowedAuthMethods)
	}
	// Empty update falls back to mtls-only.
	got, err = st.UpdateNamespace(ctx, nsRef("prod", "a"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AllowedAuthMethods) != 1 || got.AllowedAuthMethods[0] != domain.AuthMethodMTLS {
		t.Fatalf("empty update methods = %v, want [mtls]", got.AllowedAuthMethods)
	}
	_, err = st.UpdateNamespace(ctx, nsRef("prod", "missing"), "x", nil)
	mustErrIs(t, err, domain.ErrNotFound, "update missing namespace")
}

func TestListNamespacesCounts(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "a")
	seedNS(t, st, "prod", "b")
	// prod/a: 2 parameters, 1 secret. prod/b: nothing.
	if _, _, err := st.PutParameter(ctx, ref("prod", "a", "p1"), "v", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, ref("prod", "a", "p2"), "v", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	putSecret(t, st, ref("prod", "a", "s1"), false)

	list, _, err := st.ListNamespaces(ctx, ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byApp := map[string]domain.Namespace{}
	for _, ns := range list {
		byApp[ns.App] = ns
	}
	if byApp["a"].ParameterCount != 2 || byApp["a"].SecretCount != 1 {
		t.Fatalf("prod/a counts = param %d secret %d, want 2 and 1", byApp["a"].ParameterCount, byApp["a"].SecretCount)
	}
	if byApp["b"].ParameterCount != 0 || byApp["b"].SecretCount != 0 {
		t.Fatalf("prod/b counts = param %d secret %d, want 0 and 0", byApp["b"].ParameterCount, byApp["b"].SecretCount)
	}
}

func TestListNamespacesPagination(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	want := []string{"a", "b", "c", "d", "e"}
	for _, app := range want {
		seedNS(t, st, "prod", app)
	}
	var seen []string
	token := ""
	for {
		list, next, err := st.ListNamespaces(ctx, ListPage{Limit: 2, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, ns := range list {
			seen = append(seen, ns.App)
		}
		if next == "" {
			break
		}
		token = next
	}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("paginated namespaces = %v, want %v", seen, want)
	}
}

func TestDeleteNamespaceGuard(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "a")

	// Blocked by a parameter.
	if _, _, err := st.PutParameter(ctx, ref("prod", "a", "p"), "v", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	err := st.DeleteNamespace(ctx, nsRef("prod", "a"))
	mustErrIs(t, err, domain.ErrFailedPrecondition, "delete with parameter")
	if _, err := st.DeleteParameter(ctx, ref("prod", "a", "p")); err != nil {
		t.Fatal(err)
	}

	// Blocked by a secret.
	putSecret(t, st, ref("prod", "a", "s"), false)
	err = st.DeleteNamespace(ctx, nsRef("prod", "a"))
	mustErrIs(t, err, domain.ErrFailedPrecondition, "delete with secret")
	if _, err := st.DeleteSecret(ctx, ref("prod", "a", "s")); err != nil {
		t.Fatal(err)
	}

	// Blocked by a bound identity.
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "bound", Kind: domain.IdentityKindClient, Namespace: &domain.NamespaceRef{Env: "prod", App: "a"}}); err != nil {
		t.Fatal(err)
	}
	err = st.DeleteNamespace(ctx, nsRef("prod", "a"))
	mustErrIs(t, err, domain.ErrFailedPrecondition, "delete with bound identity")
	if err := st.db.Exec("DELETE FROM identities WHERE name='bound'").Error; err != nil {
		t.Fatal(err)
	}

	// Now empty: delete succeeds.
	if err := st.DeleteNamespace(ctx, nsRef("prod", "a")); err != nil {
		t.Fatalf("delete empty namespace: %v", err)
	}
	_, err = st.GetNamespace(ctx, nsRef("prod", "a"))
	mustErrIs(t, err, domain.ErrNotFound, "after delete")

	// Deleting a missing namespace is ErrNotFound.
	err = st.DeleteNamespace(ctx, nsRef("prod", "gone"))
	mustErrIs(t, err, domain.ErrNotFound, "delete missing namespace")
}

func TestIdentities(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")

	// Namespace-bound, token-carrying client.
	id, err := st.CreateIdentity(ctx, CreateIdentityParams{
		Name: "svc-1", Kind: domain.IdentityKindClient, TokenHash: []byte("h1"),
		Namespace: &domain.NamespaceRef{Env: "prod", App: "app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id.ID == 0 || id.Disabled {
		t.Fatalf("identity = %+v", id)
	}
	if !id.HasToken {
		t.Fatal("HasToken should be true")
	}
	if id.Namespace == nil || id.Namespace.Env != "prod" || id.Namespace.App != "app" {
		t.Fatalf("namespace binding = %+v", id.Namespace)
	}
	_, err = st.CreateIdentity(ctx, CreateIdentityParams{Name: "svc-1", Kind: domain.IdentityKindClient, TokenHash: []byte("h1")})
	mustErrIs(t, err, domain.ErrAlreadyExists, "dup identity")

	// Binding to a namespace that does not exist fails.
	_, err = st.CreateIdentity(ctx, CreateIdentityParams{Name: "svc-x", Kind: domain.IdentityKindClient, Namespace: &domain.NamespaceRef{Env: "prod", App: "ghost"}})
	mustErrIs(t, err, domain.ErrNotFound, "bind to missing namespace")

	byName, err := st.GetIdentityByName(ctx, "svc-1")
	if err != nil || byName.Name != "svc-1" || byName.Namespace == nil {
		t.Fatalf("byName = %+v err %v", byName, err)
	}
	byHash, err := st.GetIdentityByTokenHash(ctx, []byte("h1"))
	if err != nil || byHash.Name != "svc-1" {
		t.Fatalf("byHash = %+v err %v", byHash, err)
	}
	_, err = st.GetIdentityByTokenHash(ctx, []byte("nope"))
	mustErrIs(t, err, domain.ErrNotFound, "missing hash")

	if err := st.SetIdentityDisabled(ctx, "svc-1", true); err != nil {
		t.Fatal(err)
	}
	byName, _ = st.GetIdentityByName(ctx, "svc-1")
	if !byName.Disabled {
		t.Fatal("should be disabled")
	}
	if err := st.UpdateIdentityTokenHash(ctx, "svc-1", []byte("h2")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetIdentityByTokenHash(ctx, []byte("h2")); err != nil {
		t.Fatalf("new hash lookup: %v", err)
	}
	err = st.SetIdentityDisabled(ctx, "missing", true)
	mustErrIs(t, err, domain.ErrNotFound, "disable missing")
}

func TestIdentityCertOnly(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	// Cert-only identity: no token hash, unbound.
	id, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "certonly", Kind: domain.IdentityKindClient})
	if err != nil {
		t.Fatal(err)
	}
	if id.HasToken {
		t.Fatal("cert-only identity must not report HasToken")
	}
	if id.Namespace != nil {
		t.Fatalf("unbound identity namespace = %+v, want nil", id.Namespace)
	}
	// A cert-only identity must never be reachable by an empty/NULL token hash.
	_, err = st.GetIdentityByTokenHash(ctx, nil)
	mustErrIs(t, err, domain.ErrNotFound, "nil token hash")
	_, err = st.GetIdentityByTokenHash(ctx, []byte{})
	mustErrIs(t, err, domain.ErrNotFound, "empty token hash")

	// Token hash can be minted later, then cleared back to cert-only.
	if err := st.UpdateIdentityTokenHash(ctx, "certonly", []byte("minted")); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetIdentityByName(ctx, "certonly")
	if !got.HasToken {
		t.Fatal("HasToken should be true after minting")
	}
	if err := st.UpdateIdentityTokenHash(ctx, "certonly", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetIdentityByName(ctx, "certonly")
	if got.HasToken {
		t.Fatal("HasToken should be false after clearing")
	}
}

func TestListIdentitiesHydration(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "a", Kind: domain.IdentityKindClient, TokenHash: []byte("ta"), Namespace: &domain.NamespaceRef{Env: "prod", App: "app"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "b", Kind: domain.IdentityKindAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIdentityCert(ctx, "a", domain.IdentityCert{Serial: "01", Fingerprint: "fp", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	list, _, err := st.ListIdentities(ctx, ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]domain.Identity{}
	for _, id := range list {
		byName[id.Name] = id
	}
	if byName["a"].Namespace == nil || len(byName["a"].Certs) != 1 || !byName["a"].HasToken {
		t.Fatalf("identity a = %+v", byName["a"])
	}
	if byName["b"].Namespace != nil || byName["b"].HasToken || len(byName["b"].Certs) != 0 {
		t.Fatalf("identity b = %+v", byName["b"])
	}
}

func TestPolicies(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := domain.Policy{
		Name:    "p1",
		Subject: "svc-1",
		Allow:   []domain.PolicyRule{{Operation: domain.OpSecretRead, Env: "prod", App: "gradethis"}},
		Deny:    []domain.PolicyRule{{Operation: domain.OpSecretWrite, Env: "prod", App: "gradethis"}},
	}
	created, err := st.CreatePolicy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Allow) != 1 || created.Allow[0].App != "gradethis" || created.Allow[0].Env != "prod" {
		t.Fatalf("created = %+v", created)
	}
	_, err = st.CreatePolicy(ctx, p)
	mustErrIs(t, err, domain.ErrAlreadyExists, "dup policy")

	p.Deny = nil
	p.Allow = []domain.PolicyRule{{Operation: "*", Env: "*", App: "*"}}
	updated, err := st.UpdatePolicy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Deny) != 0 || updated.Allow[0].Operation != "*" || updated.Allow[0].App != "*" {
		t.Fatalf("updated = %+v", updated)
	}

	if _, err := st.CreatePolicy(ctx, domain.Policy{Name: "wild", Subject: "*", Allow: []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "*", App: "*"}}}); err != nil {
		t.Fatal(err)
	}
	forSubj, err := st.PoliciesForSubject(ctx, "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(forSubj) != 2 { // p1 (svc-1) + wild (*)
		t.Fatalf("policies for svc-1 = %d, want 2", len(forSubj))
	}

	if err := st.DeletePolicy(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	err = st.DeletePolicy(ctx, "p1")
	mustErrIs(t, err, domain.ErrNotFound, "delete missing policy")
}

// ---- audit ----------------------------------------------------------------

func TestAuditAppendListAndFilters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	events := []domain.AuditEvent{
		{EventType: "read", ActorIdentity: "alice", ResourceNamespaceID: 42, ResourceEnv: "prod", ResourceApp: "app", ResourceKey: "x", Decision: "allow", CreatedAt: base},
		{EventType: "write", ActorIdentity: "bob", ResourceNamespaceID: 42, ResourceEnv: "prod", ResourceApp: "app", ResourceKey: "y", Decision: "allow", CreatedAt: base.Add(time.Second)},
		{EventType: "read", ActorIdentity: "alice", ResourceNamespaceID: 43, ResourceEnv: "stage", ResourceApp: "app", ResourceKey: "z", Decision: "deny", CreatedAt: base.Add(2 * time.Second)},
	}
	for _, e := range events {
		if err := st.AppendAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// newest first.
	all, _, err := st.ListAudit(ctx, domain.AuditFilter{}, ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ResourceEnv != "stage" || all[0].ResourceKey != "z" {
		t.Fatalf("audit order = %+v", all)
	}
	if all[0].ResourceNamespaceID != 43 {
		t.Fatalf("audit namespace incarnation ID = %d, want 43", all[0].ResourceNamespaceID)
	}
	// namespace filter (env + app).
	byNS, _, _ := st.ListAudit(ctx, domain.AuditFilter{Env: "prod", App: "app"}, ListPage{Limit: 100})
	if len(byNS) != 2 {
		t.Fatalf("prod/app = %d, want 2", len(byNS))
	}
	// key prefix filter.
	byKey, _, _ := st.ListAudit(ctx, domain.AuditFilter{KeyPrefix: "x"}, ListPage{Limit: 100})
	if len(byKey) != 1 {
		t.Fatalf("key prefix x = %d, want 1", len(byKey))
	}
	// actor + event type filters.
	byActor, _, _ := st.ListAudit(ctx, domain.AuditFilter{ActorIdentity: "alice", EventType: "read"}, ListPage{Limit: 100})
	if len(byActor) != 2 {
		t.Fatalf("alice+read = %d, want 2", len(byActor))
	}
}

func TestAuditTimeRangeFractionalSeconds(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	// e1 at .000, e2 at .500 — RFC3339Nano would trim e1's fraction and misorder
	// a string range comparison; fixed-width format keeps it correct.
	if err := st.AppendAudit(ctx, domain.AuditEvent{EventType: "x", ResourceKey: "p", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, domain.AuditEvent{EventType: "x", ResourceKey: "p", CreatedAt: base.Add(500 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}

	from := base.Add(250 * time.Millisecond)
	got, _, err := st.ListAudit(ctx, domain.AuditFilter{From: from}, ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("From=.250 returned %d events, want 1 (only the .500 event)", len(got))
	}
	if !got[0].CreatedAt.Equal(base.Add(500 * time.Millisecond)) {
		t.Fatalf("returned event at %v, want .500", got[0].CreatedAt)
	}
}

func TestAuditPagination(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for range 5 {
		if err := st.AppendAudit(ctx, domain.AuditEvent{EventType: "e", ResourceKey: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	var seen int
	token := ""
	for {
		list, next, err := st.ListAudit(ctx, domain.AuditFilter{}, ListPage{Limit: 2, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(list)
		if next == "" {
			break
		}
		token = next
	}
	if seen != 5 {
		t.Fatalf("paginated audit = %d, want 5", seen)
	}
}

// ---- change log / revisions ----------------------------------------------

func TestRevisionMonotonicAfterPruneAll(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "p")
	for i := range 5 {
		if _, _, err := st.PutParameter(ctx, r, strconv.Itoa(i), "", "", "u"); err != nil {
			t.Fatal(err)
		}
	}
	maxBefore, err := st.CurrentRevision(ctx)
	if err != nil || maxBefore == 0 {
		t.Fatalf("CurrentRevision = %d err %v", maxBefore, err)
	}
	// Prune every row.
	deleted, err := st.PruneChangeLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted == 0 {
		t.Fatal("expected to delete all change_log rows")
	}
	oldest, _ := st.OldestRetainedRevision(ctx)
	if oldest != 0 {
		t.Fatalf("OldestRetainedRevision after prune-all = %d, want 0", oldest)
	}
	// CurrentRevision survives pruning (reads sqlite_sequence).
	afterPrune, _ := st.CurrentRevision(ctx)
	if afterPrune != maxBefore {
		t.Fatalf("CurrentRevision changed after prune: %d != %d", afterPrune, maxBefore)
	}
	// New writes get strictly greater revisions — never reused.
	_, newRev, err := st.PutParameter(ctx, ref("prod", "app", "q"), "v", "", "", "u")
	if err != nil {
		t.Fatal(err)
	}
	if newRev <= maxBefore {
		t.Fatalf("new revision %d not strictly greater than old max %d", newRev, maxBefore)
	}
}

func TestListChangesSince(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	_, r1, _ := st.PutParameter(ctx, ref("prod", "app", "a"), "1", "", "", "u")
	_, r2, _ := st.PutParameter(ctx, ref("prod", "app", "b"), "2", "", "", "u")
	changes, err := st.ListChangesSince(ctx, r1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Revision != r2 || changes[0].Ref.Key != "b" {
		t.Fatalf("changes since r1 = %+v", changes)
	}
	if changes[0].Ref.NS.Env != "prod" || changes[0].Ref.NS.App != "app" {
		t.Fatalf("change ref ns = %+v", changes[0].Ref.NS)
	}
	if changes[0].Value != "2" {
		t.Fatalf("parameter change value = %q, want 2", changes[0].Value)
	}
}

func TestPruneChangeLogRetentionMath(t *testing.T) {
	insert := func(st *SQLStore, n int, age time.Duration) {
		now := time.Now()
		for range n {
			if err := st.db.Create(&changeLogModel{
				ResourceType: "parameter", Env: "prod", App: "app", Key: "p", ChangeType: "put",
				CreatedAt: fmtTime(now.Add(-age)),
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("duration dominates", func(t *testing.T) {
		st := newStore(t)
		insert(st, 3, 2*time.Hour) // old
		insert(st, 3, 0)           // recent
		deleted, err := st.PruneChangeLog(context.Background(), time.Hour, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 3 {
			t.Fatalf("deleted %d, want 3 (the old ones)", deleted)
		}
	})

	t.Run("maxRows dominates", func(t *testing.T) {
		st := newStore(t)
		insert(st, 6, 0)
		deleted, err := st.PruneChangeLog(context.Background(), 1000*time.Hour, 2)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 4 {
			t.Fatalf("deleted %d, want 4 (keep 2 newest)", deleted)
		}
	})

	t.Run("intersection", func(t *testing.T) {
		st := newStore(t)
		insert(st, 3, 2*time.Hour) // old
		insert(st, 3, 0)           // recent
		// duration keeps the 3 recent; maxRows keeps 2 newest => keep 2.
		deleted, err := st.PruneChangeLog(context.Background(), time.Hour, 2)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 4 {
			t.Fatalf("deleted %d, want 4 (keep 2)", deleted)
		}
	})

	t.Run("maxRows zero deletes all", func(t *testing.T) {
		st := newStore(t)
		insert(st, 5, 0)
		deleted, err := st.PruneChangeLog(context.Background(), 1000*time.Hour, 0)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 5 {
			t.Fatalf("deleted %d, want 5", deleted)
		}
	})
}

func TestSnapshotParameters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "svc")
	seedNS(t, st, "stage", "svc")
	if _, _, err := st.PutParameter(ctx, ref("prod", "svc", "a"), "1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, ref("prod", "svc", "a"), "2", "", "", "u"); err != nil { // current is v2
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, ref("prod", "svc", "b"), "x", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, ref("stage", "svc", "c"), "y", "", "", "u"); err != nil {
		t.Fatal(err)
	}

	// A whole namespace: every current parameter in it.
	params, rev, err := st.SnapshotParameters(ctx, []domain.NamespaceRef{nsRef("prod", "svc")})
	if err != nil {
		t.Fatal(err)
	}
	if rev == 0 {
		t.Fatal("snapshot revision should be non-zero")
	}
	if len(params) != 2 {
		t.Fatalf("prod/svc returned %d, want 2", len(params))
	}
	byKey := map[string]domain.Parameter{}
	for _, p := range params {
		byKey[p.Ref.Key] = p
	}
	if byKey["a"].Value != "2" || byKey["a"].Version != 2 {
		t.Fatalf("prod/svc a snapshot = %+v", byKey["a"])
	}

	// A single other namespace.
	other, _, _ := st.SnapshotParameters(ctx, []domain.NamespaceRef{nsRef("stage", "svc")})
	if len(other) != 1 || other[0].Ref.Key != "c" || other[0].Ref.NS.Env != "stage" {
		t.Fatalf("stage/svc = %+v", other)
	}

	// Multiple namespaces in one snapshot.
	both, _, _ := st.SnapshotParameters(ctx, []domain.NamespaceRef{nsRef("prod", "svc"), nsRef("stage", "svc")})
	if len(both) != 3 {
		t.Fatalf("both namespaces = %d, want 3", len(both))
	}
	// ordered by (env, app, key): prod/svc/a, prod/svc/b, stage/svc/c.
	if both[0].Ref.Key != "a" || both[2].Ref.Key != "c" {
		t.Fatalf("order = %q..%q", both[0].Ref.Key, both[2].Ref.Key)
	}

	// No namespaces => nothing (but a valid revision).
	none, rev2, _ := st.SnapshotParameters(ctx, nil)
	if len(none) != 0 || rev2 == 0 {
		t.Fatalf("empty namespaces = %d rev %d", len(none), rev2)
	}

	// A non-existent namespace matches nothing.
	missing, rev3, _ := st.SnapshotParameters(ctx, []domain.NamespaceRef{nsRef("nope", "svc")})
	if len(missing) != 0 || rev3 == 0 {
		t.Fatalf("missing namespace = %d rev %d", len(missing), rev3)
	}
}

func TestSnapshotParametersRequiresEveryBoundNamespacePair(t *testing.T) {
	st := newStore(t)
	existing := seedNS(t, st, "prod", "existing")
	missing := nsRef("prod", "missing")
	ctx, err := BindNamespaceIncarnation(context.Background(), existing.NamespaceRef, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Even a malformed internal registration that reuses a real row ID for a
	// different display name must not let one successful pair mask the missing
	// pair during the set-based namespace resolution.
	ctx, err = BindNamespaceIncarnation(ctx, missing, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.SnapshotParameters(ctx, []domain.NamespaceRef{existing.NamespaceRef, missing})
	if !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("snapshot with one mismatched bound pair err = %v, want ErrAborted", err)
	}
}

// TestSnapshotParametersSetBased exercises the set-based snapshot query against
// the contract it must preserve: whole-namespace scope, label resolution, the
// full labels map, omission of parameters lacking a "current" label or a
// version row for it, and empty results.
func TestSnapshotParametersSetBased(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns := nsRef("prod", "svc")
	seedNS(t, st, "prod", "svc")

	// billing/a has two versions -> current v2, previous v1. The '/' is an
	// ordinary character in the key; the snapshot is whole-namespace.
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "billing/a"}, "1", "text/plain", `{"k":"1"}`, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "billing/a"}, "2", "text/plain", `{"k":"2"}`, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "other"}, "x", "", "", "u"); err != nil {
		t.Fatal(err)
	}

	byKey := func(ps []domain.Parameter) map[string]domain.Parameter {
		m := map[string]domain.Parameter{}
		for _, p := range ps {
			m[p.Ref.Key] = p
		}
		return m
	}

	all, rev, err := st.SnapshotParameters(ctx, []domain.NamespaceRef{nsRef("prod", "svc")})
	if err != nil {
		t.Fatal(err)
	}
	if wantRev, _ := st.CurrentRevision(ctx); rev != wantRev {
		t.Fatalf("snapshot rev %d != CurrentRevision %d", rev, wantRev)
	}
	if len(all) != 2 {
		t.Fatalf("namespace returned %d, want 2 (%+v)", len(all), all)
	}
	a := byKey(all)["billing/a"]
	if a.Value != "2" || a.Version != 2 || a.ContentType != "text/plain" || a.Metadata != `{"k":"2"}` || a.CreatedBy != "bob" {
		t.Fatalf("billing/a = %+v", a)
	}
	// The full labels map (current + previous), not just "current".
	if a.Labels[domain.LabelCurrent] != 2 || a.Labels[domain.LabelPrevious] != 1 {
		t.Fatalf("billing/a labels = %+v", a.Labels)
	}
	if byKey(all)["other"].Value != "x" {
		t.Fatalf("other = %+v", byKey(all)["other"])
	}

	// An empty namespace: empty slice, still a valid revision.
	seedNS(t, st, "prod", "empty")
	empty, rev3, err := st.SnapshotParameters(ctx, []domain.NamespaceRef{nsRef("prod", "empty")})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 || rev3 == 0 {
		t.Fatalf("no-match = %d rev %d", len(empty), rev3)
	}

	// A parameter whose "current" label was removed (only a non-current label
	// remains) must be omitted.
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "orphan"}, "o1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "orphan"}, "o2", "", "", "u"); err != nil { // current=2, previous=1
		t.Fatal(err)
	}
	var op parameterModel
	if err := st.db.Where("name = ?", "orphan").First(&op).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Where("parameter_id = ? AND label = ?", op.ID, domain.LabelCurrent).
		Delete(&parameterLabelModel{}).Error; err != nil {
		t.Fatal(err)
	}
	afterOrphan := byKey(mustSnapshot(t, st, ctx, []domain.NamespaceRef{nsRef("prod", "svc")}))
	if _, ok := afterOrphan["orphan"]; ok {
		t.Fatalf("param with no current label should be omitted, got %+v", afterOrphan["orphan"])
	}
	if _, ok := afterOrphan["billing/a"]; !ok {
		t.Fatal("billing/a should still be present")
	}

	// A "current" label pointing at a missing version row must also be omitted.
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "ghost"}, "g1", "", "", "u"); err != nil {
		t.Fatal(err)
	}
	var gp parameterModel
	if err := st.db.Where("name = ?", "ghost").First(&gp).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Where("parameter_id = ? AND version_number = ?", gp.ID, 1).
		Delete(&parameterVersionModel{}).Error; err != nil {
		t.Fatal(err)
	}
	afterGhost := byKey(mustSnapshot(t, st, ctx, []domain.NamespaceRef{nsRef("prod", "svc")}))
	if _, ok := afterGhost["ghost"]; ok {
		t.Fatal("param whose current version row is gone should be omitted")
	}
}

func mustSnapshot(t *testing.T, st *SQLStore, ctx context.Context, namespaces []domain.NamespaceRef) []domain.Parameter {
	t.Helper()
	ps, _, err := st.SnapshotParameters(ctx, namespaces)
	if err != nil {
		t.Fatalf("SnapshotParameters(%v): %v", namespaces, err)
	}
	return ps
}

// ---- backup / time round-trip / pagination tokens -------------------------

func TestBackup(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	if _, _, err := st.PutParameter(ctx, ref("prod", "app", "a"), "v", "", "", "u"); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := st.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("backup mode = %o, want 600", got)
		}
	}
	// backup must be a usable database with the data.
	bk, err := Open(dest)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = bk.Close() }()
	got, err := bk.GetParameter(ctx, ref("prod", "app", "a"), 0, "")
	if err != nil || got.Value != "v" {
		t.Fatalf("backup data = %+v err %v", got, err)
	}
	// existing destination is refused.
	err = st.Backup(ctx, dest)
	mustErrIs(t, err, domain.ErrAlreadyExists, "backup over existing")
}

func TestBackupRefusesDanglingSymlinkDestination(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "attacker-target")
	dest := filepath.Join(dir, "backup.db")
	if err := os.Symlink(target, dest); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := st.Backup(context.Background(), dest)
	mustErrIs(t, err, domain.ErrAlreadyExists, "backup to dangling symlink")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("backup followed dangling symlink: %v", err)
	}
	if info, err := os.Lstat(dest); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("backup destination symlink changed: info=%v err=%v", info, err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".kms-backup-*")); err != nil || len(matches) != 0 {
		t.Fatalf("backup staging directories remain after refusal: %v (glob err %v)", matches, err)
	}
}

func TestBackupRejectsSharedMutableParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows DACL rejection is covered by fileutil platform tests")
	}
	st := newStore(t)
	root := t.TempDir()
	dir := filepath.Join(root, "shared")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "backup.db")
	if err := st.Backup(context.Background(), dest); err == nil {
		t.Fatal("backup accepted a parent mutable by another account")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("unsafe backup path was created: %v", err)
	}
}

func TestTimeRoundTripNanoseconds(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 7, 1, 12, 34, 56, 123456789, time.UTC)
	ns, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: nsRef("prod", "n"), CreatedAt: ts})
	if err != nil {
		t.Fatal(err)
	}
	if !ns.CreatedAt.Equal(ts) {
		t.Fatalf("returned CreatedAt %v != %v", ns.CreatedAt, ts)
	}
	got, _ := st.GetNamespace(ctx, nsRef("prod", "n"))
	if !got.CreatedAt.Equal(ts) {
		t.Fatalf("stored CreatedAt %v != %v", got.CreatedAt, ts)
	}
}

func TestPageTokenOpaqueInvalid(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	_, _, err := st.ListParameters(ctx, nsRef("prod", "app"), "", ListPage{Limit: 10, Token: "!!!not-base64!!!"})
	mustErrIs(t, err, domain.ErrInvalidArgument, "bad page token")
	_, _, err = st.ListAudit(ctx, domain.AuditFilter{}, ListPage{Limit: 10, Token: "!!!bad!!!"})
	mustErrIs(t, err, domain.ErrInvalidArgument, "bad audit token")
}

func TestLimitClamping(t *testing.T) {
	if clampLimit(0) != 100 {
		t.Fatal("default should be 100")
	}
	if clampLimit(-5) != 100 {
		t.Fatal("negative should default to 100")
	}
	if clampLimit(5000) != 1000 {
		t.Fatal("over-max should clamp to 1000")
	}
	if clampLimit(50) != 50 {
		t.Fatal("in-range should pass through")
	}
}

// ---- concurrency ----------------------------------------------------------

func TestConcurrentPutParameterNoLostUpdates(t *testing.T) {
	// This test exercises version and label integrity under heavy contention,
	// not the production default's five-second lock-wait budget. Race and
	// coverage instrumentation can starve one of the pooled SQLite writers long
	// enough to exhaust that default on a loaded runner before its transaction
	// begins, so give the integrity stress a larger but still bounded budget.
	st := newStoreWithOptions(t, Options{BusyTimeout: 30 * time.Second})
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "hot")
	const workers, iters = 8, 40
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iters)
	verCh := make(chan uint64, workers*iters)

	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range iters {
				v, _, err := st.PutParameter(ctx, r, fmt.Sprintf("w%d-i%d", w, i), "", "", "u")
				if err != nil {
					errCh <- err
					continue
				}
				verCh <- v
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	close(verCh)

	for err := range errCh {
		t.Fatalf("concurrent put error (busy handling failed?): %v", err)
	}
	seen := make(map[uint64]bool)
	var max uint64
	count := 0
	for v := range verCh {
		if seen[v] {
			t.Fatalf("duplicate version %d assigned (lost update)", v)
		}
		seen[v] = true
		if v > max {
			max = v
		}
		count++
	}
	if count != workers*iters {
		t.Fatalf("successful puts = %d, want %d", count, workers*iters)
	}
	// Versions are exactly 1..count with no gaps.
	if max != uint64(count) {
		t.Fatalf("max version %d != count %d (gap or reuse)", max, count)
	}
	info, err := st.GetParameterInfo(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Versions) != count {
		t.Fatalf("stored versions %d != %d", len(info.Versions), count)
	}
	if info.Labels[domain.LabelCurrent] != uint64(count) {
		t.Fatalf("current label = %d, want %d", info.Labels[domain.LabelCurrent], count)
	}
}
