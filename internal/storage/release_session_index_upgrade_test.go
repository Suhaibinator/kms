package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestReleaseSessionIndexUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seedNS(t, st, "prod", "app")
	if _, _, err := st.PutParameter(context.Background(), ref("prod", "app", "value"), "retained", "", "", "admin"); err != nil {
		t.Fatal(err)
	}
	putSecret(t, st, ref("prod", "app", "secret"), false)
	// Include session state and secret ciphertext in the all-table comparison.
	if err := st.db.Create(&releaseSessionModel{SessionID: "session", NamespaceID: 1, ReleaseName: "runtime", ClientName: "client", InstanceID: "instance", Identity: "identity", ServerTimestamp: "retained", DisconnectedAt: "retained"}).Error; err != nil {
		t.Fatal(err)
	}
	rows := func() map[string][]map[string]any {
		t.Helper()
		tables, err := st.db.Migrator().GetTables()
		if err != nil {
			t.Fatal(err)
		}
		result := map[string][]map[string]any{}
		for _, table := range tables {
			var values []map[string]any
			if err := st.db.Table(table).Find(&values).Error; err != nil {
				t.Fatal(err)
			}
			result[table] = values
		}
		return result
	}
	beforeRows := rows()
	if err := st.db.Exec("DROP INDEX " + releaseSessionDisconnectIndexName).Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseBaseline4WithoutDisconnectIndex(st.db); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("injected post-DDL verification failure")
	if err := upgradeReleaseSessionsWithVerifier(st.db, func(tx *gorm.DB) error {
		if err := verifyBaselineDB(tx); err != nil {
			t.Fatal(err)
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("rollback error: %v", err)
	}
	if err := verifyReleaseBaseline4WithoutDisconnectIndex(st.db); err != nil {
		t.Fatalf("DDL did not roll back: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateKMSDatabase(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only validation mutated database")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("validation created %s: %v", suffix, err)
		}
	}
	for i := 0; i < 2; i++ {
		st, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyBaselineDB(st.db); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(beforeRows, rows()) {
			t.Fatal("upgrade changed stored rows or schema stamp")
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReleaseSessionIndexUpgradeRejectsDriftWithoutMutation(t *testing.T) {
	for name, statements := range map[string][]string{
		"wrong stamp":              {"UPDATE schema_migrations SET version = 99"},
		"missing additional index": {"DROP INDEX idx_secret_ns_name"},
		"unexpected object":        {"CREATE VIEW operator_view AS SELECT 1"},
		"wrong index definition":   {"CREATE INDEX `idx_release_sessions_disconnected_at` ON `release_sessions`(`connected`)"},
		"changed table":            {"ALTER TABLE release_sessions ADD COLUMN unexpected TEXT"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kms.db")
			st, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.db.Exec("DROP INDEX " + releaseSessionDisconnectIndexName).Error; err != nil {
				t.Fatal(err)
			}
			for _, sql := range statements {
				if err := st.db.Exec(sql).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateKMSDatabase(path); err == nil {
				t.Fatal("validation accepted unsupported schema")
			}
			opened, err := Open(path)
			if err == nil {
				_ = opened.Close()
				t.Fatal("opened unsupported schema")
			}
			if !strings.Contains(err.Error(), "incompatible KMS database schema") || !strings.Contains(err.Error(), releaseSessionDisconnectIndexName) {
				t.Fatalf("unhelpful error: %v", err)
			}
			if strings.Contains(err.Error(), "0.3.x") || strings.Contains(err.Error(), "create a fresh") {
				t.Fatalf("misleading error: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected database changed")
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
					t.Fatalf("rejection created %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestBaselineSchemaDiagnostics(t *testing.T) {
	expected := []baselineSchemaObject{{Type: "table", Name: "example", SQL: "original"}}
	for name, actual := range map[string][]baselineSchemaObject{
		"missing table":                nil,
		"unexpected index":             {expected[0], {Type: "index", Name: "extra"}},
		"definition differs for table": {{Type: "table", Name: "example", SQL: "changed"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := compareBaselineSchema(actual, expected)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("diagnostic: %v", err)
			}
		})
	}
}
