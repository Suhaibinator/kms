package core

import (
	"context"
	"errors"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func applicationTrack(app domain.Application, ns domain.NamespaceRef) domain.ReleaseTrack {
	return domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: app.SchemaVersion}
}
func trackFilter(track domain.ReleaseTrack) domain.ReleaseFilter {
	return domain.ReleaseFilter{Namespace: track.Namespace, Name: track.Name, SchemaVersion: &track.SchemaVersion}
}

// selectApplicationTrack returns a request-local definition. Selection never
// rewrites the application's configured default or another track's contract.
func (s *Service) selectApplicationTrack(ctx context.Context, app domain.Application, selected *uint64) (domain.Application, error) {
	rs, err := s.releaseStore()
	if err != nil {
		return app, err
	}
	version := app.SchemaVersion
	if selected != nil {
		version = *selected
	} else {
		schemas, _, err := rs.ListConfigurationSchemas(ctx, app.Name, app.ReleaseName, storage.ListPage{Limit: 1})
		if err != nil {
			return app, err
		}
		if len(schemas) > 0 {
			version = schemas[0].Version
		}
	}
	schema, err := rs.GetConfigurationSchema(ctx, app.Name, app.ReleaseName, version)
	if err != nil && !(version == 0 && errors.Is(err, domain.ErrNotFound)) {
		return app, err
	}
	if len(schema.Contract) > 0 {
		app.Contract = schema.Contract
	} else if version != app.SchemaVersion {
		app.Contract = nil
	}
	app.SchemaVersion = version
	return app, nil
}
