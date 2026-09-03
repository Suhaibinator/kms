package storage

import (
	"context"
	"errors"
	"sync"
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
	apps, next, err := store.ListApplications(ctx, ListPage{}, ApplicationsActiveOnly)
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

func TestApplicationSchemaOwnershipAndLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	app, first, err := store.CreateApplicationWithSchema(ctx,
		domain.Application{Name: "payments", ReleaseName: "runtime", CreatedBy: "admin"},
		domain.ConfigurationSchema{Application: "payments", ReleaseName: "runtime", Schema: `{"type":"object"}`, Digest: "digest-one", Metadata: "{}", CreatedBy: "admin"},
	)
	if err != nil || app.SchemaVersion != 1 || first.Version != 1 {
		t.Fatalf("atomic application/schema = app:%+v schema:%+v err=%v", app, first, err)
	}
	if _, err := store.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{
		Application: "payments", ReleaseName: "runtime", Schema: first.Schema, Digest: first.Digest, Metadata: "{}",
	}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate schema digest error = %v, want AlreadyExists", err)
	}
	second, err := store.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{
		Application: "payments", ReleaseName: "runtime", Schema: `{"type":"string"}`, Digest: "digest-two", Metadata: "{}",
	})
	if err != nil || second.Version != 2 {
		t.Fatalf("second schema = %+v err=%v", second, err)
	}
	if _, err := store.CreateApplication(ctx, domain.Application{Name: "worker", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	ownedSeparately, err := store.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{
		Application: "worker", ReleaseName: "runtime", Schema: first.Schema, Digest: first.Digest, Metadata: "{}",
	})
	if err != nil || ownedSeparately.Version != 1 {
		t.Fatalf("same document in another application = %+v err=%v", ownedSeparately, err)
	}
	if _, err := store.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{
		Application: "missing", ReleaseName: "runtime", Schema: `{}`, Digest: "missing", Metadata: "{}",
	}); err == nil {
		t.Fatal("schema with no owning application was accepted")
	}
	if err := store.DeleteApplication(ctx, "payments"); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("delete schema owner error = %v", err)
	}

	archived, err := store.ArchiveApplication(ctx, "payments", "operator")
	if err != nil || archived.ArchivedAt.IsZero() || archived.ArchivedBy != "operator" {
		t.Fatalf("archived application = %+v err=%v", archived, err)
	}
	active, _, err := store.ListApplications(ctx, ListPage{}, ApplicationsActiveOnly)
	if err != nil || len(active) != 1 || active[0].Name != "worker" {
		t.Fatalf("active applications = %+v err=%v", active, err)
	}
	archivedOnly, _, err := store.ListApplications(ctx, ListPage{}, ApplicationsArchivedOnly)
	if err != nil || len(archivedOnly) != 1 || archivedOnly[0].Name != "payments" {
		t.Fatalf("archived applications = %+v err=%v", archivedOnly, err)
	}
	if _, err := store.CreateNamespace(ctx, domain.Namespace{NamespaceRef: domain.NamespaceRef{Env: "prod", App: "payments"}}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("create environment for archived application error = %v", err)
	}
	unarchived, err := store.UnarchiveApplication(ctx, "payments")
	if err != nil || !unarchived.ArchivedAt.IsZero() || unarchived.ArchivedBy != "" {
		t.Fatalf("unarchived application = %+v err=%v", unarchived, err)
	}
	if _, err := store.CreateNamespace(ctx, domain.Namespace{NamespaceRef: domain.NamespaceRef{Env: "prod", App: "payments"}}); err != nil {
		t.Fatalf("create environment after unarchive: %v", err)
	}
	if _, err := store.ArchiveApplication(ctx, "payments", "operator"); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("archive application with environment error = %v", err)
	}
	if _, err := store.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{
		Namespace: domain.NamespaceRef{Env: "prod", App: "payments"}, Name: "runtime",
		SchemaVersion: unarchived.SchemaVersion, Digest: "release-digest", Metadata: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNamespace(ctx, domain.NamespaceRef{Env: "prod", App: "payments"}); err != nil {
		t.Fatalf("delete released environment: %v", err)
	}
	archived, err = store.ArchiveApplication(ctx, "payments", "operator")
	if err != nil || archived.ArchivedAt.IsZero() {
		t.Fatalf("archive released application after environment retirement = %+v err=%v", archived, err)
	}
}

func TestCreateApplicationWithSchemaRollsBackOnOwnershipFailure(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if _, _, err := store.CreateApplicationWithSchema(ctx,
		domain.Application{Name: "payments", ReleaseName: "runtime"},
		domain.ConfigurationSchema{Application: "somebody-else", ReleaseName: "runtime", Schema: `{}`, Digest: "digest", Metadata: "{}"},
	); err == nil {
		t.Fatal("atomic create accepted a schema owned by another application")
	}
	if _, err := store.GetApplication(ctx, "payments"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed atomic create left application behind: %v", err)
	}
}

func TestConfigurationSchemaConcurrentDuplicateDigest(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if _, err := store.CreateApplication(ctx, domain.Application{Name: "payments", ReleaseName: "runtime"}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{
				Application: "payments", ReleaseName: "runtime", Schema: `{"type":"object"}`,
				Digest: "same-digest", Metadata: "{}",
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, duplicate := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrAlreadyExists):
			duplicate++
		default:
			t.Fatalf("concurrent schema upload error = %v", err)
		}
	}
	if succeeded != 1 || duplicate != 1 {
		t.Fatalf("concurrent outcomes: succeeded=%d duplicate=%d", succeeded, duplicate)
	}
	rows, _, err := store.ListConfigurationSchemas(ctx, "payments", "runtime", ListPage{})
	if err != nil || len(rows) != 1 || rows[0].Version != 1 {
		t.Fatalf("schema history after duplicate race = %+v err=%v", rows, err)
	}
}
