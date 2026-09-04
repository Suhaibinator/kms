package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestListSecretsFiltersByPolicy(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)
	for _, k := range []string{"a", "b", "billing/c"} {
		putSecret(t, s, PutSecretInput{Ref: tref(k), Value: []byte("v"), ContentType: "text/plain"})
	}

	all, _, err := s.ListSecrets(ctx, adminPrincipal(), tns, "", storage.ListPage{})
	if err != nil {
		t.Fatalf("admin ListSecrets: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("admin saw %d secrets, want 3", len(all))
	}

	// Authorization is namespace-level: a client granted list+read on the
	// namespace sees every key in it.
	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretList, Env: "prod", App: "app"},
		{Operation: domain.OpSecretRead, Env: "prod", App: "app"},
	}})
	got, _, err := s.ListSecrets(ctx, clientPrincipal("app"), tns, "", storage.ListPage{})
	if err != nil {
		t.Fatalf("client ListSecrets: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("client saw %d secrets, want 3 (whole namespace)", len(got))
	}
}

func TestGetSecretInfoAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})

	if _, err := s.GetSecretInfo(ctx, clientPrincipal("app"), tref("s")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	info, err := s.GetSecretInfo(ctx, adminPrincipal(), tref("s"))
	if err != nil {
		t.Fatalf("admin GetSecretInfo: %v", err)
	}
	if info.Ref.Key != "s" {
		t.Fatalf("info key = %q", info.Ref.Key)
	}
}

func TestDeleteSecretAuthorizationAndNotFound(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})

	if _, err := s.DeleteSecret(ctx, clientPrincipal("app"), tref("s")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	if _, err := s.DeleteSecret(ctx, adminPrincipal(), tref("s")); err != nil {
		t.Fatalf("admin DeleteSecret: %v", err)
	}
	if _, err := s.DeleteSecret(ctx, adminPrincipal(), tref("s")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete again err = %v, want ErrNotFound", err)
	}
}

func TestGetParameterInfoAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	if _, _, err := store.PutParameter(ctx, tref("p"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.GetParameterInfo(ctx, clientPrincipal("app"), tref("p")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	if _, err := s.GetParameterInfo(ctx, adminPrincipal(), tref("p")); err != nil {
		t.Fatalf("admin GetParameterInfo: %v", err)
	}
}

func TestPolicyAndIdentityAdminGating(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	client := clientPrincipal("app")

	valid := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretRead, Env: "prod", App: "app"}}}
	if _, err := s.CreatePolicy(ctx, adminPrincipal(), valid); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	if _, err := s.UpdatePolicy(ctx, client, valid); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("UpdatePolicy client = %v", err)
	}
	if err := s.DeletePolicy(ctx, client, "p"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("DeletePolicy client = %v", err)
	}
	if _, _, err := s.ListPolicies(ctx, client, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("ListPolicies client = %v", err)
	}
	if _, _, err := s.ListIdentities(ctx, client, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("ListIdentities client = %v", err)
	}
	if _, _, err := s.ListSubscribers(ctx, client); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("ListSubscribers client = %v", err)
	}

	if _, err := s.UpdatePolicy(ctx, adminPrincipal(), valid); err != nil {
		t.Errorf("admin UpdatePolicy: %v", err)
	}
	if _, _, err := s.ListPolicies(ctx, adminPrincipal(), storage.ListPage{}); err != nil {
		t.Errorf("admin ListPolicies: %v", err)
	}
	if err := s.DeletePolicy(ctx, adminPrincipal(), "p"); err != nil {
		t.Errorf("admin DeletePolicy: %v", err)
	}
}

func TestListSubscribersReturnsRevision(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.revision = 42
	s := newTestService(store)

	subs, rev, err := s.ListSubscribers(ctx, adminPrincipal())
	if err != nil {
		t.Fatalf("ListSubscribers: %v", err)
	}
	if rev != 42 {
		t.Fatalf("revision = %d, want 42", rev)
	}
	if subs != nil {
		t.Fatalf("subscribers = %v, want nil from noop hub", subs)
	}
}

