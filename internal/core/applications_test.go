package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestApplicationContractIsSharedAcrossEnvironmentReleases(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service := New(store, nil, "test")
	admin := adminPrincipal()
	app, schema, err := service.CreateApplicationWithSchema(ctx, admin, domain.Application{
		Name: "payments", ReleaseName: "runtime",
		Contract: []domain.ApplicationContractField{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "integer"}},
	}, `{"type":"object","properties":{"runtime":{"type":"integer"}},"required":["runtime"],"additionalProperties":false}`, "{}")
	if err != nil || app.Name != "payments" {
		t.Fatalf("create application = %+v err=%v", app, err)
	}
	for _, env := range []string{"dev", "prod"} {
		ns := domain.NamespaceRef{Env: env, App: app.Name}
		if _, err := service.CreateNamespace(ctx, admin, ns, "", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
			t.Fatal(err)
		}
		ref := domain.Ref{NS: ns, Key: "config/runtime"}
		if _, _, err := service.PutParameter(ctx, admin, ref, "7", "integer", "{}"); err != nil {
			t.Fatal(err)
		}
		if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
			Namespace: ns, Name: app.ReleaseName, SchemaVersion: schema.Version,
			Entries: []domain.ReleaseEntrySelector{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, Ref: ref}},
		}); err != nil {
			t.Fatalf("create %s release: %v", env, err)
		}
	}
	dev := domain.NamespaceRef{Env: "dev", App: app.Name}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: app.ReleaseName,
		Entries: []domain.ReleaseEntrySelector{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "config/runtime"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("release schema unpin error = %v, want FailedPrecondition", err)
	}
	secondSchema, err := service.CreateConfigurationSchema(ctx, admin, app.Name, `{"type":"object","description":"version two"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: app.ReleaseName, SchemaVersion: secondSchema.Version,
		Entries: []domain.ReleaseEntrySelector{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "config/runtime"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("release schema repin error = %v, want FailedPrecondition", err)
	}
	unchanged, err := service.GetApplication(ctx, admin, app.Name)
	if err != nil || unchanged.SchemaVersion != schema.Version {
		t.Fatalf("release attempts changed application pin = %+v err=%v", unchanged, err)
	}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: "other", SchemaVersion: schema.Version,
		Entries: []domain.ReleaseEntrySelector{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "config/runtime"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("wrong release name error = %v", err)
	}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: "runtime", SchemaVersion: schema.Version,
		Entries: []domain.ReleaseEntrySelector{{Alias: "different", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "config/runtime"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("wrong alias error = %v", err)
	}
}

func TestApplicationDashboardAndMultiEnvironmentParameterWrite(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service := New(store, nil, "test")
	admin := adminPrincipal()
	if _, err := service.CreateApplication(ctx, admin, domain.Application{Name: "api", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "prod-gcp"} {
		if _, err := service.CreateNamespace(ctx, admin, domain.NamespaceRef{Env: env, App: "api"}, "", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
			t.Fatal(err)
		}
	}
	results, err := service.PutApplicationParameter(ctx, admin, "api", "rate-limit", "100", "integer", "{}", []string{"dev", "prod-gcp"})
	if err != nil || len(results) != 2 || results[0].Error != "" || results[1].Error != "" {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	dashboard, err := service.GetApplicationDashboard(ctx, admin, "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Environments) != 2 || len(dashboard.Rows) != 1 || dashboard.Rows[0].Cells["prod-gcp"].Value != "100" {
		t.Fatalf("dashboard = %+v", dashboard)
	}
}

func TestFirstReleaseAdoptsApplicationContract(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service := New(store, nil, "test")
	admin := adminPrincipal()
	if _, err := service.CreateApplication(ctx, admin, domain.Application{Name: "worker", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "prod"} {
		ns := domain.NamespaceRef{Env: env, App: "worker"}
		if _, err := service.CreateNamespace(ctx, admin, ns, "", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.PutParameter(ctx, admin, domain.Ref{NS: ns, Key: "settings"}, "1", "integer", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	dev := domain.NamespaceRef{Env: "dev", App: "worker"}
	schema, err := service.CreateConfigurationSchema(ctx, admin, "worker", `{"type":"object"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: "runtime", SchemaVersion: schema.Version,
		Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "settings"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("release creation repin error = %v, want FailedPrecondition", err)
	}
	before, err := service.GetApplication(ctx, admin, "worker")
	if err != nil || before.SchemaVersion != 0 || len(before.Contract) != 0 {
		t.Fatalf("rejected release mutated application = %+v err=%v", before, err)
	}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "settings"}}},
	}); err != nil {
		t.Fatal(err)
	}
	app, err := service.GetApplication(ctx, admin, "worker")
	if err != nil || len(app.Contract) != 1 || app.Contract[0].Alias != "settings" || app.Contract[0].ContentType != "integer" {
		t.Fatalf("adopted application = %+v err=%v", app, err)
	}
	if app.SchemaVersion != 0 {
		t.Fatalf("first release unexpectedly pinned schema version %d", app.SchemaVersion)
	}
	prod := domain.NamespaceRef{Env: "prod", App: "worker"}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: prod, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "other", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: prod, Key: "settings"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("divergent second environment error = %v", err)
	}
}

