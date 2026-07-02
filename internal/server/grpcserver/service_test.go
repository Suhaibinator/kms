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
	_, err := env.param().GetParameter(context.Background(), &kmsv1.GetParameterRequest{Path: "/a/x"})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("missing token: code = %v, want Unauthenticated (%v)", codeOf(err), err)
	}
}

func TestAuth_BadToken(t *testing.T) {
	env := newTestEnv(t, true)
	_, err := env.param().GetParameter(authCtx("not-a-real-token"), &kmsv1.GetParameterRequest{Path: "/a/x"})
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
	_, err := env.param().GetParameter(ctx, &kmsv1.GetParameterRequest{Path: "/a/x"})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("empty authorization: code = %v, want Unauthenticated", codeOf(err))
	}
}

func TestAuth_ValidToken(t *testing.T) {
	env := newTestEnv(t, true)
	// Admin writes then reads a parameter.
	_, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/a/x", Value: "hello"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	resp, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Path: "/a/x"})
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
	_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Path: "/a/x"})
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
	_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Path: "/nope"})
	if codeOf(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", codeOf(err))
	}
}

func TestHandler_InvalidArgument(t *testing.T) {
	env := newTestEnv(t, true)
	// A path without a leading slash fails normalization in core.
	_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Path: "bad-path"})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", codeOf(err))
	}
}

func TestHandler_PermissionDenied(t *testing.T) {
	env := newTestEnv(t, true)
	// A client identity with no policy may not read.
	_, err := env.param().GetParameter(clientCtx(), &kmsv1.GetParameterRequest{Path: "/a/x"})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", codeOf(err))
	}
}

func TestHandler_SecretPermissionDenied(t *testing.T) {
	env := newTestEnv(t, true)
	// SecretService wiring: unauthorized client -> PermissionDenied (no keyring
	// or storage secret access required, denial happens first).
	_, err := env.secret().GetSecret(clientCtx(), &kmsv1.GetSecretRequest{Path: "/s/db"})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", codeOf(err))
	}
}

func TestHandler_AlreadyExists(t *testing.T) {
	env := newTestEnv(t, true)
	_, err := env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Path: "/team"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Path: "/team"})
	if codeOf(err) != codes.AlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists", codeOf(err))
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
			_, err := env.param().GetParameter(adminCtx(), &kmsv1.GetParameterRequest{Path: "/a/x"})
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
	if _, err := env.admin().CreateNamespace(adminCtx(), &kmsv1.CreateNamespaceRequest{Path: "/team", Description: "d"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	resp, err := env.admin().ListNamespaces(adminCtx(), &kmsv1.ListNamespacesRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.GetNamespaces()) != 1 || resp.GetNamespaces()[0].GetPath() != "/team" {
		t.Fatalf("namespaces = %+v", resp.GetNamespaces())
	}
}

func TestAdmin_RequiresAdmin(t *testing.T) {
	env := newTestEnv(t, true)
	_, err := env.admin().CreateNamespace(clientCtx(), &kmsv1.CreateNamespaceRequest{Path: "/team"})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("client CreateNamespace: code = %v, want PermissionDenied", codeOf(err))
	}
}

func TestAdmin_CreateIdentityReturnsTokenOnce(t *testing.T) {
	env := newTestEnv(t, true)
	resp, err := env.admin().CreateIdentity(adminCtx(), &kmsv1.CreateIdentityRequest{Name: "svc-a", Kind: domain.IdentityKindClient})
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	if resp.GetToken() == "" {
		t.Fatal("expected a minted token")
	}
	if resp.GetIdentity().GetName() != "svc-a" {
		t.Fatalf("identity = %+v", resp.GetIdentity())
	}
}

func TestParameter_ListPassthrough(t *testing.T) {
	env := newTestEnv(t, true)
	for _, p := range []string{"/a/x", "/a/y", "/b/z"} {
		if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: p, Value: "v"}); err != nil {
			t.Fatalf("put %s: %v", p, err)
		}
	}
	resp, err := env.param().ListParameters(adminCtx(), &kmsv1.ListParametersRequest{PathPrefix: "/a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.GetParameters()) != 2 {
		t.Fatalf("got %d params under /a, want 2", len(resp.GetParameters()))
	}
}