func TestListNamespacesScopedToAccess(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	for _, ns := range []domain.NamespaceRef{mkns("prod", "app"), mkns("staging", "app")} {
		if _, err := s.CreateNamespace(ctx, adminPrincipal(), ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
			t.Fatalf("seed namespace %s: %v", ns, err)
		}
	}

	all, _, err := s.ListNamespaces(ctx, adminPrincipal(), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(admin): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("admin namespaces = %d, want 2", len(all))
	}

	// A client with no policy and no binding sees nothing.
	none, _, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(unpolicied): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unpolicied namespaces = %d, want 0", len(none))
	}

	// A client scoped to prod/app by policy sees only it.
	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretRead, Env: "prod", App: "app"},
	}})
	scoped, _, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(scoped): %v", err)
	}
	if len(scoped) != 1 || scoped[0].NamespaceRef != mkns("prod", "app") {
		t.Fatalf("scoped namespaces = %v, want [prod/app]", scoped)
	}

	// A client bound to staging/app sees its home namespace via the implicit
	// grant, with no policy.
	store.policies = nil
	home, _, err := s.ListNamespaces(ctx, boundClientPrincipal("svc", mkns("staging", "app")), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(home): %v", err)
	}
	if len(home) != 1 || home[0].NamespaceRef != mkns("staging", "app") {
		t.Fatalf("home namespaces = %v, want [staging/app]", home)
	}
}

func TestListNamespacesFiltersDisallowedAuthenticationMethods(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	mtlsOnly := mkns("prod", "mtls-only")
	tokenAllowed := mkns("prod", "token-allowed")
	store.addNamespace(mtlsOnly, domain.AuthMethodMTLS)
	store.addNamespace(tokenAllowed, domain.AuthMethodToken)
	store.addPolicy(domain.Policy{Name: "read-all", Subject: "app", Allow: []domain.PolicyRule{{
		Operation: domain.OpParameterRead, Env: "*", App: "*",
	}}})

	tokenVisible, _, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(token): %v", err)
	}
	if len(tokenVisible) != 1 || tokenVisible[0].NamespaceRef != tokenAllowed {
		t.Fatalf("token-visible namespaces = %v, want [%s]", tokenVisible, tokenAllowed)
	}

	mtlsPrincipal := clientPrincipal("app")
	mtlsPrincipal.Method = domain.AuthMethodMTLS
	mtlsVisible, _, err := s.ListNamespaces(ctx, mtlsPrincipal, storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(mTLS): %v", err)
	}
	if len(mtlsVisible) != 1 || mtlsVisible[0].NamespaceRef != mtlsOnly {
		t.Fatalf("mTLS-visible namespaces = %v, want [%s]", mtlsVisible, mtlsOnly)
	}

	adminVisible, _, err := s.ListNamespaces(ctx, adminPrincipal(), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(admin): %v", err)
	}
	if len(adminVisible) != 2 {
		t.Fatalf("admin-visible namespaces = %v, want both", adminVisible)
	}
}

func TestListNamespacesFilteredPaginationIsCompleteAndConfidential(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "namespace-pages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for _, tc := range []struct {
		app    string
		method domain.AuthMethod
	}{
		{app: "a-hidden", method: domain.AuthMethodMTLS},
		{app: "b-visible", method: domain.AuthMethodToken},
		{app: "c-hidden", method: domain.AuthMethodMTLS},
		{app: "d-visible", method: domain.AuthMethodToken},
	} {
		if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: mkns("prod", tc.app), AllowedAuthMethods: []domain.AuthMethod{tc.method}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreatePolicy(ctx, domain.Policy{Name: "read", Subject: "app", Allow: []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "*", App: "*"}}}); err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, "test")
	page1, next, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 1 || page1[0].App != "b-visible" || next == "" {
		t.Fatalf("first filtered namespace page = %+v next=%q", page1, next)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(next)
	if err != nil {
		t.Fatalf("decode opaque cursor envelope: %v", err)
	}
	for _, hidden := range []string{"prod/a-hidden", "prod/c-hidden", "prod/b-visible"} {
		if bytes.Contains(sealed, []byte(hidden)) {
			t.Fatalf("delegated cursor exposed namespace %q", hidden)
		}
	}
	page2, final, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{Limit: 1, Token: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].App != "d-visible" || final != "" {
		t.Fatalf("second filtered namespace page = %+v next=%q", page2, final)
	}
}

