package domain

import "regexp"

// Readiness state machine vocabulary (console plan §3.1). The backend computes
// every state and finding; the frontend only renders them. Finding params
// carry names and numbers only — never parameter or secret values.

// Application status, in precedence order.
const (
	AppStatusBlocked   = "blocked"
	AppStatusSetup     = "setup"
	AppStatusAttention = "attention"
	AppStatusReady     = "ready"
)

// Environment status, in precedence order (first match wins).
const (
	EnvStatusBlocked    = "blocked"
	EnvStatusEmpty      = "empty"
	EnvStatusIncomplete = "incomplete"
	EnvStatusUnreleased = "unreleased"
	EnvStatusDegraded   = "degraded"
	EnvStatusRolling    = "rolling"
	EnvStatusDrift      = "drift"
	EnvStatusReady      = "ready"
)

const (
	ValuesStateEmpty      = "empty"
	ValuesStateIncomplete = "incomplete"
	ValuesStateComplete   = "complete"
)

const (
	ReleaseStateNone    = "none"
	ReleaseStateActive  = "active"
	ReleaseStateDrift   = "drift"
	ReleaseStateBlocked = "blocked"
)

const (
	RolloutStateNoSubscribers = "no_subscribers"
	RolloutStateApplied       = "applied"
	RolloutStateRolling       = "rolling"
	RolloutStateDegraded      = "degraded"
	RolloutStateStale         = "stale"
)

const (
	FindingBlocking = "blocking"
	FindingWarning  = "warning"
	FindingInfo     = "info"
)

// Finding codes. Copy lives in frontend/lib/readiness.ts keyed by code.
const (
	FindingNoEnvironments             = "no_environments"
	FindingContractEmpty              = "contract_empty"
	FindingSchemaUnpinned             = "schema_unpinned"
	FindingSchemaMissing              = "schema_missing"
	FindingSchemaPropertyMissingAlias = "schema_property_missing_alias"
	FindingSchemaRequiredMissingAlias = "schema_required_missing_alias"
	FindingAliasNotInSchema           = "alias_not_in_schema"
	FindingContractTypeMismatch       = "contract_type_mismatch"
	FindingContractReleaseMismatch    = "contract_release_mismatch"
	FindingReleasePinStale            = "release_pin_stale"
	FindingResourceMissing            = "resource_missing"
	FindingKindMismatch               = "kind_mismatch"
	FindingContentTypeMismatch        = "content_type_mismatch"
	FindingSecretUnreadable           = "secret_unreadable"
	FindingSecretTokenRequired        = "secret_token_required"
	FindingNoActiveRelease            = "no_active_release"
	FindingUnreleasedChanges          = "unreleased_changes"
	FindingAliasNotInRelease          = "alias_not_in_release"
	FindingNoSubscribers              = "no_subscribers"
	FindingSubscriberOtherRelease     = "subscriber_other_release"
	FindingInstanceRejected           = "instance_rejected"
	FindingInstancePending            = "instance_pending"
	FindingInstanceStale              = "instance_stale"
	FindingRolledBack                 = "rolled_back"
	FindingPreviousUnavailable        = "previous_unavailable"
	FindingProduction                 = "production"
	FindingInsecureListener           = "insecure_listener"
)

// FindingScope narrows a finding to an environment, alias and/or instance.
type FindingScope struct {
	Env      string
	Alias    string
	Instance string
}

// Finding is one computed readiness observation. Params values are strings or
// numbers only.
type Finding struct {
	Code     string
	Severity string
	Scope    FindingScope
	Params   map[string]any
}

// productionEnvironmentRE matches `prod`, `prod-*` and `production` but not
// `reproduction` or `non-prod`. Mirrored by PRODUCTION_ENVIRONMENT in
// frontend/lib/readiness.ts.
var productionEnvironmentRE = regexp.MustCompile(`^prod(-|$)|^production$`)

// IsProductionEnvironment reports whether env names a production environment.
func IsProductionEnvironment(env string) bool { return productionEnvironmentRE.MatchString(env) }
