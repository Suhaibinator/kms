package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

type applicationReleasePlan struct {
	result      domain.ApplicationReleaseCreateResult
	transaction storage.ApplicationReleaseCreate
}

type applicationReleasePlanDigest struct {
	ArtifactDigest       string                                 `json:"artifact_digest"`
	NamespaceID          int64                                  `json:"namespace_id"`
	ReleaseName          string                                 `json:"release_name"`
	SchemaVersion        uint64                                 `json:"schema_version"`
	SchemaDigest         string                                 `json:"schema_digest"`
	Contract             []domain.ApplicationContractField      `json:"contract"`
	BaseReleaseVersion   uint64                                 `json:"base_release_version"`
	BaseReleaseDigest    string                                 `json:"base_release_digest"`
	ActiveReleaseVersion uint64                                 `json:"active_release_version"`
	ActivationRevision   uint64                                 `json:"activation_revision"`
	ActiveReleaseDigest  string                                 `json:"active_release_digest"`
	Metadata             string                                 `json:"metadata"`
	Entries              []domain.ApplicationReleasePlanEntry   `json:"entries"`
	Pins                 []domain.ConfigurationReleaseEntry     `json:"pins"`
	CurrentPins          []storage.ApplicationReleaseCurrentPin `json:"current_pins"`
	MissingSecrets       []string                               `json:"missing_secrets"`
	Validation           []domain.ReleaseValidationError        `json:"validation"`
}

// CreateApplicationRelease previews or creates (without activating) the
// canonical release described by a generated defaults artifact.
func (s *Service) CreateApplicationRelease(ctx context.Context, pr Principal, in domain.ApplicationReleaseCreateInput) (domain.ApplicationReleaseCreateResult, error) {
	if err := keyutil.ValidateNamespace(in.Namespace); err != nil {
		return domain.ApplicationReleaseCreateResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.requireAdmin(ctx, pr, "application.release.create", domain.ResourceApplication, in.Namespace.App); err != nil {
		return domain.ApplicationReleaseCreateResult{}, err
	}
	artifact, err := parseDefaultsArtifact(in.Artifact)
	if err != nil {
		return domain.ApplicationReleaseCreateResult{}, err
	}
	metadata, err := validateReleaseMetadata(in.Metadata)
	if err != nil {
		return domain.ApplicationReleaseCreateResult{}, err
	}
	appStore, err := s.applicationStore()
	if err != nil {
		return domain.ApplicationReleaseCreateResult{}, err
	}
	app, err := appStore.GetApplication(ctx, in.Namespace.App)
	if err != nil {
		return domain.ApplicationReleaseCreateResult{}, err
	}
	if !app.ArchivedAt.IsZero() {
		return domain.ApplicationReleaseCreateResult{}, domain.Errorf(domain.ErrFailedPrecondition, "application %s is archived", app.Name)
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseCreate, domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: app.ReleaseName})
	if err != nil {
		return domain.ApplicationReleaseCreateResult{}, err
	}
	plan, err := s.buildApplicationReleasePlan(ctx, pr, namespace, app, artifact, in.Artifact, metadata)
	if err != nil {
		return domain.ApplicationReleaseCreateResult{}, err
	}
	if !in.Execute {
		s.auditApplicationRelease(ctx, pr, namespace, plan.result, "application.release.preview", "allow")
		return plan.result, nil
	}
	if in.PlanDigest == "" || in.PlanDigest != plan.result.PlanDigest {
		s.auditApplicationRelease(ctx, pr, namespace, plan.result, "application.release.create", "deny")
		return domain.ApplicationReleaseCreateResult{}, domain.Errorf(domain.ErrAborted, "application release plan is stale; preview again")
	}
	if !plan.result.Valid {
		s.auditApplicationRelease(ctx, pr, namespace, plan.result, "application.release.create", "deny")
		return plan.result, nil
	}
	store, ok := s.store.(storage.ApplicationReleaseStore)
	if !ok {
		return domain.ApplicationReleaseCreateResult{}, domain.Errorf(domain.ErrFailedPrecondition, "application release creation is unavailable")
	}
	release, created, err := store.CreateLatestApplicationRelease(ctx, plan.transaction)
	if err != nil {
		s.auditApplicationRelease(ctx, pr, namespace, plan.result, "application.release.create", "error")
		return domain.ApplicationReleaseCreateResult{}, err
	}
	plan.result.Executed = true
	plan.result.Created = created
	plan.result.Release = &release
	s.auditApplicationRelease(ctx, pr, namespace, plan.result, "application.release.create", "allow")
	return plan.result, nil
}