func TestListNamespacesHonorsExplicitDeny(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	prod := mkns("prod", "app")
	staging := mkns("staging", "app")
	for _, ns := range []domain.NamespaceRef{prod, staging} {
		if _, err := s.CreateNamespace(ctx, adminPrincipal(), ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
			t.Fatalf("seed namespace %s: %v", ns, err)
		}
	}

	// A matching deny must override a broad allow when deciding whether namespace
	// metadata and resource counts are visible.
	store.addPolicy(domain.Policy{
		Name:    "cross-namespace-deny",
		Subject: "app",
		Allow: []domain.PolicyRule{
			{Operation: "*", Env: "*", App: "*"},
		},
		Deny: []domain.PolicyRule{
			{Operation: "*", Env: prod.Env, App: prod.App},
		},
	})
	visible, _, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(cross-namespace deny): %v", err)
	}
	if len(visible) != 1 || visible[0].NamespaceRef != staging {
		t.Fatalf("namespaces with denied prod = %v, want [staging/app]", visible)
	}

	// Deny precedence also applies to the implicit home-namespace grant.
	store.addPolicy(domain.Policy{
		Name:    "home-deny",
		Subject: "svc",
		Deny: []domain.PolicyRule{
			{Operation: "*", Env: prod.Env, App: prod.App},
		},
	})
	home, _, err := s.ListNamespaces(ctx, boundClientPrincipal("svc", prod), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(home deny): %v", err)
	}
	if len(home) != 0 {
		t.Fatalf("namespaces with denied home = %v, want none", home)
	}
}

func TestAuthorizeSubscribe(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addNamespace(tns, domain.AuthMethodMTLS) // mTLS-only
	s := newTestService(store)

	nss := []domain.NamespaceRef{tns}

	// A token client bound to the (home) namespace is rejected at subscribe by
	// the method gate, before authorization.
	home := boundClientPrincipal("app", tns) // token method
	if err := s.AuthorizeSubscribe(ctx, home, nss); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("mtls-only subscribe via token err = %v, want ErrPermissionDenied", err)
	}

	// After allowing token on the namespace, the home grant covers it.
	if _, err := s.UpdateNamespace(ctx, adminPrincipal(), tns, "", []domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if err := s.AuthorizeSubscribe(ctx, home, nss); err != nil {
		t.Fatalf("home subscribe after token enabled: %v", err)
	}

	// An unbound token client without a read policy is denied by authorization.
	if err := s.AuthorizeSubscribe(ctx, clientPrincipal("stranger"), nss); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("unbound subscribe err = %v, want ErrPermissionDenied", err)
	}

	// Admin bypasses policy/method restrictions for an existing namespace.
	if err := s.AuthorizeSubscribe(ctx, adminPrincipal(), nss); err != nil {
		t.Fatalf("admin subscribe: %v", err)
	}

	// A stream still needs a concrete immutable row to bind. Admin bypass must
	// not turn a nonexistent name into access to whatever is created later.
	missing := domain.NamespaceRef{Env: "prod", App: "missing"}
	if err := s.AuthorizeSubscribe(ctx, adminPrincipal(), []domain.NamespaceRef{missing}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin missing-namespace subscribe err = %v, want ErrNotFound", err)
	}
	if err := s.AuthorizeReleaseWatch(ctx, adminPrincipal(), missing, "runtime"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin missing-namespace release watch err = %v, want ErrNotFound", err)
	}
}

