package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// bindIdentity registers an identity with an explicit kind and namespace binding
// directly in the fake store (no token/cert), for target-guard tests.
func bindIdentity(store *fakeStore, name, kind string, ns *domain.NamespaceRef) {
	store.identitiesByName[name] = domain.Identity{
		ID:        int64(len(store.identitiesByName) + 1),
		Name:      name,
		Kind:      kind,
		Namespace: ns,
		CreatedAt: time.Now(),
	}
}

// certOpSetup builds a service (CA bootstrapped) plus a non-admin "issuer"
// principal bound to nsA and granted the delegated admin:identity:cert op scoped
// to nsA. The grant is deliberately namespace-scoped (not env:*/app:*): the
// authorization now runs against the caller's home namespace (Fix B), so this
// whole guard suite would fail authorization outright under the old empty-NS-ref
// code — the "same-namespace allowed" case genuinely depends on the fix.
func certOpSetup(t *testing.T) (*Service, *fakeStore, Principal, domain.NamespaceRef, domain.NamespaceRef) {
	t.Helper()
	nsA := mkns("prod", "a")
	nsB := mkns("prod", "b")
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	store.addNamespace(nsA, domain.AuthMethodToken, domain.AuthMethodMTLS)
	store.addNamespace(nsB, domain.AuthMethodToken, domain.AuthMethodMTLS)
	store.addPolicy(domain.Policy{Name: "cert", Subject: "issuer", Allow: []domain.PolicyRule{
		{Operation: domain.OpAdminIdentityCert, Env: nsA.Env, App: nsA.App, KeyPattern: "*"},
	}})
	return s, store, boundClientPrincipal("issuer", nsA), nsA, nsB
}

// FINDING 1: a non-admin holding admin:identity:cert must not be able to mint a
// cert bundle for an admin identity or for any identity outside its namespace —
// either would let it authenticate as that identity via mTLS.

