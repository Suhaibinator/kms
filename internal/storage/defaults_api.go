package storage

import (
	"context"

	"github.com/Suhaibinator/kms/internal/domain"
)

// DefaultsApplyStore is an additive capability because defaults import is a
// management-plane operation. Data-plane stores and existing test doubles do
// not need to implement it.
type DefaultsApplyStore interface {
	ApplyDefaults(ctx context.Context, in DefaultsApplyTransaction) ([]DefaultsAppliedWrite, error)
}

type DefaultsResolutionState struct {
	Environment        string
	NamespaceID        int64
	ActiveVersion      uint64
	ActivationRevision uint64
	LatestVersion      uint64
}

type DefaultsResourceIdentity struct {
	Environment string
	Kind        string
	Key         string
}

type DefaultsParameterExpectation struct {
	Alias               string
	Key                 string
	Value               string
	ContentType         string
	ExpectedVersion     uint64
	ExpectedDigest      string
	ExpectedContentType string
	Write               bool
}

type DefaultsApplyTransaction struct {
	Namespace            domain.NamespaceRef
	NamespaceID          int64
	ReleaseName          string
	SchemaID             string
	SchemaVersion        uint64
	SchemaDigest         string
	Contract             []domain.ApplicationContractField
	UpdateDefinition     bool
	DesiredSchemaID      string
	DesiredSchemaVersion uint64
	DesiredContract      []domain.ApplicationContractField
	ResolutionState      []DefaultsResolutionState
	Resources            []DefaultsResourceIdentity
	Parameters           []DefaultsParameterExpectation
	CreatedBy            string
}

type DefaultsAppliedWrite struct {
	Alias    string
	Version  uint64
	Revision uint64
}
