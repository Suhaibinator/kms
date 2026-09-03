package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func legacySchemaOwnershipDB(t *testing.T) *gorm.DB {
	t.Helper()
	return legacySchemaOwnershipDBAt(t, filepath.Join(t.TempDir(), "legacy.db"))
}

func legacySchemaOwnershipDBAt(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE applications (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '', release_name TEXT NOT NULL DEFAULT 'runtime',
			schema_id TEXT NOT NULL DEFAULT '', schema_version INTEGER NOT NULL DEFAULT 0,
			contract_json TEXT NOT NULL DEFAULT '[]', created_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE namespaces (
			id INTEGER PRIMARY KEY AUTOINCREMENT, env TEXT NOT NULL, app TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '', allowed_auth_methods TEXT NOT NULL DEFAULT '["mtls"]',
			created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, UNIQUE(env, app),
			FOREIGN KEY(app) REFERENCES applications(name)
		)`,
		`CREATE TABLE configuration_releases (
			id INTEGER PRIMARY KEY AUTOINCREMENT, namespace_id INTEGER NOT NULL,
			name TEXT NOT NULL, version_number INTEGER NOT NULL, schema_id TEXT NOT NULL DEFAULT '',
			schema_version INTEGER NOT NULL DEFAULT 0, digest TEXT NOT NULL,
			metadata_json TEXT NOT NULL DEFAULT '{}', created_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, UNIQUE(namespace_id, name, version_number),
			FOREIGN KEY(namespace_id) REFERENCES namespaces(id)
		)`,
		`CREATE TABLE configuration_release_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT, release_id INTEGER NOT NULL,
			alias TEXT NOT NULL, kind TEXT NOT NULL, resource_namespace_id INTEGER NOT NULL DEFAULT 0,
			resource_env TEXT NOT NULL, resource_app TEXT NOT NULL, resource_key TEXT NOT NULL,
			resource_version INTEGER NOT NULL, content_type TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL DEFAULT '{}', parameter_digest TEXT NOT NULL DEFAULT '',
			client_bound INTEGER NOT NULL DEFAULT 0, has_access_token INTEGER NOT NULL DEFAULT 0,
			UNIQUE(release_id, alias), FOREIGN KEY(release_id) REFERENCES configuration_releases(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE configuration_schemas (
			id TEXT NOT NULL, version_number INTEGER NOT NULL, schema_json TEXT NOT NULL,
			digest TEXT NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}',
			created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
			PRIMARY KEY(id, version_number)
		)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestOpenMigratesValidSchemaOwnershipV8Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	initial, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := initial.CreateApplicationWithSchema(t.Context(),
		domain.Application{Name: "app", ReleaseName: "runtime"},
		domain.ConfigurationSchema{Application: "app", ReleaseName: "runtime", Schema: `{"type":"object"}`, Digest: "digest", Metadata: "{}"},
	); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE applications ADD COLUMN schema_id TEXT NOT NULL DEFAULT ''`,
		`UPDATE applications SET schema_id = 'app/runtime' WHERE name = 'app'`,
		`ALTER TABLE configuration_releases ADD COLUMN schema_id TEXT NOT NULL DEFAULT ''`,
		`DROP TABLE configuration_schemas`,
		`CREATE TABLE configuration_schemas (
			id TEXT NOT NULL, version_number INTEGER NOT NULL, schema_json TEXT NOT NULL,
			digest TEXT NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}',
			created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
			PRIMARY KEY(id, version_number))`,
		`INSERT INTO configuration_schemas
			(id, version_number, schema_json, digest, created_at) VALUES
			('app/runtime', 1, '{"type":"object"}', 'digest', '2026-01-01T00:00:00Z')`,
		`DROP TABLE policies`,
		`DELETE FROM schema_migrations`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (7, '2026-01-01T00:00:00Z')`,
	} {
		if err := initial.db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer func() { _ = store.Close() }()
	got, err := store.GetConfigurationSchema(t.Context(), "app", "runtime", 1)
	if err != nil || got.Application != "app" || got.ReleaseName != "runtime" {
		t.Fatalf("migrated schema = %+v err=%v", got, err)
	}
	var stamped schemaMigrationModel
	if err := store.db.Order("version DESC").First(&stamped).Error; err != nil || stamped.Version != schemaVersion {
		t.Fatalf("schema stamp = %+v err=%v", stamped, err)
	}
	if !store.db.Migrator().HasTable(&policyModel{}) {
		t.Fatal("ownership migration skipped an unrelated missing table")
	}
}

