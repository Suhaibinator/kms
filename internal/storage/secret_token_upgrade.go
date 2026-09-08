package storage

import (
	"fmt"

	"gorm.io/gorm"
)

// inspectSupportedBaselineDB accepts only the current baseline without changing
// operator data. Older baselines require a fresh database.
func inspectSupportedBaselineDB(db *gorm.DB) (bool, error) {
	return inspectBaselineDB(db)
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
	return verifyBaselineDB(db)
}
