package core

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// shipMetadataSource tags releases created by the console's ship flow.
const shipMetadataSource = "console.ship"

// shipPlan is the ship candidate before anything is written: the preview
// rows, the stored pins to resolve, the unsaved values, and the resolution
// errors already known.
type shipPlan struct {
	entries    []domain.ShipPreviewEntry
	selectors  []domain.ReleaseEntrySelector
	predicted  []domain.ConfigurationReleaseEntry
	values     []shipValueChange
	overrides  map[string]releaseCandidateValue
	warnings   []domain.Finding
	validation []domain.ReleaseValidationError
}

type shipValueChange struct {
	alias       string
	ref         domain.Ref
	value       string
	contentType string
}

// ShipApplicationChange is the console's one-shot "write values, create a
// release, activate it" flow (§4). Preflight failures are returned as errors
// (4xx); every evaluated outcome is a ShipResult whose Status says what
// happened. Dry runs validate the candidate in memory and write nothing.
func (s *Service) ShipApplicationChange(ctx context.Context, pr Principal, in domain.ShipInput) (domain.ShipResult, error) {
	if err := s.requireAdmin(ctx, pr, "application.ship", domain.ResourceApplication, in.Application); err != nil {
		return domain.ShipResult{}, err
	}
	if err := keyutil.ValidateApp(in.Application); err != nil {
		return domain.ShipResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := keyutil.ValidateEnv(in.Environment); err != nil {
		return domain.ShipResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	store, err := s.applicationStore()
	if err != nil {
		return domain.ShipResult{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ShipResult{}, err
	}
	app, err := store.GetApplication(ctx, in.Application)
	if err != nil {
		return domain.ShipResult{}, err
	}
	if len(app.Contract) == 0 {
		return domain.ShipResult{}, domain.Errorf(domain.ErrFailedPrecondition, "application %s has no contract; define one before shipping", app.Name)
	}
	ns := domain.NamespaceRef{Env: in.Environment, App: in.Application}
	namespace, err := s.store.GetNamespace(ctx, ns)
	if err != nil {
		return domain.ShipResult{}, err
	}
	metadata, err := shipReleaseMetadata(in.Metadata)
	if err != nil {
		return domain.ShipResult{}, err
	}
	changes, err := validateShipChanges(app, in.Changes)
	if err != nil {
		return domain.ShipResult{}, err
	}

	facts, err := s.loadEnvironmentReleaseFacts(ctx, rs, ns, app.ReleaseName, false)
	if err != nil {
		return domain.ShipResult{}, err
	}
	var activeVersion uint64
	var activeRelease *domain.ConfigurationRelease
	if facts.Active != nil {
		activeVersion = facts.Active.Release.Version
		activeRelease = &facts.Active.Release
	}
	if in.ExpectedActiveVersion != nil && *in.ExpectedActiveVersion != activeVersion {
		return domain.ShipResult{}, domain.Errorf(domain.ErrAborted, "active release changed: expected version %d, found %d", *in.ExpectedActiveVersion, activeVersion)
	}
	environments, err := store.ListApplicationNamespaces(ctx, app.Name)
	if err != nil {
		return domain.ShipResult{}, err
	}
	otherActive := map[string]domain.ConfigurationRelease{}
	for _, other := range environments {
		if other.Env == ns.Env {
			continue
		}
		active, err := rs.GetActiveConfigurationRelease(ctx, other.NamespaceRef, app.ReleaseName)
		if err == nil {
			otherActive[other.Env] = active.Release
		} else if !errors.Is(err, domain.ErrNotFound) {
			return domain.ShipResult{}, err
		}
	}
	rows, _, err := s.collectApplicationRows(ctx, []domain.Namespace{namespace})
	if err != nil {
		return domain.ShipResult{}, err
	}
	refs := resolveContractRefs(app, ns.Env, activeRelease, facts.Latest, otherActive, rows)
	plan := buildShipPlan(app, ns, changes, activeRelease, refs, rows)

	// Resolve the stored pins and assemble the in-memory candidate.
	authCtx, _, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseCreate, domain.ResourceConfigurationRelease, domain.Ref{NS: ns, Key: app.ReleaseName})
	if err != nil {
		return domain.ShipResult{}, err
	}
	resolvedCtx, resolved, resolution, err := s.resolveReleaseEntries(authCtx, pr, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: app.ReleaseName, Entries: plan.selectors}, true)
	if err != nil {
		return domain.ShipResult{}, err
	}
	validation := append(plan.validation, resolution...)
	resolvedByAlias := make(map[string]domain.ConfigurationReleaseEntry, len(resolved))
	for _, entry := range resolved {
		resolvedByAlias[entry.Alias] = entry
	}
	for i := range plan.entries {
		if entry, ok := resolvedByAlias[plan.entries[i].Alias]; ok && plan.entries[i].Change != domain.ShipEntryEdited {
			plan.entries[i].ToVersion = entry.Version
		}
	}
	candidate := domain.ConfigurationRelease{
		Namespace: ns, Name: app.ReleaseName, SchemaID: app.SchemaID, SchemaVersion: app.SchemaVersion,
		Entries:  append(append([]domain.ConfigurationReleaseEntry(nil), resolved...), plan.predicted...),
		Metadata: metadata, CreatedBy: pr.Identity.Name,
	}
	sort.Slice(candidate.Entries, func(i, j int) bool { return candidate.Entries[i].Alias < candidate.Entries[j].Alias })
	if len(validation) == 0 {
		if err := s.validateApplicationReleaseContract(ctx, app.Name, app.ReleaseName, app.SchemaID, app.SchemaVersion, candidate.Entries, false); err != nil {
			if !errors.Is(err, domain.ErrFailedPrecondition) {
				return domain.ShipResult{}, err
			}
			validation = append(validation, domain.ReleaseValidationError{Code: domain.ReleaseValidationContract, Message: "candidate release does not match the application contract"})
		} else {
			validation, err = s.validateReleaseEntries(resolvedCtx, pr, rs, candidate, plan.overrides, false, false)
			if err != nil {
				return domain.ShipResult{}, err
			}
		}
	}
	if validation == nil {
		validation = []domain.ReleaseValidationError{}
	}
	result := domain.ShipResult{
		Preview: domain.ShipPreview{
			BaseVersion: activeVersion, ReleaseName: app.ReleaseName, SchemaID: app.SchemaID, SchemaVersion: app.SchemaVersion,
			Entries: plan.entries, Validation: validation, Warnings: plan.warnings,
		},
		Parameters: []domain.ShipParameterWrite{},
	}
	if in.DryRun {
		result.Status = domain.ShipStatusPreview
		return result, nil
	}

	// --- execute ------------------------------------------------------------
	aliases := make([]string, 0, len(changes))
	for _, change := range changes {
		aliases = append(aliases, change.Alias)
	}
	sort.Strings(aliases)
	auditMeta := func(extra map[string]string) map[string]string {
		meta := map[string]string{"environment": ns.Env, "aliases": strings.Join(aliases, ","), "activated": "false", "previous_version": strconv.FormatUint(activeVersion, 10)}
		maps.Copy(meta, extra)
		return meta
	}
	audit := func(decision string, extra map[string]string) {
		s.auditRefWithNamespaceID(ctx, pr, "application.ship", domain.ResourceApplication, domain.Ref{NS: ns, Key: app.Name}, namespace.ID, 0, decision, auditMeta(extra))
	}
	if len(validation) > 0 {
		result.Status = domain.ShipStatusRejected
		result.Error = &domain.ShipError{Code: "failed_precondition", Message: "candidate release failed validation; nothing was written", ValidationErrors: validation}
		audit("deny", map[string]string{"reason": "validation_failed"})
		return result, nil
	}
	selectors := append([]domain.ReleaseEntrySelector(nil), plan.selectors...)
	for _, change := range plan.values {
		version, revision, err := s.PutParameter(ctx, pr, change.ref, change.value, change.contentType, "{}")
		if err != nil {
			audit("error", map[string]string{"reason": "parameter_write_failed", "alias": change.alias})
			return domain.ShipResult{}, err
		}
		result.Parameters = append(result.Parameters, domain.ShipParameterWrite{Alias: change.alias, Key: change.ref.Key, Version: version, Revision: revision})
		selectors = append(selectors, domain.ReleaseEntrySelector{Alias: change.alias, Kind: domain.ReleaseEntryParameter, Ref: change.ref, Version: version})
		for i := range result.Preview.Entries {
			if result.Preview.Entries[i].Alias == change.alias {
				result.Preview.Entries[i].ToVersion = version
			}
		}
	}
	release, err := s.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: app.ReleaseName, SchemaID: app.SchemaID, SchemaVersion: app.SchemaVersion, Entries: selectors, Metadata: metadata,
	})
	if err != nil {
		audit("error", map[string]string{"reason": "release_create_failed"})
		return domain.ShipResult{}, err
	}
	result.Release = &release
	active, changed, err := s.ActivateConfigurationRelease(ctx, pr, ns, app.ReleaseName, release.Version, &activeVersion)
	if err != nil {
		var validationFailed *domain.ReleaseValidationFailedError
		switch {
		case errors.As(err, &validationFailed):
			result.Status = domain.ShipStatusReleaseCreatedNotActivated
			result.Error = &domain.ShipError{Code: "failed_precondition", Message: "release was created but failed activation validation", ValidationErrors: validationFailed.Violations()}
			audit("deny", map[string]string{"reason": "activation_validation_failed", "release_version": strconv.FormatUint(release.Version, 10)})
			return result, nil
		case errors.Is(err, domain.ErrAborted):
			result.Status = domain.ShipStatusConflict
			shipErr := &domain.ShipError{Code: "aborted", Message: "the active release changed while shipping; the new release was created but not activated"}
			if current, err := rs.GetActiveConfigurationRelease(ctx, ns, app.ReleaseName); err == nil {
				shipErr.CurrentVersion = current.Release.Version
			}
			result.Error = shipErr
			audit("deny", map[string]string{"reason": "cas_conflict", "release_version": strconv.FormatUint(release.Version, 10)})
			return result, nil
		default:
			audit("error", map[string]string{"reason": "activation_failed", "release_version": strconv.FormatUint(release.Version, 10)})
			return domain.ShipResult{}, err
		}
	}
	result.Status = domain.ShipStatusActivated
	result.Activation = &domain.ShipActivation{ActivationRevision: active.ActivationRevision, PreviousVersion: active.PreviousVersion, Changed: changed}
	audit("allow", map[string]string{"activated": "true", "release_version": strconv.FormatUint(release.Version, 10)})
	return result, nil
}

