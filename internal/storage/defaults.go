package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

var _ DefaultsApplyStore = (*SQLStore)(nil)

// ApplyDefaults verifies every preview dependency and writes all changed
// parameters in one SQLite transaction. Any stale dependency aborts before a
// version or change-log row can commit.
func (s *SQLStore) ApplyDefaults(ctx context.Context, in DefaultsApplyTransaction) ([]DefaultsAppliedWrite, error) {
	writes := make([]DefaultsAppliedWrite, 0, len(in.Parameters))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := verifyDefaultsApplication(tx, in); err != nil {
			return err
		}
		if err := verifyDefaultsResolution(tx, in); err != nil {
			return err
		}
		if err := verifyDefaultsParameters(tx, in); err != nil {
			return err
		}
		if in.UpdateDefinition {
			contract, err := contractJSON(in.DesiredContract)
			if err != nil {
				return err
			}
			if err := tx.Model(&applicationModel{}).Where("name = ?", in.Namespace.App).Updates(map[string]any{
				"schema_id": in.DesiredSchemaID, "schema_version": in.DesiredSchemaVersion,
				"contract_json": contract, "updated_at": fmtTime(nowUTC()),
			}).Error; err != nil {
				return err
			}
		}
		now := fmtTime(nowUTC())
		for _, parameter := range in.Parameters {
			if !parameter.Write {
				continue
			}
			version, revision, err := putParameterTx(tx,
				domain.Ref{NS: in.Namespace, Key: parameter.Key},
				parameter.Value, parameter.ContentType, "{}", in.CreatedBy, now)
			if err != nil {
				return err
			}
			writes = append(writes, DefaultsAppliedWrite{Alias: parameter.Alias, Version: version, Revision: revision})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return writes, nil
}

func defaultsStale() error {
	return domain.Errorf(domain.ErrAborted, "defaults plan is stale; preview again")
}

func verifyDefaultsApplication(tx *gorm.DB, in DefaultsApplyTransaction) error {
	var app applicationModel
	if err := tx.Where("name = ?", in.Namespace.App).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return defaultsStale()
		}
		return err
	}
	contract, err := contractJSON(in.Contract)
	if err != nil {
		return err
	}
	if app.ReleaseName != in.ReleaseName || app.SchemaID != in.SchemaID ||
		uint64(app.SchemaVersion) != in.SchemaVersion || app.ContractJSON != contract {
		return defaultsStale()
	}
	var ns namespaceModel
	if err := tx.Where("env = ? AND app = ? AND id = ?", in.Namespace.Env, in.Namespace.App, in.NamespaceID).First(&ns).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return defaultsStale()
		}
		return err
	}
	schemaID, schemaVersion := in.SchemaID, in.SchemaVersion
	if in.UpdateDefinition {
		schemaID, schemaVersion = in.DesiredSchemaID, in.DesiredSchemaVersion
	}
	if schemaID != "" {
		var schema configurationSchemaModel
		if err := tx.Where("id = ? AND version_number = ?", schemaID, schemaVersion).First(&schema).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return defaultsStale()
			}
			return err
		}
		if schema.Digest != in.SchemaDigest {
			return defaultsStale()
		}
	}
	return nil
}

func verifyDefaultsResolution(tx *gorm.DB, in DefaultsApplyTransaction) error {
	var namespaces []struct {
		ID  int64  `gorm:"column:id"`
		Env string `gorm:"column:env"`
	}
	if err := tx.Model(&namespaceModel{}).Select("id, env").Where("app = ?", in.Namespace.App).Order("env ASC").Scan(&namespaces).Error; err != nil {
		return err
	}
	if len(namespaces) != len(in.ResolutionState) {
		return defaultsStale()
	}
	for index, namespace := range namespaces {
		expected := in.ResolutionState[index]
		if namespace.ID != expected.NamespaceID || namespace.Env != expected.Environment {
			return defaultsStale()
		}
	}
	for _, expected := range in.ResolutionState {
		var ns namespaceModel
		if err := tx.Where("env = ? AND app = ? AND id = ?", expected.Environment, in.Namespace.App, expected.NamespaceID).First(&ns).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return defaultsStale()
			}
			return err
		}
		var latest int64
		if err := tx.Model(&configurationReleaseModel{}).
			Where("namespace_id = ? AND name = ?", expected.NamespaceID, in.ReleaseName).
			Select("COALESCE(MAX(version_number), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		if uint64(latest) != expected.LatestVersion {
			return defaultsStale()
		}
		var active configurationReleaseLabelModel
		err := tx.Where("namespace_id = ? AND release_name = ? AND label = ?", expected.NamespaceID, in.ReleaseName, domain.LabelCurrent).First(&active).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if expected.ActiveVersion != 0 || expected.ActivationRevision != 0 {
				return defaultsStale()
			}
		case err != nil:
			return err
		default:
			if uint64(active.VersionNumber) != expected.ActiveVersion || uint64(active.ActivationRevision) != expected.ActivationRevision {
				return defaultsStale()
			}
		}
	}
	var resources []DefaultsResourceIdentity
	if err := tx.Raw(`SELECT n.env AS environment, ? AS kind, p.name AS key
			FROM namespaces n JOIN parameters p ON p.namespace_id = n.id WHERE n.app = ?
			UNION ALL
			SELECT n.env AS environment, ? AS kind, s.name AS key
			FROM namespaces n JOIN secrets s ON s.namespace_id = n.id WHERE n.app = ?
			ORDER BY environment, kind, key`, domain.ResourceParameter, in.Namespace.App, domain.ResourceSecret, in.Namespace.App).Scan(&resources).Error; err != nil {
		return err
	}
	if len(resources) != len(in.Resources) {
		return defaultsStale()
	}
	for index := range resources {
		if resources[index] != in.Resources[index] {
			return defaultsStale()
		}
	}
	return nil
}

func verifyDefaultsParameters(tx *gorm.DB, in DefaultsApplyTransaction) error {
	for _, expected := range in.Parameters {
		var row struct {
			Version     int64  `gorm:"column:version_number"`
			Value       string `gorm:"column:value"`
			ContentType string `gorm:"column:content_type"`
		}
		err := tx.Raw(`SELECT pv.version_number, pv.value, pv.content_type
			FROM parameters p
			JOIN parameter_labels l ON l.parameter_id = p.id AND l.label = ?
			JOIN parameter_versions pv ON pv.parameter_id = p.id AND pv.version_number = l.version_number
			WHERE p.namespace_id = ? AND p.name = ?`, domain.LabelCurrent, in.NamespaceID, expected.Key).Scan(&row).Error
		if err != nil {
			return err
		}
		if expected.ExpectedVersion == 0 {
			if row.Version != 0 {
				return defaultsStale()
			}
			continue
		}
		if uint64(row.Version) != expected.ExpectedVersion || row.ContentType != expected.ExpectedContentType {
			return defaultsStale()
		}
		digest := sha256.Sum256([]byte(row.Value))
		if hex.EncodeToString(digest[:]) != expected.ExpectedDigest {
			return defaultsStale()
		}
	}
	return nil
}
