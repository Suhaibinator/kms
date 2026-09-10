package core

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestReleaseSessionDelegatedManagementIsNamespaceScoped(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal())
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	ref := domain.ReleaseSessionRef{Track: domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: app.SchemaVersion}, ClientName: "api", InstanceID: "one", SessionID: "one", Identity: adminPrincipal().Identity.Name}
	if err := svc.RegisterReleaseSession(ctx, adminPrincipal(), ref, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConnectReleaseSession(ctx, ref, "c", true); err != nil {
		t.Fatal(err)
	}
	pr := boundClientPrincipal("operator", ns)
	if _, err := svc.SetReleasePin(ctx, pr, ref, 0, 0); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("implicit management grant: %v", err)
	}
	if _, err := svc.CreatePolicy(ctx, adminPrincipal(), domain.Policy{Name: "instance-operator", Subject: pr.Identity.Name, Allow: []domain.PolicyRule{{Operation: domain.OpConfigurationReleaseInstanceManage, Env: ns.Env, App: ns.App}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetReleasePin(ctx, pr, ref, 0, 0); err != nil {
		t.Fatalf("delegated unpin: %v", err)
	}
	rows, _, _, err := svc.ListReleaseSubscribers(ctx, pr, domain.ReleaseFilter{Namespace: ns, Name: ref.Track.Name, SchemaVersion: &ref.Track.SchemaVersion}, storage.ListPage{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("scoped inspection: %v %v", rows, err)
	}
	if _, _, err := svc.ListSubscribers(ctx, pr); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("global inventory leaked: %v", err)
	}
	if _, err := svc.CreateNamespace(ctx, adminPrincipal(), domain.NamespaceRef{Env: "prod", App: ns.App}, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	other := ref
	other.Track.Namespace.Env = "prod"
	if _, err := svc.SetReleasePin(ctx, pr, other, 0, 0); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("cross namespace pin: %v", err)
	}
	if _, err := svc.CreatePolicy(ctx, adminPrincipal(), domain.Policy{Name: "instance-deny", Subject: pr.Identity.Name, Deny: []domain.PolicyRule{{Operation: domain.OpConfigurationReleaseInstanceManage, Env: ns.Env, App: ns.App}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetReleasePin(ctx, pr, ref, 0, 0); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("deny lost precedence: %v", err)
	}
	// No management attempt replaced the process identity or created a pin.
	got, err := st.ResolveInstanceRelease(ctx, ref)
	if err != nil || got.Pinned {
		t.Fatalf("state changed: %+v %v", got, err)
	}
}
