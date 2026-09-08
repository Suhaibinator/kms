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
	contract, err := rs.GetConfigurationSchemaContract(ctx, app.Name, app.ReleaseName, version)
	if err != nil {
		return app, err
	}
	app.Contract = contract
	app.SchemaVersion = version
	return app, nil
}

// selectArtifactApplicationTrack pins artifact-driven work to the embedded
// digest, independently of newer registrations and the application's default.
// Schema-free artifacts have no digest and must explicitly select schema zero.
func (s *Service) selectArtifactApplicationTrack(ctx context.Context, app domain.Application, selected *uint64, digest string) (domain.Application, error) {
	if digest == "" {
		if selected == nil {
			return app, domain.Errorf(domain.ErrInvalidArgument, "schema-free defaults require explicit schema_version 0")
		}
		if *selected != 0 {
			return app, domain.Errorf(domain.ErrFailedPrecondition, "defaults do not match the selected schema digest")
		}
		return s.selectApplicationTrack(ctx, app, selected)
	}
	if selected != nil && *selected == 0 {
		return app, domain.Errorf(domain.ErrFailedPrecondition, "schema-free defaults must not claim a registered schema digest")
	}
	rs, err := s.releaseStore()
	if err != nil {
		return app, err
	}
	var schema domain.ConfigurationSchema
	if selected == nil {
		schema, err = rs.GetConfigurationSchemaByDigest(ctx, app.Name, app.ReleaseName, digest)
	} else {
		schema, err = rs.GetConfigurationSchema(ctx, app.Name, app.ReleaseName, *selected)
	}
	if errors.Is(err, domain.ErrNotFound) {
		return app, domain.Errorf(domain.ErrFailedPrecondition, "generated schema is not registered for %s/%s; run schema upload before applying defaults or creating a release", app.Name, app.ReleaseName)
	}
	if err != nil {
		return app, err
	}
	if schema.Digest != digest {
		return app, domain.Errorf(domain.ErrFailedPrecondition, "defaults do not match the selected schema digest")
	}
	app.SchemaVersion, app.Contract = schema.Version, schema.Contract
	return app, nil
}

func contractsEqual(a, b []domain.ApplicationContractField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
