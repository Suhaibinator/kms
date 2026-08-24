package httpserver

import (
	"github.com/Suhaibinator/kms/internal/domain"
)

// Console read-model DTOs (plan §3.2). Field names mirror the "Console
// aggregates" section of frontend/lib/types.ts exactly.

type findingScopeDTO struct {
	Env      string `json:"env,omitempty"`
	Alias    string `json:"alias,omitempty"`
	Instance string `json:"instance,omitempty"`
}

type findingDTO struct {
	Code     string          `json:"code"`
	Severity string          `json:"severity"`
	Scope    findingScopeDTO `json:"scope"`
	Params   map[string]any  `json:"params"`
}

func toFindingDTOs(in []domain.Finding) []findingDTO {
	out := make([]findingDTO, 0, len(in))
	for _, f := range in {
		params := f.Params
		if params == nil {
			params = map[string]any{}
		}
		out = append(out, findingDTO{Code: f.Code, Severity: f.Severity, Scope: findingScopeDTO{Env: f.Scope.Env, Alias: f.Scope.Alias, Instance: f.Scope.Instance}, Params: params})
	}
	return out
}

type overviewValueDTO struct {
	Alias          string `json:"alias"`
	Kind           string `json:"kind"`
	Key            string `json:"key,omitempty"`
	Present        bool   `json:"present"`
	ContentType    string `json:"content_type,omitempty"`
	CurrentVersion uint64 `json:"current_version,omitempty"`
	PinnedVersion  uint64 `json:"pinned_version,omitempty"`
	ClientBound    bool   `json:"client_bound,omitempty"`
}

type overviewActiveReleaseDTO struct {
	Name               string            `json:"name"`
	Version            uint64            `json:"version"`
	ActivationRevision uint64            `json:"activation_revision"`
	PreviousVersion    uint64            `json:"previous_version"`
	CreatedBy          string            `json:"created_by"`
	CreatedAtUnixMS    int64             `json:"created_at_unix_ms"`
	IsRolledBack       bool              `json:"is_rolled_back"`
	SchemaID           string            `json:"schema_id"`
	SchemaVersion      uint64            `json:"schema_version"`
	Digest             string            `json:"digest"`
	Entries            []releaseEntryDTO `json:"entries"`
}

type subscriberInstanceDTO struct {
	Identity              string `json:"identity"`
	ClientName            string `json:"client_name"`
	InstanceID            string `json:"instance_id"`
	State                 string `json:"state"`
	ReleaseVersion        uint64 `json:"release_version"`
	ActivationRevision    uint64 `json:"activation_revision"`
	RejectionCategory     string `json:"rejection_category"`
	Diagnostic            string `json:"diagnostic"`
	Connected             bool   `json:"connected"`
	ServerTimestampUnixMS int64  `json:"server_timestamp_unix_ms"`
}

type rolloutDTO struct {
	Total             int                     `json:"total"`
	Connected         int                     `json:"connected"`
	AppliedCurrent    int                     `json:"applied_current"`
	Rejected          int                     `json:"rejected"`
	Pending           int                     `json:"pending"`
	Stale             int                     `json:"stale"`
	OtherReleaseNames []string                `json:"other_release_names"`
	RejectedInstances []subscriberInstanceDTO `json:"rejected_instances"`
	Truncated         bool                    `json:"truncated"`
}

func toRolloutDTO(r domain.RolloutSummary) rolloutDTO {
	names := r.OtherReleaseNames
	if names == nil {
		names = []string{}
	}
	rejected := make([]subscriberInstanceDTO, 0, len(r.RejectedInstances))
	for _, inst := range r.RejectedInstances {
		rejected = append(rejected, subscriberInstanceDTO{
			Identity: inst.Identity, ClientName: inst.ClientName, InstanceID: inst.InstanceID, State: inst.State,
			ReleaseVersion: inst.ReleaseVersion, ActivationRevision: inst.ActivationRevision,
			RejectionCategory: inst.RejectionCategory, Diagnostic: inst.Diagnostic, Connected: inst.Connected,
			ServerTimestampUnixMS: unixMS(inst.ServerTimestamp),
		})
	}
	return rolloutDTO{Total: r.Total, Connected: r.Connected, AppliedCurrent: r.AppliedCurrent, Rejected: r.Rejected, Pending: r.Pending, Stale: r.Stale, OtherReleaseNames: names, RejectedInstances: rejected, Truncated: r.Truncated}
}

