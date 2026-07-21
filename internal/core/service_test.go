package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

// Canonical test namespace and ref helpers. tns allows both token and mTLS so
// the per-namespace auth-method gate admits token-authenticated client
// principals used throughout these tests.
var tns = domain.NamespaceRef{Env: "prod", App: "app"}

func tref(key string) domain.Ref { return domain.Ref{NS: tns, Key: key} }

func mkns(env, app string) domain.NamespaceRef { return domain.NamespaceRef{Env: env, App: app} }
func mkref(env, app, key string) domain.Ref {
	return domain.Ref{NS: mkns(env, app), Key: key}
}

// seedTokenNS registers the standard test namespace permitting token auth.
func seedTokenNS(f *fakeStore) { f.addNamespace(tns, domain.AuthMethodToken, domain.AuthMethodMTLS) }

func newTestService(store *fakeStore) *Service {
	return New(store, zap.NewNop(), "test")
}

// withKeyring attaches a working keyring so secret operations can run.
func withKeyring(t *testing.T, s *Service) *crypto.Keyring {
	t.Helper()
	kek, err := crypto.NewKEKFromMaterial("kek-test", bytes.Repeat([]byte{0x7}, 32))
	if err != nil {
		t.Fatalf("NewKEKFromMaterial: %v", err)
	}
	ring := crypto.NewKeyring(kek)
	s.SetKeyring(ring)
	return ring
}

// withCA attaches a keyring and bootstraps the built-in CA.
func withCA(t *testing.T, s *Service) {
	t.Helper()
	withKeyring(t, s)
	if err := s.BootstrapCA(context.Background()); err != nil {
		t.Fatalf("BootstrapCA: %v", err)
	}
}

func adminPrincipal() Principal {
	return Principal{Identity: domain.Identity{Name: "root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
}

// clientPrincipal is a token-authenticated client with no namespace binding.
func clientPrincipal(name string) Principal {
	return Principal{Identity: domain.Identity{Name: name, Kind: domain.IdentityKindClient}, Method: domain.AuthMethodToken}
}

// clientPrincipalTok carries the bearer token too, as the transports do, so
// ReauthorizeWatch (which re-authenticates the token) can validate it.
func clientPrincipalTok(name, token string) Principal {
	return Principal{Identity: domain.Identity{Name: name, Kind: domain.IdentityKindClient}, Method: domain.AuthMethodToken, Token: token}
}

// boundClientPrincipal is a token client bound to home namespace ns.
func boundClientPrincipal(name string, ns domain.NamespaceRef) Principal {
	pr := clientPrincipal(name)
	nsCopy := ns
	pr.Identity.Namespace = &nsCopy
	return pr
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addIdentity("app", domain.IdentityKindClient, "kms_good-token")
	s := newTestService(store)

	t.Run("valid token", func(t *testing.T) {
		id, err := s.Authenticate(ctx, "kms_good-token", "1.2.3.4", "agent")
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if id.Name != "app" {
			t.Fatalf("identity = %q, want app", id.Name)
		}
	})

	t.Run("empty token", func(t *testing.T) {
		_, err := s.Authenticate(ctx, "   ", "ip", "ua")
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("unknown token audits failure", func(t *testing.T) {
		before := len(store.audits)
		_, err := s.Authenticate(ctx, "kms_wrong", "9.9.9.9", "ua")
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		if len(store.audits) != before+1 {
			t.Fatalf("expected an audit event for the failed auth")
		}
		ev, _ := store.lastAudit()
		if ev.EventType != "auth.failure" || ev.Decision != "deny" {
			t.Fatalf("audit = %+v, want auth.failure/deny", ev)
		}
	})

	t.Run("disabled identity is rejected", func(t *testing.T) {
		store.identitiesByHash[string(crypto.TokenHash("kms_disabled"))] =
			domain.Identity{Name: "old", Kind: domain.IdentityKindClient, Disabled: true}
		_, err := s.Authenticate(ctx, "kms_disabled", "ip", "ua")
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
	})
}

func TestAuditCanBeDisabled(t *testing.T) {
	store := newFakeStore()
	s := newTestService(store)
	s.SetAuditEnabled(false)
	s.auditName(context.Background(), adminPrincipal(), "test.event", domain.ResourceIdentity, "subject", "allow", nil)
	if len(store.audits) != 0 {
		t.Fatalf("disabled audit wrote %d events", len(store.audits))
	}
	store.auditErr = errors.New("audit sink unavailable")
	if err := s.auditStrict(context.Background(), domain.AuditEvent{EventType: "strict"}); err != nil {
		t.Fatalf("disabled auditStrict should be a no-op: %v", err)
	}
}

func TestAuthorizeAdminBypass(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)

	// Admin reads without any policy present and without a seeded namespace
	// (admin bypasses the method gate).
	got, err := s.GetParameter(ctx, adminPrincipal(), tref("x"), 0, "")
	if err != nil {
		t.Fatalf("admin GetParameter: %v", err)
	}
	if got.Value != "1" {
		t.Fatalf("value = %q, want 1", got.Value)
	}
}

func TestAuthorizeClientDeniedByDefault(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)

	_, err := s.GetParameter(ctx, clientPrincipal("app"), tref("x"), 0, "")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if !store.hasAudit("authz.denial", "deny") {
		t.Fatal("authorization denial was not audited as authz.denial/deny")
	}
}

func TestAuthorizeClientAllowedByPolicy(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.addPolicy(domain.Policy{
		Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "prod", App: "app"}},
	})
	s := newTestService(store)

	got, err := s.GetParameter(ctx, clientPrincipal("app"), tref("x"), 0, "")
	if err != nil {
		t.Fatalf("GetParameter: %v", err)
	}
	if got.Value != "1" {
		t.Fatalf("value = %q, want 1", got.Value)
	}
}

