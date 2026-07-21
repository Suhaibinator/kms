package domain

import "time"

// Configuration-release resource kinds and lifecycle values.
const (
	ResourceConfigurationRelease = "configuration_release"
	ResourceConfigurationSchema  = "configuration_schema"

	ReleaseEntryParameter = ResourceParameter
	ReleaseEntrySecret    = ResourceSecret

	ReleaseStateReceived = "received"
	ReleaseStatePrepared = "prepared"
	ReleaseStateApplied  = "applied"
	ReleaseStateRejected = "rejected"
)

// Bounded rejection categories used by SDK acknowledgements and metrics.
const (
	ReleaseRejectResolutionFailed = "resolution_failed"
	ReleaseRejectTokenUnavailable = "token_unavailable"
	ReleaseRejectVersionMismatch  = "version_mismatch"
	ReleaseRejectDigestMismatch   = "digest_mismatch"
	ReleaseRejectPrepareFailed    = "prepare_failed"
	ReleaseRejectSuperseded       = "superseded"
	ReleaseRejectActiveCheck      = "active_check_failed"
	ReleaseRejectInternal         = "internal"
)

// Release validation error codes. Messages accompanying these codes must be
// sanitized and must never render resource values.
const (
	ReleaseValidationNotFound         = "not_found"
	ReleaseValidationPermissionDenied = "permission_denied"
	ReleaseValidationUnreadable       = "unreadable"
	ReleaseValidationContentType      = "content_type"
	ReleaseValidationMalformedJSON    = "malformed_json"
	ReleaseValidationSchema           = "schema_violation"
	ReleaseValidationDigest           = "digest_mismatch"
)

// ConfigurationReleaseEntry is one exact resource pin. ParameterDigest is a
// SHA-256 hex digest of the exact stored parameter bytes and is empty for
// secrets. Metadata is immutable non-sensitive metadata captured at creation.
type ConfigurationReleaseEntry struct {
	Alias           string
	Kind            string
	Ref             Ref
	Version         uint64
	ContentType     string
	Metadata        string
	ParameterDigest string
	ClientBound     bool
	HasAccessToken  bool
}

// ConfigurationRelease is an immutable namespace-scoped release version.
// Digest excludes values, secret material, timestamps, and movable labels.
type ConfigurationRelease struct {
	Namespace     NamespaceRef
	Name          string
	Version       uint64
	SchemaID      string
	SchemaVersion uint64
	Entries       []ConfigurationReleaseEntry
	Digest        string
	Metadata      string
	CreatedBy     string
	CreatedAt     time.Time
}

// ActiveConfigurationRelease couples an immutable release with the global
// changelog revision at which it became active.
type ActiveConfigurationRelease struct {
	Release            ConfigurationRelease
	ActivationRevision uint64
	PreviousVersion    uint64
}

type ConfigurationReleaseSummary struct {
	Release            ConfigurationRelease
	Current            bool
	Previous           bool
	ActivationRevision uint64
}

type ReleaseEntrySelector struct {
	Alias   string
	Kind    string
	Ref     Ref
	Version uint64
	Label   string
}

type CreateConfigurationReleaseInput struct {
	Namespace     NamespaceRef
	Name          string
	SchemaID      string
	SchemaVersion uint64
	Entries       []ReleaseEntrySelector
	Metadata      string
}

type ReleaseValidationError struct {
	Alias         string
	Code          string
	SchemaPointer string
	Message       string
}

// ConfigurationSchema is one immutable JSON Schema version.
type ConfigurationSchema struct {
	ID        string
	Version   uint64
	Schema    string
	Digest    string
	Metadata  string
	CreatedBy string
	CreatedAt time.Time
}

// ReleaseAcknowledgement records one application lifecycle state. InstanceID
// is stable for one process lifetime and reused across stream reconnects.
type ReleaseAcknowledgement struct {
	Namespace          NamespaceRef
	ReleaseName        string
	ReleaseVersion     uint64
	ActivationRevision uint64
	ClientName         string
	InstanceID         string
	Identity           string
	// ConnectionID is the server-issued generation for the stream that carried
	// this acknowledgement. It is internal transport state, never accepted from
	// the wire or exposed in subscriber responses.
	ConnectionID      string
	State             string
	RejectionCategory string
	Diagnostic        string
	ClientTimestamp   time.Time
	ServerTimestamp   time.Time
	Connected         bool
}

// ReleaseSubscriberConnection is transport liveness for one process instance.
// It is separate from application lifecycle acknowledgements so registration
// never fabricates a received/prepared/applied state.
type ReleaseSubscriberConnection struct {
	Namespace       NamespaceRef
	ReleaseName     string
	ClientName      string
	InstanceID      string
	Identity        string
	ConnectionID    string
	Connected       bool
	ConnectedAt     time.Time
	DisconnectedAt  time.Time
	ServerTimestamp time.Time
}
