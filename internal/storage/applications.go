package storage

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

func contractJSON(fields []domain.ApplicationContractField) (string, error) {
	if fields == nil {
		fields = []domain.ApplicationContractField{}
	}
	b, err := json.Marshal(fields)
	return string(b), err
}

// EnsureApplication creates the minimal application owner needed before an
// environment can be stored. The application-first UI normally creates the
// richer record before adding environments.
func (s *SQLStore) EnsureApplication(ctx context.Context, name, createdBy string) (domain.Application, error) {
	app, err := s.GetApplication(ctx, name)
	if err == nil {
		return app, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Application{}, err
	}
	created := nowUTC()
	m := applicationModel{
		Name: name, ReleaseName: "runtime", ContractJSON: "[]",
		CreatedBy: createdBy, CreatedAt: fmtTime(created), UpdatedAt: fmtTime(created),
	}
	if err := s.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return s.GetApplication(ctx, name)
		}
		return domain.Application{}, err
	}
	return toApplication(m), nil
}

func (s *SQLStore) CreateApplication(ctx context.Context, app domain.Application) (domain.Application, error) {
	contract, err := contractJSON(app.Contract)
	if err != nil {
		return domain.Application{}, err
	}
	created := app.CreatedAt
	if created.IsZero() {
		created = nowUTC()
	}
	updated := app.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	m := applicationModel{
		Name: app.Name, Description: app.Description, ReleaseName: app.ReleaseName,
		SchemaID: app.SchemaID, SchemaVersion: int64(app.SchemaVersion), ContractJSON: contract,
		CreatedBy: app.CreatedBy, CreatedAt: fmtTime(created), UpdatedAt: fmtTime(updated),
	}
	if err := s.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return domain.Application{}, domain.Errorf(domain.ErrAlreadyExists, "application %s", app.Name)
		}
		return domain.Application{}, err
	}
	return toApplication(m), nil
}

func (s *SQLStore) GetApplication(ctx context.Context, name string) (domain.Application, error) {
	var m applicationModel
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Application{}, domain.Errorf(domain.ErrNotFound, "application %s", name)
		}
		return domain.Application{}, err
	}
	app := toApplication(m)
	var environmentCount int64
	if err := s.db.WithContext(ctx).Model(&namespaceModel{}).Where("app = ?", m.Name).Count(&environmentCount).Error; err != nil {
		return domain.Application{}, err
	}
	app.EnvironmentCount = uint64(environmentCount)
	return app, nil
}

// AdoptApplicationContract establishes the first release's shape exactly once.
// Concurrent first releases race on the conditional update; callers always
// receive the winning canonical application and compare their candidate to it.
func (s *SQLStore) AdoptApplicationContract(ctx context.Context, name, schemaID string, schemaVersion uint64, fields []domain.ApplicationContractField) (domain.Application, error) {
	contract, err := contractJSON(fields)
	if err != nil {
		return domain.Application{}, err
	}
	res := s.db.WithContext(ctx).Model(&applicationModel{}).
		Where("name = ? AND contract_json = ?", name, "[]").
		Updates(map[string]any{
			"schema_id": schemaID, "schema_version": schemaVersion,
			"contract_json": contract, "updated_at": fmtTime(nowUTC()),
		})
	if res.Error != nil {
		return domain.Application{}, res.Error
	}
	return s.GetApplication(ctx, name)
}

func (s *SQLStore) UpdateApplication(ctx context.Context, app domain.Application) (domain.Application, error) {
	contract, err := contractJSON(app.Contract)
	if err != nil {
		return domain.Application{}, err
	}
	res := s.db.WithContext(ctx).Model(&applicationModel{}).Where("name = ?", app.Name).Updates(map[string]any{
		"description": app.Description, "release_name": app.ReleaseName,
		"schema_id": app.SchemaID, "schema_version": app.SchemaVersion,
		"contract_json": contract, "updated_at": fmtTime(nowUTC()),
	})
	if res.Error != nil {
		return domain.Application{}, res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Application{}, domain.Errorf(domain.ErrNotFound, "application %s", app.Name)
	}
	return s.GetApplication(ctx, app.Name)
}

func (s *SQLStore) DeleteApplication(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m applicationModel
		if err := tx.Where("name = ?", name).First(&m).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "application %s", name)
			}
			return err
		}
		var count int64
		if err := tx.Model(&namespaceModel{}).Where("app = ?", m.Name).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return domain.Errorf(domain.ErrFailedPrecondition, "application %s still has %d environments", name, count)
		}
		return tx.Delete(&m).Error
	})
}

func (s *SQLStore) ListApplications(ctx context.Context, page ListPage) ([]domain.Application, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	type row struct {
		ID               int64  `gorm:"column:id"`
		Name             string `gorm:"column:name"`
		Description      string `gorm:"column:description"`
		ReleaseName      string `gorm:"column:release_name"`
		SchemaID         string `gorm:"column:schema_id"`
		SchemaVersion    int64  `gorm:"column:schema_version"`
		ContractJSON     string `gorm:"column:contract_json"`
		CreatedBy        string `gorm:"column:created_by"`
		CreatedAt        string `gorm:"column:created_at"`
		UpdatedAt        string `gorm:"column:updated_at"`
		EnvironmentCount int64  `gorm:"column:environment_count"`
	}
	q := s.db.WithContext(ctx).Table("applications AS a").
		Select("a.*, (SELECT COUNT(*) FROM namespaces n WHERE n.app = a.name) AS environment_count")
	if after != "" {
		q = q.Where("a.name > ?", after)
	}
	var rows []row
	if err := q.Order("a.name ASC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeToken(rows[len(rows)-1].Name)
	}
	out := make([]domain.Application, 0, len(rows))
	for _, r := range rows {
		app := toApplication(applicationModel{
			ID: r.ID, Name: r.Name, Description: r.Description, ReleaseName: r.ReleaseName,
			SchemaID: r.SchemaID, SchemaVersion: r.SchemaVersion, ContractJSON: r.ContractJSON,
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
		app.EnvironmentCount = uint64(r.EnvironmentCount)
		out = append(out, app)
	}
	return out, next, nil
}

func (s *SQLStore) ListApplicationNamespaces(ctx context.Context, app string) ([]domain.Namespace, error) {
	var rows []namespaceModel
	if err := s.db.WithContext(ctx).Where("app = ?", app).Order("env ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.Namespace, 0, len(rows))
	for _, row := range rows {
		ns := toNamespace(row)
		var parameterCount int64
		if err := s.db.WithContext(ctx).Model(&parameterModel{}).Where("namespace_id = ?", row.ID).Count(&parameterCount).Error; err != nil {
			return nil, err
		}
		var secretCount int64
		if err := s.db.WithContext(ctx).Model(&secretModel{}).Where("namespace_id = ?", row.ID).Count(&secretCount).Error; err != nil {
			return nil, err
		}
		ns.ParameterCount = uint64(parameterCount)
		ns.SecretCount = uint64(secretCount)
		out = append(out, ns)
	}
	return out, nil
}
