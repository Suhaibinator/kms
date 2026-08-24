package core

import (
	"context"
	"errors"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// maxOverviewEnvironments bounds the named overview; larger applications must
// ask for one environment at a time.
const maxOverviewEnvironments = 64

// maxOverviewAcks bounds the acknowledgement rows folded per environment.
const maxOverviewAcks = 1000

// OverviewOptions tunes GetApplicationOverview / GetFleetOverview.
type OverviewOptions struct {
	// Environments restricts the named overview to these environments (all
	// when empty). Every named environment must exist.
	Environments []string
	// InsecureListener reports the transport's TLS state so the overview can
	// carry the insecure_listener finding; the HTTP layer sets it.
	InsecureListener bool
}

// environmentReleaseFacts is what the overview reads per environment.
type environmentReleaseFacts struct {
	Active        *domain.ActiveConfigurationRelease
	Latest        *domain.ConfigurationRelease
	LatestVersion uint64
	Count         uint64
	Acks          []domain.ReleaseAcknowledgement
}

func (s *Service) loadEnvironmentReleaseFacts(ctx context.Context, rs storage.ReleaseStore, ns domain.NamespaceRef, releaseName string, withAcks bool) (environmentReleaseFacts, error) {
	var facts environmentReleaseFacts
	active, err := rs.GetActiveConfigurationRelease(ctx, ns, releaseName)
	switch {
	case err == nil:
		facts.Active = &active
	case errors.Is(err, domain.ErrNotFound):
	default:
		return facts, err
	}
	latest, _, err := rs.ListConfigurationReleases(ctx, ns, releaseName, storage.ListPage{Limit: 1})
	if err != nil {
		return facts, err
	}
	if len(latest) > 0 {
		release := latest[0].Release
		facts.Latest = &release
		facts.LatestVersion = release.Version
	}
	if facts.Count, err = rs.CountConfigurationReleases(ctx, ns, releaseName); err != nil {
		return facts, err
	}
	if withAcks {
		if facts.Acks, _, err = rs.ListReleaseAcknowledgements(ctx, ns, "", storage.ListPage{Limit: maxOverviewAcks}); err != nil {
			return facts, err
		}
	}
	return facts, nil
}

// loadApplicationSchema fetches the pinned schema; missing reports a pin the
// registry no longer has.
func (s *Service) loadApplicationSchema(ctx context.Context, rs storage.ReleaseStore, app domain.Application) (*domain.ConfigurationSchema, bool, error) {
	if app.SchemaID == "" {
		return nil, false, nil
	}
	schema, err := rs.GetConfigurationSchema(ctx, app.SchemaID, app.SchemaVersion)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &schema, false, nil
}

// secretStates reads the `current` version state of every contract secret
// that resolves to a present secret in ns.
func (s *Service) secretStates(ctx context.Context, app domain.Application, ns domain.NamespaceRef, refs map[string]domain.Ref, present map[string]domain.Secret, now time.Time) map[string]secretCurrentState {
	out := map[string]secretCurrentState{}
	for _, field := range app.Contract {
		if field.Kind != domain.ReleaseEntrySecret {
			continue
		}
		ref, ok := refs[field.Alias]
		if !ok || ref.NS != ns {
			continue
		}
		if _, ok := present[ref.Key]; !ok {
			continue
		}
		_, ver, err := s.store.GetSecretVersion(ctx, ref, 0, domain.LabelCurrent)
		if err != nil {
			continue
		}
		out[ref.Key] = secretCurrentState{State: ver.State, Expired: !ver.ExpiresAt.IsZero() && now.After(ver.ExpiresAt)}
	}
	return out
}

// activeReleaseValidation re-checks the active release's pins in memory so
// readiness can report stale pins. A release that no longer matches the
// contract is reported by readiness itself and is not re-validated here.
func (s *Service) activeReleaseValidation(ctx context.Context, pr Principal, rs storage.ReleaseStore, app domain.Application, active *domain.ActiveConfigurationRelease) []domain.ReleaseValidationError {
	if active == nil {
		return nil
	}
	if len(app.Contract) > 0 && !releaseMatchesContract(app.Contract, active.Release.Entries) {
		return nil
	}
	validation, err := s.validateReleaseEntries(ctx, pr, rs, active.Release, nil, false, false)
	if err != nil {
		return nil
	}
	return validation
}

// GetApplicationOverview is the console's application read model (§3.2): per
// environment values, release, rollout and findings, plus the matrix rows.
func (s *Service) GetApplicationOverview(ctx context.Context, pr Principal, name string, opts OverviewOptions) (domain.ApplicationOverview, error) {
	app, err := s.GetApplication(ctx, pr, name)
	if err != nil {
		return domain.ApplicationOverview{}, err
	}
	store, err := s.applicationStore()
	if err != nil {
		return domain.ApplicationOverview{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.ApplicationOverview{}, err
	}
	environments, err := store.ListApplicationNamespaces(ctx, name)
	if err != nil {
		return domain.ApplicationOverview{}, err
	}
	selected := environments
	if len(opts.Environments) > 0 {
		byEnv := make(map[string]domain.Namespace, len(environments))
		for _, ns := range environments {
			byEnv[ns.Env] = ns
		}
		selected = make([]domain.Namespace, 0, len(opts.Environments))
		seen := map[string]struct{}{}
		for _, env := range opts.Environments {
			if err := keyutil.ValidateEnv(env); err != nil {
				return domain.ApplicationOverview{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
			}
			if _, dup := seen[env]; dup {
				continue
			}
			seen[env] = struct{}{}
			ns, ok := byEnv[env]
			if !ok {
				return domain.ApplicationOverview{}, domain.Errorf(domain.ErrNotFound, "application %s has no environment %s", name, env)
			}
			selected = append(selected, ns)
		}
		if len(selected) > maxOverviewEnvironments {
			return domain.ApplicationOverview{}, domain.Errorf(domain.ErrFailedPrecondition, "at most %d environments per overview request", maxOverviewEnvironments)
		}
	} else if len(environments) > maxOverviewEnvironments {
		return domain.ApplicationOverview{}, domain.Errorf(domain.ErrFailedPrecondition, "application %s has %d environments; request one at a time with env=", name, len(environments))
	}
	rows, secretsByEnv, err := s.collectApplicationRows(ctx, selected)
	if err != nil {
		return domain.ApplicationOverview{}, err
	}
	schema, schemaMissing, err := s.loadApplicationSchema(ctx, rs, app)
	if err != nil {
		return domain.ApplicationOverview{}, err
	}
	now := s.now()
	facts := make(map[string]environmentReleaseFacts, len(environments))
	otherActive := map[string]domain.ConfigurationRelease{}
	for _, ns := range environments {
		f, err := s.loadEnvironmentReleaseFacts(ctx, rs, ns.NamespaceRef, app.ReleaseName, true)
		if err != nil {
			return domain.ApplicationOverview{}, err
		}
		facts[ns.Env] = f
		if f.Active != nil {
			otherActive[ns.Env] = f.Active.Release
		}
	}
	overview := domain.ApplicationOverview{Application: app, Environments: make([]domain.EnvironmentOverview, 0, len(selected)), Rows: rows}
	if schema != nil {
		overview.SchemaJSON = schema.Schema
	}
	for _, ns := range selected {
		f := facts[ns.Env]
		var activeRelease *domain.ConfigurationRelease
		if f.Active != nil {
			activeRelease = &f.Active.Release
		}
		refs := resolveContractRefs(app, ns.Env, activeRelease, f.Latest, otherActive, rows)
		in := environmentReadinessInput{
			App: app, Namespace: ns, Rows: rows, Refs: refs,
			Secrets:          s.secretStates(ctx, app, ns.NamespaceRef, refs, secretsByEnv[ns.Env], now),
			Active:           f.Active,
			ActiveValidation: s.activeReleaseValidation(ctx, pr, rs, app, f.Active),
			LatestVersion:    f.LatestVersion, ReleaseCount: f.Count, Acks: f.Acks,
			SchemaMissing: schemaMissing, Now: now,
		}
		overview.Environments = append(overview.Environments, computeEnvironmentReadiness(in))
	}
	overview.Status, overview.Findings = computeApplicationFindings(applicationReadinessInput{
		App: app, Environments: overview.Environments, Schema: schema, SchemaMissing: schemaMissing, InsecureListener: opts.InsecureListener,
	})
	return overview, nil
}

// GetFleetOverview is the cheap fleet form: status per application and per
// environment without subscriber or pin re-validation detail.
func (s *Service) GetFleetOverview(ctx context.Context, pr Principal, opts OverviewOptions) ([]domain.FleetApplication, error) {
	if err := s.requireAdmin(ctx, pr, "application.list", domain.ResourceApplication, "applications"); err != nil {
		return nil, err
	}
	store, err := s.applicationStore()
	if err != nil {
		return nil, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return nil, err
	}
	var apps []domain.Application
	token := ""
	for {
		page, next, err := store.ListApplications(ctx, storage.ListPage{Limit: 1000, Token: token})
		if err != nil {
			return nil, err
		}
		apps = append(apps, page...)
		if next == "" {
			break
		}
		token = next
	}
	now := s.now()
	out := make([]domain.FleetApplication, 0, len(apps))
	for _, app := range apps {
		environments, err := store.ListApplicationNamespaces(ctx, app.Name)
		if err != nil {
			return nil, err
		}
		rows, _, err := s.collectApplicationRows(ctx, environments)
		if err != nil {
			return nil, err
		}
		schema, schemaMissing, err := s.loadApplicationSchema(ctx, rs, app)
		if err != nil {
			return nil, err
		}
		facts := make(map[string]environmentReleaseFacts, len(environments))
		otherActive := map[string]domain.ConfigurationRelease{}
		for _, ns := range environments {
			f, err := s.loadEnvironmentReleaseFacts(ctx, rs, ns.NamespaceRef, app.ReleaseName, false)
			if err != nil {
				return nil, err
			}
			facts[ns.Env] = f
			if f.Active != nil {
				otherActive[ns.Env] = f.Active.Release
			}
		}
		envOverviews := make([]domain.EnvironmentOverview, 0, len(environments))
		for _, ns := range environments {
			f := facts[ns.Env]
			var activeRelease *domain.ConfigurationRelease
			if f.Active != nil {
				activeRelease = &f.Active.Release
			}
			refs := resolveContractRefs(app, ns.Env, activeRelease, f.Latest, otherActive, rows)
			envOverviews = append(envOverviews, computeEnvironmentReadiness(environmentReadinessInput{
				App: app, Namespace: ns, Rows: rows, Refs: refs, Active: f.Active,
				LatestVersion: f.LatestVersion, ReleaseCount: f.Count, SchemaMissing: schemaMissing, Now: now,
			}))
		}
		status, _ := computeApplicationFindings(applicationReadinessInput{App: app, Environments: envOverviews, Schema: schema, SchemaMissing: schemaMissing, InsecureListener: opts.InsecureListener})
		fleet := domain.FleetApplication{Application: app, Status: status, Environments: make([]domain.FleetEnvironment, 0, len(envOverviews))}
		for _, env := range envOverviews {
			fleet.Environments = append(fleet.Environments, domain.FleetEnvironment{Env: env.Namespace.Env, Status: env.Status, Production: env.Production})
		}
		out = append(out, fleet)
	}
	return out, nil
}