func TestResolveContractRefs(t *testing.T) {
	app := domain.Application{Name: "worker", Contract: []domain.ApplicationContractField{
		{Alias: "from_active", Kind: domain.ReleaseEntryParameter, ContentType: "string"},
		{Alias: "from_latest", Kind: domain.ReleaseEntryParameter, ContentType: "string"},
		{Alias: "by_key", Kind: domain.ReleaseEntryParameter, ContentType: "string"},
		{Alias: "by_key_wrong_kind", Kind: domain.ReleaseEntrySecret},
		{Alias: "from_other_env", Kind: domain.ReleaseEntryParameter, ContentType: "string"},
		{Alias: "unresolved", Kind: domain.ReleaseEntryParameter, ContentType: "string"},
	}}
	prod := domain.NamespaceRef{Env: "prod", App: "worker"}
	shared := domain.NamespaceRef{Env: "shared", App: "worker"}
	active := &domain.ConfigurationRelease{Entries: []domain.ConfigurationReleaseEntry{
		{Alias: "from_active", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: shared, Key: "active-key"}},
	}}
	latest := &domain.ConfigurationRelease{Entries: []domain.ConfigurationReleaseEntry{
		{Alias: "from_active", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: prod, Key: "should-lose-to-active"}},
		{Alias: "from_latest", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: prod, Key: "latest-key"}},
	}}
	rows := []domain.ApplicationConfigurationRow{
		{Key: "by_key", Kind: domain.ResourceParameter, Cells: map[string]domain.ApplicationConfigurationCell{"prod": {Present: true}}},
		{Key: "by_key_wrong_kind", Kind: domain.ResourceParameter, Cells: map[string]domain.ApplicationConfigurationCell{"prod": {Present: true}}},
		{Key: "from_other_env", Kind: domain.ResourceParameter, Cells: map[string]domain.ApplicationConfigurationCell{"dev": {Present: true}}},
	}
	otherActive := map[string]domain.ConfigurationRelease{
		"prod":    {Entries: []domain.ConfigurationReleaseEntry{{Alias: "unresolved", Ref: domain.Ref{NS: prod, Key: "must-not-use-own-env"}}}},
		"staging": {Entries: []domain.ConfigurationReleaseEntry{{Alias: "from_other_env", Ref: domain.Ref{NS: domain.NamespaceRef{Env: "staging", App: "worker"}, Key: "staging-key"}}}},
		"dev":     {Entries: []domain.ConfigurationReleaseEntry{{Alias: "from_other_env", Ref: domain.Ref{NS: domain.NamespaceRef{Env: "dev", App: "worker"}, Key: "dev-key"}}}},
	}
	got := resolveContractRefs(app, "prod", active, latest, otherActive, rows)
	want := map[string]domain.Ref{
		"from_active":    {NS: shared, Key: "active-key"},
		"from_latest":    {NS: prod, Key: "latest-key"},
		"by_key":         {NS: prod, Key: "by_key"},
		"from_other_env": {NS: prod, Key: "dev-key"}, // sorted env order: dev before staging
	}
	if len(got) != len(want) {
		t.Fatalf("resolved = %+v, want %+v", got, want)
	}
	for alias, ref := range want {
		if got[alias] != ref {
			t.Fatalf("alias %s = %+v, want %+v", alias, got[alias], ref)
		}
	}
	if _, ok := got["unresolved"]; ok {
		t.Fatalf("unresolved alias must be absent: %+v", got)
	}
	if _, ok := got["by_key_wrong_kind"]; ok {
		t.Fatalf("key of a different kind must not resolve: %+v", got)
	}
	if got := resolveContractRefs(app, "prod", nil, nil, nil, nil); len(got) != 0 {
		t.Fatalf("nothing to resolve from: %+v", got)
	}
}

