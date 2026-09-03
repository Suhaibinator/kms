package domain

import "time"

// ApplicationConfigurationCell is the current value or secret metadata for
// one environment in the application dashboard. SecretValue is never present;
// Value is populated only for parameters.
type ApplicationConfigurationCell struct {
	Environment    string
	Present        bool
	Value          string
	ContentType    string
	Version        uint64
	ClientBound    bool
	HasAccessToken bool
}

// ApplicationConfigurationRow compares one physical configuration key across
// every environment of an application.
type ApplicationConfigurationRow struct {
	Key   string
	Kind  string
	Cells map[string]ApplicationConfigurationCell
}

type ApplicationDashboard struct {
	Application  Application
	Environments []Namespace
	Rows         []ApplicationConfigurationRow
}

type ApplicationParameterWriteResult struct {
	Environment string `json:"environment"`
	Version     uint64 `json:"version"`
	Revision    uint64 `json:"revision"`
	Error       string `json:"error,omitempty"`
}

// Defaults import is intentionally parameter-only. The artifact contract may
// name secrets so operators can see which ones still need to be provisioned,
// but no request or result type has a field capable of carrying secret bytes.
const (
	DefaultsStatusCreate    = "create"
	DefaultsStatusUnchanged = "unchanged"
	DefaultsStatusUpdate    = "update"
	DefaultsStatusBlocked   = "blocked"
)

type DefaultsApplyInput struct {
	Namespace        NamespaceRef
	Artifact         []byte
	Overwrite        bool
	UpdateDefinition bool
	Execute          bool
	PlanDigest       string
}

type DefaultsApplyEntry struct {
	Alias          string
	Key            string
	ContentType    string
	Status         string
	CurrentVersion uint64
	AppliedVersion uint64
	Revision       uint64
}

type DefaultsApplyResult struct {
	Profile           string
	SchemaSHA256      string
	ArtifactDigest    string
	PlanDigest        string
	Entries           []DefaultsApplyEntry
	MissingSecrets    []string
	Executed          bool
	DefinitionChanged bool
	DefinitionUpdated bool
}

// ApplicationReleaseCreateInput previews or executes creation of the
// application's canonical release from one generated defaults artifact.
// Execute never activates the release; the exact preview digest is required.
type ApplicationReleaseCreateInput struct {
	Namespace  NamespaceRef
	Artifact   []byte
	Metadata   string
	Execute    bool
	PlanDigest string
}

const (
	ApplicationReleaseSourceGeneratedDefault      = "generated_default"
	ApplicationReleaseSourceCarriedActiveSecret   = "carried_active_secret"
	ApplicationReleaseSourceResolvedCurrentSecret = "resolved_current_secret"
)

type ApplicationReleasePlanEntry struct {
	Alias       string
	Kind        string
	Ref         Ref
	FromVersion uint64
	ToVersion   uint64
	Source      string
}

type ApplicationReleaseCreateResult struct {
	Profile            string
	PlanDigest         string
	Valid              bool
	Executed           bool
	Created            bool
	ReleaseName        string
	SchemaVersion      uint64
	BaseReleaseVersion uint64
	Entries            []ApplicationReleasePlanEntry
	MissingSecrets     []string
	Validation         []ReleaseValidationError
	Release            *ConfigurationRelease
}

// --- console read models ----------------------------------------------------

// OverviewValue is one contract alias resolved against an environment. Key is
// empty when the alias resolved to nothing.
type OverviewValue struct {
	Alias          string
	Kind           string
	Key            string
	Present        bool
	ContentType    string
	CurrentVersion uint64
	PinnedVersion  uint64
	ClientBound    bool
}

// OverviewActiveRelease is the active release of one environment plus the
// activation facts the console shows next to it.
type OverviewActiveRelease struct {
	Release            ConfigurationRelease
	ActivationRevision uint64
	PreviousVersion    uint64
	IsRolledBack       bool
}

// SubscriberInstance is the effective lifecycle row for one
// (identity, client, instance) triple, folded from the per-state
// acknowledgement rows.
type SubscriberInstance struct {
	Identity           string
	ClientName         string
	InstanceID         string
	State              string
	ReleaseVersion     uint64
	ActivationRevision uint64
	RejectionCategory  string
	Diagnostic         string
	Connected          bool
	ServerTimestamp    time.Time
	// AppliedDivergent reports an applied generation that differs from the
	// application's source-owned defaults (see ReleaseAcknowledgement).
	AppliedDivergent    bool
	DivergentFieldCount uint32
}

