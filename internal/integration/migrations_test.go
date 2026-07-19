package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
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
	if version != 2 {
		t.Errorf("schema version = %d, want 2", version)
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
		"change_log", "schema_migrations", "configuration_releases",
		"configuration_release_entries", "configuration_release_labels",
		"configuration_release_activations", "configuration_schemas", "release_subscriber_states", "release_subscriber_connections",
	} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist: %v", table, err)
		}
	}
}

// Configuration releases are an additive schema-v2 upgrade. A database
// stamped at v1 must gain the new tables without disturbing existing data.
func TestMigrationV1ToV2AddsConfigurationReleaseTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kms-v1.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("create database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	for _, table := range []string{
		"release_subscriber_states", "release_subscriber_connections", "configuration_release_labels",
		"configuration_release_activations", "configuration_release_entries", "configuration_releases", "configuration_schemas",
	} {
		if _, err := db.Exec("DROP TABLE " + table); err != nil {
			_ = db.Close()
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	if _, err := db.Exec("UPDATE schema_migrations SET version = 1"); err != nil {
		_ = db.Close()
		t.Fatalf("stamp v1: %v", err)
	}
	if _, err := db.Exec("INSERT INTO namespaces(env,app,description,allowed_auth_methods,created_by,created_at) VALUES('prod','kept','x','[\"token\"]','root','2026-01-01T00:00:00.000000000Z')"); err != nil {
		_ = db.Close()
		t.Fatalf("seed existing data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	upgraded, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("upgrade v1 database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if _, err := upgraded.GetNamespace(context.Background(), domain.NamespaceRef{Env: "prod", App: "kept"}); err != nil {
		t.Fatalf("existing namespace lost during upgrade: %v", err)
	}

	check, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("inspect upgraded database: %v", err)
	}
	defer func() { _ = check.Close() }()
	for _, table := range []string{
		"configuration_releases", "configuration_release_entries",
		"configuration_release_labels", "configuration_release_activations", "configuration_schemas", "release_subscriber_states", "release_subscriber_connections",
	} {
		var count int
		if err := check.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Errorf("schema-v2 table %q missing: count=%d err=%v", table, count, err)
		}
	}
}