// shipReleaseMetadata validates caller metadata and stamps the console source.
func shipReleaseMetadata(raw string) (string, error) {
	metadata, err := validateReleaseMetadata(raw)
	if err != nil {
		return "", err
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(metadata), &obj); err != nil || obj == nil {
		return "", domain.Errorf(domain.ErrInvalidArgument, "metadata must be a JSON object")
	}
	obj["source"] = shipMetadataSource
	b, err := json.Marshal(obj)
	if err != nil {
		return "", domain.Errorf(domain.ErrInvalidArgument, "metadata must be a JSON object")
	}
	return validateReleaseMetadata(string(b))
}

// validateShipChanges checks the request rows against the contract and
// normalises content types. Errors are InvalidArgument (preflight).
func validateShipChanges(app domain.Application, changes []domain.ShipChange) ([]domain.ShipChange, error) {
	if len(changes) == 0 {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "at least one change is required")
	}
	fields := make(map[string]domain.ApplicationContractField, len(app.Contract))
	for _, field := range app.Contract {
		fields[field.Alias] = field
	}
	seen := make(map[string]struct{}, len(changes))
	out := make([]domain.ShipChange, 0, len(changes))
	for _, change := range changes {
		field, ok := fields[change.Alias]
		if !ok {
			return nil, domain.Errorf(domain.ErrInvalidArgument, "alias %q is not in the application contract", change.Alias)
		}
		if _, dup := seen[change.Alias]; dup {
			return nil, domain.Errorf(domain.ErrInvalidArgument, "duplicate change for alias %q", change.Alias)
		}
		seen[change.Alias] = struct{}{}
		pinned := change.Version > 0 || change.Label != ""
		switch {
		case change.Value != nil && pinned:
			return nil, domain.Errorf(domain.ErrInvalidArgument, "alias %q specifies both a value and a version/label", change.Alias)
		case change.Value == nil && !pinned:
			return nil, domain.Errorf(domain.ErrInvalidArgument, "alias %q needs a value or a version/label", change.Alias)
		case change.Version > 0 && change.Label != "":
			return nil, domain.Errorf(domain.ErrInvalidArgument, "alias %q specifies both version and label", change.Alias)
		}
		if change.Value != nil {
			if field.Kind != domain.ReleaseEntryParameter {
				return nil, domain.Errorf(domain.ErrInvalidArgument, "secret alias %q accepts only a version or label; secret values are never shipped", change.Alias)
			}
			if change.ContentType == "" {
				change.ContentType = field.ContentType
			}
			if change.ContentType != field.ContentType {
				return nil, domain.Errorf(domain.ErrInvalidArgument, "alias %q content type %q does not match the contract (%s)", change.Alias, change.ContentType, field.ContentType)
			}
			if len(*change.Value) > maxValueBytes {
				return nil, domain.Errorf(domain.ErrInvalidArgument, "alias %q value exceeds %d bytes", change.Alias, maxValueBytes)
			}
			if err := validateParameterValue(*change.Value, change.ContentType); err != nil {
				return nil, domain.Errorf(domain.ErrInvalidArgument, "alias %q: %v", change.Alias, err)
			}
		}
		out = append(out, change)
	}
	return out, nil
}

