package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if version != 8 {
		t.Errorf("schema version = %d, want 8", version)
	}

	var ddl string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='change_log'").Scan(&ddl); err != nil {
		t.Fatalf("read change_log ddl: %v", err)
	}
	if !strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT") {
		t.Errorf("change_log DDL missing AUTOINCREMENT: %q", ddl)
	}
	for _, column := range []struct{ table, name string }{
		{"audit_events", "resource_namespace_id"},
		{"change_log", "namespace_id"},
		{"configuration_release_entries", "resource_namespace_id"},
	} {
		var count int
		query := "SELECT COUNT(*) FROM pragma_table_info('" + column.table + "') WHERE name = ?"
		if err := db.QueryRow(query, column.name).Scan(&count); err != nil {
			t.Fatalf("inspect %s.%s: %v", column.table, column.name, err)
		}
		if count != 1 {
			t.Errorf("expected physical column %s.%s", column.table, column.name)
		}
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

// Schema v3 adds authenticated identity to the release-subscriber primary
// keys. Existing lifecycle and connection rows must survive the SQLite table
// rebuild, and a second identity must then be able to use the same client tuple
// without overwriting the first.
func TestMigrationV2ToV3ScopesReleaseSubscribersByIdentity(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "kms-v2.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := store.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := store.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "alice", ConnectionID: "alice-1", Connected: true, ServerTimestamp: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReleaseAcknowledgement(ctx, domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7,
		ClientName: "api", InstanceID: "replica-1", Identity: "alice", ConnectionID: "alice-1",
		State: domain.ReleaseStateReceived, ClientTimestamp: at, ServerTimestamp: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`ALTER TABLE release_subscriber_states RENAME TO release_subscriber_states_v3_seed`,
		`CREATE TABLE release_subscriber_states (
			namespace_id INTEGER NOT NULL, release_name TEXT NOT NULL,
			client_name TEXT NOT NULL, instance_id TEXT NOT NULL, state TEXT NOT NULL,
			identity TEXT NOT NULL DEFAULT '', release_version INTEGER NOT NULL,
			activation_revision INTEGER NOT NULL, rejection_category TEXT NOT NULL DEFAULT '',
			diagnostic TEXT NOT NULL DEFAULT '', client_timestamp TEXT NOT NULL,
			server_timestamp TEXT NOT NULL, connected INTEGER NOT NULL DEFAULT 0,
			disconnected_at TEXT,
			PRIMARY KEY (namespace_id, release_name, client_name, instance_id, state),
			FOREIGN KEY (namespace_id) REFERENCES namespaces(id)
		)`,
		`INSERT INTO release_subscriber_states SELECT namespace_id, release_name,
			client_name, instance_id, state, identity, release_version,
			activation_revision, rejection_category, diagnostic, client_timestamp,
			server_timestamp, connected, disconnected_at
			FROM release_subscriber_states_v3_seed`,
		`DROP TABLE release_subscriber_states_v3_seed`,
		`ALTER TABLE release_subscriber_connections RENAME TO release_subscriber_connections_v3_seed`,
		`CREATE TABLE release_subscriber_connections (
			namespace_id INTEGER NOT NULL, release_name TEXT NOT NULL,
			client_name TEXT NOT NULL, instance_id TEXT NOT NULL,
			identity TEXT NOT NULL DEFAULT '', connection_id TEXT NOT NULL DEFAULT '',
			connected INTEGER NOT NULL DEFAULT 0, connected_at TEXT NOT NULL,
			disconnected_at TEXT, server_timestamp TEXT NOT NULL,
			PRIMARY KEY (namespace_id, release_name, client_name, instance_id),
			FOREIGN KEY (namespace_id) REFERENCES namespaces(id)
		)`,
		`INSERT INTO release_subscriber_connections SELECT namespace_id,
			release_name, client_name, instance_id, identity, connection_id,
			connected, connected_at, disconnected_at, server_timestamp
			FROM release_subscriber_connections_v3_seed`,
		`DROP TABLE release_subscriber_connections_v3_seed`,
		`UPDATE schema_migrations SET version = 2`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare v2 schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("upgrade v2 database: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if err := upgraded.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "bob", ConnectionID: "bob-1", Connected: true, ServerTimestamp: at.Add(time.Second),
	}); err != nil {
		t.Fatalf("second identity after migration: %v", err)
	}
	rows, _, err := upgraded.ListReleaseAcknowledgements(ctx, ns, "runtime", storage.ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	identities := make(map[string]bool, len(rows))
	for _, row := range rows {
		identities[row.Identity] = true
	}
	if !identities["alice"] || !identities["bob"] {
		t.Fatalf("subscriber identities after migration = %v, rows=%+v", identities, rows)
	}
}

// A current schema stamp is not proof that every physical table migration
// completed. Repair either subscriber table independently when an interrupted
// or manually restored database contains one stale v2 key.
func TestMigrationRepairsPartialSubscriberSchemaDespiteCurrentStamp(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "kms-partial-v3.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := store.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := store.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "alice", ConnectionID: "alice-1", Connected: true, ServerTimestamp: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReleaseAcknowledgement(ctx, domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7,
		ClientName: "api", InstanceID: "replica-1", Identity: "alice", ConnectionID: "alice-1",
		State: domain.ReleaseStateReceived, ClientTimestamp: at, ServerTimestamp: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`ALTER TABLE release_subscriber_states RENAME TO release_subscriber_states_v3_seed`,
		`CREATE TABLE release_subscriber_states (
			namespace_id INTEGER NOT NULL, release_name TEXT NOT NULL,
			client_name TEXT NOT NULL, instance_id TEXT NOT NULL, state TEXT NOT NULL,
			identity TEXT NOT NULL DEFAULT '', release_version INTEGER NOT NULL,
			activation_revision INTEGER NOT NULL, rejection_category TEXT NOT NULL DEFAULT '',
			diagnostic TEXT NOT NULL DEFAULT '', client_timestamp TEXT NOT NULL,
			server_timestamp TEXT NOT NULL, connected INTEGER NOT NULL DEFAULT 0,
			disconnected_at TEXT,
			PRIMARY KEY (namespace_id, release_name, client_name, instance_id, state),
			FOREIGN KEY (namespace_id) REFERENCES namespaces(id)
		)`,
		`INSERT INTO release_subscriber_states SELECT namespace_id, release_name,
			client_name, instance_id, state, identity, release_version,
			activation_revision, rejection_category, diagnostic, client_timestamp,
			server_timestamp, connected, disconnected_at
			FROM release_subscriber_states_v3_seed`,
		`DROP TABLE release_subscriber_states_v3_seed`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare partial schema: %v", err)
		}
	}
	var version int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 8 {
		t.Fatalf("partial database schema stamp = %d, want current v8", version)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("repair partial current-stamped database: %v", err)
	}
	defer func() { _ = repaired.Close() }()
	if err := repaired.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "bob", ConnectionID: "bob-1", Connected: true, ServerTimestamp: at,
	}); err != nil {
		t.Fatalf("second identity connection after partial repair: %v", err)
	}
	if err := repaired.UpsertReleaseAcknowledgement(ctx, domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7,
		ClientName: "api", InstanceID: "replica-1", Identity: "bob", ConnectionID: "bob-1",
		State: domain.ReleaseStateReceived, ClientTimestamp: at, ServerTimestamp: at,
	}); err != nil {
		t.Fatalf("second identity after partial repair: %v", err)
	}
	rows, _, err := repaired.ListReleaseAcknowledgements(ctx, ns, "runtime", storage.ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	identities := make(map[string]bool, len(rows))
	for _, row := range rows {
		identities[row.Identity] = true
	}
	if !identities["alice"] || !identities["bob"] {
		t.Fatalf("subscriber identities after partial repair = %v, rows=%+v", identities, rows)
	}
}