type environmentReleaseDTO struct {
	Active        *overviewActiveReleaseDTO `json:"active,omitempty"`
	LatestVersion uint64                    `json:"latest_version"`
	ReleaseCount  uint64                    `json:"release_count"`
}

type environmentOverviewDTO struct {
	Namespace    namespaceDTO          `json:"namespace"`
	Production   bool                  `json:"production"`
	Status       string                `json:"status"`
	ValuesState  string                `json:"values_state"`
	ReleaseState string                `json:"release_state"`
	RolloutState string                `json:"rollout_state"`
	Values       []overviewValueDTO    `json:"values"`
	Release      environmentReleaseDTO `json:"release"`
	Rollout      rolloutDTO            `json:"rollout"`
	Findings     []findingDTO          `json:"findings"`
}

func toEnvironmentOverviewDTO(e domain.EnvironmentOverview) environmentOverviewDTO {
	values := make([]overviewValueDTO, 0, len(e.Values))
	for _, v := range e.Values {
		values = append(values, overviewValueDTO{Alias: v.Alias, Kind: v.Kind, Key: v.Key, Present: v.Present, ContentType: v.ContentType, CurrentVersion: v.CurrentVersion, PinnedVersion: v.PinnedVersion, ClientBound: v.ClientBound})
	}
	release := environmentReleaseDTO{LatestVersion: e.LatestVersion, ReleaseCount: e.ReleaseCount}
	if e.Active != nil {
		rel := toReleaseDTO(e.Active.Release)
		release.Active = &overviewActiveReleaseDTO{
			Name: rel.Name, Version: rel.Version, ActivationRevision: e.Active.ActivationRevision, PreviousVersion: e.Active.PreviousVersion,
			CreatedBy: rel.CreatedBy, CreatedAtUnixMS: rel.CreatedAtUnixMS, IsRolledBack: e.Active.IsRolledBack,
			SchemaID: rel.SchemaID, SchemaVersion: rel.SchemaVersion, Digest: rel.Digest, Entries: rel.Entries,
		}
	}
	return environmentOverviewDTO{
		Namespace: toNamespaceDTO(e.Namespace), Production: e.Production, Status: e.Status,
		ValuesState: e.ValuesState, ReleaseState: e.ReleaseState, RolloutState: e.RolloutState,
		Values: values, Release: release, Rollout: toRolloutDTO(e.Rollout), Findings: toFindingDTOs(e.Findings),
	}
}

type applicationOverviewDTO struct {
	Application  applicationDTO           `json:"application"`
	Status       string                   `json:"status"`
	Findings     []findingDTO             `json:"findings"`
	Environments []environmentOverviewDTO `json:"environments"`
	Rows         []applicationRowDTO      `json:"rows"`
	SchemaJSON   string                   `json:"schema_json,omitempty"`
}

func toApplicationOverviewDTO(o domain.ApplicationOverview) applicationOverviewDTO {
	environments := make([]environmentOverviewDTO, 0, len(o.Environments))
	for _, env := range o.Environments {
		environments = append(environments, toEnvironmentOverviewDTO(env))
	}
	return applicationOverviewDTO{Application: toApplicationDTO(o.Application), Status: o.Status, Findings: toFindingDTOs(o.Findings), Environments: environments, Rows: toApplicationRowDTOs(o.Rows), SchemaJSON: o.SchemaJSON}
}

type fleetEnvironmentDTO struct {
	Env        string `json:"env"`
	Status     string `json:"status"`
	Production bool   `json:"production"`
}

type fleetApplicationDTO struct {
	Application  applicationDTO        `json:"application"`
	Status       string                `json:"status"`
	Environments []fleetEnvironmentDTO `json:"environments"`
}

func toFleetDTO(apps []domain.FleetApplication) map[string]any {
	out := make([]fleetApplicationDTO, 0, len(apps))
	for _, app := range apps {
		envs := make([]fleetEnvironmentDTO, 0, len(app.Environments))
		for _, env := range app.Environments {
			envs = append(envs, fleetEnvironmentDTO{Env: env.Env, Status: env.Status, Production: env.Production})
		}
		out = append(out, fleetApplicationDTO{Application: toApplicationDTO(app.Application), Status: app.Status, Environments: envs})
	}
	return map[string]any{"applications": out}
}

