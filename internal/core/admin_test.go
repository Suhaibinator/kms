package core

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestRequireAdminGatesPolicyWrites(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	valid := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretRead, Env: "prod", App: "app"}}}

	if _, err := s.CreatePolicy(ctx, clientPrincipal("app"), valid); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client CreatePolicy err = %v, want ErrPermissionDenied", err)
	}
	if !store.hasAudit("policy.write", "deny") {
		t.Error("policy write denial not audited")
	}

	out, err := s.CreatePolicy(ctx, adminPrincipal(), valid)
	if err != nil {
		t.Fatalf("admin CreatePolicy: %v", err)
	}
	if out.Allow[0].App != "app" || out.Allow[0].Env != "prod" {
		t.Fatalf("stored rule = %+v", out.Allow[0])
	}
	if len(store.policies) != 1 {
		t.Fatalf("policies stored = %d, want 1", len(store.policies))
	}
}

func TestCreatePolicyValidatesRules(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())
	bad := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: "secret:teleport", Env: "prod", App: "app"}}}
	if _, err := s.CreatePolicy(ctx, adminPrincipal(), bad); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestNamespaceCRUD(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Create with an explicit auth-method set.
	ns, err := s.CreateNamespace(ctx, adminPrincipal(), mkns("prod", "gradethis"), "prod ns",
		[]domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken})
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if len(ns.AllowedAuthMethods) != 2 {
		t.Fatalf("methods = %v, want 2", ns.AllowedAuthMethods)
	}

	// Default auth methods when none supplied is mTLS-only.
	def, err := s.CreateNamespace(ctx, adminPrincipal(), mkns("prod", "other"), "", nil)
	if err != nil {
		t.Fatalf("CreateNamespace(default): %v", err)
	}
	if len(def.AllowedAuthMethods) != 1 || def.AllowedAuthMethods[0] != domain.AuthMethodMTLS {
		t.Fatalf("default methods = %v, want [mtls]", def.AllowedAuthMethods)
	}

	// Update replaces description + methods.
	upd, err := s.UpdateNamespace(ctx, adminPrincipal(), mkns("prod", "gradethis"), "updated",
		[]domain.AuthMethod{domain.AuthMethodToken})
	if err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if upd.Description != "updated" || len(upd.AllowedAuthMethods) != 1 {
		t.Fatalf("update = %+v", upd)
	}
	if !store.hasAudit("namespace.update", "allow") {
		t.Error("namespace.update not audited")
	}

	// Delete an empty namespace succeeds.
	if err := s.DeleteNamespace(ctx, adminPrincipal(), mkns("prod", "other")); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if !store.hasAudit("namespace.delete", "allow") {
		t.Error("namespace.delete not audited")
	}
}

func TestDeleteNamespaceGuardsNonEmpty(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	if _, err := s.CreateNamespace(ctx, adminPrincipal(), tns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed param: %v", err)
	}
	if err := s.DeleteNamespace(ctx, adminPrincipal(), tns); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("delete non-empty err = %v, want ErrFailedPrecondition", err)
	}
}

func TestCreateNamespaceAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Client without the dedicated operation is denied.
	if _, err := s.CreateNamespace(ctx, clientPrincipal("app"), mkns("team", "x"), "", nil); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	// Granting admin:namespace:create lets the client create the namespace.
	store.addPolicy(domain.Policy{Name: "ns", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpAdminNamespaceCreate, Env: "team", App: "x"}}})
	if _, err := s.CreateNamespace(ctx, clientPrincipal("app"), mkns("team", "x"), "", nil); err != nil {
		t.Fatalf("authorized CreateNamespace: %v", err)
	}
}

func TestDelegatedNamespaceAdminObeysMethodGate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addNamespace(tns, domain.AuthMethodMTLS)
	store.addPolicy(domain.Policy{Name: "delegated", Subject: "app", Allow: []domain.PolicyRule{{
		Operation: domain.OpAdminNamespaceUpdate, Env: tns.Env, App: tns.App,
	}}})
	s := newTestService(store)
	pr := boundClientPrincipal("app", tns) // token-authenticated

	if _, err := s.UpdateNamespace(ctx, pr, tns, "bypass", []domain.AuthMethod{domain.AuthMethodToken}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("token delegated update err = %v, want ErrPermissionDenied", err)
	}
	got, _ := store.GetNamespace(ctx, tns)
	if got.Description == "bypass" {
		t.Fatal("disallowed token caller changed an mTLS-only namespace")
	}

	pr.Method = domain.AuthMethodMTLS
	if _, err := s.UpdateNamespace(ctx, pr, tns, "allowed", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
		t.Fatalf("mTLS delegated update: %v", err)
	}
}

