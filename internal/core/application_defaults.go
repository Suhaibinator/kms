package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func parseDefaultsArtifact(raw []byte) (configstore.DefaultsArtifact, error) {
	artifact, err := configstore.ParseDefaultsArtifact(raw)
	if err != nil {
		// The SDK error can include attacker-controlled JSON field names. Keep the
		// transport error bounded and value-free while retaining InvalidArgument.
		return configstore.DefaultsArtifact{}, domain.Errorf(domain.ErrInvalidArgument, "invalid defaults artifact")
	}
	return artifact, nil
}

type defaultsPlan struct {
	result      domain.DefaultsApplyResult
	transaction storage.DefaultsApplyTransaction
	blocked     int
}

type defaultsPlanDigestEntry struct {
	Alias          string `json:"alias"`
	Key            string `json:"key"`
	Status         string `json:"status"`
	CurrentVersion uint64 `json:"current_version"`
	CurrentDigest  string `json:"current_digest"`
	CurrentType    string `json:"current_content_type"`
}

type defaultsPlanDigestInput struct {
	ExpectedApplicationSchemaVersion uint64                             `json:"expected_application_schema_version,omitempty"`
	ExpectedApplicationContract      []domain.ApplicationContractField  `json:"expected_application_contract,omitempty"`
	ExpectedApplicationUpdatedAt     time.Time                          `json:"expected_application_updated_at"`
	ArtifactDigest                   string                             `json:"artifact_digest"`
	NamespaceID                      int64                              `json:"namespace_id"`
	ReleaseName                      string                             `json:"release_name"`
	SchemaVersion                    uint64                             `json:"schema_version"`
	Contract                         []domain.ApplicationContractField  `json:"contract"`
	DesiredSchemaVersion             uint64                             `json:"desired_schema_version"`
	DesiredContract                  []domain.ApplicationContractField  `json:"desired_contract"`
	UpdateDefinition                 bool                               `json:"update_definition"`
	Resolution                       []storage.DefaultsResolutionState  `json:"resolution"`
	Resources                        []storage.DefaultsResourceIdentity `json:"resources"`
	Entries                          []defaultsPlanDigestEntry          `json:"entries"`
	MissingSecrets                   []string                           `json:"missing_secrets"`
	Overwrite                        bool                               `json:"overwrite"`
}

func (s *Service) defaultsApplyStore() (storage.DefaultsApplyStore, error) {
	store, ok := s.store.(storage.DefaultsApplyStore)
	if !ok {
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "defaults import is unavailable")
	}
	return store, nil
}

