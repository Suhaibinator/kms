package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"reflect"
	"sort"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// MigrateApplicationRelease previews or atomically applies a reviewed migration
// from the active release to an explicit registered schema and full contract.
func (s *Service) MigrateApplicationRelease(ctx context.Context, pr Principal, in domain.ApplicationReleaseMigrationInput) (domain.ApplicationReleaseMigrationResult, error) {
	empty := domain.ApplicationReleaseMigrationResult{}
	if err := keyutil.ValidateNamespace(in.Namespace); err != nil {
		return empty, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.requireAdmin(ctx, pr, "application.release.migrate", domain.ResourceApplication, in.Namespace.App); err != nil {
		return empty, err
	}
	ms, ok := s.store.(storage.ApplicationMigrationStore)
	if !ok {
		return empty, domain.Errorf(domain.ErrFailedPrecondition, "application migration is unavailable")
	}
	as, err := s.applicationStore()
	if err != nil {
		return empty, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return empty, err
	}
	base, err := ms.ApplicationMigrationSnapshot(ctx, in.Namespace)
	if err != nil {
		return empty, err
	}
	app, err := as.GetApplication(ctx, in.Namespace.App)
	if err != nil {
		return empty, err
	}
	if !app.ArchivedAt.IsZero() {
		return empty, domain.Errorf(domain.ErrFailedPrecondition, "application is archived")
	}
	if in.SchemaVersion == 0 || len(in.Contract) == 0 || len(in.Contract) > maxReleaseEntries || len(in.Changes) > maxReleaseEntries {
		return empty, domain.Errorf(domain.ErrInvalidArgument, "registered schema and bounded explicit target contract are required")
	}
	candidateApp := app
	candidateApp.SchemaVersion = in.SchemaVersion
	candidateApp.Contract = append([]domain.ApplicationContractField(nil), in.Contract...)
	candidateApp, err = normalizeApplication(candidateApp)
	if err != nil {
		return empty, err
	}
	if _, err = rs.GetConfigurationSchema(ctx, app.Name, app.ReleaseName, in.SchemaVersion); err != nil {
		return empty, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpConfigurationReleaseCreate, domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: app.ReleaseName})
	if err != nil {
		return empty, err
	}
	ctx, _, err = s.authorize(ctx, pr, domain.OpConfigurationReleaseActivate, domain.ResourceConfigurationRelease, domain.Ref{NS: in.Namespace, Key: app.ReleaseName})
	if err != nil {
		return empty, err
	}
	source, err := rs.GetActiveConfigurationRelease(ctx, in.Namespace, app.ReleaseName)
	if errors.Is(err, domain.ErrNotFound) {
		return empty, domain.Errorf(domain.ErrFailedPrecondition, "an active release is required for migration")
	}
	if err != nil {
		return empty, err
	}
	if (in.ExpectedSourceVersion == nil) != (in.ExpectedSourceActivationRevision == nil) {
		return empty, domain.Errorf(domain.ErrInvalidArgument, "expected source version and activation revision must be supplied together")
	}
	if in.ExpectedSourceVersion != nil && (*in.ExpectedSourceVersion != source.Release.Version || *in.ExpectedSourceActivationRevision != source.ActivationRevision) {
		return empty, domain.Errorf(domain.ErrAborted, "source release changed; refresh the migration source")
	}
	metadata := in.Metadata
	if metadata == "" {
		metadata = source.Release.Metadata
	}
	metadata, err = validateReleaseMetadata(metadata)
	if err != nil {
		return empty, err
	}
	active := map[string]domain.ConfigurationReleaseEntry{}
	for _, e := range source.Release.Entries {
		active[e.Alias] = e
	}
	changes := map[string]domain.ApplicationMigrationChange{}
	fields := map[string]domain.ApplicationContractField{}
	for _, f := range candidateApp.Contract {
		fields[f.Alias] = f
	}
	for _, c := range in.Changes {
		f, exists := fields[c.Alias]
		if !exists {
			return empty, domain.Errorf(domain.ErrInvalidArgument, "change alias is not in target contract")
		}
		if _, exists = changes[c.Alias]; exists {
			return empty, domain.Errorf(domain.ErrInvalidArgument, "duplicate change alias")
		}
		if c.Value != nil && (f.Kind != domain.ReleaseEntryParameter || c.Version != 0) {
			return empty, domain.Errorf(domain.ErrInvalidArgument, "only parameter edits accept value and cannot select a version")
		}
		if c.ContentType != "" && (c.Value == nil || c.ContentType != f.ContentType) {
			return empty, domain.Errorf(domain.ErrInvalidArgument, "content_type must match target parameter edit")
		}
		if c.Key != "" {
			if err := keyutil.ValidateKey(c.Key); err != nil {
				return empty, domain.Errorf(domain.ErrInvalidArgument, "invalid resource key")
			}
			if c.Value == nil && c.Version == 0 {
				return empty, domain.Errorf(domain.ErrInvalidArgument, "resource key requires an exact version or parameter value")
			}
		}
		changes[c.Alias] = c
	}
	resources := []storage.MigrationResource{}
	for _, f := range candidateApp.Contract {
		c := changes[f.Alias]
		from := f.Alias
		if c.FromAlias != "" {
			from = c.FromAlias
		}
		old, hasOld := active[from]
		key := c.Key
		if key == "" {
			key = f.Alias
			if hasOld {
				key = old.Ref.Key
			}
		}
		version := c.Version
		if version == 0 && hasOld {
			version = old.Version
		}
		resources = append(resources, storage.MigrationResource{Kind: f.Kind, Key: key, Version: version, Write: c.Value != nil})
	}
	before, err := ms.ApplicationMigrationSnapshot(ctx, in.Namespace, resources...)
	if err != nil {
		return empty, err
	}
	if before.BaseDigest != base.BaseDigest {
		return empty, domain.Errorf(domain.ErrAborted, "migration state changed; preview again")
	}
	result := domain.ApplicationReleaseMigrationResult{
		ReleaseName:              app.ReleaseName,
		SourceVersion:            source.Release.Version,
		SourceActivationRevision: source.ActivationRevision,
		SchemaVersion:            in.SchemaVersion,
		DefinitionChanged:        app.SchemaVersion != candidateApp.SchemaVersion || !reflect.DeepEqual(app.Contract, candidateApp.Contract),
		Entries:                  []domain.ApplicationReleasePlanEntry{},
		Validation:               []domain.ReleaseValidationError{},
		AffectedEnvironments:     []domain.ApplicationMigrationEnvironment{},
	}
	environments, err := as.ListApplicationNamespaces(ctx, app.Name)
	if err != nil {
		return empty, err
	}
	for _, env := range environments {
		if env.Env == in.Namespace.Env {
			continue
		}
		a, e := rs.GetActiveConfigurationRelease(ctx, env.NamespaceRef, app.ReleaseName)
		if e != nil && !errors.Is(e, domain.ErrNotFound) {
			return empty, e
		}
		affected := domain.ApplicationMigrationEnvironment{Environment: env.Env}
		if e == nil {
			affected.ActiveVersion = a.Release.Version
			affected.SchemaVersion = a.Release.SchemaVersion
		}
		result.AffectedEnvironments = append(result.AffectedEnvironments, affected)
	}
	sort.Slice(result.AffectedEnvironments, func(i, j int) bool {
		return result.AffectedEnvironments[i].Environment < result.AffectedEnvironments[j].Environment
	})
	release := domain.ConfigurationRelease{Namespace: in.Namespace, Name: app.ReleaseName, SchemaVersion: in.SchemaVersion, Metadata: metadata, CreatedBy: pr.Identity.Name, Entries: []domain.ConfigurationReleaseEntry{}}
	overrides := map[string]releaseCandidateValue{}
	writes := []storage.MigrationParameterWrite{}
	writeKeys := map[string]bool{}
	used := map[string]bool{}
	for _, f := range candidateApp.Contract {
		c := changes[f.Alias]
		from := f.Alias
		if c.FromAlias != "" {
			from = c.FromAlias
		}
		old, hasOld := active[from]
		if c.FromAlias != "" && !hasOld {
			if in.Execute {
				return empty, domain.Errorf(domain.ErrAborted, "migration plan is stale; preview again")
			}
			return empty, domain.Errorf(domain.ErrInvalidArgument, "from_alias must name an active release alias")
		}
		entry := domain.ApplicationReleasePlanEntry{Alias: f.Alias, Kind: f.Kind, Source: "preserved"}
		if hasOld {
			entry.FromVersion = old.Version
			used[from] = true
		}
		key := c.Key
		if key == "" {
			key = f.Alias
			if hasOld {
				key = old.Ref.Key
			}
		}
		ref := domain.Ref{NS: in.Namespace, Key: key}
		entry.Ref = ref
		var pin domain.ConfigurationReleaseEntry
		if c.Value != nil {
			if len(*c.Value) > maxValueBytes {
				return empty, domain.Errorf(domain.ErrInvalidArgument, "parameter value exceeds size limit")
			}
			if err := validateParameterValue(*c.Value, f.ContentType); err != nil {
				return empty, err
			}
			if writeKeys[key] {
				return empty, domain.Errorf(domain.ErrInvalidArgument, "multiple edits target the same parameter key")
			}
			writeKeys[key] = true
			if _, _, err = s.authorize(ctx, pr, domain.OpParameterWrite, domain.ResourceParameter, ref); err != nil {
				return empty, err
			}
			v := before.ParameterNext[key]
			if v == 0 {
				v = 1
			}
			writes = append(writes, storage.MigrationParameterWrite{Alias: f.Alias, Key: key, Value: *c.Value, ContentType: f.ContentType, Version: v})
			overrides[f.Alias] = releaseCandidateValue{value: []byte(*c.Value), contentType: f.ContentType}
			pin = domain.ConfigurationReleaseEntry{Alias: f.Alias, Kind: f.Kind, Ref: ref, Version: v, ResourceNamespaceID: namespace.ID, ContentType: f.ContentType, Metadata: "{}", ParameterDigest: sha256Hex([]byte(*c.Value))}
			entry.Source = "edited"
			if !hasOld {
				entry.Source = "added"
			}
		} else if c.Version > 0 {
			_, pins, validation, e := s.resolveReleaseEntries(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: in.Namespace, Name: app.ReleaseName, Entries: []domain.ReleaseEntrySelector{{Alias: f.Alias, Kind: f.Kind, Ref: ref, Version: c.Version}}}, true)
			if e != nil {
				return empty, e
			}
			result.Validation = append(result.Validation, validation...)
			if len(pins) > 0 {
				pin = pins[0]
			}
			entry.Source = "pinned"
			if !hasOld {
				entry.Source = "added"
			}
		} else if hasOld && old.Kind == f.Kind {
			pin = old
			pin.Alias = f.Alias
			entry.Ref = pin.Ref
			if from != f.Alias {
				entry.Source = "renamed"
			}
		} else {
			entry.Source = "missing"
			result.Validation = append(result.Validation, domain.ReleaseValidationError{Alias: f.Alias, Code: domain.ReleaseValidationNotFound, Message: "target alias requires a parameter edit or exact existing resource reference"})
		}
		if pin.Alias != "" {
			if pin.Kind != f.Kind || (f.Kind == domain.ReleaseEntryParameter && pin.ContentType != f.ContentType) {
				result.Validation = append(result.Validation, domain.ReleaseValidationError{Alias: f.Alias, Code: domain.ReleaseValidationContract, Message: "entry does not match target contract"})
			}
			release.Entries = append(release.Entries, pin)
			entry.ToVersion = pin.Version
		}
		result.Entries = append(result.Entries, entry)
	}
	for _, old := range source.Release.Entries {
		if !used[old.Alias] {
			result.Entries = append(result.Entries, domain.ApplicationReleasePlanEntry{Alias: old.Alias, Kind: old.Kind, Ref: old.Ref, FromVersion: old.Version, Source: "removed"})
		}
	}
	if len(result.Validation) == 0 {
		result.Validation, err = s.validateReleaseValues(ctx, pr, rs, release, overrides, true)
		if err != nil {
			return empty, err
		}
	}
	if result.Validation == nil {
		result.Validation = []domain.ReleaseValidationError{}
	}
	result.Valid = len(result.Validation) == 0
	if result.Valid {
		release.Digest, err = releaseDigest(release)
		if err != nil {
			return empty, err
		}
	}
	after, err := ms.ApplicationMigrationSnapshot(ctx, in.Namespace, resources...)
	if err != nil {
		return empty, err
	}
	if before.Digest != after.Digest {
		return empty, domain.Errorf(domain.ErrAborted, "migration state changed; preview again")
	}
	// Hash canonical intended pins and values, target definition, source metadata,
	// and all state dependencies. Neither the result nor audit contains values.
	digest, err := json.Marshal(struct {
		State    string
		Contract []domain.ApplicationContractField
		Release  domain.ConfigurationRelease
		Writes   []storage.MigrationParameterWrite
		Result   domain.ApplicationReleaseMigrationResult
	}{before.Digest, candidateApp.Contract, release, writes, result}, json.Deterministic(true))
	if err != nil {
		return empty, err
	}
	result.PlanDigest = sha256Hex(digest)
	if !in.Execute {
		return result, nil
	}
	if in.PlanDigest == "" || in.PlanDigest != result.PlanDigest {
		return empty, domain.Errorf(domain.ErrAborted, "migration plan is stale; preview again")
	}
	if !result.Valid {
		return result, nil
	}
	audit := domain.AuditEvent{
		EventType:     "application.release.migrate",
		ActorIdentity: pr.Identity.Name, ActorType: pr.Identity.Kind,
		SourceIP: pr.RemoteAddr, UserAgent: pr.UserAgent, RequestID: pr.RequestID,
		ResourceType: domain.ResourceConfigurationRelease, ResourceNamespaceID: namespace.ID,
		ResourceEnv: in.Namespace.Env, ResourceApp: in.Namespace.App, ResourceKey: app.ReleaseName,
		Decision: "allow", Metadata: `{"operation":"schema_migration"}`,
	}
	migrated, err := ms.ApplyApplicationMigration(ctx, storage.ApplicationMigrationTransaction{
		Resources: resources, Namespace: in.Namespace, Snapshot: before.Digest,
		Contract: candidateApp.Contract, Release: release, Writes: writes,
		ExpectedActiveVersion: source.Release.Version, Audit: audit,
	})
	if err != nil {
		return empty, err
	}
	result.Executed = true
	result.Release = &migrated.Release
	result.Activation = &domain.ShipActivation{ActivationRevision: migrated.ActivationRevision, PreviousVersion: migrated.PreviousVersion, Changed: true}
	s.getHub().Wake()
	s.notifyReleaseSubscribers(in.Namespace, app.ReleaseName)
	return result, nil
}
