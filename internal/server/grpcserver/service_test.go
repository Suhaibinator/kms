package grpcserver

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

func codeOf(err error) codes.Code {
	st, _ := status.FromError(err)
	return st.Code()
}

// --- authentication ---

func TestAuth_MissingToken(t *testing.T) {
	env := newTestEnv(t, true)
	// No metadata at all.
	_, err := env.param().GetParameter(context.Background(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("missing token: code = %v, want Unauthenticated (%v)", codeOf(err), err)
	}
}

func TestAuth_BadToken(t *testing.T) {
	env := newTestEnv(t, true)
	_, err := env.param().GetParameter(authCtx("not-a-real-token"), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("bad token: code = %v, want Unauthenticated", codeOf(err))
	}
	// The message must be generic.
	st, _ := status.FromError(err)
	if st.Message() != "unauthenticated" {
		t.Fatalf("auth failure message = %q, want generic", st.Message())
	}
}

func TestAuth_MalformedAuthorizationHeader(t *testing.T) {
	env := newTestEnv(t, true)
	// "authorization" present but without a bearer token.
	md := metadata.Pairs("authorization", "")
	ctx := metadata.NewOutgoingContext(context.Background(), md)
	_, err := env.param().GetParameter(ctx, &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("empty authorization: code = %v, want Unauthenticated", codeOf(err))
	}
}

func TestAuth_ValidToken(t *testing.T) {
	env := newTestEnv(t, true)
	// Admin writes then reads a parameter (admin bypasses the auth-method gate).
	_, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "svc", "x"), Value: "hello"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	resp, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.GetParameter().GetValue() != "hello" {
		t.Fatalf("value = %q, want hello", resp.GetParameter().GetValue())
	}
}

// --- readiness gating ---

func TestReadiness_GatesNonHealthMethods(t *testing.T) {
	env := newTestEnv(t, false) // no keyring => not ready
	_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if codeOf(err) != codes.Unavailable {
		t.Fatalf("not-ready GetParameter: code = %v, want Unavailable", codeOf(err))
	}
}

func TestReadiness_HealthExemptAndUnauthenticated(t *testing.T) {
	env := newTestEnv(t, false) // not ready
	// Health works without a token and while not ready.
	resp, err := env.admin().Health(context.Background(), &kmsv1.HealthRequest{})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !resp.GetHealthy() {
		t.Fatal("healthy should be true")
	}
	if resp.GetReady() {
		t.Fatal("ready should be false without a keyring")
	}
	if resp.GetVersion() != "v-test" {
		t.Fatalf("version = %q", resp.GetVersion())
	}
}

func TestReadiness_HealthReadyWhenKeyed(t *testing.T) {
	env := newTestEnv(t, true)
	resp, err := env.admin().Health(context.Background(), &kmsv1.HealthRequest{})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !resp.GetReady() {
		t.Fatal("ready should be true when keyed and store reachable")
	}
}

// --- error mapping through real handlers ---

func TestHandler_NotFound(t *testing.T) {
	env := newTestEnv(t, true)
	_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "nope")})
	if codeOf(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", codeOf(err))
	}
}

func TestHandler_InvalidArgument(t *testing.T) {
	env := newTestEnv(t, true)
	// An empty namespace fails validation in core.
	_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Ref: pRef("", "", "x")})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", codeOf(err))
	}
}

func TestHandler_PermissionDenied(t *testing.T) {
	env := newTestEnv(t, true)
	// The namespace exists and admits token clients, but the client identity has
	// no policy granting a read.
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	_, err := env.param().GetParameter(clientCtx(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", codeOf(err))
	}
}

func TestHandler_SecretPermissionDenied(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	// SecretService wiring: unauthorized client -> PermissionDenied (denial
	// happens before any keyring or secret storage access).
	_, err := env.secret().GetSecret(clientCtx(), &kmsv1.GetSecretRequest{Ref: pRef("prod", "svc", "db")})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", codeOf(err))
	}
}

// --- auth-method gate ---

func TestGate_MTLSOnlyNamespaceRejectsTokenClient(t *testing.T) {
	env := newTestEnv(t, true)
	// The namespace admits only mTLS; a token client is refused even a plain read.
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"}, domain.AuthMethodMTLS)
	// Grant the client a read policy, so the denial can only come from the gate.
	env.store.addPolicy(domain.Policy{
		Name:    "read",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "prod", App: "svc", KeyPattern: "*"}},
	})
	_, err := env.param().GetParameter(clientCtx(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("mtls-only namespace, token client: code = %v, want PermissionDenied", codeOf(err))
	}
}

func TestGate_TokenNamespaceAdmitsTokenClient(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"}, domain.AuthMethodToken)
	env.store.addPolicy(domain.Policy{
		Name:    "read",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "prod", App: "svc", KeyPattern: "*"}},
	})
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "svc", "x"), Value: "v"}); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	resp, err := env.param().GetParameter(clientCtx(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
	if err != nil {
		t.Fatalf("token client on token namespace: %v", err)
	}
	if resp.GetParameter().GetValue() != "v" {
		t.Fatalf("value = %q, want v", resp.GetParameter().GetValue())
	}
}

// TestHandler_InjectedSentinelMapping drives every remaining sentinel through
// GetParameter via store-level error injection, proving the handler path maps
// each one.
func TestHandler_InjectedSentinelMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"failed-precondition", domain.Errorf(domain.ErrFailedPrecondition, "x"), codes.FailedPrecondition},
		{"decrypt", domain.ErrDecryptFailed, codes.Internal},
		{"already-exists", domain.Errorf(domain.ErrAlreadyExists, "x"), codes.AlreadyExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, true)
			env.store.mu.Lock()
			env.store.getParamErr = tc.err
			env.store.mu.Unlock()
			_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "x")})
			if codeOf(err) != tc.want {
				t.Fatalf("code = %v, want %v", codeOf(err), tc.want)
			}
			if tc.want == codes.Internal {
				st, _ := status.FromError(err)
				if st.Message() != "internal error" {
					t.Fatalf("decrypt leaked message %q", st.Message())
				}
			}
		})
	}
}

