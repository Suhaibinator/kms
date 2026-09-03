package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// migrateSchemaOwnershipV8 replaces caller-constructed schema IDs with the
// application and release coordinates they encoded. It intentionally refuses
// to guess: a malformed, unowned, shared, or duplicated lineage must be
// repaired with the older binary before the database can be upgraded.
func migrateSchemaOwnershipV8(db *gorm.DB, current int) error {
	if current >= 8 {
		return nil
	}
	if !db.Migrator().HasTable("configuration_schemas") {
		// A truly fresh database has none of the ownership tables yet; ordinary
		// AutoMigrate below creates the v8 shape. Any older stamped or partially
		// populated database missing its registry is corrupt and must not be
		// silently promoted past a migration whose invariants cannot be checked.
		fresh := current == 0 && !db.Migrator().HasTable("applications") && !db.Migrator().HasTable("configuration_releases")
		if fresh {
			return nil
		}
		for _, table := range []string{"applications", "configuration_releases"} {
			if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, "schema_id") {
				continue
			}
			var count int64
			if err := db.Raw("SELECT COUNT(*) FROM " + table + " WHERE schema_id <> '' OR schema_version <> 0").Scan(&count).Error; err != nil {
				return fmt.Errorf("migrate schema ownership: inspect %s without registry: %w", table, err)
			}
			if count != 0 {
				return fmt.Errorf("migrate schema ownership: legacy configuration_schemas table is missing but %s contains %d schema pins", table, count)
			}
		}
		// Release/schema functionality was additive in older versions. A database
		// with no registry and no pins has no ownership history to backfill. Still
		// remove any legacy ownership columns transactionally so a successfully
		// stamped v8 database always has the clean structured shape.
		return db.Transaction(func(tx *gorm.DB) error {
			for _, table := range []string{"applications", "configuration_releases"} {
				if !tx.Migrator().HasTable(table) || !tx.Migrator().HasColumn(table, "schema_id") {
					continue
				}
				if err := tx.Exec("ALTER TABLE " + table + " DROP COLUMN schema_id").Error; err != nil {
					return fmt.Errorf("migrate schema ownership: drop unused %s.schema_id: %w", table, err)
				}
			}
			if tx.Migrator().HasTable("applications") && !tx.Migrator().HasColumn("applications", "archived_at") {
				if err := tx.Exec(`ALTER TABLE applications ADD COLUMN archived_at text`).Error; err != nil {
					return fmt.Errorf("migrate schema ownership: add applications.archived_at: %w", err)
				}
			}
			if tx.Migrator().HasTable("applications") && !tx.Migrator().HasColumn("applications", "archived_by") {
				if err := tx.Exec(`ALTER TABLE applications ADD COLUMN archived_by text NOT NULL DEFAULT ''`).Error; err != nil {
					return fmt.Errorf("migrate schema ownership: add applications.archived_by: %w", err)
				}
			}
			if tx.Migrator().HasTable("applications") {
				if err := tx.Exec(`CREATE TABLE configuration_schemas (
					application_name text NOT NULL,
					release_name text NOT NULL,
					version_number integer NOT NULL,
					schema_json text NOT NULL,
					digest text NOT NULL,
					metadata_json text NOT NULL DEFAULT '{}',
					created_by text NOT NULL DEFAULT '',
					created_at text NOT NULL,
					PRIMARY KEY (application_name, release_name, version_number),
					CONSTRAINT fk_configuration_schemas_application
						FOREIGN KEY (application_name) REFERENCES applications(name) ON DELETE RESTRICT,
					CONSTRAINT idx_schema_digest UNIQUE (application_name, release_name, digest)
				)`).Error; err != nil {
					return fmt.Errorf("migrate schema ownership: create empty structured registry: %w", err)
				}
			}
			return nil
		})
	}
	if !db.Migrator().HasColumn("configuration_schemas", "id") {
		structured := db.Migrator().HasColumn("configuration_schemas", "application_name") &&
			db.Migrator().HasColumn("configuration_schemas", "release_name") &&
			db.Migrator().HasColumn("configuration_schemas", "version_number") &&
			db.Migrator().HasTable("applications") &&
			db.Migrator().HasTable("configuration_releases")
		legacyPinsRemain := db.Migrator().HasColumn("applications", "schema_id") ||
			db.Migrator().HasColumn("configuration_releases", "schema_id")
		if structured && !legacyPinsRemain {
			// The physical v8 ownership rewrite already completed and only the
			// schema-version stamp is stale (for example after restoring a backup).
			return nil
		}
		return fmt.Errorf("migrate schema ownership: legacy configuration_schemas.id column is missing (database is partially migrated)")
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var mismatch string
		var schemaIDs []string
		if err := tx.Raw(`SELECT DISTINCT id FROM configuration_schemas ORDER BY id`).Scan(&schemaIDs).Error; err != nil {
			return fmt.Errorf("validate schema registry coordinates: %w", err)
		}
		for _, id := range schemaIDs {
			if strings.Count(id, "/") != 1 {
				return fmt.Errorf("migrate schema ownership: schema %q is malformed; expected application/release", id)
			}
			parts := strings.SplitN(id, "/", 2)
			if err := keyutil.ValidateApp(parts[0]); err != nil {
				return fmt.Errorf("migrate schema ownership: schema %q has invalid application: %v", id, err)
			}
			if err := keyutil.ValidateKey(parts[1]); err != nil {
				return fmt.Errorf("migrate schema ownership: schema %q has invalid release name: %v", id, err)
			}
		}
		mismatch = ""
		if err := tx.Raw(`SELECT id || '@' || version_number
			FROM configuration_schemas
			WHERE version_number <= 0
			ORDER BY id, version_number LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate schema registry versions: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: schema version must be positive: %s", mismatch)
		}
		if err := tx.Raw(`SELECT name || ': schema_id and schema_version must either both be set or both be empty'
			FROM applications
			WHERE (schema_id = '' AND schema_version <> 0)
			   OR (schema_id <> '' AND schema_version = 0)
			ORDER BY name LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate application schema pins: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: invalid application pin: %s", mismatch)
		}

		mismatch = ""
		if err := tx.Raw(`SELECT name || ': expected ' || name || '/' || release_name || ', found ' || schema_id
			FROM applications
			WHERE schema_id <> '' AND schema_id <> name || '/' || release_name
			ORDER BY name LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate application schema ownership: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: application pin mismatch: %s", mismatch)
		}

		mismatch = ""
		if err := tx.Raw(`SELECT a.name || ': schema version ' || a.schema_version || ' does not exist in ' || a.schema_id
			FROM applications a
			LEFT JOIN configuration_schemas cs
			  ON cs.id = a.schema_id AND cs.version_number = a.schema_version
			WHERE a.schema_version <> 0 AND cs.id IS NULL
			ORDER BY a.name LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate application schema versions: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: invalid application pin: %s", mismatch)
		}

		mismatch = ""
		if err := tx.Raw(`SELECT n.env || '/' || n.app || '/' || r.name || ': schema_id and schema_version must either both be set or both be empty'
			FROM configuration_releases r
			JOIN namespaces n ON n.id = r.namespace_id
			WHERE (r.schema_id = '' AND r.schema_version <> 0)
			   OR (r.schema_id <> '' AND r.schema_version = 0)
			ORDER BY n.app, n.env, r.name, r.version_number LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate release schema pins: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: invalid release pin: %s", mismatch)
		}

		mismatch = ""
		if err := tx.Raw(`SELECT n.env || '/' || n.app || '/' || r.name || ': expected ' || n.app || '/' || r.name || ', found ' || r.schema_id
			FROM configuration_releases r
			JOIN namespaces n ON n.id = r.namespace_id
			WHERE r.schema_id <> '' AND r.schema_id <> n.app || '/' || r.name
			ORDER BY n.app, n.env, r.name, r.version_number LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate release schema ownership: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: release pin mismatch: %s", mismatch)
		}

		mismatch = ""
		if err := tx.Raw(`SELECT n.env || '/' || n.app || '/' || r.name || ': schema version ' || r.schema_version || ' does not exist in ' || r.schema_id
			FROM configuration_releases r
			JOIN namespaces n ON n.id = r.namespace_id
			LEFT JOIN configuration_schemas cs
			  ON cs.id = r.schema_id AND cs.version_number = r.schema_version
			WHERE r.schema_version <> 0 AND cs.id IS NULL
			ORDER BY n.app, n.env, r.name, r.version_number LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate release schema versions: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: invalid release pin: %s", mismatch)
		}

		mismatch = ""
		if err := tx.Raw(`SELECT cs.id
			FROM configuration_schemas cs
			LEFT JOIN applications a
			  ON a.name = substr(cs.id, 1, instr(cs.id, '/') - 1)
			 AND a.release_name = substr(cs.id, instr(cs.id, '/') + 1)
			WHERE instr(cs.id, '/') <= 1
			   OR instr(substr(cs.id, instr(cs.id, '/') + 1), '/') <> 0
			   OR a.name IS NULL
			ORDER BY cs.id LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate schema registry ownership: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: schema %q is not owned by an application's canonical release", mismatch)
		}

		mismatch = ""
		if err := tx.Raw(`SELECT id || '@' || MIN(version_number)
			FROM configuration_schemas
			GROUP BY id, digest HAVING COUNT(*) > 1
			ORDER BY id LIMIT 1`).Scan(&mismatch).Error; err != nil {
			return fmt.Errorf("validate duplicate schema digests: %w", err)
		}
		if mismatch != "" {
			return fmt.Errorf("migrate schema ownership: duplicate schema digest in lineage %s", mismatch)
		}

		statements := []string{
			`CREATE TABLE configuration_schemas_v8 (
				application_name text NOT NULL,
				release_name text NOT NULL,
				version_number integer NOT NULL,
				schema_json text NOT NULL,
				digest text NOT NULL,
				metadata_json text NOT NULL DEFAULT '{}',
				created_by text NOT NULL DEFAULT '',
				created_at text NOT NULL,
				PRIMARY KEY (application_name, release_name, version_number),
				CONSTRAINT fk_configuration_schemas_application
					FOREIGN KEY (application_name) REFERENCES applications(name) ON DELETE RESTRICT,
				CONSTRAINT idx_schema_digest UNIQUE (application_name, release_name, digest)
			)`,
			`INSERT INTO configuration_schemas_v8 (
				application_name, release_name, version_number, schema_json, digest,
				metadata_json, created_by, created_at
			) SELECT substr(id, 1, instr(id, '/') - 1),
				substr(id, instr(id, '/') + 1), version_number, schema_json, digest,
				metadata_json, created_by, created_at
			FROM configuration_schemas`,
			`DROP TABLE configuration_schemas`,
			`ALTER TABLE configuration_schemas_v8 RENAME TO configuration_schemas`,
			`ALTER TABLE applications DROP COLUMN schema_id`,
			`ALTER TABLE configuration_releases DROP COLUMN schema_id`,
		}
		if !tx.Migrator().HasColumn("applications", "archived_at") {
			statements = append(statements, `ALTER TABLE applications ADD COLUMN archived_at text`)
		}
		if !tx.Migrator().HasColumn("applications", "archived_by") {
			statements = append(statements, `ALTER TABLE applications ADD COLUMN archived_by text NOT NULL DEFAULT ''`)
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("migrate schema ownership: %w", err)
			}
		}
		if err := recomputeReleaseDigestsV8(tx); err != nil {
			return fmt.Errorf("migrate schema ownership release digests: %w", err)
		}
		return nil
	})
}

