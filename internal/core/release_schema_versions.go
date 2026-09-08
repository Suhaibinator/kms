package core

import (
	"context"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// authorizeReleaseList keeps schema discovery within the existing release-list
// permission, including its named and broad-list audit resource identities.
func (s *Service) authorizeReleaseList(ctx context.Context, pr Principal, filter domain.ReleaseFilter) (context.Context, error) {
	ctx = withReleaseAuditFilter(ctx, filter)
	ns, name := filter.Namespace, filter.Name
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return ctx, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if name != "" {
		if err := keyutil.ValidateKey(name); err != nil {
			return ctx, domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
		}
	}
	key := name
	if key == "" {
		key = "releases"
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseList, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: key})
	if err != nil {
		return ctx, err
	}
	return ctx, nil
}

// ListReleaseSchemaVersions discovers registered tracks, including those with
// no releases yet, without granting access to schema documents or contracts.
// Schema zero is implicit and is never a registered schema version.
func (s *Service) ListReleaseSchemaVersions(ctx context.Context, pr Principal, ns domain.NamespaceRef, name string, page storage.ListPage) ([]uint64, string, error) {
	ctx, err := s.authorizeReleaseList(ctx, pr, domain.ReleaseFilter{Namespace: ns, Name: name})
	if err != nil {
		return nil, "", err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return nil, "", err
	}
	schemas, next, err := rs.ListConfigurationSchemas(ctx, ns.App, name, page)
	if err != nil {
		return nil, "", err
	}
	// Schemas are application-owned, so their storage read does not resolve the
	// authorized namespace. Fence its incarnation explicitly before returning.
	if _, err := s.store.GetNamespace(ctx, ns); err != nil {
		return nil, "", err
	}
	versions := make([]uint64, 0, len(schemas))
	for _, schema := range schemas {
		if schema.Version != 0 {
			versions = append(versions, schema.Version)
		}
	}
	return versions, next, nil
}
