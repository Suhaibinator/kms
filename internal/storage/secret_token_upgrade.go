package storage

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// inspectSupportedBaselineDB accepts the current baseline and exact baselines 3 and 4
// schema without changing operator data. All other baselines remain unsupported.
func inspectSupportedBaselineDB(db *gorm.DB) (bool, error) {
	empty, err := inspectBaselineDB(db)
	if err == nil {
		return empty, nil
	}
	if legacyErr := verifyReleaseBaseline3(db); legacyErr == nil {
		return false, nil
	}
	if legacyErr := verifyReleaseBaseline4(db); legacyErr == nil {
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
		baseline4 := false
		if err := verifyReleaseBaseline4(tx); err != nil {
			if err := verifyReleaseBaseline3(tx); err != nil {
				return err
			}
		} else {
			baseline4 = true
		}
		var sessionColumns []string
		if baseline4 {
			columns, err := tx.Migrator().ColumnTypes(&releaseSessionModel{})
			if err != nil {
				return err
			}
			for _, column := range columns {
				sessionColumns = append(sessionColumns, "`"+column.Name()+"`")
			}
			// Rebuild using canonical DDL: SQLite ALTER ADD COLUMN rewrites SQL
			// differently from fresh creation, which would fail exact verification.
			if err := tx.Exec("CREATE TABLE release_sessions_baseline_upgrade AS SELECT * FROM release_sessions").Error; err != nil {
				return err
			}
			if err := tx.Migrator().DropTable(&releaseSessionModel{}); err != nil {
				return err
			}
		}
		if err := tx.AutoMigrate(&releaseSessionModel{}, &releaseTargetDeliveryModel{}, &releaseSessionEventModel{}); err != nil {
			return err
		}
		if baseline4 {
			columns := strings.Join(sessionColumns, ",")
			if err := tx.Exec("INSERT INTO release_sessions (" + columns + ") SELECT " + columns + " FROM release_sessions_baseline_upgrade").Error; err != nil {
				return err
			}
			// Pre-ledger event identities cannot be verified. Fence those sequences
			// so replay cannot manufacture new historical applied evidence.
			if err := tx.Exec("UPDATE release_sessions SET pruned_ack_sequence = last_ack_sequence").Error; err != nil {
				return err
			}
			if err := tx.Exec("DROP TABLE release_sessions_baseline_upgrade").Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&schemaMigrationModel{}).Where("version IN ?", []int{3, 4}).Update("version", schemaVersion).Error; err != nil {
			return err
		}
		return verify(tx)
	})
}

// Compare the full physical baseline before permitting any upgrade writes.
func verifyReleaseBaseline3(db *gorm.DB) error {
	return verifyReleaseBaselineVersion(db, 3)
}

func verifyReleaseBaseline4(db *gorm.DB) error {
	return verifyReleaseBaselineVersion(db, 4)
}

func releaseBaselineSchema(version int) ([]baselineSchemaObject, error) {
	current, err := referenceBaselineSchema()
	if err != nil {
		return nil, err
	}
	expected := make([]baselineSchemaObject, 0, len(current))
	for _, obj := range current {
		if obj.TableName == "release_session_events" || (version == 3 && (obj.TableName == "release_sessions" || obj.TableName == "release_target_deliveries")) {
			continue
		}
		if obj.Name == "release_sessions" && obj.Type == "table" {
			// Baseline 4 has the same session columns except these baseline-5 additions.
			for _, column := range []string{"last_applied_sequence", "pruned_ack_sequence"} {
				obj.SQL = strings.ReplaceAll(obj.SQL, ",`"+column+"` integer", "")
			}
			for _, column := range []string{"diagnostic", "client_timestamp"} {
				obj.SQL = strings.ReplaceAll(obj.SQL, ",`"+column+"` text", "")
			}
		}
		expected = append(expected, obj)
	}
	return expected, nil
}

func verifyReleaseBaselineVersion(db *gorm.DB, version int) error {
	actual, err := readBaselineSchema(db)
	if err != nil {
		return err
	}
	expected, err := releaseBaselineSchema(version)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return incompatibleBaseline("unsupported baseline %d schema", version)
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return incompatibleBaseline("unsupported baseline %d object %q", version, actual[i].Name)
		}
	}
	var stamps []schemaMigrationModel
	if err := db.Find(&stamps).Error; err != nil {
		return err
	}
	if len(stamps) != 1 || stamps[0].Version != version {
		return incompatibleBaseline("expected baseline %d", version)
	}
	return nil
}
