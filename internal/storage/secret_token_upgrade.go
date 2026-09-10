package storage

import (
	"fmt"

	"gorm.io/gorm"
)

// inspectSupportedBaselineDB accepts the current baseline and the exact baseline-3
// schema without changing operator data. All other baselines remain unsupported.
func inspectSupportedBaselineDB(db *gorm.DB) (bool, error) {
	empty, err := inspectBaselineDB(db)
	if err == nil {
		return empty, nil
	}
	if legacyErr := verifyReleaseBaseline3(db); legacyErr == nil {
		return false, nil
	}
	return false, err
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
	return upgradeReleaseSessionsWithVerifier(db, verifyBaselineDB)
}

func upgradeReleaseSessionsWithVerifier(db *gorm.DB, verify func(*gorm.DB) error) error {
	if err := verifyBaselineDB(db); err == nil {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := verifyReleaseBaseline3(tx); err != nil {
			return err
		}
		if err := tx.AutoMigrate(&releaseSessionModel{}, &releaseTargetDeliveryModel{}); err != nil {
			return err
		}
		if err := tx.Model(&schemaMigrationModel{}).Where("version = 3").Update("version", schemaVersion).Error; err != nil {
			return err
		}
		return verify(tx)
	})
}

// Compare the full physical baseline before permitting any upgrade writes.
func verifyReleaseBaseline3(db *gorm.DB) error {
	actual, err := readBaselineSchema(db)
	if err != nil {
		return err
	}
	current, err := referenceBaselineSchema()
	if err != nil {
		return err
	}
	expected := make([]baselineSchemaObject, 0, len(current))
	for _, obj := range current {
		if obj.TableName != "release_sessions" && obj.TableName != "release_target_deliveries" {
			expected = append(expected, obj)
		}
	}
	if len(actual) != len(expected) {
		return incompatibleBaseline("unsupported baseline 3 schema")
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return incompatibleBaseline("unsupported baseline 3 object %q", actual[i].Name)
		}
	}
	var stamps []schemaMigrationModel
	if err := db.Find(&stamps).Error; err != nil {
		return err
	}
	if len(stamps) != 1 || stamps[0].Version != 3 {
		return incompatibleBaseline("expected baseline 3")
	}
	return nil
}
