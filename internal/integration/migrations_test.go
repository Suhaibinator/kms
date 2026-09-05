package integration

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/storage"
)

type baselineSchemaObject struct {
	Type      string
	Name      string
	TableName string
	SQL       string
}

func TestV03GreenfieldBaselineAndExactReopen(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "kms.db")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open fresh 0.3 database: %v", err)
	}
	if revision, err := store.CurrentRevision(context.Background()); err != nil || revision != 0 {
		t.Fatalf("fresh revision = %d, %v; want 0, nil", revision, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close fresh database: %v", err)
	}

	wantTables := []string{
		"applications",
		"audit_events",
		"ca_keys",
		"change_log",
		"configuration_release_activations",
		"configuration_release_entries",
		"configuration_release_labels",
		"configuration_releases",
		"configuration_schemas",
		"identities",
		"identity_certs",
		"key_metadata",
		"namespaces",
		"parameter_labels",
		"parameter_versions",
		"parameters",
		"policies",
		"release_subscriber_connections",
		"release_subscriber_states",
		"schema_migrations",
		"secret_labels",
		"secret_version_high_water",
		"secret_versions",
		"secrets",
	}
	before := readBaselineSchema(t, path)
	if got := baselineTables(before); !reflect.DeepEqual(got, wantTables) {
		t.Fatalf("fresh baseline tables = %v, want %v", got, wantTables)
	}

	db := openRawDatabase(t, path)
	var versions []int
	rows, err := db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		_ = db.Close()
		t.Fatalf("read schema version: %v", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			_ = db.Close()
			t.Fatalf("scan schema version: %v", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = db.Close()
		t.Fatalf("iterate schema versions: %v", err)
	}
	if err := rows.Close(); err != nil {
		_ = db.Close()
		t.Fatalf("close schema-version rows: %v", err)
	}
	if !reflect.DeepEqual(versions, []int{1}) {
		_ = db.Close()
		t.Fatalf("schema versions = %v, want [1]", versions)
	}
	var changeLogDDL string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'change_log'").Scan(&changeLogDDL); err != nil {
		_ = db.Close()
		t.Fatalf("read change_log DDL: %v", err)
	}
	if !strings.Contains(strings.ToUpper(changeLogDDL), "AUTOINCREMENT") {
		_ = db.Close()
		t.Fatalf("change_log DDL lacks AUTOINCREMENT: %q", changeLogDDL)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close baseline inspection: %v", err)
	}

	reopened, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen exact 0.3 baseline: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened database: %v", err)
	}
	after := readBaselineSchema(t, path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("reopen changed physical schema\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestV03RejectsIncompatibleDatabasesWithoutMutation(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"legacy unstamped": func(t *testing.T, path string) {
			createRawDatabase(t, path,
				`CREATE TABLE legacy_secrets (id INTEGER PRIMARY KEY, value TEXT)`,
				`INSERT INTO legacy_secrets VALUES (1, 'keep-me')`,
			)
		},
		"partial stamped baseline": func(t *testing.T, path string) {
			createRawDatabase(t, path,
				`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
				`INSERT INTO schema_migrations VALUES (1, 'partial')`,
				`CREATE TABLE secrets (id INTEGER PRIMARY KEY, client_bound INTEGER NOT NULL DEFAULT 0)`,
			)
		},
		"drifted baseline": func(t *testing.T, path string) {
			store, err := storage.Open(path)
			if err != nil {
				t.Fatalf("create baseline for drift case: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close baseline for drift case: %v", err)
			}
			createRawDatabase(t, path,
				`PRAGMA journal_mode = DELETE`,
				`DROP INDEX idx_secret_ns_name`,
			)
		},
	}

	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateTempDir(t), "incompatible.db")
			setup(t, path)
			if runtime.GOOS != "windows" {
				if err := os.Chmod(path, 0o400); err != nil {
					t.Fatalf("make incompatible database read-only: %v", err)
				}
			}
			assertNoSQLiteSidecars(t, path)

			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read incompatible database: %v", err)
			}
			beforeInfo, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat incompatible database: %v", err)
			}

			if opened, err := storage.Open(path); err == nil {
				_ = opened.Close()
				t.Fatal("incompatible database was accepted")
			} else if !strings.Contains(err.Error(), "incompatible 0.3.x database baseline") {
				t.Fatalf("rejection error = %v, want incompatible baseline", err)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reread incompatible database: %v", err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("rejected database contents changed")
			}
			afterInfo, err := os.Stat(path)
			if err != nil {
				t.Fatalf("restat incompatible database: %v", err)
			}
			if afterInfo.Mode() != beforeInfo.Mode() {
				t.Fatalf("rejected database mode changed from %v to %v", beforeInfo.Mode(), afterInfo.Mode())
			}
			if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
				t.Fatalf("rejected database modification time changed from %v to %v", beforeInfo.ModTime(), afterInfo.ModTime())
			}
			assertNoSQLiteSidecars(t, path)
		})
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("make test directory private: %v", err)
		}
	}
	return dir
}

func openRawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	return db
}

func createRawDatabase(t *testing.T, path string, statements ...string) {
	t.Helper()
	db := openRawDatabase(t, path)
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("execute setup statement %q: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}
}

func readBaselineSchema(t *testing.T, path string) []baselineSchemaObject {
	t.Helper()
	db := openRawDatabase(t, path)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close schema inspection: %v", err)
		}
	}()
	rows, err := db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table', 'index', 'trigger', 'view')
		  AND name NOT GLOB 'sqlite_*'
		ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read physical schema: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close physical-schema rows: %v", err)
		}
	}()
	var objects []baselineSchemaObject
	for rows.Next() {
		var object baselineSchemaObject
		if err := rows.Scan(&object.Type, &object.Name, &object.TableName, &object.SQL); err != nil {
			t.Fatalf("scan physical schema: %v", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate physical schema: %v", err)
	}
	return objects
}

func baselineTables(objects []baselineSchemaObject) []string {
	var tables []string
	for _, object := range objects {
		if object.Type == "table" {
			tables = append(tables, object.Name)
		}
	}
	sort.Strings(tables)
	return tables
}

func assertNoSQLiteSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("unexpected SQLite sidecar %s: %v", suffix, err)
		}
	}
}