// RolloutSummary aggregates subscriber instances against the current
// activation revision.
type RolloutSummary struct {
	Total          int
	Connected      int
	AppliedCurrent int
	// AppliedDivergent counts instances applied at the current revision whose
	// generation diverges from source defaults. Divergence is a warning, not a
	// rollout failure.
	AppliedDivergent  int
	Rejected          int
	Pending           int
	Stale             int
	OtherReleaseNames []string
	RejectedInstances []SubscriberInstance
	Truncated         bool
}

type EnvironmentOverview struct {
	Namespace     Namespace
	Production    bool
	Status        string
	ValuesState   string
	ReleaseState  string
	RolloutState  string
	Values        []OverviewValue
	Active        *OverviewActiveRelease
	LatestVersion uint64
	ReleaseCount  uint64
	Rollout       RolloutSummary
	Findings      []Finding
}

type ApplicationOverview struct {
	Application  Application
	Status       string
	Findings     []Finding
	Environments []EnvironmentOverview
	Rows         []ApplicationConfigurationRow
	SchemaJSON   string
}

type FleetEnvironment struct {
	Env        string
	Status     string
	Production bool
}

type FleetApplication struct {
	Application  Application
	Status       string
	Environments []FleetEnvironment
}

// --- ship -------------------------------------------------------------------

// ShipChange is one row of a ship request: Value writes a new parameter
// version; Version/Label pins an existing one without writing. Secrets accept
// Version/Label only.
type ShipChange struct {
	Alias       string
	Value       *string
	ContentType string
	Version     uint64
	Label       string
}

type ShipInput struct {
	Application           string
	Environment           string
	Changes               []ShipChange
	Metadata              string
	DryRun                bool
	ExpectedActiveVersion *uint64
	RequestID             string
}

const (
	ShipEntryEdited   = "edited"
	ShipEntryPinned   = "pinned"
	ShipEntryIncluded = "included"
	ShipEntryMissing  = "missing"

	ShipStatusPreview                    = "preview"
	ShipStatusActivated                  = "activated"
	ShipStatusRejected                   = "rejected"
	ShipStatusReleaseCreatedNotActivated = "release_created_not_activated"
	ShipStatusConflict                   = "conflict"
)

type ShipPreviewEntry struct {
	Alias       string
	Kind        string
	Key         string
	FromVersion uint64
	ToVersion   uint64
	Change      string
}

type ShipPreview struct {
	BaseVersion   uint64
	ReleaseName   string
	SchemaVersion uint64
	Entries       []ShipPreviewEntry
	Validation    []ReleaseValidationError
	Warnings      []Finding
}

type ShipParameterWrite struct {
	Alias    string
	Key      string
	Version  uint64
	Revision uint64
}

type ShipActivation struct {
	ActivationRevision uint64
	PreviousVersion    uint64
	Changed            bool
}

type ShipError struct {
	Code             string
	Message          string
	ValidationErrors []ReleaseValidationError
	CurrentVersion   uint64
}

type ShipResult struct {
	Status     string
	Preview    ShipPreview
	Parameters []ShipParameterWrite
	Release    *ConfigurationRelease
	Activation *ShipActivation
	Error      *ShipError
}

// --- clone ------------------------------------------------------------------

type CloneEnvironmentInput struct {
	Application string
	SourceEnv   string
	TargetEnv   string
	CopyValues  bool
	AuthMethods []AuthMethod
	Description string
}

const (
	CloneItemCopied          = "copied"
	CloneItemNeedsValue      = "needs_value"
	CloneItemExists          = "exists"
	CloneItemMissingInSource = "missing_in_source"
	CloneItemError           = "error"
)

type CloneEnvironmentItem struct {
	Alias         string
	Key           string
	Kind          string
	Action        string
	SourceVersion uint64
	TargetVersion uint64
	Error         string
}

type CloneEnvironmentResult struct {
	Namespace        Namespace
	NamespaceCreated bool
	Items            []CloneEnvironmentItem
	NeedsValue       []string
}

// --- rollback ---------------------------------------------------------------

type RollbackResult struct {
	Active         ActiveConfigurationRelease
	RolledBackFrom uint64
	Changed        bool
}

// SubscriberStreamSnapshot is one frame of the live rollout stream.
type SubscriberStreamSnapshot struct {
	Summary         RolloutSummary
	Subscribers     []ReleaseAcknowledgement
	CurrentRevision uint64
	ServerTime      time.Time
}
