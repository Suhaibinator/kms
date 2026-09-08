package core

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestReleaseSchemaResolutionUsesReleaseReadPermission(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal(), "dev")
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	schema, err := st.GetConfigurationSchema(ctx, app.Name, app.ReleaseName, app.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	pr := clientPrincipal("reader")
	if _, err := svc.ResolveReleaseSchema(ctx, pr, ns, app.ReleaseName, schema.Digest); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("unauthorized resolve: %v", err)
	}
	if _, err := st.CreatePolicy(ctx, domain.Policy{Name: "reader", Subject: "reader", Allow: []domain.PolicyRule{{Operation: domain.OpConfigurationReleaseRead, Env: ns.Env, App: ns.App}}}); err != nil {
		t.Fatal(err)
	}
	version, err := svc.ResolveReleaseSchema(ctx, pr, ns, app.ReleaseName, schema.Digest)
	if err != nil || version != app.SchemaVersion {
		t.Fatalf("read-only resolver: %d %v", version, err)
	}
	if _, err := svc.GetConfigurationSchema(ctx, pr, app.Name, app.ReleaseName, version); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("resolver granted registry admin access: %v", err)
	}
	if _, err := svc.GetActiveConfigurationRelease(ctx, pr, applicationTrack(app, ns)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("known inactive track: %v", err)
	}
}

func TestManagementSelectionDoesNotRepinApplication(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal(), "dev")
	newer, err := svc.CreateConfigurationSchema(ctx, adminPrincipal(), app.Name, `{"type":"object","x-kms-contract":[{"alias":"extra","kind":"parameter","content_type":"string"}]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	overview, err := svc.GetApplicationOverview(ctx, adminPrincipal(), app.Name, OverviewOptions{})
	if err != nil || overview.Application.SchemaVersion != newer.Version || len(overview.Application.Contract) != 1 || overview.Application.Contract[0].Alias != "extra" {
		t.Fatalf("latest overview: %+v %v", overview.Application, err)
	}
	old, err := svc.GetApplicationOverview(ctx, adminPrincipal(), app.Name, OverviewOptions{SchemaVersion: &app.SchemaVersion})
	if err != nil || old.Application.SchemaVersion != app.SchemaVersion || len(old.Application.Contract) != len(app.Contract) {
		t.Fatalf("old overview: %+v %v", old.Application, err)
	}
	persisted, err := st.GetApplication(ctx, app.Name)
	if err != nil || persisted.SchemaVersion != app.SchemaVersion {
		t.Fatalf("selector repinned app: %+v %v", persisted, err)
	}
	_, _, err = svc.ListConfigurationReleases(ctx, adminPrincipal(), domain.ReleaseFilter{Namespace: domain.NamespaceRef{Env: "dev", App: app.Name}, Name: app.ReleaseName}, storage.ListPage{})
	if err != nil {
		t.Fatal(err)
	}
}
