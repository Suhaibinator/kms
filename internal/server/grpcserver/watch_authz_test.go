package grpcserver

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
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