func cellFor(rows []domain.ApplicationConfigurationRow, env, kind, key string) (domain.ApplicationConfigurationCell, bool) {
	for _, row := range rows {
		if row.Kind == kind && row.Key == key {
			cell, ok := row.Cells[env]
			return cell, ok && cell.Present
		}
	}
	return domain.ApplicationConfigurationCell{}, false
}

// buildShipPlan derives the candidate from the contract: changed aliases are
// edited (unsaved value) or pinned (explicit version/label); untouched aliases
// keep the active pin when one exists (unreleased newer versions are reported
// as warnings, not silently included) and otherwise take `current`.
func buildShipPlan(app domain.Application, ns domain.NamespaceRef, changes []domain.ShipChange, active *domain.ConfigurationRelease, refs map[string]domain.Ref, rows []domain.ApplicationConfigurationRow) shipPlan {
	plan := shipPlan{
		entries: make([]domain.ShipPreviewEntry, 0, len(app.Contract)), overrides: map[string]releaseCandidateValue{},
		warnings: []domain.Finding{}, validation: []domain.ReleaseValidationError{},
	}
	byAlias := make(map[string]domain.ShipChange, len(changes))
	for _, change := range changes {
		byAlias[change.Alias] = change
	}
	activeEntries := map[string]domain.ConfigurationReleaseEntry{}
	if active != nil {
		for _, entry := range active.Entries {
			activeEntries[entry.Alias] = entry
		}
	}
	for _, field := range app.Contract {
		change, changed := byAlias[field.Alias]
		activeEntry, hasActive := activeEntries[field.Alias]
		ref, resolved := refs[field.Alias]
		entry := domain.ShipPreviewEntry{Alias: field.Alias, Kind: field.Kind}
		if hasActive {
			entry.FromVersion = activeEntry.Version
		}
		switch {
		case changed && change.Value != nil:
			key := field.Alias
			if resolved {
				key = ref.Key
			}
			target := domain.Ref{NS: ns, Key: key}
			if resolved && ref.NS == ns {
				target = ref
			}
			predicted := uint64(1)
			if cell, present := cellFor(rows, ns.Env, domain.ReleaseEntryParameter, target.Key); present {
				predicted = cell.Version + 1
			}
			entry.Key, entry.ToVersion, entry.Change = target.Key, predicted, domain.ShipEntryEdited
			plan.overrides[field.Alias] = releaseCandidateValue{value: []byte(*change.Value), contentType: change.ContentType}
			plan.predicted = append(plan.predicted, domain.ConfigurationReleaseEntry{Alias: field.Alias, Kind: domain.ReleaseEntryParameter, Ref: target, Version: predicted, ContentType: change.ContentType, Metadata: "{}", ParameterDigest: sha256Hex([]byte(*change.Value))})
			plan.values = append(plan.values, shipValueChange{alias: field.Alias, ref: target, value: *change.Value, contentType: change.ContentType})
		case changed:
			if !resolved {
				entry.Change = domain.ShipEntryMissing
				plan.validation = append(plan.validation, domain.ReleaseValidationError{Alias: field.Alias, Code: domain.ReleaseValidationNotFound, Message: "no resource resolves for this alias"})
				break
			}
			entry.Key, entry.Change = ref.Key, domain.ShipEntryPinned
			plan.selectors = append(plan.selectors, domain.ReleaseEntrySelector{Alias: field.Alias, Kind: field.Kind, Ref: ref, Version: change.Version, Label: change.Label})
		case hasActive:
			entry.Key, entry.ToVersion, entry.Change = activeEntry.Ref.Key, activeEntry.Version, domain.ShipEntryIncluded
			plan.selectors = append(plan.selectors, domain.ReleaseEntrySelector{Alias: field.Alias, Kind: activeEntry.Kind, Ref: activeEntry.Ref, Version: activeEntry.Version})
			if activeEntry.Ref.NS == ns {
				if cell, present := cellFor(rows, ns.Env, activeEntry.Kind, activeEntry.Ref.Key); present && cell.Version != activeEntry.Version {
					plan.warnings = append(plan.warnings, finding(domain.FindingUnreleasedChanges, domain.FindingWarning, domain.FindingScope{Env: ns.Env, Alias: field.Alias}, map[string]any{"alias": field.Alias, "current": cell.Version, "pinned": activeEntry.Version}))
				}
			}
		case resolved:
			entry.Key, entry.Change = ref.Key, domain.ShipEntryIncluded
			plan.selectors = append(plan.selectors, domain.ReleaseEntrySelector{Alias: field.Alias, Kind: field.Kind, Ref: ref, Label: domain.LabelCurrent})
		default:
			entry.Change = domain.ShipEntryMissing
			plan.validation = append(plan.validation, domain.ReleaseValidationError{Alias: field.Alias, Code: domain.ReleaseValidationNotFound, Message: "no resource resolves for this alias"})
		}
		plan.entries = append(plan.entries, entry)
	}
	return plan
}
