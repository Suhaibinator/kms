package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func legacyTokenDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	file, err := fileutil.OpenPrivateExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	}()
	objects, err := referenceSchema(true)
	if err != nil {
		t.Fatal(err)
	}
	// Tables must exist before indexes. Foreign-key references are legal before
	// their target table is materialized in SQLite.
	for _, kind := range []string{"table", "index"} {
		for _, object := range objects {
			if object.Type == kind {
				if err := db.Exec(object.SQL).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := db.Create(&schemaMigrationModel{Version: 1, AppliedAt: "2026-01-01T00:00:00.000000000Z"}).Error; err != nil {
		t.Fatal(err)
	}
	st := &SQLStore{db: db}
	seedNS(t, st, "prod", "app")
	putSecret(t, st, ref("prod", "app", "plain"), false)
	putSecret(t, st, ref("prod", "app", "bound"), true)
	return path
}

func TestUnusedSecretTokenBaselineRejectedWithoutChanges(t *testing.T) {
	path := legacyTokenDatabase(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateKMSDatabase(path); err == nil {
		t.Fatal("legacy baseline accepted")
	}
	if st, err := Open(path); err == nil {
		_ = st.Close()
		t.Fatal("legacy baseline opened")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected database changed")
	}
}

func TestSecretTokenUpgradeRejectsUsedColumns(t *testing.T) {
	for _, query := range []string{"UPDATE secrets SET access_token_hash = X'01'", "UPDATE secret_versions SET has_access_token = 1"} {
		t.Run(query, func(t *testing.T) {
			path := legacyTokenDatabase(t)
			db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			pool, _ := db.DB()
			if err := db.Exec(query).Error; err != nil {
				t.Fatal(err)
			}
			if err := pool.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateKMSDatabase(path); err == nil || !strings.Contains(err.Error(), "incompatible") {
				t.Fatalf("validation error = %v, want baseline rejection", err)
			}
			if st, err := Open(path); err == nil {
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				t.Fatal("upgrade accepted token use")
			} else if !strings.Contains(err.Error(), "incompatible") {
				t.Fatalf("upgrade error = %v, want baseline rejection", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("rejected database changed")
			}
		})
	}
}

func TestLegacyBaselineRejectedBeforeAnyWrite(t *testing.T) {
	path := legacyTokenDatabase(t)
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	}()
	wrote := false
	if err := db.Callback().Update().Before("gorm:update").Register("test:observe-write", func(tx *gorm.DB) { wrote = true }); err != nil {
		t.Fatal(err)
	}
	if err := upgradeSecretTokenSchema(db); err == nil {
		t.Fatal("legacy baseline accepted")
	}
	if wrote {
		t.Fatal("baseline rejection attempted a write")
	}
	if err := verifyTokenFreeLegacyBaseline(db); err != nil {
		t.Fatalf("legacy state changed: %v", err)
	}
}