func TestMigrationRepairsConnectionOnlyStaleKey(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "kms-stale-connection.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := store.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE release_subscriber_connections RENAME TO release_subscriber_connections_v3_seed`,
		`CREATE TABLE release_subscriber_connections (
			namespace_id INTEGER NOT NULL, release_name TEXT NOT NULL,
			client_name TEXT NOT NULL, instance_id TEXT NOT NULL,
			identity TEXT NOT NULL DEFAULT '', connection_id TEXT NOT NULL DEFAULT '',
			connected INTEGER NOT NULL DEFAULT 0, connected_at TEXT NOT NULL,
			disconnected_at TEXT, server_timestamp TEXT NOT NULL,
			PRIMARY KEY (namespace_id, release_name, client_name, instance_id),
			FOREIGN KEY (namespace_id) REFERENCES namespaces(id)
		)`,
		`INSERT INTO release_subscriber_connections SELECT namespace_id, release_name,
			client_name, instance_id, identity, connection_id, connected,
			connected_at, disconnected_at, server_timestamp
			FROM release_subscriber_connections_v3_seed`,
		`DROP TABLE release_subscriber_connections_v3_seed`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare stale connection table: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("repair connection-only stale key: %v", err)
	}
	defer func() { _ = repaired.Close() }()
	at := time.Now().UTC()
	for _, identity := range []string{"alice", "bob"} {
		if err := repaired.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
			Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
			Identity: identity, ConnectionID: identity + "-1", Connected: true, ServerTimestamp: at,
		}); err != nil {
			t.Fatalf("connect %s after repair: %v", identity, err)
		}
	}
	rows, _, err := repaired.ListReleaseAcknowledgements(ctx, ns, "runtime", storage.ListPage{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("connection identities after repair = %+v err=%v, want two", rows, err)
	}
}