// TestAuthorizeSubscribeNamespaceLevel pins the namespace-level model:
// authorization is all-or-nothing per namespace. A client with an explicit
// allow rule on a namespace may subscribe to the WHOLE namespace (there is no
// finer key scoping), while a client not authorized for a namespace is denied
// subscribing to it.
func TestAuthorizeSubscribeNamespaceLevel(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	other := domain.NamespaceRef{Env: "prod", App: "other"}
	store.addNamespace(tns, domain.AuthMethodToken, domain.AuthMethodMTLS)
	store.addNamespace(other, domain.AuthMethodToken, domain.AuthMethodMTLS)
	store.addPolicy(domain.Policy{Name: "reader", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpParameterRead, Env: tns.Env, App: tns.App},
	}})
	s := newTestService(store)
	pr := clientPrincipal("app") // token method, unbound (no implicit grant)

	// Authorized for the whole granted namespace.
	if err := s.AuthorizeSubscribe(ctx, pr, []domain.NamespaceRef{tns}); err != nil {
		t.Fatalf("subscribe to granted namespace: err = %v, want allowed", err)
	}
	// Denied for a namespace the client has no grant on.
	if err := s.AuthorizeSubscribe(ctx, pr, []domain.NamespaceRef{other}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("subscribe to ungranted namespace: err = %v, want ErrPermissionDenied", err)
	}
	// A mix denies as soon as one namespace is unauthorized.
	if err := s.AuthorizeSubscribe(ctx, pr, []domain.NamespaceRef{tns, other}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("subscribe to mixed namespaces: err = %v, want ErrPermissionDenied", err)
	}
}

func TestReauthorizeWatchRevocation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("app", domain.IdentityKindClient, "kms_tok")
	pr := clientPrincipalTok("app", "kms_tok")

	if err := s.ReauthorizeWatch(ctx, pr); err != nil {
		t.Fatalf("ReauthorizeWatch(active): %v", err)
	}

	// Rotated token: the stream's stale token must no longer authenticate.
	if err := store.UpdateIdentityTokenHash(ctx, "app", crypto.TokenHash("kms_rotated")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(rotated token) err = %v, want ErrUnauthenticated", err)
	}

	// Disabled identity: stream must be closed even with the current token.
	store.addIdentity("app3", domain.IdentityKindClient, "kms_tok3")
	pr3 := clientPrincipalTok("app3", "kms_tok3")
	if err := store.SetIdentityDisabled(ctx, "app3", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := s.ReauthorizeWatch(ctx, pr3); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(disabled) err = %v, want ErrUnauthenticated", err)
	}

	// Kind mismatch rejected.
	store.addIdentity("app2", domain.IdentityKindClient, "kms_tok2")
	mismatch := Principal{Identity: domain.Identity{Name: "app2", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken, Token: "kms_tok2"}
	if err := s.ReauthorizeWatch(ctx, mismatch); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(kind mismatch) err = %v, want ErrUnauthenticated", err)
	}
}

func TestReauthorizeWatchMTLSDisabled(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc", Kind: domain.IdentityKindClient, AuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	pr := Principal{
		Identity:    domain.Identity{Name: "svc", Kind: domain.IdentityKindClient},
		Method:      domain.AuthMethodMTLS,
		Serial:      res.Cert.Serial,
		Fingerprint: res.Cert.Fingerprint,
	}

	// An enabled mTLS identity re-authorizes.
	if err := s.ReauthorizeWatch(ctx, pr); err != nil {
		t.Fatalf("ReauthorizeWatch(mtls active): %v", err)
	}
	// Disabling it tears the stream down on the next tick.
	if err := store.SetIdentityDisabled(ctx, "svc", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(mtls disabled) err = %v, want ErrUnauthenticated", err)
	}
}

func TestRevealSecretErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("not found", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		if _, err := s.RevealSecret(ctx, adminPrincipal(), tref("missing"), 0, "", "", ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("decrypt failure audited", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
		store.tamperCiphertext(tref("s"), 1)
		if _, err := s.RevealSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", ""); !errors.Is(err, domain.ErrDecryptFailed) {
			t.Fatalf("err = %v, want ErrDecryptFailed", err)
		}
		if !store.hasAudit("secret.reveal", "error") {
			t.Error("reveal decrypt failure not audited")
		}
	})

	t.Run("fails closed when audit unavailable", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
		store.auditErr = errors.New("audit down")
		val, err := s.RevealSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", "")
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("err = %v, want ErrFailedPrecondition", err)
		}
		if len(val.Value) != 0 {
			t.Fatal("plaintext returned despite fail-closed audit")
		}
	})
}

func TestCurrentRevision(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.revision = 7
	s := newTestService(store)
	rev, err := s.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if rev != 7 {
		t.Fatalf("revision = %d, want 7", rev)
	}
}
