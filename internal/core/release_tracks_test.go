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

func TestApplicationMetadataUpdatePreservesFirstReleaseContract(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app, err := svc.CreateApplication(ctx, adminPrincipal(), domain.Application{Name: "adoption", ReleaseName: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	app.Description = "before adoption"
	app, err = svc.UpdateApplication(ctx, adminPrincipal(), app)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := st.GetConfigurationSchemaContract(ctx, app.Name, app.ReleaseName, 0)
	if err != nil || contract != nil {
		t.Fatalf("metadata update prematurely adopted empty contract: %#v %v", contract, err)
	}
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	if _, err := svc.CreateNamespace(ctx, adminPrincipal(), ns, "", nil); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "setting"}
	if _, _, err := svc.PutParameter(ctx, adminPrincipal(), ref, "1", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateConfigurationRelease(ctx, adminPrincipal(), domain.CreateConfigurationReleaseInput{Namespace: ns, Name: app.ReleaseName, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: ref}}}); err != nil {
		t.Fatal(err)
	}
	app, err = st.GetApplication(ctx, app.Name)
	if err != nil {
		t.Fatal(err)
	}
	app.Description = "updated"
	updated, err := svc.UpdateApplication(ctx, adminPrincipal(), app)
	if err != nil || updated.Description != "updated" || len(updated.Contract) != 1 || updated.SchemaVersion != 0 {
		t.Fatalf("metadata update: %+v %v", updated, err)
	}
}

type applicationDefinitionRaceStore struct {
	*storage.SQLStore
	beforeUpdate func()
}

func (s *applicationDefinitionRaceStore) UpdateApplication(ctx context.Context, app domain.Application) (domain.Application, error) {
	s.beforeUpdate()
	return s.SQLStore.UpdateApplication(ctx, app)
}
func TestApplicationMetadataUpdateRejectsConcurrentRepin(t *testing.T) {
	for _, aba := range []bool{false, true} {
		t.Run(map[bool]string{false: "repin", true: "aba"}[aba], func(t *testing.T) {
			ctx := context.Background()
			svc, st := newConsoleTestService(t)
			admin := adminPrincipal()
			app := seedConsoleApp(t, svc, admin, "dev")
			schema, err := svc.CreateConfigurationSchema(ctx, admin, app.Name, `{"type":"object","description":"concurrent"}`, "{}")
			if err != nil {
				t.Fatal(err)
			}
			original := app
			svc.store = &applicationDefinitionRaceStore{SQLStore: st, beforeUpdate: func() {
				app.SchemaVersion = schema.Version
				if _, err := st.UpdateApplication(ctx, app); err != nil {
					t.Fatal(err)
				}
				if aba {
					if _, err := st.UpdateApplication(ctx, original); err != nil {
						t.Fatal(err)
					}
				}
			}}
			original.Description = "metadata update"
			if _, err := svc.UpdateApplication(ctx, admin, original); !errors.Is(err, domain.ErrAborted) {
				t.Fatalf("metadata update overwrote concurrent definition: %v", err)
			}
			persisted, err := st.GetApplication(ctx, app.Name)
			want := schema.Version
			if aba {
				want = original.SchemaVersion
			}
			if err != nil || persisted.SchemaVersion != want {
				t.Fatalf("lost racing definition: %+v %v", persisted, err)
			}
		})
	}
}