func TestOpenMigratesLegacyPinsWithoutSchemaHistoryToCompleteV8Shape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-empty-registry.db")
	initial, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.CreateApplication(t.Context(), domain.Application{Name: "app", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE configuration_schemas`,
		`ALTER TABLE applications ADD COLUMN schema_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE configuration_releases ADD COLUMN schema_id TEXT NOT NULL DEFAULT ''`,
		`DELETE FROM schema_migrations`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (7, '2026-01-01T00:00:00Z')`,
	} {
		if err := initial.db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy database without schema history: %v", err)
	}
	defer func() { _ = store.Close() }()
	if !store.db.Migrator().HasTable(&configurationSchemaModel{}) ||
		!store.db.Migrator().HasColumn(&applicationModel{}, "ArchivedAt") ||
		!store.db.Migrator().HasColumn(&applicationModel{}, "ArchivedBy") {
		t.Fatal("migration did not create the complete v8 ownership shape")
	}
	if store.db.Migrator().HasColumn("applications", "schema_id") ||
		store.db.Migrator().HasColumn("configuration_releases", "schema_id") {
		t.Fatal("migration retained a legacy schema_id column")
	}
	created, err := store.CreateConfigurationSchema(t.Context(), domain.ConfigurationSchema{
		Application: "app", ReleaseName: "runtime", Schema: `{"type":"object"}`,
		Digest: "digest", Metadata: "{}",
	})
	if err != nil || created.Version != 1 {
		t.Fatalf("create schema after empty-registry migration = %+v err=%v", created, err)
	}
}