func TestMigrationRecreatesOneMissingSubscriberTable(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "kms-missing-connection.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := store.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE release_subscriber_connections`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("recreate missing connection table: %v", err)
	}
	defer func() { _ = repaired.Close() }()
	if err := repaired.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "alice", ConnectionID: "alice-1", Connected: true, ServerTimestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("use recreated connection table: %v", err)
	}
}

func TestMigrationV3ToV4AddsAuditNamespaceIncarnation(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "kms-v3-audit.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAudit(ctx, domain.AuditEvent{EventType: "legacy", ResourceType: domain.ResourceParameter}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP INDEX idx_audit_namespace_id`,
		`ALTER TABLE audit_events DROP COLUMN resource_namespace_id`,
		`UPDATE schema_migrations SET version = 3`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare v3 audit schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("upgrade v3 audit schema: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if err := upgraded.AppendAudit(ctx, domain.AuditEvent{EventType: "current", ResourceType: domain.ResourceParameter, ResourceNamespaceID: 77}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := upgraded.ListAudit(ctx, domain.AuditFilter{}, storage.ListPage{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("audit rows after v4 migration = %+v err=%v", rows, err)
	}
	if rows[0].ResourceNamespaceID != 77 || rows[1].ResourceNamespaceID != 0 {
		t.Fatalf("audit incarnation IDs after v4 migration = [%d %d], want [77 0]", rows[0].ResourceNamespaceID, rows[1].ResourceNamespaceID)
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

// Schema v7 adds applied_divergent / divergent_field_count to the subscriber
// lifecycle table. That table is never AutoMigrate'd once it exists, so a v6
// database must gain the columns through the explicit ALTER TABLE path, keep
// its rows (reading as not-divergent), and accept divergent acknowledgements
// afterwards.
func TestMigrationV6ToV7AddsSubscriberDivergence(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "kms-v6-divergence.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := store.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := store.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "alice", ConnectionID: "alice-1", Connected: true, ServerTimestamp: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReleaseAcknowledgement(ctx, domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7,
		ClientName: "api", InstanceID: "replica-1", Identity: "alice", ConnectionID: "alice-1",
		State: domain.ReleaseStateApplied, ClientTimestamp: at, ServerTimestamp: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE release_subscriber_states DROP COLUMN applied_divergent`,
		`ALTER TABLE release_subscriber_states DROP COLUMN divergent_field_count`,
		`UPDATE schema_migrations SET version = 6`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare v6 subscriber schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("upgrade v6 subscriber schema: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	rows, _, err := upgraded.ListReleaseAcknowledgements(ctx, ns, "runtime", storage.ListPage{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("subscriber rows after v7 migration = %+v err=%v", rows, err)
	}
	if rows[0].State != domain.ReleaseStateApplied || rows[0].AppliedDivergent || rows[0].DivergentFieldCount != 0 {
		t.Fatalf("legacy applied row should read as not divergent: %+v", rows[0])
	}
	if err := upgraded.UpsertReleaseAcknowledgement(ctx, domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 8,
		ClientName: "api", InstanceID: "replica-1", Identity: "alice", ConnectionID: "alice-1",
		State: domain.ReleaseStateApplied, AppliedDivergent: true, DivergentFieldCount: 2,
		ClientTimestamp: time.Now().UTC(), ServerTimestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("divergent ack after migration: %v", err)
	}
	rows, _, err = upgraded.ListReleaseAcknowledgements(ctx, ns, "runtime", storage.ListPage{})
	if err != nil || len(rows) != 1 || !rows[0].AppliedDivergent || rows[0].DivergentFieldCount != 2 {
		t.Fatalf("divergent row after v7 migration = %+v err=%v", rows, err)
	}

	// Reopening a v7 database is idempotent: the ALTER path is skipped.
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen v7: %v", err)
	}
	_ = again.Close()
}
