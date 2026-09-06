package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func legacyTokenDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
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

func TestUnusedSecretTokenSchemaUpgrade(t *testing.T) {
	path := legacyTokenDatabase(t)
	if err := ValidateKMSDatabase(path); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"plain", "bound"} {
		_, version, err := st.GetSecretVersion(context.Background(), ref("prod", "app", key), 1, "")
		if err != nil || string(version.Ciphertext) != "ct-1" || version.Bound != (key == "bound") {
			t.Fatalf("preserved %s: %+v, %v", key, version, err)
		}
	}
	if err := verifyBaselineDB(st.db); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
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
			pool.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateKMSDatabase(path); err == nil {
				t.Fatal("validation accepted token use")
			}
			if st, err := Open(path); err == nil {
				st.Close()
				t.Fatal("upgrade accepted token use")
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

func TestSecretTokenUpgradeRollsBackOnStampFailure(t *testing.T) {
	path := legacyTokenDatabase(t)
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	injected := fmt.Errorf("injected schema stamp failure")
	if err := db.Callback().Update().Before("gorm:update").Register("test:fail-stamp", func(tx *gorm.DB) { tx.AddError(injected) }); err != nil {
		t.Fatal(err)
	}
	if err := upgradeSecretTokenSchema(db); !errors.Is(err, injected) {
		t.Fatalf("upgrade error = %v", err)
	}
	if err := verifyTokenFreeLegacyBaseline(db); err != nil {
		t.Fatalf("rollback did not restore legacy schema: %v", err)
	}
	if err := db.Callback().Update().Remove("test:fail-stamp"); err != nil {
		t.Fatal(err)
	}
	if err := upgradeSecretTokenSchema(db); err != nil {
		t.Fatal(err)
	}
	st := &SQLStore{db: db}
	_, version, err := st.GetSecretVersion(context.Background(), ref("prod", "app", "bound"), 1, "")
	if err != nil || !version.Bound || string(version.Ciphertext) != "ct-1" {
		t.Fatalf("secret not preserved: %+v, %v", version, err)
	}
}