// --- admin round trips ---

func TestAdmin_NamespaceRoundTrip(t *testing.T) {
	env := newTestEnv(t, true)
	if _, err := env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{
		Ref: pNS("prod", "gradethis"), Description: "d", AllowedAuthMethods: []string{"mtls", "token"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	resp, err := env.admin().ListNamespaces(adminCtx(), &kmsv1.ListNamespacesRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.GetNamespaces()) != 1 {
		t.Fatalf("namespaces = %+v", resp.GetNamespaces())
	}
	got := resp.GetNamespaces()[0]
	if got.GetRef().GetEnv() != "prod" || got.GetRef().GetApp() != "gradethis" {
		t.Fatalf("namespace ref = %+v", got.GetRef())
	}
}

func TestAdmin_NamespaceDefaultsMTLSOnly(t *testing.T) {
	env := newTestEnv(t, true)
	resp, err := env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Ref: pNS("prod", "svc")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	methods := resp.GetNamespace().GetAllowedAuthMethods()
	if len(methods) != 1 || methods[0] != "mtls" {
		t.Fatalf("default auth methods = %v, want [mtls]", methods)
	}
}

func TestAdmin_UpdateAndDeleteNamespace(t *testing.T) {
	env := newTestEnv(t, true)
	if _, err := env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Ref: pNS("prod", "svc")}); err != nil {
		t.Fatalf("create: %v", err)
	}
	upd, err := env.admin().UpdateNamespace(adminCtx(), &kmsv1.UpdateNamespaceRequest{
		Ref: pNS("prod", "svc"), Description: "updated", AllowedAuthMethods: []string{"token"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := upd.GetNamespace().GetAllowedAuthMethods(); len(got) != 1 || got[0] != "token" {
		t.Fatalf("updated methods = %v", got)
	}
	if _, err := env.admin().DeleteNamespace(adminCtx(), &kmsv1.DeleteNamespaceRequest{Ref: pNS("prod", "svc")}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestAdmin_DeleteNamespaceBlockedWhenNonEmpty(t *testing.T) {
	env := newTestEnv(t, true)
	if _, err := env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Ref: pNS("prod", "svc")}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "svc", "x"), Value: "v"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	_, err := env.admin().DeleteNamespace(adminCtx(), &kmsv1.DeleteNamespaceRequest{Ref: pNS("prod", "svc")})
	if codeOf(err) != codes.FailedPrecondition {
		t.Fatalf("delete non-empty: code = %v, want FailedPrecondition", codeOf(err))
	}
}

func TestHandler_AlreadyExists(t *testing.T) {
	env := newTestEnv(t, true)
	_, err := env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Ref: pNS("team", "app")})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Ref: pNS("team", "app")})
	if codeOf(err) != codes.AlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists", codeOf(err))
	}
}

func TestAdmin_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t, true)
	_, err := env.admin().CreateNamespace(clientCtx(), &kmsv1.CreateNamespaceRequest{Ref: pNS("team", "app")})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("client CreateNamespace: code = %v, want PermissionDenied", codeOf(err))
	}
}

func TestAdmin_CreateIdentityTokenOnce(t *testing.T) {
	env := newTestEnv(t, true)
	resp, err := env.admin().CreateIdentity(adminCtx(), &kmsv1.CreateIdentityRequest{
		Name: "svc-a", Kind: domain.IdentityKindClient, AuthMethods: []string{"token"},
	})
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	if resp.GetToken() == "" {
		t.Fatal("expected a minted token for a token identity")
	}
	if resp.GetIdentity().GetName() != "svc-a" {
		t.Fatalf("identity = %+v", resp.GetIdentity())
	}
}

func TestAdmin_CreateIdentityMintsCertBundle(t *testing.T) {
	env := newTLSTestEnv(t)
	conn := env.dial(t, nil) // admin over TLS with a bearer token (no client cert)
	admin := kmsv1.NewAdminServiceClient(conn)
	resp, err := admin.CreateIdentity(adminCtx(), &kmsv1.CreateIdentityRequest{
		Name: "svc-cert", Kind: domain.IdentityKindClient, AuthMethods: []string{"mtls"},
		Namespace: pNS("prod", "svc"),
	})
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	bundle := resp.GetCert()
	if bundle == nil || bundle.GetCertPem() == "" || bundle.GetKeyPem() == "" {
		t.Fatalf("expected a cert bundle, got %+v", bundle)
	}
	if resp.GetToken() != "" {
		t.Fatal("mtls-only identity should not receive a token")
	}
}

func TestParameter_ListNamespaceScoped(t *testing.T) {
	env := newTestEnv(t, true)
	for _, r := range []struct{ env, app, key string }{
		{"prod", "svc", "x"}, {"prod", "svc", "y"}, {"prod", "other", "z"},
	} {
		if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef(r.env, r.app, r.key), Value: "v"}); err != nil {
			t.Fatalf("put %s/%s/%s: %v", r.env, r.app, r.key, err)
		}
	}
	resp, err := env.param().ListParameters(adminCtx(), &kmsv1.ListParametersRequest{Namespace: pNS("prod", "svc")})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.GetParameters()) != 2 {
		t.Fatalf("got %d params in prod/svc, want 2", len(resp.GetParameters()))
	}
}
