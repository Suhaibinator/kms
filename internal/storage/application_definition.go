package storage

import (
	"context"
	"errors"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
)

type applicationDefinitionContextKey struct{}
type applicationDefinitionExpectation struct {
	name          string
	releaseName   string
	schemaVersion uint64
	updatedAt     string
	createdAt     string
}

// WithApplicationDefinitionExpectation binds a metadata update to the definition
// core observed. SQLStore checks it inside the write transaction, so metadata
// updates cannot restore a default that another request has just changed.
func WithApplicationDefinitionExpectation(ctx context.Context, app domain.Application) context.Context {
	return context.WithValue(ctx, applicationDefinitionContextKey{}, applicationDefinitionExpectation{
		name: app.Name, releaseName: app.ReleaseName, schemaVersion: app.SchemaVersion,
		updatedAt: fmtTime(app.UpdatedAt), createdAt: fmtTime(app.CreatedAt),
	})
}

func verifyApplicationDefinitionExpectation(ctx context.Context, tx *gorm.DB, name string) error {
	expected, ok := ctx.Value(applicationDefinitionContextKey{}).(applicationDefinitionExpectation)
	if !ok {
		return nil
	}
	var app applicationModel
	if err := tx.Where("name = ?", name).First(&app).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return domain.Errorf(domain.ErrAborted, "application definition changed; reload and retry")
	}
	if expected.name != name || expected.releaseName != app.ReleaseName || int64(expected.schemaVersion) != app.SchemaVersion || expected.updatedAt != app.UpdatedAt || expected.createdAt != app.CreatedAt || app.ArchivedAt != nil {
		return domain.Errorf(domain.ErrAborted, "application definition changed; reload and retry")
	}
	return nil
}
