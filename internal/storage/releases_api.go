package storage

import (
	"context"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// ReleaseReference identifies an active/previous release entry that prevents
// a destructive resource operation.
type ReleaseReference struct {
	Namespace      domain.NamespaceRef
	ReleaseName    string
	ReleaseVersion uint64
	Alias          string
}

// ReleaseStore is the persistence contract for configuration releases. It is
// separate from Store so existing test doubles and external Store
// implementations remain source-compatible while the new service is additive.
type ReleaseStore interface {
	CreateConfigurationRelease(ctx context.Context, release domain.ConfigurationRelease) (domain.ConfigurationRelease, error)
	GetConfigurationRelease(ctx context.Context, ns domain.NamespaceRef, name string, version uint64) (domain.ConfigurationRelease, error)
	GetActiveConfigurationRelease(ctx context.Context, ns domain.NamespaceRef, name string) (domain.ActiveConfigurationRelease, error)
	ListConfigurationReleases(ctx context.Context, ns domain.NamespaceRef, name string, page ListPage) ([]domain.ConfigurationReleaseSummary, string, error)
	// CountConfigurationReleases reports how many immutable release versions
	// exist in ns; an empty name counts every release name.
	CountConfigurationReleases(ctx context.Context, ns domain.NamespaceRef, name string) (uint64, error)
	ActivateConfigurationRelease(ctx context.Context, ns domain.NamespaceRef, name string, version uint64, expectedCurrent *uint64) (active domain.ActiveConfigurationRelease, changed bool, err error)
	ConfigurationReleaseActivationExists(ctx context.Context, ns domain.NamespaceRef, name string, version, revision uint64) (bool, error)

	CreateConfigurationSchema(ctx context.Context, schema domain.ConfigurationSchema) (domain.ConfigurationSchema, error)
	GetConfigurationSchema(ctx context.Context, id string, version uint64) (domain.ConfigurationSchema, error)
	ListConfigurationSchemas(ctx context.Context, id string, page ListPage) ([]domain.ConfigurationSchema, string, error)

	UpsertReleaseAcknowledgement(ctx context.Context, ack domain.ReleaseAcknowledgement) error
	ListReleaseAcknowledgements(ctx context.Context, ns domain.NamespaceRef, name string, page ListPage) ([]domain.ReleaseAcknowledgement, string, error)
	SetReleaseInstanceConnected(ctx context.Context, connection domain.ReleaseSubscriberConnection) error
	ResetReleaseInstanceConnections(ctx context.Context, at time.Time) error

	FindProtectedReleaseReference(ctx context.Context, ref domain.Ref, kind string, version uint64) (ReleaseReference, error)
	PruneConfigurationReleases(ctx context.Context, retainDuration time.Duration, retainVersions int) (int, error)
	PruneReleaseAcknowledgements(ctx context.Context, disconnectedBefore time.Time) (int, error)
}