func (s *Service) buildApplicationReleasePlan(ctx context.Context, pr Principal, namespace domain.Namespace, app domain.Application, artifact configstore.DefaultsArtifact, rawArtifact []byte, metadata string) (applicationReleasePlan, error) {
	if !app.ArchivedAt.IsZero() {
		return applicationReleasePlan{}, domain.Errorf(domain.ErrFailedPrecondition, "application %s is archived", app.Name)
	}
	if len(app.Contract) == 0 || len(app.Contract) > maxReleaseEntries {
		return applicationReleasePlan{}, domain.Errorf(domain.ErrFailedPrecondition, "application contract must contain between 1 and %d entries", maxReleaseEntries)
	}
	artifactContract := applicationContractFromArtifact(artifact.Contract)
	if !reflect.DeepEqual(artifactContract, app.Contract) {
		return applicationReleasePlan{}, domain.Errorf(domain.ErrFailedPrecondition, "application contract differs from generated defaults; run defaults apply with --update-definition first")
	}
	rs, err := s.releaseStore()
	if err != nil {
		return applicationReleasePlan{}, err
	}
	if app.SchemaVersion == 0 {
		return applicationReleasePlan{}, applicationReleaseSchemaDriftError(ctx, rs, app, artifact.SchemaSHA256)
	}
	schema, err := rs.GetConfigurationSchema(ctx, app.Name, app.ReleaseName, app.SchemaVersion)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return applicationReleasePlan{}, applicationReleaseSchemaDriftError(ctx, rs, app, artifact.SchemaSHA256)
		}
		return applicationReleasePlan{}, err
	}
	if schema.Digest != artifact.SchemaSHA256 {
		return applicationReleasePlan{}, applicationReleaseSchemaDriftError(ctx, rs, app, artifact.SchemaSHA256)
	}
	parameters := make(map[string]configstore.DefaultsParameter, len(artifact.Parameters))
	for _, parameter := range artifact.Parameters {
		if _, err := parameterSchemaValue(parameter.Value, parameter.ContentType); err != nil {
			return applicationReleasePlan{}, domain.Errorf(domain.ErrInvalidArgument, "generated default for %q does not parse as %s", parameter.Alias, parameter.ContentType)
		}
		parameters[parameter.Alias] = parameter
	}
	facts, err := s.loadEnvironmentReleaseFacts(ctx, rs, namespace.NamespaceRef, app.ReleaseName, false)
	if err != nil {
		return applicationReleasePlan{}, err
	}
	appStore, err := s.applicationStore()
	if err != nil {
		return applicationReleasePlan{}, err
	}
	environments, err := appStore.ListApplicationNamespaces(ctx, app.Name)
	if err != nil {
		return applicationReleasePlan{}, err
	}
	rows, _, err := s.collectApplicationRows(ctx, environments)
	if err != nil {
		return applicationReleasePlan{}, err
	}
	otherActive := make(map[string]domain.ConfigurationRelease)
	for _, environment := range environments {
		if environment.Env == namespace.Env {
			continue
		}
		active, activeErr := rs.GetActiveConfigurationRelease(ctx, environment.NamespaceRef, app.ReleaseName)
		if activeErr == nil {
			otherActive[environment.Env] = active.Release
		} else if !errors.Is(activeErr, domain.ErrNotFound) {
			return applicationReleasePlan{}, activeErr
		}
	}
	var activeRelease *domain.ConfigurationRelease
	activeVersion, activationRevision, activeDigest := uint64(0), uint64(0), ""
	if facts.Active != nil {
		activeRelease = &facts.Active.Release
		activeVersion = facts.Active.Release.Version
		activationRevision = facts.Active.ActivationRevision
		activeDigest = facts.Active.Release.Digest
	}
	refs := resolveContractRefs(app, namespace.Env, activeRelease, facts.Latest, otherActive, rows)
	// With no active release, inactive history must not select a secret pin.
	bootstrapRefs := resolveContractRefs(app, namespace.Env, nil, nil, otherActive, rows)
	activeByAlias := make(map[string]domain.ConfigurationReleaseEntry)
	if activeRelease != nil {
		for _, entry := range activeRelease.Entries {
			activeByAlias[entry.Alias] = entry
		}
	}
	selectors := make([]domain.ReleaseEntrySelector, 0, len(app.Contract))
	planEntries := make([]domain.ApplicationReleasePlanEntry, 0, len(app.Contract))
	for _, field := range app.Contract {
		from := uint64(0)
		if active, ok := activeByAlias[field.Alias]; ok && active.Kind == field.Kind {
			from = active.Version
		}
		entry := domain.ApplicationReleasePlanEntry{Alias: field.Alias, Kind: field.Kind, FromVersion: from}
		switch field.Kind {
		case domain.ReleaseEntryParameter:
			ref := domain.Ref{NS: namespace.NamespaceRef, Key: field.Alias}
			if resolved, ok := refs[field.Alias]; ok {
				// Defaults apply resolves only the physical key, then writes in
				// the target namespace. Release creation must inspect that same
				// target resource rather than inheriting a historical cross-namespace
				// parameter ref.
				ref.Key = resolved.Key
			}
			entry.Ref, entry.Source = ref, domain.ApplicationReleaseSourceGeneratedDefault
			selectors = append(selectors, domain.ReleaseEntrySelector{Alias: field.Alias, Kind: field.Kind, Ref: ref, Label: domain.LabelCurrent})
		case domain.ReleaseEntrySecret:
			if active, ok := activeByAlias[field.Alias]; ok && active.Kind == field.Kind {
				entry.Ref, entry.ToVersion, entry.Source = active.Ref, active.Version, domain.ApplicationReleaseSourceCarriedActiveSecret
				selectors = append(selectors, domain.ReleaseEntrySelector{Alias: field.Alias, Kind: field.Kind, Ref: active.Ref, Version: active.Version})
			} else {
				ref := domain.Ref{NS: namespace.NamespaceRef, Key: field.Alias}
				if resolved, ok := bootstrapRefs[field.Alias]; ok {
					ref.Key = resolved.Key
				}
				entry.Ref, entry.Source = ref, domain.ApplicationReleaseSourceResolvedCurrentSecret
				selectors = append(selectors, domain.ReleaseEntrySelector{Alias: field.Alias, Kind: field.Kind, Ref: ref, Label: domain.LabelCurrent})
			}
		}
		planEntries = append(planEntries, entry)
	}
	resolvedCtx, resolved, validation, err := s.resolveReleaseEntries(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: namespace.NamespaceRef, Name: app.ReleaseName, Entries: selectors}, true)
	if err != nil {
		return applicationReleasePlan{}, err
	}
	resolvedByAlias := make(map[string]domain.ConfigurationReleaseEntry, len(resolved))
	for _, entry := range resolved {
		resolvedByAlias[entry.Alias] = entry
	}
	currentPins := make([]storage.ApplicationReleaseCurrentPin, 0, len(resolved))
	missingSecrets := []string{}
	for index := range planEntries {
		planned := &planEntries[index]
		entry, ok := resolvedByAlias[planned.Alias]
		if !ok {
			if planned.Kind == domain.ReleaseEntrySecret {
				missingSecrets = append(missingSecrets, planned.Alias)
			}
			continue
		}
		planned.Ref, planned.ToVersion = entry.Ref, entry.Version
		if planned.Source != domain.ApplicationReleaseSourceCarriedActiveSecret {
			currentPins = append(currentPins, storage.ApplicationReleaseCurrentPin{Kind: planned.Kind, Ref: entry.Ref, Version: entry.Version})
		}
		if planned.Kind == domain.ReleaseEntryParameter {
			current, readErr := s.store.GetParameter(resolvedCtx, entry.Ref, entry.Version, "")
			if readErr != nil {
				validation = append(validation, validationReadError(planned.Alias, readErr))
				continue
			}
			want := parameters[planned.Alias]
			if current.Value != want.Value || current.ContentType != want.ContentType {
				validation = append(validation, domain.ReleaseValidationError{Alias: planned.Alias, Code: domain.ReleaseValidationDefaultMismatch, Message: "current parameter differs from the generated default"})
			}
		}
	}
	candidate := domain.ConfigurationRelease{Namespace: namespace.NamespaceRef, Name: app.ReleaseName, SchemaVersion: app.SchemaVersion, Entries: resolved, Metadata: metadata, CreatedBy: pr.Identity.Name}
	if len(validation) == 0 {
		validation, err = s.validateReleaseEntries(resolvedCtx, pr, rs, candidate, nil, false, false)
		if err != nil {
			return applicationReleasePlan{}, err
		}
	}
	if validation == nil {
		validation = []domain.ReleaseValidationError{}
	}
	sort.Slice(validation, func(i, j int) bool {
		if validation[i].Alias != validation[j].Alias {
			return validation[i].Alias < validation[j].Alias
		}
		if validation[i].Code != validation[j].Code {
			return validation[i].Code < validation[j].Code
		}
		return validation[i].SchemaPointer < validation[j].SchemaPointer
	})
	if len(validation) == 0 {
		candidate.Digest, err = releaseDigest(candidate)
		if err != nil {
			return applicationReleasePlan{}, fmt.Errorf("calculate application release digest: %w", err)
		}
	}
	baseDigest := ""
	if facts.Latest != nil {
		baseDigest = facts.Latest.Digest
	}
	result := domain.ApplicationReleaseCreateResult{
		Profile: artifact.Profile, Valid: len(validation) == 0, ReleaseName: app.ReleaseName,
		SchemaVersion: app.SchemaVersion, BaseReleaseVersion: facts.LatestVersion,
		Entries: planEntries, MissingSecrets: missingSecrets, Validation: validation,
	}
	digestInput := applicationReleasePlanDigest{
		ArtifactDigest: sha256Hex(rawArtifact), NamespaceID: namespace.ID, ReleaseName: app.ReleaseName,
		SchemaVersion: app.SchemaVersion, SchemaDigest: schema.Digest, Contract: app.Contract,
		BaseReleaseVersion: facts.LatestVersion, BaseReleaseDigest: baseDigest,
		ActiveReleaseVersion: activeVersion, ActivationRevision: activationRevision, ActiveReleaseDigest: activeDigest,
		Metadata: metadata, Entries: planEntries, Pins: resolved, CurrentPins: currentPins,
		MissingSecrets: missingSecrets, Validation: validation,
	}
	digestJSON, err := json.Marshal(digestInput, json.Deterministic(true))
	if err != nil {
		return applicationReleasePlan{}, fmt.Errorf("encode application release plan: %w", err)
	}
	result.PlanDigest = sha256Hex(digestJSON)
	transaction := storage.ApplicationReleaseCreate{
		Release: candidate, NamespaceID: namespace.ID, Contract: append([]domain.ApplicationContractField(nil), app.Contract...),
		SchemaDigest: schema.Digest, ExpectedLatestVersion: facts.LatestVersion,
		ExpectedActiveVersion: activeVersion, ExpectedActivationRevision: activationRevision,
		CurrentPins: currentPins,
	}
	return applicationReleasePlan{result: result, transaction: transaction}, nil
}

