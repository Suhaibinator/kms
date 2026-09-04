package core

import (
	"encoding/json/v2"
	"sort"
	"strings"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// Readiness is computed by pure functions over data the
// overview endpoint has already fetched, so the rules are unit-testable
// without storage and the frontend never re-derives them.

// staleDisconnectAfter is how long a disconnected instance that has not
// applied the current activation may be missing before it counts as stale
// rather than pending.
const staleDisconnectAfter = 90 * time.Second

// maxRolloutInstanceFindings caps per-instance findings and the
// rejected_instances list so a large fleet cannot bloat one response.
const maxRolloutInstanceFindings = 50

// secretCurrentState is what readiness needs to know about the `current`
// version of a secret in one environment.
type secretCurrentState struct {
	State   string
	Expired bool
}

// environmentReadinessInput is everything computeEnvironmentReadiness looks
// at for one environment. Every field is optional except App and Namespace;
// missing data degrades to the corresponding "unknown" finding.
type environmentReadinessInput struct {
	App       domain.Application
	Namespace domain.Namespace
	// Rows spans every environment of the application (matrix rows); only the
	// cells of Namespace.Env are consulted here.
	Rows []domain.ApplicationConfigurationRow
	// Secrets keys secret state by key within Namespace.Env for the contract's
	// secret aliases.
	Secrets map[string]secretCurrentState
	// Refs is resolveContractRefs for this environment.
	Refs   map[string]domain.Ref
	Active *domain.ActiveConfigurationRelease
	// ActiveValidation is validateReleaseEntries over the active release;
	// alias-bearing errors mean a pin no longer resolves.
	ActiveValidation []domain.ReleaseValidationError
	LatestVersion    uint64
	ReleaseCount     uint64
	// Acks are the acknowledgement rows of the namespace across every release
	// name (bounded by the caller).
	Acks          []domain.ReleaseAcknowledgement
	SchemaMissing bool
	Now           time.Time
}

func finding(code, severity string, scope domain.FindingScope, params map[string]any) domain.Finding {
	if params == nil {
		params = map[string]any{}
	}
	return domain.Finding{Code: code, Severity: severity, Scope: scope, Params: params}
}

// computeEnvironmentReadiness applies the per-environment rules of §3.1.
func computeEnvironmentReadiness(in environmentReadinessInput) domain.EnvironmentOverview {
	env := in.Namespace.Env
	here := domain.NamespaceRef{Env: env, App: in.App.Name}
	out := domain.EnvironmentOverview{
		Namespace:     in.Namespace,
		Production:    domain.IsProductionEnvironment(env),
		Values:        make([]domain.OverviewValue, 0, len(in.App.Contract)),
		LatestVersion: in.LatestVersion,
		ReleaseCount:  in.ReleaseCount,
		Findings:      []domain.Finding{},
	}
	envScope := domain.FindingScope{Env: env}
	aliasScope := func(alias string) domain.FindingScope { return domain.FindingScope{Env: env, Alias: alias} }
	add := func(f domain.Finding) { out.Findings = append(out.Findings, f) }

	if out.Production {
		add(finding(domain.FindingProduction, domain.FindingInfo, envScope, nil))
	}

	rowFor := func(kind, key string) (domain.ApplicationConfigurationCell, bool) {
		for _, row := range in.Rows {
			if row.Kind == kind && row.Key == key {
				cell, ok := row.Cells[env]
				return cell, ok && cell.Present
			}
		}
		return domain.ApplicationConfigurationCell{}, false
	}
	anyRows := false
	for _, row := range in.Rows {
		if cell, ok := row.Cells[env]; ok && cell.Present {
			anyRows = true
			break
		}
	}

	activeEntries := map[string]domain.ConfigurationReleaseEntry{}
	if in.Active != nil {
		for _, entry := range in.Active.Release.Entries {
			activeEntries[entry.Alias] = entry
		}
	}

	// --- values ---------------------------------------------------------------
	incomplete := false
	for _, field := range in.App.Contract {
		value := domain.OverviewValue{Alias: field.Alias, Kind: field.Kind}
		if entry, ok := activeEntries[field.Alias]; ok {
			value.PinnedVersion = entry.Version
		}
		ref, resolved := in.Refs[field.Alias]
		if !resolved {
			incomplete = true
			add(finding(domain.FindingResourceMissing, domain.FindingBlocking, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "kind": field.Kind}))
			out.Values = append(out.Values, value)
			continue
		}
		value.Key = ref.Key
		if ref.NS != here {
			// A cross-namespace pin: rows only cover this application, so the
			// active pin is the evidence of presence.
			value.Present = value.PinnedVersion > 0
			if !value.Present {
				incomplete = true
				add(finding(domain.FindingResourceMissing, domain.FindingBlocking, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "kind": field.Kind}))
			}
			out.Values = append(out.Values, value)
			continue
		}
		cell, present := rowFor(field.Kind, ref.Key)
		if !present {
			incomplete = true
			otherKind := domain.ReleaseEntrySecret
			if field.Kind == domain.ReleaseEntrySecret {
				otherKind = domain.ReleaseEntryParameter
			}
			if _, found := rowFor(otherKind, ref.Key); found {
				add(finding(domain.FindingKindMismatch, domain.FindingBlocking, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "kind": field.Kind, "found": otherKind}))
			} else {
				add(finding(domain.FindingResourceMissing, domain.FindingBlocking, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "kind": field.Kind}))
			}
			out.Values = append(out.Values, value)
			continue
		}
		value.Present = true
		value.ContentType = cell.ContentType
		value.CurrentVersion = cell.Version
		value.Bound = cell.Bound
		switch field.Kind {
		case domain.ReleaseEntryParameter:
			if cell.ContentType != field.ContentType {
				incomplete = true
				add(finding(domain.FindingContentTypeMismatch, domain.FindingBlocking, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "content_type": field.ContentType, "found": cell.ContentType}))
			}
		case domain.ReleaseEntrySecret:
			if state, ok := in.Secrets[ref.Key]; ok {
				if state.Expired {
					incomplete = true
					add(finding(domain.FindingSecretUnreadable, domain.FindingBlocking, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "state": "expired"}))
				} else if state.State != domain.StateEnabled {
					incomplete = true
					add(finding(domain.FindingSecretUnreadable, domain.FindingBlocking, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "state": state.State}))
				}
			}
			if cell.HasAccessToken {
				add(finding(domain.FindingSecretTokenRequired, domain.FindingInfo, aliasScope(field.Alias), map[string]any{"alias": field.Alias}))
			}
		}
		out.Values = append(out.Values, value)
	}
	switch {
	case !anyRows:
		out.ValuesState = domain.ValuesStateEmpty
	case incomplete:
		out.ValuesState = domain.ValuesStateIncomplete
	default:
		out.ValuesState = domain.ValuesStateComplete
	}

	// --- release --------------------------------------------------------------
	blocked, drift := false, false
	if in.SchemaMissing {
		blocked = true
	}
	if in.Active == nil {
		out.ReleaseState = domain.ReleaseStateNone
		add(finding(domain.FindingNoActiveRelease, domain.FindingWarning, envScope, nil))
	} else {
		active := in.Active
		out.Active = &domain.OverviewActiveRelease{
			Release: active.Release, ActivationRevision: active.ActivationRevision, PreviousVersion: active.PreviousVersion,
			IsRolledBack: active.PreviousVersion > 0 && active.Release.Version < active.PreviousVersion,
		}
		if len(in.App.Contract) > 0 && !releaseMatchesContract(in.App.Contract, active.Release.Entries) {
			blocked = true
			add(finding(domain.FindingContractReleaseMismatch, domain.FindingBlocking, envScope, nil))
		}
		staleSeen := map[string]bool{}
		for _, verr := range in.ActiveValidation {
			if verr.Alias == "" || staleSeen[verr.Alias] {
				continue
			}
			switch verr.Code {
			case domain.ReleaseValidationNotFound, domain.ReleaseValidationUnreadable, domain.ReleaseValidationDigest,
				domain.ReleaseValidationPermissionDenied, domain.ReleaseValidationContentType:
				staleSeen[verr.Alias] = true
				blocked = true
				add(finding(domain.FindingReleasePinStale, domain.FindingBlocking, aliasScope(verr.Alias), map[string]any{"alias": verr.Alias, "reason": verr.Code}))
			}
		}
		for _, field := range in.App.Contract {
			entry, pinned := activeEntries[field.Alias]
			if !pinned {
				drift = true
				add(finding(domain.FindingAliasNotInRelease, domain.FindingWarning, aliasScope(field.Alias), map[string]any{"alias": field.Alias}))
				continue
			}
			if entry.Ref.NS != here {
				continue
			}
			if cell, present := rowFor(entry.Kind, entry.Ref.Key); present && cell.Version != entry.Version {
				drift = true
				add(finding(domain.FindingUnreleasedChanges, domain.FindingWarning, aliasScope(field.Alias), map[string]any{"alias": field.Alias, "current": cell.Version, "pinned": entry.Version}))
			}
		}
		if out.Active.IsRolledBack {
			add(finding(domain.FindingRolledBack, domain.FindingInfo, envScope, map[string]any{"from": active.PreviousVersion}))
		}
		if active.PreviousVersion == 0 {
			add(finding(domain.FindingPreviousUnavailable, domain.FindingInfo, envScope, nil))
		}
		switch {
		case blocked:
			out.ReleaseState = domain.ReleaseStateBlocked
		case drift:
			out.ReleaseState = domain.ReleaseStateDrift
		default:
			out.ReleaseState = domain.ReleaseStateActive
		}
	}
	if in.Active == nil && blocked {
		out.ReleaseState = domain.ReleaseStateBlocked
	}

	// --- rollout --------------------------------------------------------------
	currentRevision := uint64(0)
	if in.Active != nil {
		currentRevision = in.Active.ActivationRevision
	}
	out.Rollout, out.RolloutState = computeRollout(in.Acks, in.App.ReleaseName, currentRevision, in.Now)
	if out.ReleaseState == domain.ReleaseStateActive || out.ReleaseState == domain.ReleaseStateDrift {
		if out.Rollout.Total == 0 {
			add(finding(domain.FindingNoSubscribers, domain.FindingInfo, envScope, nil))
		}
		if n := len(out.Rollout.OtherReleaseNames); n > 0 {
			add(finding(domain.FindingSubscriberOtherRelease, domain.FindingWarning, envScope, map[string]any{"count": otherReleaseInstanceCount(in.Acks, in.App.ReleaseName), "names": strings.Join(out.Rollout.OtherReleaseNames, ",")}))
		}
		emitted := 0
		for _, inst := range groupSubscriberInstances(filterAcks(in.Acks, in.App.ReleaseName)) {
			if emitted >= maxRolloutInstanceFindings {
				break
			}
			class := classifyInstance(inst, currentRevision, in.Now)
			scope := domain.FindingScope{Env: env, Instance: inst.InstanceID}
			params := map[string]any{"client_name": inst.ClientName, "instance_id": inst.InstanceID, "identity": inst.Identity}
			switch class {
			case instanceApplied:
				// An applied instance is only worth a finding when the generation it
				// runs diverges from the application's source-owned defaults.
				if !inst.AppliedDivergent {
					continue
				}
				params["divergent_fields"] = int(inst.DivergentFieldCount)
				add(finding(domain.FindingInstanceDivergent, domain.FindingWarning, scope, params))
			case instanceRejected:
				params["category"] = inst.RejectionCategory
				add(finding(domain.FindingInstanceRejected, domain.FindingWarning, scope, params))
			case instancePending:
				add(finding(domain.FindingInstancePending, domain.FindingInfo, scope, params))
			case instanceStale:
				add(finding(domain.FindingInstanceStale, domain.FindingInfo, scope, params))
			default:
				continue
			}
			emitted++
		}
	}

	// --- status ---------------------------------------------------------------
	sortFindings(out.Findings)
	switch {
	case out.ReleaseState == domain.ReleaseStateBlocked:
		out.Status = domain.EnvStatusBlocked
	case out.ValuesState == domain.ValuesStateEmpty:
		out.Status = domain.EnvStatusEmpty
	case out.ValuesState == domain.ValuesStateIncomplete:
		out.Status = domain.EnvStatusIncomplete
	case out.ReleaseState == domain.ReleaseStateNone:
		out.Status = domain.EnvStatusUnreleased
	case out.RolloutState == domain.RolloutStateDegraded:
		out.Status = domain.EnvStatusDegraded
	case out.RolloutState == domain.RolloutStateRolling:
		out.Status = domain.EnvStatusRolling
	case out.ReleaseState == domain.ReleaseStateDrift:
		out.Status = domain.EnvStatusDrift
	default:
		out.Status = domain.EnvStatusReady
	}
	return out
}