// --- ship -------------------------------------------------------------------

type shipChangeDTO struct {
	Alias       string  `json:"alias"`
	Value       *string `json:"value"`
	ContentType string  `json:"content_type"`
	Version     uint64  `json:"version"`
	Label       string  `json:"label"`
}

type shipRequestDTO struct {
	Application           string          `json:"application"`
	Environment           string          `json:"environment"`
	Changes               []shipChangeDTO `json:"changes"`
	MetadataJSON          string          `json:"metadata_json"`
	DryRun                bool            `json:"dry_run"`
	ExpectedActiveVersion *uint64         `json:"expected_active_version"`
	RequestID             string          `json:"request_id"`
}

func (d shipRequestDTO) toDomain() domain.ShipInput {
	changes := make([]domain.ShipChange, 0, len(d.Changes))
	for _, c := range d.Changes {
		changes = append(changes, domain.ShipChange{Alias: c.Alias, Value: c.Value, ContentType: c.ContentType, Version: c.Version, Label: c.Label})
	}
	return domain.ShipInput{Application: d.Application, Environment: d.Environment, Changes: changes, Metadata: d.MetadataJSON, DryRun: d.DryRun, ExpectedActiveVersion: d.ExpectedActiveVersion, RequestID: d.RequestID}
}

type shipPreviewEntryDTO struct {
	Alias       string `json:"alias"`
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	FromVersion uint64 `json:"from_version,omitempty"`
	ToVersion   uint64 `json:"to_version,omitempty"`
	Change      string `json:"change"`
}

type validateReleaseResponseDTO struct {
	Valid  bool                        `json:"valid"`
	Errors []releaseValidationErrorDTO `json:"errors"`
}

type shipPreviewDTO struct {
	BaseVersion   uint64                     `json:"base_version"`
	ReleaseName   string                     `json:"release_name"`
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion uint64                     `json:"schema_version"`
	Entries       []shipPreviewEntryDTO      `json:"entries"`
	Validation    validateReleaseResponseDTO `json:"validation"`
	Warnings      []findingDTO               `json:"warnings"`
}

type shipParameterDTO struct {
	Alias    string `json:"alias"`
	Key      string `json:"key"`
	Version  uint64 `json:"version"`
	Revision uint64 `json:"revision"`
}

type shipReleaseDTO struct {
	Name    string `json:"name"`
	Version uint64 `json:"version"`
	Digest  string `json:"digest"`
}

type shipActivationDTO struct {
	ActivationRevision uint64 `json:"activation_revision"`
	PreviousVersion    uint64 `json:"previous_version"`
	Changed            bool   `json:"changed"`
}

type shipErrorDTO struct {
	Code             string                      `json:"code"`
	Message          string                      `json:"message"`
	ValidationErrors []releaseValidationErrorDTO `json:"validation_errors,omitempty"`
	CurrentVersion   uint64                      `json:"current_version,omitempty"`
}

type shipResultDTO struct {
	Status     string             `json:"status"`
	Preview    shipPreviewDTO     `json:"preview"`
	Parameters []shipParameterDTO `json:"parameters"`
	Release    *shipReleaseDTO    `json:"release,omitempty"`
	Activation *shipActivationDTO `json:"activation,omitempty"`
	Error      *shipErrorDTO      `json:"error,omitempty"`
}

