package storage

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"sort"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// canonicalSchemaContract deliberately excludes binding keys and values.
func canonicalSchemaContract(fields []domain.ApplicationContractField) (string, error) {
	fields = append([]domain.ApplicationContractField{}, fields...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Alias < fields[j].Alias })
	for i, field := range fields {
		if field.Alias == "" || (i > 0 && fields[i-1].Alias == field.Alias) || (field.Kind != domain.ReleaseEntryParameter && field.Kind != domain.ReleaseEntrySecret) || (field.Kind == domain.ReleaseEntrySecret && field.ContentType != "") {
			return "", domain.Errorf(domain.ErrInvalidArgument, "invalid schema contract field")
		}
	}
	return contractJSON(fields)
}

func schemaAnnotationContract(schema string) ([]domain.ApplicationContractField, bool, error) {
	// Boolean schemas are valid JSON Schemas but cannot carry annotations.
	trimmed := bytes.TrimSpace([]byte(schema))
	if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
		return nil, false, nil
	}
	var root map[string]jsontext.Value
	if err := json.Unmarshal([]byte(schema), &root); err != nil {
		return nil, false, domain.Errorf(domain.ErrInvalidArgument, "invalid JSON schema")
	}
	raw, ok := root["x-kms-contract"]
	if !ok {
		return nil, false, nil
	}
	// An annotation must be an array; null must not masquerade as an unadopted contract.
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false, domain.Errorf(domain.ErrInvalidArgument, "invalid schema contract annotation")
	}
	var fields []domain.ApplicationContractField
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false, domain.Errorf(domain.ErrInvalidArgument, "invalid schema contract annotation")
	}
	canonical, err := canonicalSchemaContract(fields)
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal([]byte(canonical), &fields); err != nil {
		return nil, false, err
	}
	return fields, true, nil
}

func (s *SQLStore) GetConfigurationSchemaByDigest(ctx context.Context, application, releaseName, digest string) (domain.ConfigurationSchema, error) {
	var m configurationSchemaModel
	err := s.db.WithContext(ctx).Where("application_name = ? AND release_name = ? AND digest = ?", application, releaseName, digest).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ConfigurationSchema{}, domain.ErrNotFound
	}
	return schemaFromModel(m), err
}

func (s *SQLStore) AdoptConfigurationSchemaContract(ctx context.Context, application, releaseName string, version uint64, fields []domain.ApplicationContractField) (domain.ConfigurationSchema, error) {
	var out domain.ConfigurationSchema
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = adoptSchemaContractTx(tx, application, releaseName, version, fields)
		return err
	})
	return out, err
}

func adoptSchemaContractTx(tx *gorm.DB, application, releaseName string, version uint64, fields []domain.ApplicationContractField) (domain.ConfigurationSchema, error) {
	canonical, err := canonicalSchemaContract(fields)
	if err != nil {
		return domain.ConfigurationSchema{}, err
	}
	var app applicationModel
	if err := tx.Where("name = ?", application).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ConfigurationSchema{}, domain.ErrNotFound
		}
		return domain.ConfigurationSchema{}, err
	}
	if app.ArchivedAt != nil || app.ReleaseName != releaseName {
		return domain.ConfigurationSchema{}, domain.ErrFailedPrecondition
	}
	if version == 0 {
		row := schemaFreeContractModel{ApplicationName: application, ReleaseName: releaseName, ContractJSON: canonical}
		if err := tx.Omit(clause.Associations).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return domain.ConfigurationSchema{}, err
		}
		if err := tx.Where("application_name = ? AND release_name = ?", application, releaseName).First(&row).Error; err != nil {
			return domain.ConfigurationSchema{}, err
		}
		if row.ContractJSON != canonical {
			return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrFailedPrecondition, "schema contract is immutable")
		}
		var adopted []domain.ApplicationContractField
		if err := json.Unmarshal([]byte(canonical), &adopted); err != nil {
			return domain.ConfigurationSchema{}, err
		}
		return domain.ConfigurationSchema{Application: application, ReleaseName: releaseName, Contract: adopted}, nil
	}
	var row configurationSchemaModel
	q := tx.Where("application_name = ? AND release_name = ? AND version_number = ?", application, releaseName, version)
	if err := q.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ConfigurationSchema{}, domain.ErrNotFound
		}
		return domain.ConfigurationSchema{}, err
	}
	if row.ContractJSON != nil && *row.ContractJSON != canonical {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrFailedPrecondition, "schema contract is immutable")
	}
	if row.ContractJSON == nil {
		if err := tx.Model(&configurationSchemaModel{}).Where("application_name = ? AND release_name = ? AND version_number = ? AND contract_json IS NULL", application, releaseName, version).Update("contract_json", canonical).Error; err != nil {
			return domain.ConfigurationSchema{}, err
		}
		row.ContractJSON = &canonical
	}
	return schemaFromModel(row), nil
}

// GetConfigurationSchemaContract selects exactly one schema, including the schema-free track.
// Nil means unadopted; a nonnil empty slice is an established empty contract.
func (s *SQLStore) GetConfigurationSchemaContract(ctx context.Context, application, releaseName string, version uint64) ([]domain.ApplicationContractField, error) {
	if version != 0 {
		schema, err := s.GetConfigurationSchema(ctx, application, releaseName, version)
		return schema.Contract, err
	}
	var app applicationModel
	err := s.db.WithContext(ctx).Where("name = ? AND release_name = ?", application, releaseName).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var row schemaFreeContractModel
	err = s.db.WithContext(ctx).Where("application_name = ? AND release_name = ?", application, releaseName).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var fields []domain.ApplicationContractField
	if err := json.Unmarshal([]byte(row.ContractJSON), &fields); err != nil {
		return nil, err
	}
	return fields, nil
}