func seedLegacyApplication(t *testing.T, db *gorm.DB, name, releaseName, schemaID string, schemaVersion int) {
	t.Helper()
	if err := db.Exec(`INSERT INTO applications
		(name, release_name, schema_id, schema_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		name, releaseName, schemaID, schemaVersion).Error; err != nil {
		t.Fatal(err)
	}
}

func TestMigrateSchemaOwnershipV8BackfillsCoordinatesAndReleaseDigest(t *testing.T) {
	db := legacySchemaOwnershipDB(t)
	seedLegacyApplication(t, db, "gradethis", "runtime", "gradethis/runtime", 1)
	if err := db.Exec(`INSERT INTO namespaces
		(id, env, app, created_at) VALUES (1, 'prod', 'gradethis', '2026-01-01T00:00:00Z')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO configuration_schemas
		(id, version_number, schema_json, digest, created_at)
		VALUES ('gradethis/runtime', 1, '{"type":"object"}', 'schema-digest', '2026-01-01T00:00:00Z')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO configuration_releases
		(id, namespace_id, name, version_number, schema_id, schema_version, digest, metadata_json, created_at)
		VALUES (1, 1, 'runtime', 1, 'gradethis/runtime', 1, 'legacy-digest', '{}', '2026-01-01T00:00:00Z')`).Error; err != nil {
		t.Fatal(err)
	}

	wantDigest, err := releaseDigestV8(
		namespaceModel{ID: 1, Env: "prod", App: "gradethis"},
		configurationReleaseModel{ID: 1, NamespaceID: 1, Name: "runtime", VersionNumber: 1, SchemaVersion: 1, MetadataJSON: "{}"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSchemaOwnershipV8(db, 7); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn("applications", "schema_id") || db.Migrator().HasColumn("configuration_releases", "schema_id") {
		t.Fatal("legacy schema_id column survived migration")
	}
	var schema configurationSchemaModel
	if err := db.First(&schema).Error; err != nil {
		t.Fatal(err)
	}
	if schema.ApplicationName != "gradethis" || schema.ReleaseName != "runtime" || schema.VersionNumber != 1 {
		t.Fatalf("migrated schema = %+v", schema)
	}
	var digest string
	if err := db.Raw(`SELECT digest FROM configuration_releases WHERE id = 1`).Scan(&digest).Error; err != nil {
		t.Fatal(err)
	}
	if digest != wantDigest || digest == "legacy-digest" {
		t.Fatalf("release digest = %q, want recomputed %q", digest, wantDigest)
	}
	if err := db.Exec(`DELETE FROM configuration_releases; DELETE FROM namespaces`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`DELETE FROM applications WHERE name = 'gradethis'`).Error; err == nil {
		t.Fatal("schema ownership foreign key allowed application deletion")
	}
}

func TestMigrateSchemaOwnershipV8RejectsMissingOrPartiallyMigratedRegistry(t *testing.T) {
	for _, test := range []struct {
		name string
		seed func(*testing.T, *gorm.DB)
		want string
	}{
		{
			name: "missing table with pin",
			seed: func(t *testing.T, db *gorm.DB) {
				if err := db.Exec(`CREATE TABLE applications (name TEXT PRIMARY KEY, schema_id TEXT NOT NULL DEFAULT '', schema_version INTEGER NOT NULL DEFAULT 0)`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`INSERT INTO applications (name, schema_id, schema_version) VALUES ('app', 'app/runtime', 1)`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "table is missing",
		},
		{
			name: "already structured without stamp",
			seed: func(t *testing.T, db *gorm.DB) {
				if err := db.Exec(`CREATE TABLE configuration_schemas (application_name TEXT, release_name TEXT, version_number INTEGER)`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "id column is missing",
		},
		{
			name: "structured registry with legacy pins",
			seed: func(t *testing.T, db *gorm.DB) {
				for _, ddl := range []string{
					`CREATE TABLE applications (name TEXT PRIMARY KEY, schema_id TEXT NOT NULL DEFAULT '', schema_version INTEGER NOT NULL DEFAULT 0)`,
					`CREATE TABLE configuration_releases (id INTEGER PRIMARY KEY, schema_id TEXT NOT NULL DEFAULT '', schema_version INTEGER NOT NULL DEFAULT 0)`,
					`CREATE TABLE configuration_schemas (application_name TEXT, release_name TEXT, version_number INTEGER)`,
				} {
					if err := db.Exec(ddl).Error; err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "partially migrated",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/partial.db"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			test.seed(t, db)
			if err := migrateSchemaOwnershipV8(db, 7); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("migration error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMigrateSchemaOwnershipV8RejectsInvalidOwnershipAndRollsBack(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, *gorm.DB)
		want string
	}{
		{
			name: "application version without id",
			seed: func(t *testing.T, db *gorm.DB) { seedLegacyApplication(t, db, "app", "runtime", "", 1) },
			want: "invalid application pin",
		},
		{
			name: "application id without version",
			seed: func(t *testing.T, db *gorm.DB) { seedLegacyApplication(t, db, "app", "runtime", "app/runtime", 0) },
			want: "invalid application pin",
		},
		{
			name: "wrong application prefix",
			seed: func(t *testing.T, db *gorm.DB) { seedLegacyApplication(t, db, "app", "runtime", "other/runtime", 1) },
			want: "application pin mismatch",
		},
		{
			name: "shared application id",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "first", "runtime", "first/runtime", 1)
				seedLegacyApplication(t, db, "second", "runtime", "first/runtime", 1)
			},
			want: "application pin mismatch",
		},
		{
			name: "release version without id",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "", 0)
				if err := db.Exec(`INSERT INTO namespaces (id, env, app, created_at) VALUES (1, 'prod', 'app', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`INSERT INTO configuration_releases
					(namespace_id, name, version_number, schema_id, schema_version, digest, created_at)
					VALUES (1, 'runtime', 1, '', 1, 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "invalid release pin",
		},
		{
			name: "wrong release prefix",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "", 0)
				if err := db.Exec(`INSERT INTO namespaces (id, env, app, created_at) VALUES (1, 'prod', 'app', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`INSERT INTO configuration_releases
					(namespace_id, name, version_number, schema_id, schema_version, digest, created_at)
					VALUES (1, 'runtime', 1, 'other/runtime', 1, 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "release pin mismatch",
		},
		{
			name: "malformed registry id",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "", 0)
				if err := db.Exec(`INSERT INTO configuration_schemas (id, version_number, schema_json, digest, created_at) VALUES ('app/runtime/extra', 1, '{}', 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "malformed",
		},
		{
			name: "empty release registry id",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "", 0)
				if err := db.Exec(`INSERT INTO configuration_schemas (id, version_number, schema_json, digest, created_at) VALUES ('app/', 1, '{}', 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "invalid release name",
		},
		{
			name: "invalid application registry id",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "", 0)
				if err := db.Exec(`INSERT INTO configuration_schemas (id, version_number, schema_json, digest, created_at) VALUES ('APP/runtime', 1, '{}', 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "invalid application",
		},
		{
			name: "unowned registry id",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "", 0)
				if err := db.Exec(`INSERT INTO configuration_schemas (id, version_number, schema_json, digest, created_at) VALUES ('ghost/runtime', 1, '{}', 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "not owned",
		},
		{
			name: "non-positive registry version",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "", 0)
				if err := db.Exec(`INSERT INTO configuration_schemas (id, version_number, schema_json, digest, created_at) VALUES ('app/runtime', 0, '{}', 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "schema version must be positive",
		},
		{
			name: "application pin references missing version",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "app/runtime", 2)
				if err := db.Exec(`INSERT INTO configuration_schemas (id, version_number, schema_json, digest, created_at) VALUES ('app/runtime', 1, '{}', 'd', 'now')`).Error; err != nil {
					t.Fatal(err)
				}
			},
			want: "invalid application pin",
		},
		{
			name: "duplicate digest history",
			seed: func(t *testing.T, db *gorm.DB) {
				seedLegacyApplication(t, db, "app", "runtime", "app/runtime", 1)
				for _, version := range []int{1, 2} {
					if err := db.Exec(`INSERT INTO configuration_schemas (id, version_number, schema_json, digest, created_at) VALUES ('app/runtime', ?, '{}', 'same', 'now')`, version).Error; err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "duplicate schema digest",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := legacySchemaOwnershipDB(t)
			test.seed(t, db)
			err := migrateSchemaOwnershipV8(db, 7)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("migration error = %v, want %q", err, test.want)
			}
			// Every validation and rebuild happens in one transaction. A failed
			// upgrade must leave the complete v7 shape available for repair.
			if !db.Migrator().HasColumn("configuration_schemas", "id") ||
				!db.Migrator().HasColumn("applications", "schema_id") ||
				!db.Migrator().HasColumn("configuration_releases", "schema_id") {
				t.Fatal("failed migration partially rewrote the legacy schema")
			}
		})
	}
}

func TestOpenRollsBackSchemaOwnershipWhenV8StampFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	initial, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := initial.CreateApplicationWithSchema(t.Context(),
		domain.Application{Name: "app", ReleaseName: "runtime"},
		domain.ConfigurationSchema{Application: "app", ReleaseName: "runtime", Schema: `{"type":"object"}`, Digest: "digest", Metadata: "{}"},
	); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE applications ADD COLUMN schema_id TEXT NOT NULL DEFAULT ''`,
		`UPDATE applications SET schema_id = 'app/runtime' WHERE name = 'app'`,
		`ALTER TABLE configuration_releases ADD COLUMN schema_id TEXT NOT NULL DEFAULT ''`,
		`DROP TABLE configuration_schemas`,
		`CREATE TABLE configuration_schemas (
			id TEXT NOT NULL, version_number INTEGER NOT NULL, schema_json TEXT NOT NULL,
			digest TEXT NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}',
			created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
			PRIMARY KEY(id, version_number))`,
		`INSERT INTO configuration_schemas
			(id, version_number, schema_json, digest, created_at) VALUES
			('app/runtime', 1, '{"type":"object"}', 'digest', '2026-01-01T00:00:00Z')`,
		`DROP TABLE policies`,
		`DELETE FROM schema_migrations`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (7, '2026-01-01T00:00:00Z')`,
		`CREATE TRIGGER reject_v8_stamp BEFORE INSERT ON schema_migrations
			WHEN NEW.version = 8 BEGIN SELECT RAISE(ABORT, 'refuse-v8-stamp'); END`,
	} {
		if err := initial.db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "refuse-v8-stamp") {
		t.Fatalf("open error = %v, want forced stamp failure", err)
	}
	inspect, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !inspect.Migrator().HasColumn("configuration_schemas", "id") ||
		!inspect.Migrator().HasColumn("applications", "schema_id") ||
		!inspect.Migrator().HasColumn("configuration_releases", "schema_id") {
		t.Fatal("failed v8 stamp committed part of the ownership rewrite")
	}
	var version int
	if err := inspect.Raw(`SELECT MAX(version) FROM schema_migrations`).Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != 7 {
		t.Fatalf("schema version after rollback = %d, want 7", version)
	}
	if inspect.Migrator().HasTable(&policyModel{}) {
		t.Fatal("failed v8 migration committed an unrelated table migration")
	}
}
