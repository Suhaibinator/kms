package grpcserver

import (
	"context"
	"encoding/json/v2"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// TestSubscribe_UnauthorizedNamespaceRejected proves subscribe-time
// authorization: a client with no read grant in the selector's namespace is
// rejected at registration (before any event flows), not merely filtered.
func TestSubscribe_UnauthorizedNamespaceRejected(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"}, domain.AuthMethodToken)

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, err = stream.Recv()
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("unauthorized subscribe: code = %v, want PermissionDenied", codeOf(err))
	}
}

// TestSubscribe_MTLSOnlyNamespaceRejectsTokenClient proves the per-namespace
// auth-method gate applies at subscribe time: a token-authenticated client is
// refused registration against an mTLS-only namespace even when granted read.
func TestSubscribe_MTLSOnlyNamespaceRejectsTokenClient(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "secure"}, domain.AuthMethodMTLS)
	env.store.addPolicy(domain.Policy{Name: "r", Subject: "client", Allow: []domain.PolicyRule{
		{Operation: domain.OpParameterRead, Env: "prod", App: "secure"},
	}})

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "secure")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	_, err = stream.Recv()
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("token subscribe to mtls-only ns: code = %v, want PermissionDenied", codeOf(err))
	}
}

// TestSubscribe_HomeNamespaceGrantAllows proves the implicit home-namespace
// grant covers subscription: a namespace-bound token client may subscribe to
// its own (token-admitting) namespace with no explicit policy.
func TestSubscribe_HomeNamespaceGrantAllows(t *testing.T) {
	env := newTestEnv(t, true)
	ns := domain.NamespaceRef{Env: "prod", App: "home"}
	env.store.addNamespace(ns, domain.AuthMethodToken)
	env.store.addIdentity("homeclient", domain.IdentityKindClient, "home-token", &ns)

	ctx, cancel := context.WithTimeout(authCtx("home-token"), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "home", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "home")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// A successful registration yields the initial (empty) snapshot rather than an error.
	recvMatching(t, stream, isSnapshot)
}

func TestReleaseWatchRegistrationDenialAuditsSelectedTrack(t *testing.T) {
	env := newTestEnv(t, true)
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	env.store.addNamespace(ns, domain.AuthMethodToken)
	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	for _, schema := range []uint64{0, 2} {
		stream, err := kmsv1.NewConfigurationReleaseServiceClient(env.conn).WatchRelease(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{
			Namespace: pNS(ns.Env, ns.App), Name: "runtime", SchemaVersion: &schema, ClientName: "client", InstanceId: "instance",
		}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); codeOf(err) != codes.PermissionDenied {
			t.Fatalf("watch denial for schema %d: %v", schema, err)
		}
	}
	events, _, err := env.store.ListAudit(ctx, domain.AuditFilter{Decision: "deny"}, storage.ListPage{})
	if err != nil || len(events) != 2 {
		t.Fatalf("watch denial audits: %+v %v", events, err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		var metadata map[string]string
		if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
			t.Fatal(err)
		}
		if event.EventType != "authz.denial" || event.ResourceType != domain.ResourceConfigurationRelease || event.ResourceEnv != ns.Env || event.ResourceApp != ns.App || event.ResourceKey != "runtime" || metadata["operation"] != domain.OpConfigurationReleaseWatch {
			t.Fatalf("watch denial lost track identity: %+v", event)
		}
		seen[metadata["schema_version"]] = true
	}
	if !seen["0"] || !seen["2"] {
		t.Fatalf("watch denial schemas: %v", seen)
	}
}