func toShipResultDTO(r domain.ShipResult) shipResultDTO {
	entries := make([]shipPreviewEntryDTO, 0, len(r.Preview.Entries))
	for _, e := range r.Preview.Entries {
		entries = append(entries, shipPreviewEntryDTO{Alias: e.Alias, Kind: e.Kind, Key: e.Key, FromVersion: e.FromVersion, ToVersion: e.ToVersion, Change: e.Change})
	}
	validation := releaseValidationErrorDTOs(r.Preview.Validation)
	out := shipResultDTO{
		Status: r.Status,
		Preview: shipPreviewDTO{
			BaseVersion: r.Preview.BaseVersion, ReleaseName: r.Preview.ReleaseName, SchemaID: r.Preview.SchemaID, SchemaVersion: r.Preview.SchemaVersion,
			Entries: entries, Validation: validateReleaseResponseDTO{Valid: len(validation) == 0, Errors: validation}, Warnings: toFindingDTOs(r.Preview.Warnings),
		},
		Parameters: make([]shipParameterDTO, 0, len(r.Parameters)),
	}
	for _, p := range r.Parameters {
		out.Parameters = append(out.Parameters, shipParameterDTO{Alias: p.Alias, Key: p.Key, Version: p.Version, Revision: p.Revision})
	}
	if r.Release != nil {
		out.Release = &shipReleaseDTO{Name: r.Release.Name, Version: r.Release.Version, Digest: r.Release.Digest}
	}
	if r.Activation != nil {
		out.Activation = &shipActivationDTO{ActivationRevision: r.Activation.ActivationRevision, PreviousVersion: r.Activation.PreviousVersion, Changed: r.Activation.Changed}
	}
	if r.Error != nil {
		errDTO := &shipErrorDTO{Code: r.Error.Code, Message: r.Error.Message, CurrentVersion: r.Error.CurrentVersion}
		if len(r.Error.ValidationErrors) > 0 {
			errDTO.ValidationErrors = releaseValidationErrorDTOs(r.Error.ValidationErrors)
		}
		out.Error = errDTO
	}
	return out
}

// --- clone ------------------------------------------------------------------

type cloneEnvironmentRequestDTO struct {
	Application string   `json:"application"`
	SourceEnv   string   `json:"source_env"`
	TargetEnv   string   `json:"target_env"`
	CopyValues  bool     `json:"copy_values"`
	AuthMethods []string `json:"auth_methods"`
	Description string   `json:"description"`
}

type cloneEnvironmentItemDTO struct {
	Alias         string `json:"alias"`
	Key           string `json:"key"`
	Kind          string `json:"kind"`
	Action        string `json:"action"`
	SourceVersion uint64 `json:"source_version,omitempty"`
	TargetVersion uint64 `json:"target_version,omitempty"`
	Error         string `json:"error,omitempty"`
}

type cloneEnvironmentResponseDTO struct {
	Namespace        namespaceDTO              `json:"namespace"`
	NamespaceCreated bool                      `json:"namespace_created"`
	Items            []cloneEnvironmentItemDTO `json:"items"`
	NeedsValue       []string                  `json:"needs_value"`
}

func toCloneEnvironmentDTO(r domain.CloneEnvironmentResult) cloneEnvironmentResponseDTO {
	items := make([]cloneEnvironmentItemDTO, 0, len(r.Items))
	for _, item := range r.Items {
		items = append(items, cloneEnvironmentItemDTO{Alias: item.Alias, Key: item.Key, Kind: item.Kind, Action: item.Action, SourceVersion: item.SourceVersion, TargetVersion: item.TargetVersion, Error: item.Error})
	}
	needs := r.NeedsValue
	if needs == nil {
		needs = []string{}
	}
	return cloneEnvironmentResponseDTO{Namespace: toNamespaceDTO(r.Namespace), NamespaceCreated: r.NamespaceCreated, Items: items, NeedsValue: needs}
}

// --- live rollout stream ----------------------------------------------------

type subscriberStreamSnapshotDTO struct {
	Summary          rolloutDTO             `json:"summary"`
	Subscribers      []releaseSubscriberDTO `json:"subscribers"`
	CurrentRevision  uint64                 `json:"current_revision"`
	ServerTimeUnixMS int64                  `json:"server_time_unix_ms"`
}

func toSubscriberStreamSnapshotDTO(s domain.SubscriberStreamSnapshot) subscriberStreamSnapshotDTO {
	subscribers := make([]releaseSubscriberDTO, 0, len(s.Subscribers))
	for _, ack := range s.Subscribers {
		subscribers = append(subscribers, toReleaseSubscriberDTO(ack))
	}
	return subscriberStreamSnapshotDTO{Summary: toRolloutDTO(s.Summary), Subscribers: subscribers, CurrentRevision: s.CurrentRevision, ServerTimeUnixMS: unixMS(s.ServerTime)}
}