func applicationReleaseSchemaDriftError(ctx context.Context, store storage.ReleaseStore, app domain.Application, digest string) error {
	if _, err := findConfigurationSchemaByDigest(ctx, store, app.Name, app.ReleaseName, digest); err != nil {
		if errors.Is(err, domain.ErrFailedPrecondition) {
			return domain.Errorf(domain.ErrFailedPrecondition, "generated schema is not registered for %s/%s; run schema upload, then defaults apply with --update-definition", app.Name, app.ReleaseName)
		}
		return err
	}
	return domain.Errorf(domain.ErrFailedPrecondition, "application schema differs from generated defaults; run defaults apply with --update-definition first")
}

func (s *Service) auditApplicationRelease(ctx context.Context, pr Principal, namespace domain.Namespace, result domain.ApplicationReleaseCreateResult, event, decision string) {
	metadata := map[string]string{
		"valid": fmt.Sprint(result.Valid), "executed": fmt.Sprint(result.Executed),
		"created": fmt.Sprint(result.Created), "validation_count": fmt.Sprint(len(result.Validation)),
		"missing_secret_count": fmt.Sprint(len(result.MissingSecrets)),
	}
	s.auditRefWithNamespaceID(ctx, pr, event, domain.ResourceApplication, domain.Ref{NS: namespace.NamespaceRef, Key: result.ReleaseName}, namespace.ID, 0, decision, metadata)
}