// ApplyApplicationDefaults previews or atomically executes a parameter-only
// defaults artifact. Applications, namespaces and schemas must already exist.
// An explicit UpdateDefinition request may atomically replace the existing
// application contract and repin an already registered matching schema;
// namespaces, schemas, releases and secrets are never mutated.
func (s *Service) ApplyApplicationDefaults(ctx context.Context, pr Principal, in domain.DefaultsApplyInput) (domain.DefaultsApplyResult, error) {
	if err := keyutil.ValidateNamespace(in.Namespace); err != nil {
		return domain.DefaultsApplyResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if in.SchemaVersion != nil {
		ctx = withReleaseAuditTrack(ctx, domain.ReleaseTrack{Namespace: in.Namespace, SchemaVersion: *in.SchemaVersion})
	}
	if err := s.requireAdmin(ctx, pr, "application.defaults", domain.ResourceApplication, in.Namespace.App); err != nil {
		return domain.DefaultsApplyResult{}, err
	}
	errorMetadata := func(reason, digest string) map[string]string {
		meta := map[string]string{"reason": reason}
		if in.SchemaVersion != nil {
			return releaseAuditMetadata(*in.SchemaVersion, 0, meta)
		}
		// Failed artifact preflight can still name a registered track. Resolve
		// only for this audit enrichment; unknown/malformed artifacts remain
		// unscoped and lookup failures never replace the original error.
		if digest == "" {
			return meta
		}
		apps, err := s.applicationStore()
		if err != nil {
			return meta
		}
		app, err := apps.GetApplication(ctx, in.Namespace.App)
		if err != nil {
			return meta
		}
		releases, err := s.releaseStore()
		if err != nil {
			return meta
		}
		schema, err := releases.GetConfigurationSchemaByDigest(ctx, app.Name, app.ReleaseName, digest)
		if err == nil {
			return releaseAuditMetadata(schema.Version, 0, meta)
		}
		return meta
	}
	artifact, err := parseDefaultsArtifact(in.Artifact)
	if err != nil {
		s.auditRef(ctx, pr, "application.defaults.preview", domain.ResourceApplication, domain.Ref{NS: in.Namespace, Key: "defaults"}, 0, "error", errorMetadata("invalid_artifact", ""))
		return domain.DefaultsApplyResult{}, err
	}
	plan, err := s.buildDefaultsPlan(ctx, in, artifact)
	if err != nil {
		s.auditRef(ctx, pr, "application.defaults.preview", domain.ResourceApplication, domain.Ref{NS: in.Namespace, Key: "defaults"}, 0, "error", errorMetadata("preflight_failed", artifact.SchemaSHA256))
		return domain.DefaultsApplyResult{}, err
	}
	if !in.Execute {
		s.auditDefaults(ctx, pr, in.Namespace, plan, "application.defaults.preview", "allow")
		return plan.result, nil
	}
	plan.transaction.CreatedBy = pr.Identity.Name
	if in.PlanDigest == "" || in.PlanDigest != plan.result.PlanDigest {
		s.auditDefaults(ctx, pr, in.Namespace, plan, "application.defaults.apply", "deny")
		return domain.DefaultsApplyResult{}, domain.Errorf(domain.ErrAborted, "defaults plan is stale; preview again")
	}
	if plan.blocked > 0 {
		s.auditDefaults(ctx, pr, in.Namespace, plan, "application.defaults.apply", "deny")
		return domain.DefaultsApplyResult{}, domain.Errorf(domain.ErrFailedPrecondition, "%d parameter defaults differ; use overwrite and preview again", plan.blocked)
	}
	if plan.result.DefinitionChanged && !in.UpdateDefinition {
		s.auditDefaults(ctx, pr, in.Namespace, plan, "application.defaults.apply", "deny")
		return domain.DefaultsApplyResult{}, domain.Errorf(domain.ErrFailedPrecondition, "application definition differs; use update_definition and preview again")
	}
	store, err := s.defaultsApplyStore()
	if err != nil {
		return domain.DefaultsApplyResult{}, err
	}
	writes, err := store.ApplyDefaults(ctx, plan.transaction)
	if err != nil {
		s.auditDefaults(ctx, pr, in.Namespace, plan, "application.defaults.apply", "error")
		return domain.DefaultsApplyResult{}, err
	}
	byAlias := make(map[string]storage.DefaultsAppliedWrite, len(writes))
	for _, write := range writes {
		byAlias[write.Alias] = write
	}
	for index := range plan.result.Entries {
		if write, ok := byAlias[plan.result.Entries[index].Alias]; ok {
			plan.result.Entries[index].AppliedVersion = write.Version
			plan.result.Entries[index].Revision = write.Revision
		}
	}
	plan.result.Executed = true
	plan.result.DefinitionUpdated = plan.result.DefinitionChanged
	s.auditDefaults(ctx, pr, in.Namespace, plan, "application.defaults.apply", "allow")
	if len(writes) > 0 || plan.result.DefinitionUpdated {
		s.getHub().Wake()
	}
	return plan.result, nil
}

func (s *Service) buildDefaultsPlan(ctx context.Context, in domain.DefaultsApplyInput, artifact configstore.DefaultsArtifact) (defaultsPlan, error) {
	appStore, err := s.applicationStore()
	if err != nil {
		return defaultsPlan{}, err
	}
	if _, err := s.defaultsApplyStore(); err != nil {
		return defaultsPlan{}, err
	}
	app, err := appStore.GetApplication(ctx, in.Namespace.App)
	if err != nil {
		return defaultsPlan{}, err
	}
	if !app.ArchivedAt.IsZero() {
		return defaultsPlan{}, domain.Errorf(domain.ErrFailedPrecondition, "application %s is archived", app.Name)
	}
	persistedApp := app
	if in.UpdateDefinition && in.SchemaVersion != nil {
		return defaultsPlan{}, domain.Errorf(domain.ErrInvalidArgument, "schema override cannot update the application default")
	}
	app, err = s.selectArtifactApplicationTrack(ctx, app, in.SchemaVersion, artifact.SchemaSHA256)
	if err != nil {
		return defaultsPlan{}, err
	}
	desiredApp := app
	desiredContract := applicationContractFromArtifact(artifact.Contract)
	if app.Contract != nil && !reflect.DeepEqual(desiredContract, app.Contract) {
		return defaultsPlan{}, domain.Errorf(domain.ErrFailedPrecondition, "defaults do not match the selected schema contract")
	}
	desiredApp.Contract = desiredContract
	app.Contract = desiredContract
	for _, parameter := range artifact.Parameters {
		if err := validateParameterValue(parameter.Value, parameter.ContentType); err != nil {
			return defaultsPlan{}, domain.Errorf(domain.ErrInvalidArgument, "defaults parameter %q does not parse as %s", parameter.Alias, parameter.ContentType)
		}
	}
	namespace, err := s.store.GetNamespace(ctx, in.Namespace)
	if err != nil {
		return defaultsPlan{}, err
	}
	releaseStore, err := s.releaseStore()
	if err != nil {
		return defaultsPlan{}, err
	}
	definitionChanged := in.UpdateDefinition && (persistedApp.SchemaVersion != desiredApp.SchemaVersion || !reflect.DeepEqual(persistedApp.Contract, desiredApp.Contract))
	environments, err := appStore.ListApplicationNamespaces(ctx, app.Name)
	if err != nil {
		return defaultsPlan{}, err
	}
	rows, _, err := s.collectApplicationRows(ctx, environments)
	if err != nil {
		return defaultsPlan{}, err
	}
	resolution := make([]storage.DefaultsResolutionState, 0, len(environments))
	resources := make([]storage.DefaultsResourceIdentity, 0, len(rows))
	for _, environment := range environments {
		for _, row := range rows {
			if row.Cells[environment.Env].Present {
				resources = append(resources, storage.DefaultsResourceIdentity{Environment: environment.Env, Kind: row.Kind, Key: row.Key})
			}
		}
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Environment != resources[j].Environment {
			return resources[i].Environment < resources[j].Environment
		}
		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}
		return resources[i].Key < resources[j].Key
	})
	otherActive := make(map[string]domain.ConfigurationRelease)
	var targetActive, targetLatest *domain.ConfigurationRelease
	for _, environment := range environments {
		facts, err := s.loadEnvironmentReleaseFacts(ctx, releaseStore, applicationTrack(app, environment.NamespaceRef), false)
		if err != nil {
			return defaultsPlan{}, err
		}
		state := storage.DefaultsResolutionState{SchemaVersion: app.SchemaVersion, Environment: environment.Env, NamespaceID: environment.ID, LatestVersion: facts.LatestVersion}
		if facts.Active != nil {
			state.ActiveVersion = facts.Active.Release.Version
			state.ActivationRevision = facts.Active.ActivationRevision
			otherActive[environment.Env] = facts.Active.Release
		}
		resolution = append(resolution, state)
		if environment.Env == in.Namespace.Env {
			if facts.Active != nil {
				release := facts.Active.Release
				targetActive = &release
			}
			if facts.Latest != nil {
				release := *facts.Latest
				targetLatest = &release
			}
		}
	}
	refs := resolveContractRefs(desiredApp, in.Namespace.Env, targetActive, targetLatest, otherActive, rows)
	artifactDigest := sha256Hex(in.Artifact)
	result := domain.DefaultsApplyResult{
		Profile: artifact.Profile, SchemaSHA256: artifact.SchemaSHA256,
		ArtifactDigest:    artifactDigest,
		Entries:           make([]domain.DefaultsApplyEntry, 0, len(artifact.Parameters)),
		MissingSecrets:    []string{},
		DefinitionChanged: definitionChanged,
	}
	transaction := storage.DefaultsApplyTransaction{
		Namespace: in.Namespace, NamespaceID: namespace.ID, ReleaseName: app.ReleaseName,
		SchemaVersion: app.SchemaVersion,
		SchemaDigest:  artifact.SchemaSHA256, Contract: append([]domain.ApplicationContractField(nil), app.Contract...),
		UpdateDefinition:     definitionChanged && in.UpdateDefinition,
		DesiredSchemaVersion: desiredApp.SchemaVersion,
		DesiredContract:      append([]domain.ApplicationContractField(nil), desiredApp.Contract...),
		ResolutionState:      resolution, Resources: resources,
	}
	if in.UpdateDefinition {
		transaction.ExpectedApplicationSchemaVersion = persistedApp.SchemaVersion
		transaction.ExpectedApplicationContract = persistedApp.Contract
		transaction.ExpectedApplicationUpdatedAt = persistedApp.UpdatedAt
	}
	digestEntries := make([]defaultsPlanDigestEntry, 0, len(artifact.Parameters))
	blocked := 0
	keys := make(map[string]string, len(artifact.Parameters))
	for _, parameter := range artifact.Parameters {
		key := parameter.Alias
		if resolved, ok := refs[parameter.Alias]; ok {
			key = resolved.Key
		}
		if err := keyutil.ValidateKey(key); err != nil {
			return defaultsPlan{}, domain.Errorf(domain.ErrFailedPrecondition, "defaults alias %q has no valid application key mapping", parameter.Alias)
		}
		if previousAlias, exists := keys[key]; exists {
			return defaultsPlan{}, domain.Errorf(domain.ErrFailedPrecondition, "defaults aliases %q and %q resolve to the same application key", previousAlias, parameter.Alias)
		}
		keys[key] = parameter.Alias
		ref := domain.Ref{NS: in.Namespace, Key: key}
		entry := domain.DefaultsApplyEntry{Alias: parameter.Alias, Key: key, ContentType: parameter.ContentType}
		expectation := storage.DefaultsParameterExpectation{Alias: parameter.Alias, Key: key, Value: parameter.Value, ContentType: parameter.ContentType}
		currentDigest, currentType := "", ""
		current, err := s.store.GetParameter(ctx, ref, 0, domain.LabelCurrent)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			entry.Status = domain.DefaultsStatusCreate
			expectation.Write = true
		case err != nil:
			return defaultsPlan{}, err
		default:
			entry.CurrentVersion = current.Version
			expectation.ExpectedVersion = current.Version
			expectation.ExpectedDigest = sha256Hex([]byte(current.Value))
			expectation.ExpectedContentType = current.ContentType
			currentDigest, currentType = expectation.ExpectedDigest, current.ContentType
			if current.Value == parameter.Value && current.ContentType == parameter.ContentType {
				entry.Status = domain.DefaultsStatusUnchanged
			} else if in.Overwrite {
				entry.Status = domain.DefaultsStatusUpdate
				expectation.Write = true
			} else {
				entry.Status = domain.DefaultsStatusBlocked
				blocked++
			}
		}
		result.Entries = append(result.Entries, entry)
		transaction.Parameters = append(transaction.Parameters, expectation)
		digestEntries = append(digestEntries, defaultsPlanDigestEntry{
			Alias: parameter.Alias, Key: key, Status: entry.Status,
			CurrentVersion: entry.CurrentVersion, CurrentDigest: currentDigest, CurrentType: currentType,
		})
	}
	for _, field := range desiredApp.Contract {
		if field.Kind != domain.ReleaseEntrySecret {
			continue
		}
		key := field.Alias
		if resolved, ok := refs[field.Alias]; ok {
			key = resolved.Key
		}
		if err := keyutil.ValidateKey(key); err != nil {
			result.MissingSecrets = append(result.MissingSecrets, field.Alias)
			continue
		}
		secret, err := s.store.GetSecretInfo(ctx, domain.Ref{NS: in.Namespace, Key: key})
		if errors.Is(err, domain.ErrNotFound) || (err == nil && secret.Labels[domain.LabelCurrent] == 0) {
			result.MissingSecrets = append(result.MissingSecrets, field.Alias)
			continue
		}
		if err != nil {
			return defaultsPlan{}, err
		}
	}
	digestInput := defaultsPlanDigestInput{
		ArtifactDigest: artifactDigest, NamespaceID: namespace.ID, ReleaseName: app.ReleaseName,
		SchemaVersion: app.SchemaVersion, Contract: app.Contract,
		DesiredSchemaVersion: desiredApp.SchemaVersion,
		DesiredContract:      desiredApp.Contract, UpdateDefinition: in.UpdateDefinition,
		Resolution: resolution, Resources: resources, Entries: digestEntries,
		MissingSecrets: result.MissingSecrets, Overwrite: in.Overwrite,
	}
	if in.UpdateDefinition {
		digestInput.ExpectedApplicationSchemaVersion = persistedApp.SchemaVersion
		digestInput.ExpectedApplicationContract = persistedApp.Contract
		digestInput.ExpectedApplicationUpdatedAt = persistedApp.UpdatedAt
	}
	digestJSON, err := json.Marshal(digestInput, json.Deterministic(true))
	if err != nil {
		return defaultsPlan{}, fmt.Errorf("encode defaults plan: %w", err)
	}
	result.PlanDigest = sha256Hex(digestJSON)
	return defaultsPlan{result: result, transaction: transaction, blocked: blocked}, nil
}

