package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/storage"
)

// §25.2.7 — migrations run on a fresh database, are idempotent on reopen, and
// establish the critical AUTOINCREMENT invariant on change_log.
func TestMigrationsFreshAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kms.db")

	// First open runs migrations.
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open (fresh): %v", err)
	}
	if _, err := store.CurrentRevision(context.Background()); err != nil {
		t.Fatalf("CurrentRevision on fresh DB: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening the same database must succeed (idempotent migrate).
	store2, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open (reopen): %v", err)
	}
	if err := store2.Close(); err != nil {
		t.Fatalf("close 2: %v", err)
	}

	// Inspect the physical schema directly.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()

	var version int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}

	var ddl string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='change_log'").Scan(&ddl); err != nil {
		t.Fatalf("read change_log ddl: %v", err)
	}
	if !strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT") {
		t.Errorf("change_log DDL missing AUTOINCREMENT: %q", ddl)
	}

	// Every expected table exists (namespace-native schema: namespaces plus the
	// built-in CA and client-certificate tables).
	for _, table := range []string{
		"key_metadata", "namespaces", "parameters", "parameter_versions",
		"parameter_labels", "secrets", "secret_versions", "secret_labels",
		"identities", "ca_keys", "identity_certs", "policies", "audit_events",
		"change_log", "schema_migrations",
	} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist: %v", table, err)
		}
	}
}