var severityRank = map[string]int{domain.FindingBlocking: 0, domain.FindingWarning: 1, domain.FindingInfo: 2}

// sortFindings orders blocking before warning before info, keeping the
// emission order within a severity so output stays deterministic.
func sortFindings(findings []domain.Finding) {
	sort.SliceStable(findings, func(i, j int) bool { return severityRank[findings[i].Severity] < severityRank[findings[j].Severity] })
}

// releaseMatchesContract reports whether the entries are exactly the
// contract's (alias, kind, parameter content type) set, in any order.
func releaseMatchesContract(contract []domain.ApplicationContractField, entries []domain.ConfigurationReleaseEntry) bool {
	if len(contract) != len(entries) {
		return false
	}
	byAlias := make(map[string]domain.ConfigurationReleaseEntry, len(entries))
	for _, entry := range entries {
		byAlias[entry.Alias] = entry
	}
	for _, field := range contract {
		entry, ok := byAlias[field.Alias]
		if !ok || entry.Kind != field.Kind {
			return false
		}
		if field.Kind == domain.ReleaseEntryParameter && entry.ContentType != field.ContentType {
			return false
		}
	}
	return true
}

func filterAcks(acks []domain.ReleaseAcknowledgement, releaseName string) []domain.ReleaseAcknowledgement {
	out := make([]domain.ReleaseAcknowledgement, 0, len(acks))
	for _, ack := range acks {
		if ack.ReleaseName == releaseName {
			out = append(out, ack)
		}
	}
	return out
}