func TestIssueCertificateTargetGuard(t *testing.T) {
	ctx := context.Background()

	t.Run("admin-kind target denied even in own namespace", func(t *testing.T) {
		s, store, issuer, nsA, _ := certOpSetup(t)
		bindIdentity(store, "boss", domain.IdentityKindAdmin, &nsA)
		if _, err := s.IssueIdentityCertificate(ctx, issuer, "boss", 0); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("issue for admin target err = %v, want ErrPermissionDenied", err)
		}
		if !store.hasAudit("identity.cert.issue", "deny") {
			t.Error("admin-target denial not audited")
		}
	})

	t.Run("cross-namespace target denied", func(t *testing.T) {
		s, store, issuer, _, nsB := certOpSetup(t)
		bindIdentity(store, "stranger", domain.IdentityKindClient, &nsB)
		if _, err := s.IssueIdentityCertificate(ctx, issuer, "stranger", 0); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("issue for cross-ns target err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("unbound target denied", func(t *testing.T) {
		s, store, issuer, _, _ := certOpSetup(t)
		bindIdentity(store, "floating", domain.IdentityKindClient, nil)
		if _, err := s.IssueIdentityCertificate(ctx, issuer, "floating", 0); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("issue for unbound target err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("same-namespace client target allowed", func(t *testing.T) {
		s, store, issuer, nsA, _ := certOpSetup(t)
		bindIdentity(store, "buddy", domain.IdentityKindClient, &nsA)
		bundle, err := s.IssueIdentityCertificate(ctx, issuer, "buddy", 0)
		if err != nil {
			t.Fatalf("issue for same-ns target: %v", err)
		}
		if bundle.KeyPEM == "" || bundle.Serial == "" {
			t.Fatalf("incomplete bundle: %+v", bundle)
		}
	})

	t.Run("admin caller unrestricted", func(t *testing.T) {
		s, store, _, _, nsB := certOpSetup(t)
		bindIdentity(store, "boss", domain.IdentityKindAdmin, &nsB)
		if _, err := s.IssueIdentityCertificate(ctx, adminPrincipal(), "boss", 0); err != nil {
			t.Fatalf("admin issue for any target: %v", err)
		}
	})

	// A wildcard admin:identity:cert grant still authorizes (retains the coverage
	// certOpSetup gave up when it switched to a namespace-scoped grant).
	t.Run("wildcard grant allowed", func(t *testing.T) {
		nsA := mkns("prod", "a")
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		store.addNamespace(nsA, domain.AuthMethodToken, domain.AuthMethodMTLS)
		store.addPolicy(domain.Policy{Name: "cert", Subject: "issuer", Allow: []domain.PolicyRule{
			{Operation: domain.OpAdminIdentityCert, Env: "*", App: "*", KeyPattern: "*"},
		}})
		issuer := boundClientPrincipal("issuer", nsA)
		bindIdentity(store, "buddy", domain.IdentityKindClient, &nsA)
		if _, err := s.IssueIdentityCertificate(ctx, issuer, "buddy", 0); err != nil {
			t.Fatalf("wildcard grant issue: %v", err)
		}
	})

	// A grant scoped to a DIFFERENT namespace than the issuer's home does not
	// authorize, even for an in-home target: env/app scoping is enforced at the
	// authorization layer (before guardCertTarget).
	t.Run("grant scoped to another namespace denied", func(t *testing.T) {
		nsA := mkns("prod", "a")
		nsB := mkns("prod", "b")
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		store.addNamespace(nsA, domain.AuthMethodToken, domain.AuthMethodMTLS)
		store.addNamespace(nsB, domain.AuthMethodToken, domain.AuthMethodMTLS)
		store.addPolicy(domain.Policy{Name: "cert", Subject: "issuer", Allow: []domain.PolicyRule{
			{Operation: domain.OpAdminIdentityCert, Env: nsB.Env, App: nsB.App, KeyPattern: "*"},
		}})
		issuer := boundClientPrincipal("issuer", nsA) // home is nsA
		bindIdentity(store, "buddy", domain.IdentityKindClient, &nsA)
		if _, err := s.IssueIdentityCertificate(ctx, issuer, "buddy", 0); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("wrong-namespace grant err = %v, want ErrPermissionDenied", err)
		}
	})
}

func TestRevokeCertificateTargetGuard(t *testing.T) {
	ctx := context.Background()

	// seedCert binds an identity and records a certificate for it.
	seedCert := func(store *fakeStore, name, kind string, ns *domain.NamespaceRef, serial string) {
		bindIdentity(store, name, kind, ns)
		if err := store.InsertIdentityCert(ctx, name, domain.IdentityCert{Serial: serial, NotAfter: time.Now().Add(time.Hour)}); err != nil {
			t.Fatalf("seed cert: %v", err)
		}
	}

	t.Run("admin-kind target denied", func(t *testing.T) {
		s, store, issuer, nsA, _ := certOpSetup(t)
		seedCert(store, "boss", domain.IdentityKindAdmin, &nsA, "adm-1")
		if err := s.RevokeIdentityCertificate(ctx, issuer, "boss", "adm-1"); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("revoke admin target err = %v, want ErrPermissionDenied", err)
		}
		if !store.hasAudit("identity.cert.revoke", "deny") {
			t.Error("admin-target revoke denial not audited")
		}
	})

	t.Run("cross-namespace target denied", func(t *testing.T) {
		s, store, issuer, _, nsB := certOpSetup(t)
		seedCert(store, "stranger", domain.IdentityKindClient, &nsB, "str-1")
		if err := s.RevokeIdentityCertificate(ctx, issuer, "stranger", "str-1"); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("revoke cross-ns target err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("same-namespace client target allowed", func(t *testing.T) {
		s, store, issuer, nsA, _ := certOpSetup(t)
		seedCert(store, "buddy", domain.IdentityKindClient, &nsA, "bud-1")
		if err := s.RevokeIdentityCertificate(ctx, issuer, "buddy", "bud-1"); err != nil {
			t.Fatalf("revoke same-ns target: %v", err)
		}
		rec, err := store.GetIdentityCertBySerial(ctx, "bud-1")
		if err != nil {
			t.Fatalf("get cert: %v", err)
		}
		if rec.Cert.RevokedAt.IsZero() {
			t.Fatal("certificate was not marked revoked")
		}
	})

	t.Run("admin caller unrestricted", func(t *testing.T) {
		s, store, _, _, nsB := certOpSetup(t)
		seedCert(store, "stranger", domain.IdentityKindClient, &nsB, "str-2")
		if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "stranger", "str-2"); err != nil {
			t.Fatalf("admin revoke any target: %v", err)
		}
	})
}

