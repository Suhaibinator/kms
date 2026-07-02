package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestListSecretsFiltersByPolicy(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	for _, p := range []string{"/prod/a", "/prod/b", "/staging/c"} {
		putSecret(t, s, PutSecretInput{Path: p, Value: []byte("v"), ContentType: "text/plain"})
	}

	all, _, err := s.ListSecrets(ctx, adminPrincipal(), "", storage.ListPage{})
	if err != nil {
		t.Fatalf("admin ListSecrets: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("admin saw %d secrets, want 3", len(all))
	}

	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretList, Path: "/prod/*"},
		{Operation: domain.OpSecretRead, Path: "/prod/*"},
	}})
	got, _, err := s.ListSecrets(ctx, clientPrincipal("app"), "", storage.ListPage{})
	if err != nil {
		t.Fatalf("client ListSecrets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("client saw %d secrets, want 2", len(got))
	}
	for _, sec := range got {
		if !strings.HasPrefix(sec.Path, "/prod/") {
			t.Fatalf("client saw unpermitted secret %q", sec.Path)
		}
	}
}

func TestGetSecretInfoAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Path: "/prod/s", Value: []byte("v"), ContentType: "text/plain"})

	if _, err := s.GetSecretInfo(ctx, clientPrincipal("app"), "/prod/s"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	info, err := s.GetSecretInfo(ctx, adminPrincipal(), "/prod/s")
	if err != nil {
		t.Fatalf("admin GetSecretInfo: %v", err)
	}
	if info.Path != "/prod/s" {
		t.Fatalf("info path = %q", info.Path)
	}
}

func TestDeleteSecretAuthorizationAndNotFound(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Path: "/s", Value: []byte("v"), ContentType: "text/plain"})

	if _, err := s.DeleteSecret(ctx, clientPrincipal("app"), "/s"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	if _, err := s.DeleteSecret(ctx, adminPrincipal(), "/s"); err != nil {
		t.Fatalf("admin DeleteSecret: %v", err)
	}
	if _, err := s.DeleteSecret(ctx, adminPrincipal(), "/s"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete again err = %v, want ErrNotFound", err)
	}
}

func TestGetParameterInfoAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	if _, _, err := store.PutParameter(ctx, "/prod/p", "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.GetParameterInfo(ctx, clientPrincipal("app"), "/prod/p"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	if _, err := s.GetParameterInfo(ctx, adminPrincipal(), "/prod/p"); err != nil {
		t.Fatalf("admin GetParameterInfo: %v", err)
	}
}

func TestPolicyAndIdentityAdminGating(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	client := clientPrincipal("app")

	valid := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretRead, Path: "/prod/*"}}}
	if _, err := s.CreatePolicy(ctx, adminPrincipal(), valid); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	// Every admin-only management call rejects a client.
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

	// Admin succeeds at the same calls.
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
	for _, ns := range []string{"/prod", "/staging"} {
		if _, err := s.CreateNamespace(ctx, adminPrincipal(), ns, ""); err != nil {
			t.Fatalf("seed namespace %s: %v", ns, err)
		}
	}

	// Admin sees every namespace.
	all, _, err := s.ListNamespaces(ctx, adminPrincipal(), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(admin): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("admin namespaces = %d, want 2", len(all))
	}

	// A client with no policy sees nothing (namespaces are not a recon surface).
	none, _, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(unpolicied): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unpolicied namespaces = %d, want 0", len(none))
	}

	// A client scoped to /prod sees only /prod.
	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretRead, Path: "/prod/*"},
	}})
	scoped, _, err := s.ListNamespaces(ctx, clientPrincipal("app"), storage.ListPage{})
	if err != nil {
		t.Fatalf("ListNamespaces(scoped): %v", err)
	}
	if len(scoped) != 1 || scoped[0].Path != "/prod" {
		t.Fatalf("scoped namespaces = %v, want [/prod]", scoped)
	}
}

func TestWatchAccessChecker(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Admin predicate allows everything.
	adminCheck, err := s.WatchAccessChecker(ctx, adminPrincipal())
	if err != nil {
		t.Fatalf("admin WatchAccessChecker: %v", err)
	}
	if !adminCheck(domain.ResourceSecret, "/anything") {
		t.Fatal("admin predicate denied access")
	}

	// Client predicate reflects read permissions per resource type.
	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretRead, Path: "/prod/*"},
	}})
	check, err := s.WatchAccessChecker(ctx, clientPrincipal("app"))
	if err != nil {
		t.Fatalf("client WatchAccessChecker: %v", err)
	}
	if !check(domain.ResourceSecret, "/prod/x") {
		t.Error("secret read under /prod should be visible")
	}
	if check(domain.ResourceSecret, "/staging/x") {
		t.Error("secret outside /prod must not be visible")
	}
	if check(domain.ResourceParameter, "/prod/x") {
		t.Error("parameter read was not granted, must not be visible")
	}
}

func TestReauthorizeWatchRevocation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("app", domain.IdentityKindClient, "kms_tok")
	pr := clientPrincipalTok("app", "kms_tok")

	// Valid, active identity with a matching token: succeeds.
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

	// Kind mismatch (identity kind differs from the token's identity) rejected.
	store.addIdentity("app2", domain.IdentityKindClient, "kms_tok2")
	mismatch := Principal{Identity: domain.Identity{Name: "app2", Kind: domain.IdentityKindAdmin}, Token: "kms_tok2"}
	if _, err := s.ReauthorizeWatch(ctx, mismatch); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("ReauthorizeWatch(kind mismatch) err = %v, want ErrUnauthenticated", err)
	}
}

func TestRevealSecretErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("not found", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		if _, err := s.RevealSecret(ctx, adminPrincipal(), "/missing", 0, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("decrypt failure audited", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		putSecret(t, s, PutSecretInput{Path: "/s", Value: []byte("v"), ContentType: "text/plain"})
		store.tamperCiphertext("/s", 1)
		if _, err := s.RevealSecret(ctx, adminPrincipal(), "/s", 0, ""); !errors.Is(err, domain.ErrDecryptFailed) {
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
		putSecret(t, s, PutSecretInput{Path: "/s", Value: []byte("v"), ContentType: "text/plain"})
		store.auditErr = errors.New("audit down")
		val, err := s.RevealSecret(ctx, adminPrincipal(), "/s", 0, "")
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
