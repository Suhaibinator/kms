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
	ReleaseRejectResolutionFailed       = "resolution_failed"
	ReleaseRejectTokenUnavailable       = "token_unavailable"
	ReleaseRejectVersionMismatch        = "version_mismatch"
	ReleaseRejectDigestMismatch         = "digest_mismatch"
	ReleaseRejectPrepareFailed          = "prepare_failed"
	ReleaseRejectConfigContractMismatch = "config_contract_mismatch"
	ReleaseRejectConfigDecodeFailed     = "config_decode_failed"
	ReleaseRejectConfigValidationFailed = "config_validation_failed"
	ReleaseRejectDefaultMismatch        = "default_mismatch"
	ReleaseRejectRestartRequired        = "restart_required"
	ReleaseRejectSuperseded             = "superseded"
	ReleaseRejectActiveCheck            = "active_check_failed"
	ReleaseRejectInternal               = "internal"
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
	ReleaseValidationDefaultMismatch  = "default_mismatch"
	// ReleaseValidationContract reports a candidate whose entries do not match
	// the application contract (alias set, kind or content type).
	ReleaseValidationContract = "contract_mismatch"
)

// Bounded per-alias verdicts returned by VerifyReleaseDefaults. They never
// carry values, digests, or hashes.
const (
	VerifyVerdictMatch                  = "match"
	VerifyVerdictDiffers                = "differs"
	VerifyVerdictMissingInRelease       = "missing_in_release"
	VerifyVerdictUnknownAlias           = "unknown_alias"
	VerifyVerdictSecretAlias            = "secret_alias"
	VerifyVerdictUnsupportedContentType = "unsupported_content_type"
)

// VerifyDefaultsEntry is one caller-supplied alias hash.
type VerifyDefaultsEntry struct {
	Alias       string
	ContentType string
	SHA256      string
}

// VerifyReleaseDefaultsInput is the value-free verification request.
type VerifyReleaseDefaultsInput struct {
	Namespace    NamespaceRef
	ReleaseName  string
	Profile      string
	SchemaSHA256 string
	Entries      []VerifyDefaultsEntry
}

// VerifyEntryVerdict is the bounded verdict for one requested alias.
type VerifyEntryVerdict struct {
	Alias   string
	Verdict string
}

// VerifyDefaultsSummary counts verdicts by kind. Unverified counts release
// parameter aliases the request did not mention.
type VerifyDefaultsSummary struct {
	Match                  int
	Differs                int
	MissingInRelease       int
	UnknownAlias           int
	SecretAlias            int
	UnsupportedContentType int
	Unverified             int
}

// VerifyReleaseDefaultsResult is the value-free verification response.
type VerifyReleaseDefaultsResult struct {
	ReleaseName        string
	ReleaseVersion     uint64
	ActivationRevision uint64
	SchemaMatches      bool
	Entries            []VerifyEntryVerdict
	Summary            VerifyDefaultsSummary
}

// ConfigurationReleaseEntry is one exact resource pin. ParameterDigest is a
// SHA-256 hex digest of the exact stored parameter bytes and is empty for
// secrets. Metadata is immutable non-sensitive metadata captured at creation.
type ConfigurationReleaseEntry struct {
	Alias   string
	Kind    string
	Ref     Ref
	Version uint64
	// ResourceNamespaceID is an internal storage binding for the exact
	// namespace incarnation pinned when the immutable release was created. It
	// is deliberately omitted from every public transport and release digest.
	ResourceNamespaceID int64
	ContentType         string
	Metadata            string
	ParameterDigest     string
	ClientBound         bool
	HasAccessToken      bool
}

// ConfigurationRelease is an immutable namespace-scoped release version.
// Digest excludes values, secret material, timestamps, and movable labels.
type ConfigurationRelease struct {
	Namespace     NamespaceRef
	Name          string
	Version       uint64
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
	SchemaVersion uint64
	Entries       []ReleaseEntrySelector
	Metadata      string
	// RequireFirst is an internal management-flow guard. Storage atomically
	// rejects creation if this release stream already has any version.
	RequireFirst bool
}

type ReleaseValidationError struct {
	Alias         string
	Code          string
	SchemaPointer string
	Message       string
}

// ReleaseValidationFailedError is the bounded, value-free failure returned
// when activation is refused. Transports may expose its sanitized violations
// without leaking parameter or secret values.
type ReleaseValidationFailedError struct {
	violations []ReleaseValidationError
}

func NewReleaseValidationFailedError(violations []ReleaseValidationError) *ReleaseValidationFailedError {
	return &ReleaseValidationFailedError{violations: append([]ReleaseValidationError(nil), violations...)}
}

func (*ReleaseValidationFailedError) Error() string { return "configuration release validation failed" }
func (*ReleaseValidationFailedError) Unwrap() error { return ErrFailedPrecondition }

func (e *ReleaseValidationFailedError) Violations() []ReleaseValidationError {
	if e == nil {
		return nil
	}
	return append([]ReleaseValidationError(nil), e.violations...)
}

// ConfigurationSchema is one immutable JSON Schema version.
type ConfigurationSchema struct {
	Application string
	ReleaseName string
	Version     uint64
	Schema      string
	Digest      string
	Metadata    string
	CreatedBy   string
	CreatedAt   time.Time
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
	// AppliedDivergent reports that the applied generation differs from the
	// application's source-owned defaults; DivergentFieldCount is the number of
	// differing canonical fields. Both are only meaningful for applied state.
	AppliedDivergent    bool
	DivergentFieldCount uint32
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
