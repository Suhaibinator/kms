package core

import (
	"context"
	"errors"
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

	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretList, Env: "prod", App: "app", KeyPattern: "billing/*"},
		{Operation: domain.OpSecretRead, Env: "prod", App: "app", KeyPattern: "billing/*"},
	}})
	got, _, err := s.ListSecrets(ctx, clientPrincipal("app"), tns, "", storage.ListPage{})
	if err != nil {
		t.Fatalf("client ListSecrets: %v", err)
	}
	if len(got) != 1 || got[0].Ref.Key != "billing/c" {
		t.Fatalf("client saw %v, want [billing/c]", got)
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
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretRead, Env: "prod", App: "app", KeyPattern: "*"}}}
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
		{Operation: domain.OpSecretRead, Env: "prod", App: "app", KeyPattern: "*"},
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

func TestWatchAccessChecker(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	adminCheck, err := s.WatchAccessChecker(ctx, adminPrincipal())
	if err != nil {
		t.Fatalf("admin WatchAccessChecker: %v", err)
	}
	if !adminCheck(domain.ResourceSecret, mkref("any", "app", "k")) {
		t.Fatal("admin predicate denied access")
	}

	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretRead, Env: "prod", App: "app", KeyPattern: "*"},
	}})
	check, err := s.WatchAccessChecker(ctx, clientPrincipal("app"))
	if err != nil {
		t.Fatalf("client WatchAccessChecker: %v", err)
	}
	if !check(domain.ResourceSecret, mkref("prod", "app", "x")) {
		t.Error("secret read under prod/app should be visible")
	}
	if check(domain.ResourceSecret, mkref("staging", "app", "x")) {
		t.Error("secret outside prod/app must not be visible")
	}
	if check(domain.ResourceParameter, mkref("prod", "app", "x")) {
		t.Error("parameter read was not granted, must not be visible")
	}
}

func TestAuthorizeSubscribe(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addNamespace(tns, domain.AuthMethodMTLS) // mTLS-only
	s := newTestService(store)

	sel := []domain.WatchSelector{{NS: tns, KeyPattern: "*"}}

	// A token client bound to the (home) namespace is rejected at subscribe by
	// the method gate, before authorization.
	home := boundClientPrincipal("app", tns) // token method
	if err := s.AuthorizeSubscribe(ctx, home, sel); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("mtls-only subscribe via token err = %v, want ErrPermissionDenied", err)
	}

	// After allowing token on the namespace, the home grant covers the selector.
	if _, err := s.UpdateNamespace(ctx, adminPrincipal(), tns, "", []domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if err := s.AuthorizeSubscribe(ctx, home, sel); err != nil {
		t.Fatalf("home subscribe after token enabled: %v", err)
	}

	// An unbound token client without a read policy is denied by authorization.
	if err := s.AuthorizeSubscribe(ctx, clientPrincipal("stranger"), sel); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("unbound subscribe err = %v, want ErrPermissionDenied", err)
	}

	// Admin may subscribe to anything.
	if err := s.AuthorizeSubscribe(ctx, adminPrincipal(), sel); err != nil {
		t.Fatalf("admin subscribe: %v", err)
	}
}

func TestReauthorizeWatchRevocation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("app", domain.IdentityKindClient, "kms_tok")
	pr := clientPrincipalTok("app", "kms_tok")

	if _, err := s.ReauthorizeWatch(ctx, pr); err != nil {
		t.Fatalf("ReauthorizeWatch(active): %v", err)
	}

	// Rotated token: the stream's stale token must no longer authenticate.
	if err := store.UpdateIdentityTokenHash(ctx, "app", crypto.TokenHash("kms_rotated")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(rotated token) err = %v, want ErrUnauthenticated", err)
	}

	// Disabled identity: stream must be closed even with the current token.
	store.addIdentity("app3", domain.IdentityKindClient, "kms_tok3")
	pr3 := clientPrincipalTok("app3", "kms_tok3")
	if err := store.SetIdentityDisabled(ctx, "app3", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := s.ReauthorizeWatch(ctx, pr3); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(disabled) err = %v, want ErrUnauthenticated", err)
	}

	// Kind mismatch rejected.
	store.addIdentity("app2", domain.IdentityKindClient, "kms_tok2")
	mismatch := Principal{Identity: domain.Identity{Name: "app2", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken, Token: "kms_tok2"}
	if _, err := s.ReauthorizeWatch(ctx, mismatch); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(kind mismatch) err = %v, want ErrUnauthenticated", err)
	}
}

func TestReauthorizeWatchMTLSDisabled(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "")
	pr := Principal{Identity: domain.Identity{Name: "svc", Kind: domain.IdentityKindClient}, Method: domain.AuthMethodMTLS}

	// An enabled mTLS identity re-authorizes.
	if _, err := s.ReauthorizeWatch(ctx, pr); err != nil {
		t.Fatalf("ReauthorizeWatch(mtls active): %v", err)
	}
	// Disabling it tears the stream down on the next tick.
	if err := store.SetIdentityDisabled(ctx, "svc", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(mtls disabled) err = %v, want ErrUnauthenticated", err)
	}
}

func TestRevealSecretErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("not found", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		if _, err := s.RevealSecret(ctx, adminPrincipal(), tref("missing"), 0, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("decrypt failure audited", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
		store.tamperCiphertext(tref("s"), 1)
		if _, err := s.RevealSecret(ctx, adminPrincipal(), tref("s"), 0, ""); !errors.Is(err, domain.ErrDecryptFailed) {
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
		val, err := s.RevealSecret(ctx, adminPrincipal(), tref("s"), 0, "")
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
