package core

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestListReleaseSchemaVersionsPermissionAndPagination(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal(), "dev", "prod")
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	newest, err := svc.CreateConfigurationSchema(ctx, adminPrincipal(), app.Name, `{"type":"object","description":"inactive newest"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	reader := clientPrincipal("discovery-reader")
	reader.Identity.Namespace = &ns // Home read/watch grants must not imply release-list access.
	if _, _, err := svc.ListReleaseSchemaVersions(ctx, reader, ns, app.ReleaseName, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("ungranted discovery: %v", err)
	}
	if _, err := st.CreatePolicy(ctx, domain.Policy{Name: "discovery-list", Subject: reader.Identity.Name, Allow: []domain.PolicyRule{{Operation: domain.OpConfigurationReleaseList, Env: ns.Env, App: ns.App}}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{app.ReleaseName, ""} {
		versions, next, err := svc.ListReleaseSchemaVersions(ctx, reader, ns, name, storage.ListPage{Limit: 1})
		if err != nil || !reflect.DeepEqual(versions, []uint64{newest.Version}) || next == "" {
			t.Fatalf("first page %q: %v %q %v", name, versions, next, err)
		}
		versions, next, err = svc.ListReleaseSchemaVersions(ctx, reader, ns, name, storage.ListPage{Limit: 1, Token: next})
		if err != nil || !reflect.DeepEqual(versions, []uint64{app.SchemaVersion}) || next != "" {
			t.Fatalf("second page %q: %v %q %v", name, versions, next, err)
		}
		if _, _, err := svc.ListConfigurationReleases(ctx, reader, domain.ReleaseFilter{Namespace: ns, Name: name}, storage.ListPage{}); err != nil {
			t.Fatalf("release-list parity: %v", err)
		}
	}
	if _, _, err := svc.ListConfigurationSchemas(ctx, reader, app.Name, app.ReleaseName, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("registry permission widened: %v", err)
	}
	for _, other := range []domain.NamespaceRef{{Env: "prod", App: ns.App}, {Env: ns.Env, App: "other"}} {
		versions, _, err := svc.ListReleaseSchemaVersions(ctx, reader, other, app.ReleaseName, storage.ListPage{})
		if err == nil || len(versions) != 0 {
			t.Fatalf("namespace isolation %v: %v %v", other, versions, err)
		}
	}
	versions, _, err := svc.ListReleaseSchemaVersions(ctx, reader, ns, "unknown", storage.ListPage{})
	if err != nil || versions == nil || len(versions) != 0 {
		t.Fatalf("unknown release name: %v %v", versions, err)
	}
	if _, _, err := svc.ListReleaseSchemaVersions(ctx, reader, ns, app.ReleaseName, storage.ListPage{Token: "invalid"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid cursor: %v", err)
	}
	if _, err := st.UpdateNamespace(ctx, ns, "", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
		t.Fatal(err)
	}
	versions, _, err = svc.ListReleaseSchemaVersions(ctx, reader, ns, app.ReleaseName, storage.ListPage{})
	if !errors.Is(err, domain.ErrPermissionDenied) || len(versions) != 0 {
		t.Fatalf("auth method gate: %v %v", versions, err)
	}
}

func TestListReleaseSchemaVersionsSchemaFreeAndInvalidAddress(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	ns := domain.NamespaceRef{Env: "dev", App: "schema-free"}
	if _, err := svc.CreateNamespace(ctx, adminPrincipal(), ns, "", nil); err != nil {
		t.Fatal(err)
	}
	versions, next, err := svc.ListReleaseSchemaVersions(ctx, adminPrincipal(), ns, "runtime", storage.ListPage{})
	if err != nil || versions == nil || len(versions) != 0 || next != "" {
		t.Fatalf("schema-free discovery: %v %q %v", versions, next, err)
	}
	for _, tt := range []struct {
		ns   domain.NamespaceRef
		name string
	}{{domain.NamespaceRef{}, "runtime"}, {ns, "../invalid"}} {
		if _, _, err := svc.ListReleaseSchemaVersions(ctx, adminPrincipal(), tt.ns, tt.name, storage.ListPage{}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("invalid address %v: %v", tt, err)
		}
	}
}

type schemaDiscoveryRaceStore struct {
	*storage.SQLStore
	afterLookup func()
}

func (s *schemaDiscoveryRaceStore) ListConfigurationSchemas(ctx context.Context, app, name string, page storage.ListPage) ([]domain.ConfigurationSchema, string, error) {
	schemas, next, err := s.SQLStore.ListConfigurationSchemas(ctx, app, name, page)
	s.afterLookup()
	return schemas, next, err
}
func TestListReleaseSchemaVersionsFencesNamespaceRecreation(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	ns := domain.NamespaceRef{Env: "dev", App: "discovery"}
	if _, err := svc.CreateNamespace(ctx, adminPrincipal(), ns, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateConfigurationSchema(ctx, adminPrincipal(), ns.App, `{"type":"object"}`, "{}"); err != nil {
		t.Fatal(err)
	}
	svc.store = &schemaDiscoveryRaceStore{SQLStore: st, afterLookup: func() {
		if err := st.DeleteNamespace(ctx, ns); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS}}); err != nil {
			t.Fatal(err)
		}
	}}
	versions, next, err := svc.ListReleaseSchemaVersions(ctx, adminPrincipal(), ns, "runtime", storage.ListPage{})
	if !errors.Is(err, domain.ErrAborted) || versions != nil || next != "" {
		t.Fatalf("recreated namespace leaked discovery: %v %q %v", versions, next, err)
	}
}

func TestListReleaseSchemaVersionsDenialAuditUsesListResource(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal(), "dev")
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	reader := clientPrincipal("denied-discovery")
	ctx = withReleaseAuditTrack(ctx, domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: 7})
	for _, name := range []string{app.ReleaseName, ""} {
		if _, _, err := svc.ListReleaseSchemaVersions(ctx, reader, ns, name, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("denied discovery: %v", err)
		}
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{Decision: "deny", ActorIdentity: reader.Identity.Name}, storage.ListPage{})
	if err != nil || len(events) != 2 {
		t.Fatalf("denials: %v %v", events, err)
	}
	keys := map[string]bool{}
	for _, event := range events {
		keys[event.ResourceKey] = true
		if event.ResourceType != domain.ResourceConfigurationRelease || event.ResourceEnv != ns.Env || event.ResourceApp != ns.App {
			t.Fatalf("wrong list identity: %+v", event)
		}
		if _, ok := auditMetadataForTest(t, event)["schema_version"]; ok {
			t.Fatalf("discovery fabricated schema selection: %+v", event)
		}
	}
	if !keys[app.ReleaseName] || !keys["releases"] {
		t.Fatalf("list resource keys: %v", keys)
	}
}