func TestCreateIdentity(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	// Non-admin denied.
	if _, err := s.CreateIdentity(ctx, clientPrincipal("app"), CreateIdentityInput{Name: "svc", Kind: domain.IdentityKindClient}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	// Invalid name and kind rejected.
	if _, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{Name: "bad name!", Kind: domain.IdentityKindClient}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bad name err = %v, want ErrInvalidArgument", err)
	}
	if _, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{Name: "svc", Kind: "robot"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bad kind err = %v, want ErrInvalidArgument", err)
	}

	// Token identity: usable token returned once, no cert.
	res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc", Kind: domain.IdentityKindClient, AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if res.Identity.Name != "svc" {
		t.Fatalf("identity name = %q", res.Identity.Name)
	}
	if !strings.HasPrefix(res.Token, "kms_") {
		t.Fatalf("token %q missing kms_ prefix", res.Token)
	}
	if res.Cert != nil {
		t.Fatal("token-only identity should not receive a cert")
	}
	if _, err := s.Authenticate(ctx, res.Token, "ip", "ua"); err != nil {
		t.Fatalf("Authenticate with new token: %v", err)
	}

	// Both methods: token and cert.
	both, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "dual", Kind: domain.IdentityKindClient,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodToken, domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatalf("CreateIdentity(both): %v", err)
	}
	if both.Token == "" || both.Cert == nil {
		t.Fatalf("both-method identity missing token or cert: token=%q cert=%v", both.Token, both.Cert)
	}
	if !both.Identity.HasToken || len(both.Identity.Certs) != 1 {
		t.Fatalf("identity view = %+v, want HasToken + 1 cert", both.Identity)
	}
}

func TestCreateIdentityBoundNamespace(t *testing.T) {
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
	if res.Identity.Namespace == nil || *res.Identity.Namespace != tns {
		t.Fatalf("bound namespace = %v, want %v", res.Identity.Namespace, tns)
	}
}

func TestIssueAndRevokeCertificate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	store.addIdentity("svc", domain.IdentityKindClient, "")

	bundle, err := s.IssueIdentityCertificate(ctx, adminPrincipal(), "svc", 0)
	if err != nil {
		t.Fatalf("IssueIdentityCertificate: %v", err)
	}
	if bundle.KeyPEM == "" || bundle.Serial == "" {
		t.Fatalf("incomplete bundle: %+v", bundle)
	}
	if !store.hasAudit("identity.cert.issue", "allow") {
		t.Error("cert issue not audited")
	}

	// Revoking a serial that belongs to a different identity is a not-found.
	store.addIdentity("other", domain.IdentityKindClient, "")
	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "other", bundle.Serial); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-identity revoke err = %v, want ErrNotFound", err)
	}

	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "svc", bundle.Serial); err != nil {
		t.Fatalf("RevokeIdentityCertificate: %v", err)
	}
	if !store.hasAudit("identity.cert.revoke", "allow") {
		t.Error("cert revoke not audited")
	}
}

func TestRotateIdentityToken(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "kms_old")

	if _, err := s.RotateIdentityToken(ctx, clientPrincipal("svc"), "svc"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	newTok, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc")
	if err != nil {
		t.Fatalf("RotateIdentityToken: %v", err)
	}
	if _, err := s.Authenticate(ctx, newTok, "ip", "ua"); err != nil {
		t.Fatalf("Authenticate with rotated token: %v", err)
	}
}

func TestRotateIdentityTokenRejectsCertOnly(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "") // cert-only, no token

	if _, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc"); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("rotate cert-only err = %v, want ErrFailedPrecondition", err)
	}
}