// FINDING 2: a live mTLS watch stream must be torn down when the single
// certificate it presented is revoked, and a token stream must be torn down when
// its namespace is tightened to a method it no longer satisfies.

func TestReauthorizeWatchMTLSCertRevoked(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	store.addNamespace(tns, domain.AuthMethodMTLS)

	res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc", Kind: domain.IdentityKindClient, Namespace: &tns,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	serial := res.Cert.Serial
	nsCopy := tns
	pr := Principal{
		Identity: domain.Identity{Name: "svc", Kind: domain.IdentityKindClient, Namespace: &nsCopy},
		Method:   domain.AuthMethodMTLS,
		Serial:   serial,
	}
	sel := domain.WatchSelector{NS: tns, KeyPattern: "*"}

	// Baseline: a valid cert re-authorizes.
	if _, err := s.ReauthorizeWatch(ctx, pr, sel); err != nil {
		t.Fatalf("baseline reauth: %v", err)
	}

	// Revoking that single serial (identity stays enabled) must tear the stream down.
	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "svc", serial); err != nil {
		t.Fatalf("RevokeIdentityCertificate: %v", err)
	}
	if _, err := s.ReauthorizeWatch(ctx, pr, sel); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("reauth after cert revoke err = %v, want ErrUnauthenticated", err)
	}
}

func TestReauthorizeWatchNamespaceTightenedToMTLS(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addNamespace(tns, domain.AuthMethodToken, domain.AuthMethodMTLS)
	store.addIdentity("app", domain.IdentityKindClient, "kms_tok")
	pr := clientPrincipalTok("app", "kms_tok") // token method
	sel := domain.WatchSelector{NS: tns, KeyPattern: "*"}

	// Baseline: the token method is admitted, so reauth succeeds.
	if _, err := s.ReauthorizeWatch(ctx, pr, sel); err != nil {
		t.Fatalf("baseline reauth: %v", err)
	}

	// Tighten the namespace to mTLS-only; the token stream's next reauth is denied.
	if _, err := s.UpdateNamespace(ctx, adminPrincipal(), tns, "", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if _, err := s.ReauthorizeWatch(ctx, pr, sel); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reauth after tighten err = %v, want ErrPermissionDenied", err)
	}
}

// TestReauthorizeWatchAdminBypassesTightenedGate confirms the method-gate re-check
// preserves the admin management-plane bypass: an admin stream is not dropped
// when a namespace is tightened.
func TestReauthorizeWatchAdminBypassesTightenedGate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addNamespace(tns, domain.AuthMethodMTLS)
	store.addIdentity("root", domain.IdentityKindAdmin, "kms_admin")
	pr := Principal{
		Identity: domain.Identity{Name: "root", Kind: domain.IdentityKindAdmin},
		Method:   domain.AuthMethodToken,
		Token:    "kms_admin",
	}
	sel := domain.WatchSelector{NS: tns, KeyPattern: "*"}
	if _, err := s.ReauthorizeWatch(ctx, pr, sel); err != nil {
		t.Fatalf("admin reauth on mtls-only ns: %v", err)
	}
}