func TestAuthorizePolicyLoadFailureDenies(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	store.policiesErr = errors.New("db down")
	s := newTestService(store)

	// Fail closed: if policy retrieval fails, access is denied, not granted.
	_, err := s.GetParameter(ctx, clientPrincipal("app"), tref("x"), 0, "")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}

func TestReady(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// No keyring yet: not ready.
	if err := s.Ready(ctx); !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("Ready(no keyring) = %v, want ErrNotReady", err)
	}
	withKeyring(t, s)
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("Ready(ready) = %v, want nil", err)
	}
	// Database unreachable: not ready.
	store.pingErr = errors.New("unreachable")
	if err := s.Ready(ctx); !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("Ready(ping fail) = %v, want ErrNotReady", err)
	}
}

func TestSecretOpsRequireKeyring(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store) // no keyring attached

	_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "")
	if !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("GetSecret without keyring err = %v, want ErrNotReady", err)
	}
	_, err = s.PutSecret(ctx, adminPrincipal(), PutSecretInput{Ref: tref("s"), Value: []byte("v")})
	if !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("PutSecret without keyring err = %v, want ErrNotReady", err)
	}
}

// --- namespace auth-method gate -------------------------------------------

func TestMethodGateRejectsDisallowedMethod(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addNamespace(tns, domain.AuthMethodMTLS) // mTLS-only
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The identity is bound to the namespace (implicit grant would otherwise let
	// it read), but it authenticated with a token the namespace does not admit.
	pr := boundClientPrincipal("app", tns)
	pr.Method = domain.AuthMethodToken
	s := newTestService(store)

	_, err := s.GetParameter(ctx, pr, tref("x"), 0, "")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if !store.hasAudit("authz.method_denied", "deny") {
		t.Fatal("method-gate denial not audited")
	}
}

func TestMethodGateAllowsAfterUpdateNamespace(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addNamespace(tns, domain.AuthMethodMTLS)
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)
	pr := boundClientPrincipal("app", tns) // token method, home namespace

	// Denied while the namespace is mTLS-only.
	if _, err := s.GetParameter(ctx, pr, tref("x"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("pre-update err = %v, want ErrPermissionDenied", err)
	}
	// Add "token" via UpdateNamespace; now the token client is admitted and the
	// implicit home grant lets it read.
	if _, err := s.UpdateNamespace(ctx, adminPrincipal(), tns, "", []domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if _, err := s.GetParameter(ctx, pr, tref("x"), 0, ""); err != nil {
		t.Fatalf("post-update GetParameter: %v", err)
	}
}

func TestMethodGateAdminBypass(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addNamespace(tns, domain.AuthMethodMTLS) // mTLS-only
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)

	// Admin (management plane) bypasses the method gate even for an mTLS-only
	// namespace, using a token.
	if _, err := s.GetParameter(ctx, adminPrincipal(), tref("x"), 0, ""); err != nil {
		t.Fatalf("admin bypass GetParameter: %v", err)
	}
}

// --- implicit home-namespace grant ----------------------------------------

func TestImplicitHomeGrant(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)                                                 // home namespace, token allowed
	store.addNamespace(mkns("staging", "app"), domain.AuthMethodToken) // a foreign namespace
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)
	home := boundClientPrincipal("app", tns)

	t.Run("home read allowed without policy", func(t *testing.T) {
		if _, err := s.GetParameter(ctx, home, tref("x"), 0, ""); err != nil {
			t.Fatalf("home read: %v", err)
		}
	})

	t.Run("home write denied without policy", func(t *testing.T) {
		if _, _, err := s.PutParameter(ctx, home, tref("y"), "2", "integer", "{}"); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("home write err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("cross-namespace read denied without policy", func(t *testing.T) {
		if _, err := s.GetParameter(ctx, home, mkref("staging", "app", "x"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("cross-ns read err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("deny rule beats implicit grant", func(t *testing.T) {
		store.policies = nil
		store.addPolicy(domain.Policy{Name: "d", Subject: "app",
			Deny: []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "prod", App: "app"}}})
		if _, err := s.GetParameter(ctx, home, tref("x"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("deny-over-grant err = %v, want ErrPermissionDenied", err)
		}
		store.policies = nil
	})
}

// --- built-in CA + client certificates ------------------------------------

func TestBootstrapCAIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	if err := s.BootstrapCA(ctx); err != nil {
		t.Fatalf("BootstrapCA (generate): %v", err)
	}
	first, err := s.CACertPEM()
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}
	if block, _ := pem.Decode(first); block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("CA cert PEM is not a CERTIFICATE block")
	}

	// A second service over the same store loads (not regenerates) the CA.
	s2 := newTestService(store)
	withKeyring(t, s2)
	if err := s2.BootstrapCA(ctx); err != nil {
		t.Fatalf("BootstrapCA (load): %v", err)
	}
	second, err := s2.CACertPEM()
	if err != nil {
		t.Fatalf("CACertPEM (2): %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("reload produced a different CA certificate")
	}
}

func TestVerifyClientCertLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	// Create a cert-only client identity; the bundle is returned once.
	res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc", Kind: domain.IdentityKindClient, AuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if res.Cert == nil {
		t.Fatal("expected a cert bundle")
	}
	if res.Token != "" {
		t.Fatal("cert-only identity should not receive a token")
	}
	leaf := parseCertPEM(t, res.Cert.CertPEM)

	// A valid cert maps to the identity.
	id, err := s.VerifyClientCert(ctx, leaf, "ip", "ua")
	if err != nil {
		t.Fatalf("VerifyClientCert: %v", err)
	}
	if id.Name != "svc" {
		t.Fatalf("mapped identity = %q, want svc", id.Name)
	}

	// Revoke by serial: verification now fails.
	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "svc", res.Cert.Serial); err != nil {
		t.Fatalf("RevokeIdentityCertificate: %v", err)
	}
	if _, err := s.VerifyClientCert(ctx, leaf, "ip", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked cert err = %v, want ErrUnauthenticated", err)
	}
}

func TestVerifyClientCertRejectsFingerprintMismatch(t *testing.T) {
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
	leaf := parseCertPEM(t, res.Cert.CertPEM)

	// Keep the identity claims and serial unchanged on a valid leaf from a
	// different trusted issuer. Core must bind the exact presented leaf to the
	// enrolled record after the TLS layer has accepted its chain.
	alternate := alternateTrustedLeaf(t, leaf)
	if _, err := s.VerifyClientCert(ctx, alternate, "ip", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("same SAN/serial with different fingerprint err = %v, want ErrUnauthenticated", err)
	}
}

func TestVerifyClientCertRejectsDisabledIdentity(t *testing.T) {
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
	leaf := parseCertPEM(t, res.Cert.CertPEM)

	if err := s.RevokeIdentity(ctx, adminPrincipal(), "svc"); err != nil {
		t.Fatalf("RevokeIdentity: %v", err)
	}
	if _, err := s.VerifyClientCert(ctx, leaf, "ip", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("disabled identity cert err = %v, want ErrUnauthenticated", err)
	}
}

func TestVerifyClientCertRejectsExpired(t *testing.T) {
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
	leaf := parseCertPEM(t, res.Cert.CertPEM)

	// Advance the service clock past the certificate's not_after.
	s.now = func() time.Time { return res.Cert.NotAfter.Add(time.Hour) }
	if _, err := s.VerifyClientCert(ctx, leaf, "ip", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expired cert err = %v, want ErrUnauthenticated", err)
	}
}

func TestVerifyClientCertRejectsUnknownCert(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	// A well-formed cert from the CA for an identity that was never recorded
	// (no identity_certs row) must be rejected.
	authority := s.ca.Load()
	issued, err := authority.IssueClientCert("ghost", 0)
	if err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}
	leaf := parseCertPEM(t, string(issued.CertPEM))
	if _, err := s.VerifyClientCert(ctx, leaf, "ip", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("unknown cert err = %v, want ErrUnauthenticated", err)
	}
}

func TestWhoAmI(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())

	pr := boundClientPrincipal("svc", tns)
	pr.Method = domain.AuthMethodMTLS
	who, err := s.WhoAmI(ctx, pr)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if who.Name != "svc" || who.Kind != domain.IdentityKindClient {
		t.Fatalf("WhoAmI = %+v", who)
	}
	if who.Namespace == nil || *who.Namespace != tns {
		t.Fatalf("WhoAmI namespace = %v, want %v", who.Namespace, tns)
	}
	if who.Method != domain.AuthMethodMTLS {
		t.Fatalf("WhoAmI method = %q, want mtls", who.Method)
	}
}

// parseCertPEM decodes a PEM certificate for tests.
func parseCertPEM(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

// alternateTrustedLeaf returns a valid client leaf signed by a separate CA,
// while retaining the enrolled leaf's serial and identity claims.
func alternateTrustedLeaf(t *testing.T, enrolled *x509.Certificate) *x509.Certificate {
	t.Helper()
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate alternate CA key: %v", err)
	}
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPub, caPriv)
	if err != nil {
		t.Fatalf("create alternate CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse alternate CA: %v", err)
	}

	leafPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate alternate leaf key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          new(big.Int).Set(enrolled.SerialNumber),
		Subject:               enrolled.Subject,
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  enrolled.URIs,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, leafPub, caPriv)
	if err != nil {
		t.Fatalf("create alternate leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse alternate leaf: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("verify alternate leaf: %v", err)
	}
	return leaf
}