func TestRevokeIdentity(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "kms_tok")

	if err := s.RevokeIdentity(ctx, clientPrincipal("svc"), "svc"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	if err := s.RevokeIdentity(ctx, adminPrincipal(), "svc"); err != nil {
		t.Fatalf("RevokeIdentity: %v", err)
	}
	if id := store.identitiesByName["svc"]; !id.Disabled {
		t.Fatal("identity not marked disabled")
	}
}

func TestListKeyMetadataStripsMaterial(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.keys = []domain.KeyMetadata{{
		ID: "kek-1", Source: domain.KeySourceFile, State: domain.KeyStateActive,
		KeyCheck: []byte("verifier"), KDFSalt: []byte("salt"),
	}}
	s := newTestService(store)

	if _, err := s.ListKeyMetadata(ctx, clientPrincipal("app")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	keys, err := s.ListKeyMetadata(ctx, adminPrincipal())
	if err != nil {
		t.Fatalf("ListKeyMetadata: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(keys))
	}
	if keys[0].KeyCheck != nil || keys[0].KDFSalt != nil {
		t.Fatal("key verifier/salt material was not stripped")
	}
}

func TestListAuditAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	if _, _, err := s.ListAuditEvents(ctx, clientPrincipal("app"), domain.AuditFilter{}, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	store.addPolicy(domain.Policy{Name: "a", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpAdminAuditRead, Env: "*", App: "*"}}})
	if _, _, err := s.ListAuditEvents(ctx, clientPrincipal("app"), domain.AuditFilter{}, storage.ListPage{}); err != nil {
		t.Fatalf("authorized ListAuditEvents: %v", err)
	}
	if _, _, err := s.ListAuditEvents(ctx, adminPrincipal(), domain.AuditFilter{}, storage.ListPage{}); err != nil {
		t.Fatalf("admin ListAuditEvents: %v", err)
	}
}

func TestRotateKEKRewrapsSecretsAndCA(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	caPEM, err := s.CACertPEM()
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}

	putSecret(t, s, PutSecretInput{Ref: tref("a"), Value: []byte("alpha"), ContentType: "text/plain"})
	putSecret(t, s, PutSecretInput{Ref: tref("b"), Value: []byte("bravo"), ContentType: "text/plain"})

	// Non-admin cannot rotate.
	if _, _, err := s.RotateKEK(ctx, clientPrincipal("app"),
		domain.KeyMetadata{ID: "kek-rotated"}, bytes.Repeat([]byte{0x9}, 32)); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client RotateKEK err = %v, want ErrPermissionDenied", err)
	}

	secrets, ca, err := s.RotateKEK(ctx, adminPrincipal(),
		domain.KeyMetadata{ID: "kek-rotated"}, bytes.Repeat([]byte{0x9}, 32))
	if err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	if secrets != 2 {
		t.Fatalf("secrets rewrapped = %d, want 2", secrets)
	}
	if ca != 1 {
		t.Fatalf("ca rewrapped = %d, want 1", ca)
	}

	// Secrets still decrypt under the rotated KEK.
	for _, tc := range []struct{ key, want string }{{"a", "alpha"}, {"b", "bravo"}} {
		val, err := s.GetSecret(ctx, adminPrincipal(), tref(tc.key), 0, "")
		if err != nil {
			t.Fatalf("GetSecret(%s) after rotate: %v", tc.key, err)
		}
		if string(val.Value) != tc.want {
			t.Fatalf("GetSecret(%s) = %q, want %q", tc.key, val.Value, tc.want)
		}
	}

	// The rewrapped CA key loads under a keyring holding only the new KEK,
	// yielding the same CA certificate.
	newKEK, err := crypto.NewKEKFromMaterial("kek-rotated", bytes.Repeat([]byte{0x9}, 32))
	if err != nil {
		t.Fatalf("NewKEKFromMaterial: %v", err)
	}
	s2 := newTestService(store)
	s2.SetKeyring(crypto.NewKeyring(newKEK))
	if err := s2.BootstrapCA(ctx); err != nil {
		t.Fatalf("BootstrapCA after rotate: %v", err)
	}
	reloaded, err := s2.CACertPEM()
	if err != nil {
		t.Fatalf("CACertPEM after rotate: %v", err)
	}
	if !bytes.Equal(caPEM, reloaded) {
		t.Fatal("CA certificate changed after KEK rotation")
	}
}
