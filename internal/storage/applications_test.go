package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestApplicationLifecycleAndEnvironmentOwnership(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	created, err := store.CreateApplication(ctx, domain.Application{
		Name: "payments-api", Description: "Payments", ReleaseName: "runtime",
		Contract:  []domain.ApplicationContractField{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "json"}},
		CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "payments-api" || len(created.Contract) != 1 {
		t.Fatalf("created = %+v", created)
	}
	for _, env := range []string{"dev", "prod-gcp"} {
		if _, err := store.CreateNamespace(ctx, domain.Namespace{Env: env, App: created.Name, CreatedBy: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetApplication(ctx, created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvironmentCount != 2 {
		t.Fatalf("environment count = %d, want 2", got.EnvironmentCount)
	}
	namespaces, err := store.ListApplicationNamespaces(ctx, created.Name)
	if err != nil || len(namespaces) != 2 || namespaces[0].Env != "dev" || namespaces[1].Env != "prod-gcp" {
		t.Fatalf("namespaces = %+v, err=%v", namespaces, err)
	}
	apps, next, err := store.ListApplications(ctx, ListPage{})
	if err != nil || next != "" || len(apps) != 1 || apps[0].Name != "payments-api" || apps[0].Description != "Payments" || apps[0].ReleaseName != "runtime" || len(apps[0].Contract) != 1 || apps[0].CreatedBy != "admin" || apps[0].EnvironmentCount != 2 {
		t.Fatalf("applications = %+v next=%q err=%v", apps, next, err)
	}
	if err := store.DeleteApplication(ctx, created.Name); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("delete non-empty application = %v", err)
	}
}

func TestCreateNamespaceEnsuresApplication(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if _, err := store.CreateNamespace(ctx, domain.Namespace{Env: "dev", App: "worker", CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetApplication(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if app.ReleaseName != "runtime" || app.EnvironmentCount != 1 || len(app.Contract) != 0 {
		t.Fatalf("inferred application = %+v", app)
	}
}