func TestApplicationSchemaUploadIsRegistrationOnly(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service := New(store, nil, "test")
	admin := adminPrincipal()

	if _, err := service.CreateApplication(ctx, admin, domain.Application{
		Name: "non-atomic", ReleaseName: "runtime", SchemaVersion: 1,
	}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("non-atomic create with schema pin error = %v", err)
	}
	if _, err := service.GetApplication(ctx, admin, "non-atomic"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rejected non-atomic create left application behind: %v", err)
	}
	if _, _, err := service.CreateApplicationWithSchema(ctx, admin,
		domain.Application{Name: "payments", ReleaseName: "runtime"},
		`{"type":"definitely-not-a-real-type"}`, "{}",
	); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid atomic schema error = %v", err)
	}
	if _, err := service.GetApplication(ctx, admin, "payments"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid atomic create left application behind: %v", err)
	}

	app, first, err := service.CreateApplicationWithSchema(ctx, admin,
		domain.Application{Name: "payments", ReleaseName: "runtime"},
		`{"type":"object"}`, "{}",
	)
	if err != nil || app.SchemaVersion != 1 || first.Application != app.Name || first.ReleaseName != app.ReleaseName {
		t.Fatalf("atomic create = app:%+v schema:%+v err=%v", app, first, err)
	}
	second, err := service.CreateConfigurationSchema(ctx, admin, app.Name, `{"type":"string"}`, "{}")
	if err != nil || second.Version != 2 || second.Application != app.Name || second.ReleaseName != app.ReleaseName {
		t.Fatalf("schema upload = %+v err=%v", second, err)
	}
	unchanged, err := service.GetApplication(ctx, admin, app.Name)
	if err != nil || unchanged.SchemaVersion != first.Version {
		t.Fatalf("schema upload repinned application: %+v err=%v", unchanged, err)
	}
	if _, err := service.CreateConfigurationSchema(ctx, admin, app.Name, `{"type":"string"}`, "{}"); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate schema upload error = %v, want AlreadyExists", err)
	}

	unchanged.Description = "updated"
	unchanged.SchemaVersion = second.Version
	if _, err := service.UpdateApplication(ctx, admin, unchanged); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("direct schema repin error = %v", err)
	}
	unchanged.SchemaVersion = first.Version
	unchanged.ReleaseName = "other"
	if _, err := service.UpdateApplication(ctx, admin, unchanged); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("release lineage move error = %v", err)
	}
	unchanged.ReleaseName = "runtime"
	updated, err := service.UpdateApplication(ctx, admin, unchanged)
	if err != nil || updated.Description != "updated" || updated.SchemaVersion != first.Version {
		t.Fatalf("ordinary application update = %+v err=%v", updated, err)
	}
}

func TestArchivedApplicationRejectsMutationsUntilUnarchived(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service := New(store, nil, "test")
	admin := adminPrincipal()
	app, _, err := service.CreateApplicationWithSchema(ctx, admin,
		domain.Application{Name: "payments", ReleaseName: "runtime"}, `{"type":"object"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	archived, err := service.ArchiveApplication(ctx, admin, app.Name)
	if err != nil || archived.ArchivedAt.IsZero() || archived.ArchivedBy != admin.Identity.Name {
		t.Fatalf("archive = %+v err=%v", archived, err)
	}
	if listed, _, err := service.ListApplications(ctx, admin, storage.ListPage{}); err != nil || len(listed) != 0 {
		t.Fatalf("default application list = %+v err=%v", listed, err)
	}
	if listed, _, err := service.ListApplicationsFiltered(ctx, admin, storage.ListPage{}, storage.ApplicationsArchivedOnly); err != nil || len(listed) != 1 || listed[0].Name != app.Name {
		t.Fatalf("archived application list = %+v err=%v", listed, err)
	}
	if _, err := service.CreateConfigurationSchema(ctx, admin, app.Name, `{"type":"string"}`, "{}"); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("schema upload to archived application error = %v", err)
	}
	archived.Description = "forbidden"
	if _, err := service.UpdateApplication(ctx, admin, archived); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("archived application update error = %v", err)
	}
	if _, err := service.CreateNamespace(ctx, admin, domain.NamespaceRef{Env: "prod", App: app.Name}, "", nil); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("archived environment create error = %v", err)
	}
	if _, err := service.ShipApplicationChange(ctx, admin, domain.ShipInput{Application: app.Name, Environment: "prod", DryRun: true}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("archived ship error = %v", err)
	}
	if _, err := service.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{
		Namespace: domain.NamespaceRef{Env: "prod", App: app.Name}, Artifact: consoleDefaultsArtifact(t, `{}`, "1"),
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("archived defaults error = %v", err)
	}
	unarchived, err := service.UnarchiveApplication(ctx, admin, app.Name)
	if err != nil || !unarchived.ArchivedAt.IsZero() {
		t.Fatalf("unarchive = %+v err=%v", unarchived, err)
	}
	if _, err := service.CreateConfigurationSchema(ctx, admin, app.Name, `{"type":"string"}`, "{}"); err != nil {
		t.Fatalf("schema upload after unarchive: %v", err)
	}
	if _, err := service.CreateNamespace(ctx, admin, domain.NamespaceRef{Env: "prod", App: app.Name}, "", nil); err != nil {
		t.Fatalf("environment create after unarchive: %v", err)
	}
	if _, err := service.ArchiveApplication(ctx, admin, app.Name); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("archive with environment error = %v", err)
	}
}