func recomputeReleaseDigestsV8(tx *gorm.DB) error {
	var releases []configurationReleaseModel
	if err := tx.Order("id ASC").Find(&releases).Error; err != nil {
		return err
	}
	for _, release := range releases {
		var namespace namespaceModel
		if err := tx.Where("id = ?", release.NamespaceID).First(&namespace).Error; err != nil {
			return err
		}
		var entries []configurationReleaseEntryModel
		if err := tx.Where("release_id = ?", release.ID).Order("alias ASC").Find(&entries).Error; err != nil {
			return err
		}
		digest, err := releaseDigestV8(namespace, release, entries)
		if err != nil {
			return err
		}
		if err := tx.Model(&configurationReleaseModel{}).Where("id = ?", release.ID).Update("digest", digest).Error; err != nil {
			return err
		}
	}
	return nil
}

func releaseDigestV8(namespace namespaceModel, release configurationReleaseModel, entries []configurationReleaseEntryModel) (string, error) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Alias < entries[j].Alias })
	pb := &kmsv1.ConfigurationRelease{
		Namespace: &kmsv1.NamespaceRef{Env: namespace.Env, App: namespace.App},
		Name:      release.Name, SchemaVersion: uint64(release.SchemaVersion), MetadataJson: release.MetadataJSON,
	}
	for _, entry := range entries {
		pb.Entries = append(pb.Entries, &kmsv1.ConfigurationReleaseEntry{
			Alias: entry.Alias, Kind: entry.Kind,
			Ref:     &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: entry.ResourceEnv, App: entry.ResourceApp}, Key: entry.ResourceKey},
			Version: uint64(entry.ResourceVersion), ContentType: entry.ContentType,
			MetadataJson: entry.MetadataJSON, ParameterDigest: entry.ParameterDigest,
			ClientBound: i2b(entry.ClientBound), HasAccessToken: i2b(entry.HasAccessToken),
		})
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(pb)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
