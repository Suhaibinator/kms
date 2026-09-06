package storage

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// inspectSupportedBaselineDB never changes operator data. Version 1 is accepted
// only when its exact physical schema and all token columns are safe to remove.
func inspectSupportedBaselineDB(db *gorm.DB) (bool, error) {
	empty, currentErr := inspectBaselineDB(db)
	if currentErr == nil {
		return empty, nil
	}
	if err := verifyTokenFreeLegacyBaseline(db); err != nil {
		return false, err
	}
	return false, nil
}

func verifyTokenFreeLegacyBaseline(db *gorm.DB) error {
	actual, err := readBaselineSchema(db)
	if err != nil {
		return incompatibleBaseline("cannot inspect schema: %v", err)
	}
	expected, err := referenceSchema(true)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return incompatibleBaseline("unsupported physical schema")
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return incompatibleBaseline("unsupported physical schema at %s %q", actual[i].Type, actual[i].Name)
		}
	}
	var stamps []schemaMigrationModel
	if err := db.Find(&stamps).Error; err != nil {
		return err
	}
	if len(stamps) != 1 || stamps[0].Version != 1 {
		return incompatibleBaseline("unsupported schema version")
	}
	var count int64
	if err := db.Raw(`SELECT (SELECT COUNT(*) FROM secrets WHERE length(access_token_hash) > 0) + (SELECT COUNT(*) FROM secret_versions WHERE has_access_token <> 0)`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("cannot upgrade KMS database: per-secret access tokens are in use; database was not upgraded")
	}
	return nil
}

func upgradeSecretTokenSchema(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := verifyBaselineDB(tx); err == nil {
			return nil
		}
		// Recheck after acquiring the write lock, so a concurrent old writer cannot
		// add token protection between inspection and the schema upgrade.
		if err := verifyTokenFreeLegacyBaseline(tx); err != nil {
			return err
		}
		for _, ddl := range []string{
			"ALTER TABLE secrets DROP COLUMN access_token_hash",
			"ALTER TABLE secret_versions DROP COLUMN has_access_token",
		} {
			if err := tx.Exec(ddl).Error; err != nil {
				return fmt.Errorf("remove unused secret-token column: %w", err)
			}
		}
		if err := tx.Model(&schemaMigrationModel{}).Where("version = 1").Updates(map[string]any{"version": schemaVersion, "applied_at": fmtTime(time.Now())}).Error; err != nil {
			return err
		}
		return verifyBaselineDB(tx)
	})
}