func otherReleaseInstanceCount(acks []domain.ReleaseAcknowledgement, releaseName string) int {
	others := make([]domain.ReleaseAcknowledgement, 0)
	for _, ack := range acks {
		if ack.ReleaseName != releaseName {
			others = append(others, ack)
		}
	}
	return len(groupSubscriberInstances(others))
}

var lifecycleRank = map[string]int{
	domain.ReleaseStateReceived: 1,
	domain.ReleaseStatePrepared: 2,
	domain.ReleaseStateApplied:  3,
	domain.ReleaseStateRejected: 4,
}

// groupSubscriberInstances folds the per-state acknowledgement rows into one
// effective row per (identity, client, instance): the highest-ranked
// lifecycle state at the instance's newest activation revision, connected if
// any row is connected. Mirrors groupSubscriberInstances in
// frontend/lib/subscribers.ts. Output is sorted by identity, client, instance.
func groupSubscriberInstances(acks []domain.ReleaseAcknowledgement) []domain.SubscriberInstance {
	type key struct{ identity, client, instance string }
	grouped := map[key]*domain.SubscriberInstance{}
	order := make([]key, 0)
	for _, ack := range acks {
		k := key{ack.Identity, ack.ClientName, ack.InstanceID}
		inst := grouped[k]
		if inst == nil {
			inst = &domain.SubscriberInstance{Identity: ack.Identity, ClientName: ack.ClientName, InstanceID: ack.InstanceID}
			grouped[k] = inst
			order = append(order, k)
		}
		inst.Connected = inst.Connected || ack.Connected
		if ack.ServerTimestamp.After(inst.ServerTimestamp) {
			inst.ServerTimestamp = ack.ServerTimestamp
		}
		rank, lifecycle := lifecycleRank[ack.State]
		if !lifecycle {
			continue
		}
		current := lifecycleRank[inst.State]
		if ack.ActivationRevision > inst.ActivationRevision || (ack.ActivationRevision == inst.ActivationRevision && rank > current) {
			inst.State = ack.State
			inst.ReleaseVersion = ack.ReleaseVersion
			inst.ActivationRevision = ack.ActivationRevision
			inst.RejectionCategory = ack.RejectionCategory
			inst.Diagnostic = ack.Diagnostic
			// Divergence is only meaningful on an applied row; never let a stale
			// prepared/rejected row's flag leak into the instance.
			inst.AppliedDivergent = ack.State == domain.ReleaseStateApplied && ack.AppliedDivergent
			inst.DivergentFieldCount = 0
			if inst.AppliedDivergent {
				inst.DivergentFieldCount = ack.DivergentFieldCount
			}
		}
	}
	out := make([]domain.SubscriberInstance, 0, len(order))
	for _, k := range order {
		out = append(out, *grouped[k])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Identity != out[j].Identity {
			return out[i].Identity < out[j].Identity
		}
		if out[i].ClientName != out[j].ClientName {
			return out[i].ClientName < out[j].ClientName
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

type instanceClass int

const (
	instanceApplied instanceClass = iota
	instanceRejected
	instancePending
	instanceStale
)

// classifyInstance places one instance relative to the current activation
// revision. Below-applied instances are pending while connected (or only
// briefly disconnected) and stale once disconnected for staleDisconnectAfter.
func classifyInstance(inst domain.SubscriberInstance, currentRevision uint64, now time.Time) instanceClass {
	if currentRevision > 0 && inst.ActivationRevision == currentRevision {
		switch inst.State {
		case domain.ReleaseStateRejected:
			return instanceRejected
		case domain.ReleaseStateApplied:
			return instanceApplied
		}
	}
	if inst.Connected || now.Sub(inst.ServerTimestamp) <= staleDisconnectAfter {
		return instancePending
	}
	return instanceStale
}

// computeRollout summarises the namespace's subscriber instances for
// releaseName against currentRevision. Instances subscribed under other
// release names are reported by name only.
func computeRollout(acks []domain.ReleaseAcknowledgement, releaseName string, currentRevision uint64, now time.Time) (domain.RolloutSummary, string) {
	summary := domain.RolloutSummary{OtherReleaseNames: []string{}, RejectedInstances: []domain.SubscriberInstance{}}
	otherNames := map[string]struct{}{}
	for _, ack := range acks {
		if ack.ReleaseName != releaseName {
			otherNames[ack.ReleaseName] = struct{}{}
		}
	}
	for name := range otherNames {
		summary.OtherReleaseNames = append(summary.OtherReleaseNames, name)
	}
	sort.Strings(summary.OtherReleaseNames)
	for _, inst := range groupSubscriberInstances(filterAcks(acks, releaseName)) {
		summary.Total++
		if inst.Connected {
			summary.Connected++
		}
		switch classifyInstance(inst, currentRevision, now) {
		case instanceApplied:
			summary.AppliedCurrent++
			if inst.AppliedDivergent {
				summary.AppliedDivergent++
			}
		case instanceRejected:
			summary.Rejected++
			if len(summary.RejectedInstances) < maxRolloutInstanceFindings {
				summary.RejectedInstances = append(summary.RejectedInstances, inst)
			} else {
				summary.Truncated = true
			}
		case instancePending:
			summary.Pending++
		case instanceStale:
			summary.Stale++
		}
	}
	state := domain.RolloutStateApplied
	switch {
	case summary.Total == 0:
		state = domain.RolloutStateNoSubscribers
	case summary.Rejected > 0:
		state = domain.RolloutStateDegraded
	case summary.Pending > 0:
		state = domain.RolloutStateRolling
	case summary.Stale > 0:
		state = domain.RolloutStateStale
	}
	return summary, state
}

// applicationReadinessInput feeds computeApplicationFindings.
type applicationReadinessInput struct {
	App          domain.Application
	Environments []domain.EnvironmentOverview
	// Schema is the pinned schema when it exists; SchemaMissing is set when the
	// application pins a schema the registry no longer has.
	Schema           *domain.ConfigurationSchema
	SchemaMissing    bool
	InsecureListener bool
}

// computeApplicationFindings derives the application-level findings and the
// aggregate status (§3.1). insecure_listener is reported but deliberately
// excluded from the status so a loopback development install is not
// permanently "attention".
func computeApplicationFindings(in applicationReadinessInput) (string, []domain.Finding) {
	findings := []domain.Finding{}
	appScope := domain.FindingScope{}
	if len(in.Environments) == 0 {
		findings = append(findings, finding(domain.FindingNoEnvironments, domain.FindingWarning, appScope, nil))
	}
	if len(in.App.Contract) == 0 {
		findings = append(findings, finding(domain.FindingContractEmpty, domain.FindingWarning, appScope, nil))
	}
	switch {
	case in.App.SchemaVersion == 0:
		findings = append(findings, finding(domain.FindingSchemaUnpinned, domain.FindingInfo, appScope, nil))
	case in.SchemaMissing || in.Schema == nil:
		findings = append(findings, finding(domain.FindingSchemaMissing, domain.FindingBlocking, appScope, map[string]any{"application": in.App.Name, "release_name": in.App.ReleaseName, "schema_version": in.App.SchemaVersion}))
	default:
		findings = append(findings, contractSchemaAlignment(in.App.Contract, in.Schema.Schema)...)
	}
	if in.InsecureListener {
		findings = append(findings, finding(domain.FindingInsecureListener, domain.FindingWarning, appScope, nil))
	}

	blocked := in.SchemaMissing
	for _, f := range findings {
		if f.Severity == domain.FindingBlocking {
			blocked = true
		}
	}
	warning := false
	for _, f := range findings {
		if f.Severity == domain.FindingWarning && f.Code != domain.FindingInsecureListener {
			warning = true
		}
	}
	allSetup := true
	attention := false
	for _, env := range in.Environments {
		switch env.Status {
		case domain.EnvStatusBlocked:
			blocked = true
			allSetup = false
		case domain.EnvStatusEmpty, domain.EnvStatusIncomplete, domain.EnvStatusUnreleased:
			if env.Status != domain.EnvStatusEmpty {
				attention = true
			}
		case domain.EnvStatusDegraded, domain.EnvStatusRolling, domain.EnvStatusDrift:
			allSetup = false
			attention = true
		default:
			allSetup = false
		}
		for _, f := range env.Findings {
			if f.Severity == domain.FindingWarning {
				warning = true
			}
		}
	}
	sortFindings(findings)
	status := domain.AppStatusReady
	switch {
	case blocked:
		status = domain.AppStatusBlocked
	case len(in.Environments) == 0 || allSetup:
		status = domain.AppStatusSetup
	case attention || warning:
		status = domain.AppStatusAttention
	}
	return status, findings
}

// JSONTypeToContentType maps a JSON Schema property to the parameter content
// type its value must be stored as. Mirrored in frontend/lib/contract-derive.ts
// and pinned by the readiness-cases fixture.
func JSONTypeToContentType(property map[string]any) string {
	typ, ok := property["type"].(string)
	if !ok {
		return "json"
	}
	switch typ {
	case "object", "array":
		return "json"
	case "string":
		if format, _ := property["format"].(string); format == "kms-base64" {
			return "binary"
		}
		return "string"
	case "integer":
		return "integer"
	case "number":
		return "float"
	case "boolean":
		return "boolean"
	default:
		return "json"
	}
}

// contractSchemaAlignment compares the contract's parameter aliases with the
// schema's properties/required. Secrets never enter the validated object, so
// they are ignored except when the schema requires them.
func contractSchemaAlignment(contract []domain.ApplicationContractField, schemaJSON string) []domain.Finding {
	var schema map[string]any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return nil
	}
	properties, _ := schema["properties"].(map[string]any)
	additional, hasAdditional := schema["additionalProperties"].(bool)
	closed := hasAdditional && !additional
	parameterAliases := map[string]struct{}{}
	findings := []domain.Finding{}
	for _, field := range contract {
		if field.Kind != domain.ReleaseEntryParameter {
			continue
		}
		parameterAliases[field.Alias] = struct{}{}
		scope := domain.FindingScope{Alias: field.Alias}
		property, ok := properties[field.Alias].(map[string]any)
		if !ok {
			if closed {
				findings = append(findings, finding(domain.FindingAliasNotInSchema, domain.FindingWarning, scope, map[string]any{"alias": field.Alias}))
			} else {
				findings = append(findings, finding(domain.FindingSchemaPropertyMissingAlias, domain.FindingWarning, scope, map[string]any{"alias": field.Alias}))
			}
			continue
		}
		if expected := JSONTypeToContentType(property); expected != field.ContentType {
			schemaType, _ := property["type"].(string)
			if schemaType == "" {
				schemaType = "json"
			}
			findings = append(findings, finding(domain.FindingContractTypeMismatch, domain.FindingWarning, scope, map[string]any{"alias": field.Alias, "content_type": field.ContentType, "schema_type": schemaType}))
		}
	}
	required, _ := schema["required"].([]any)
	for _, item := range required {
		name, ok := item.(string)
		if !ok {
			continue
		}
		if _, ok := parameterAliases[name]; !ok {
			findings = append(findings, finding(domain.FindingSchemaRequiredMissingAlias, domain.FindingBlocking, domain.FindingScope{Alias: name}, map[string]any{"alias": name}))
		}
	}
	return findings
}