func applicationContractFromArtifact(artifact []configstore.ContractEntry) []domain.ApplicationContractField {
	converted := make([]domain.ApplicationContractField, len(artifact))
	for index, entry := range artifact {
		converted[index] = domain.ApplicationContractField{Alias: entry.Alias, Kind: string(entry.Kind), ContentType: entry.ContentType}
	}
	return converted
}

func (s *Service) auditDefaults(ctx context.Context, pr Principal, ns domain.NamespaceRef, plan defaultsPlan, eventType, decision string) {
	counts := map[string]int{}
	for _, entry := range plan.result.Entries {
		counts[entry.Status]++
	}
	meta := map[string]string{
		"schema_version":       strconv.FormatUint(plan.transaction.SchemaVersion, 10),
		"create_count":         strconv.Itoa(counts[domain.DefaultsStatusCreate]),
		"unchanged_count":      strconv.Itoa(counts[domain.DefaultsStatusUnchanged]),
		"update_count":         strconv.Itoa(counts[domain.DefaultsStatusUpdate]),
		"blocked_count":        strconv.Itoa(counts[domain.DefaultsStatusBlocked]),
		"missing_secret_count": strconv.Itoa(len(plan.result.MissingSecrets)),
		"definition_changed":   strconv.FormatBool(plan.result.DefinitionChanged),
	}
	namespaceID := plan.transaction.NamespaceID
	s.auditRefWithNamespaceID(ctx, pr, eventType, domain.ResourceApplication, domain.Ref{NS: ns, Key: "defaults"}, namespaceID, 0, decision, meta)
}
