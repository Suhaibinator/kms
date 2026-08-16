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
	schema, err := service.CreateConfigurationSchema(ctx, admin, "payments/runtime", `{"type":"object","properties":{"runtime":{"type":"integer"}},"required":["runtime"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApplication(ctx, admin, domain.Application{
		Name: "payments", ReleaseName: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version,
		Contract: []domain.ApplicationContractField{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "integer"}},
	})
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
			Namespace: ns, Name: app.ReleaseName, SchemaID: schema.ID, SchemaVersion: schema.Version,
			Entries: []domain.ReleaseEntrySelector{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, Ref: ref}},
		}); err != nil {
			t.Fatalf("create %s release: %v", env, err)
		}
	}
	dev := domain.NamespaceRef{Env: "dev", App: app.Name}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: "other", SchemaID: schema.ID, SchemaVersion: schema.Version,
		Entries: []domain.ReleaseEntrySelector{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "config/runtime"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("wrong release name error = %v", err)
	}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version,
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
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: dev, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: dev, Key: "settings"}}},
	}); err != nil {
		t.Fatal(err)
	}
	app, err := service.GetApplication(ctx, admin, "worker")
	if err != nil || len(app.Contract) != 1 || app.Contract[0].Alias != "settings" || app.Contract[0].ContentType != "integer" {
		t.Fatalf("adopted application = %+v err=%v", app, err)
	}
	prod := domain.NamespaceRef{Env: "prod", App: "worker"}
	if _, err := service.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: prod, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "other", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: prod, Key: "settings"}}},
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("divergent second environment error = %v", err)
	}
}
