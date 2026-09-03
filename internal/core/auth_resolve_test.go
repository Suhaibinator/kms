package core

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

// --- fixtures --------------------------------------------------------------

// cliAdminPrincipal mirrors the principal the offline CLI synthesizes for
// IssueLocalAdminCertificate: admin kind, no credential material (host access
// to the database and master key is the credential).
func cliAdminPrincipal() Principal {
	return Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
}

// issueAdminCert seeds an admin identity with a bearer token and mints its
// offline client certificate, returning the parsed leaf and the token — the
// exact credential pair an admin must present under the requirement.
func issueAdminCert(t *testing.T, s *Service, store *fakeStore, name string) (*x509.Certificate, string) {
	t.Helper()
	token, _, err := crypto.GenerateToken("kms")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	store.addIdentity(name, domain.IdentityKindAdmin, token)
	bundle, err := s.IssueLocalAdminCertificate(context.Background(), cliAdminPrincipal(), name, 0)
	if err != nil {
		t.Fatalf("IssueLocalAdminCertificate(%s): %v", name, err)
	}
	return parseCertPEM(t, bundle.CertPEM), token
}

// adminCertFixture builds a service with a bootstrapped CA and one admin
// identity holding both credentials, as the offline CLI would leave it.
func adminCertFixture(t *testing.T, name string) (*Service, *fakeStore, *x509.Certificate, string) {
	t.Helper()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	cert, token := issueAdminCert(t, s, store, name)
	return s, store, cert, token
}

// newClient creates a client identity with the requested auth methods and
// returns its leaf certificate (nil when none was minted) and bearer token
// (empty when none was minted). A non-nil namespace is seeded if absent.
func newClient(t *testing.T, s *Service, store *fakeStore, name string, ns *domain.NamespaceRef, methods ...domain.AuthMethod) (*x509.Certificate, string) {
	t.Helper()
	ctx := context.Background()
	if ns != nil {
		if _, err := store.GetNamespace(ctx, *ns); err != nil {
			store.addNamespace(*ns, domain.AuthMethodMTLS, domain.AuthMethodToken)
		}
	}
	res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: name, Kind: domain.IdentityKindClient, Namespace: ns, AuthMethods: methods,
	})
	if err != nil {
		t.Fatalf("CreateIdentity(%s): %v", name, err)
	}
	var leaf *x509.Certificate
	if res.Cert != nil {
		leaf = parseCertPEM(t, res.Cert.CertPEM)
	}
	return leaf, res.Token
}

// certOnlyClient creates an mTLS-only client identity and returns its leaf.
func certOnlyClient(t *testing.T, s *Service, store *fakeStore, name string, ns *domain.NamespaceRef) *x509.Certificate {
	t.Helper()
	leaf, _ := newClient(t, s, store, name, ns, domain.AuthMethodMTLS)
	return leaf
}

// requireLastAudit asserts the most recent audit row is eventType/decision and
// returns it.
func requireLastAudit(t *testing.T, store *fakeStore, eventType, decision string) domain.AuditEvent {
	t.Helper()
	ev, ok := store.lastAudit()
	if !ok {
		t.Fatalf("no audit events recorded, want %s/%s", eventType, decision)
	}
	if ev.EventType != eventType || ev.Decision != decision {
		t.Fatalf("last audit = %s/%s, want %s/%s", ev.EventType, ev.Decision, eventType, decision)
	}
	return ev
}

// assertMetadata checks that every want appears verbatim in the event's
// serialized metadata.
func assertMetadata(t *testing.T, ev domain.AuditEvent, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(ev.Metadata, want) {
			t.Errorf("audit metadata %s missing %s", ev.Metadata, want)
		}
	}
}

// --- the requirement's default --------------------------------------------

func TestNewEnablesAdminClientCertRequirement(t *testing.T) {
	s := newTestService(newFakeStore())
	if !s.AdminRequireClientCert() {
		t.Fatal("a freshly constructed service must require admin client certificates")
	}
	s.SetAdminRequireClientCert(false)
	if s.AdminRequireClientCert() {
		t.Fatal("SetAdminRequireClientCert(false) did not relax the requirement")
	}
	s.SetAdminRequireClientCert(true)
	if !s.AdminRequireClientCert() {
		t.Fatal("SetAdminRequireClientCert(true) did not restore the requirement")
	}
}

// --- client credential combination -----------------------------------------

func TestResolvePrincipalClientCredentials(t *testing.T) {
	ctx := context.Background()

	t.Run("token only", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		_, token := newClient(t, s, store, "app", nil, domain.AuthMethodToken)

		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token})
		if err != nil {
			t.Fatalf("ResolvePrincipal: %v", err)
		}
		if pr.Identity.Name != "app" || pr.Identity.Kind != domain.IdentityKindClient {
			t.Fatalf("identity = %+v, want client app", pr.Identity)
		}
		if pr.Method != domain.AuthMethodToken {
			t.Fatalf("method = %q, want token", pr.Method)
		}
		if pr.Token != token {
			t.Fatalf("token was not retained: %q", pr.Token)
		}
		if pr.Serial != "" || pr.Fingerprint != "" {
			t.Fatalf("token principal carries certificate bindings: serial=%q fingerprint=%q", pr.Serial, pr.Fingerprint)
		}
	})

	t.Run("certificate only", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		leaf := certOnlyClient(t, s, store, "svc", nil)

		pr, err := s.ResolvePrincipal(ctx, CredentialInput{PeerCert: leaf})
		if err != nil {
			t.Fatalf("ResolvePrincipal: %v", err)
		}
		if pr.Identity.Name != "svc" {
			t.Fatalf("identity = %q, want svc", pr.Identity.Name)
		}
		if pr.Method != domain.AuthMethodMTLS {
			t.Fatalf("method = %q, want mtls", pr.Method)
		}
		if pr.Serial != CertSerial(leaf) || pr.Fingerprint != CertFingerprint(leaf) {
			t.Fatalf("certificate bindings = %q/%q, want %q/%q", pr.Serial, pr.Fingerprint, CertSerial(leaf), CertFingerprint(leaf))
		}
		if pr.Token != "" {
			t.Fatalf("cert-only principal carries a token: %q", pr.Token)
		}
	})

	t.Run("certificate and token for the same identity", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		leaf, token := newClient(t, s, store, "both", nil, domain.AuthMethodMTLS, domain.AuthMethodToken)
		if leaf == nil || token == "" {
			t.Fatalf("fixture did not mint both credentials (cert=%v token=%q)", leaf != nil, token)
		}

		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: leaf})
		if err != nil {
			t.Fatalf("ResolvePrincipal: %v", err)
		}
		if pr.Identity.Name != "both" {
			t.Fatalf("identity = %q, want both", pr.Identity.Name)
		}
		// mTLS is the stronger proof, so it wins the Method, but the token is
		// retained for stream re-authentication.
		if pr.Method != domain.AuthMethodMTLS {
			t.Fatalf("method = %q, want mtls", pr.Method)
		}
		if pr.Token != token {
			t.Fatalf("token was not retained: %q", pr.Token)
		}
		if pr.Serial != CertSerial(leaf) {
			t.Fatalf("serial = %q, want %q", pr.Serial, CertSerial(leaf))
		}
	})
}

func TestResolvePrincipalCredentialMismatchDenied(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	leaf := certOnlyClient(t, s, store, "svc", nil)
	_, otherToken := newClient(t, s, store, "other", nil, domain.AuthMethodToken)

	pr, err := s.ResolvePrincipal(ctx, CredentialInput{
		Token: otherToken, PeerCert: leaf, RemoteAddr: "10.1.1.1", UserAgent: "agent", RequestID: "req-1",
	})
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if pr.Identity.Name != "" || pr.Method != "" {
		t.Fatalf("denied principal must be zero-valued, got %+v", pr)
	}
	ev := requireLastAudit(t, store, "auth.failure", "deny")
	if ev.ActorType != "unknown" {
		t.Errorf("actor type = %q, want unknown (neither credential may be attributed)", ev.ActorType)
	}
	if ev.ActorIdentity != "" {
		t.Errorf("actor identity = %q, want empty", ev.ActorIdentity)
	}
	assertMetadata(t, ev, `"reason":"credential_mismatch"`, `"method":"mtls+token"`)
	if ev.SourceIP != "10.1.1.1" || ev.UserAgent != "agent" || ev.RequestID != "req-1" {
		t.Errorf("request context not recorded: %+v", ev)
	}
}

func TestResolvePrincipalNoCredentialsIsNotAudited(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	before := len(store.audits)
	if _, err := s.ResolvePrincipal(ctx, CredentialInput{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if got := len(store.audits); got != before {
		t.Fatalf("unauthenticated probe wrote %d audit rows; a caller that presents nothing must not be auditable noise", got-before)
	}
	// Whitespace-only tokens count as "nothing presented" too.
	if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: "   "}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("blank token err = %v, want ErrUnauthenticated", err)
	}
	if got := len(store.audits); got != before {
		t.Fatalf("blank token wrote %d audit rows", got-before)
	}

	// A presented-but-invalid credential IS audited (by Authenticate).
	if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: "kms_nope"}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("invalid token err = %v, want ErrUnauthenticated", err)
	}
	if len(store.audits) != before+1 {
		t.Fatalf("invalid token audit rows = %d, want 1", len(store.audits)-before)
	}
	requireLastAudit(t, store, "auth.failure", "deny")
}

func TestResolvePrincipalRevokedCertFallsBackToToken(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	leaf, token := newClient(t, s, store, "svc", nil, domain.AuthMethodMTLS, domain.AuthMethodToken)

	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "svc", CertSerial(leaf)); err != nil {
		t.Fatalf("RevokeIdentityCertificate: %v", err)
	}

	// The certificate is dropped (recorded as auth.credential_ignored, see
	// TestResolvePrincipalIgnoredCredentialIsAuditedAsAllowed), but the
	// still-valid token admits the non-admin caller by the token method.
	pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: leaf})
	if err != nil {
		t.Fatalf("ResolvePrincipal after cert revocation: %v", err)
	}
	if pr.Identity.Name != "svc" {
		t.Fatalf("identity = %q, want svc", pr.Identity.Name)
	}
	if pr.Method != domain.AuthMethodToken {
		t.Fatalf("method = %q, want token", pr.Method)
	}
	if pr.Serial != "" || pr.Fingerprint != "" {
		t.Fatalf("revoked certificate still bound: serial=%q fingerprint=%q", pr.Serial, pr.Fingerprint)
	}
	if pr.Token != token {
		t.Fatalf("token was not retained: %q", pr.Token)
	}
}

// --- admin admission --------------------------------------------------------

func TestResolvePrincipalAdminRequiresCertificateAndToken(t *testing.T) {
	ctx := context.Background()

	t.Run("token alone denied", func(t *testing.T) {
		s, store, _, token := adminCertFixture(t, "ops")
		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, RemoteAddr: "10.0.0.9", UserAgent: "cli", RequestID: "r1"})
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		if pr.Identity.Name != "" {
			t.Fatalf("denied principal must be zero-valued, got %+v", pr)
		}
		ev := requireLastAudit(t, store, "auth.failure", "deny")
		if ev.ActorIdentity != "ops" || ev.ActorType != domain.IdentityKindAdmin {
			t.Errorf("actor = %q/%q, want ops/admin (identity was cryptographically proven)", ev.ActorIdentity, ev.ActorType)
		}
		assertMetadata(t, ev, `"reason":"admin_client_cert_required"`, `"method":"token"`)
		if ev.SourceIP != "10.0.0.9" || ev.UserAgent != "cli" || ev.RequestID != "r1" {
			t.Errorf("request context not recorded: %+v", ev)
		}
		if strings.Contains(ev.Metadata, token) {
			t.Error("audit metadata leaked the bearer token")
		}
	})

	t.Run("certificate alone denied", func(t *testing.T) {
		s, store, cert, _ := adminCertFixture(t, "ops")
		if _, err := s.ResolvePrincipal(ctx, CredentialInput{PeerCert: cert}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		ev := requireLastAudit(t, store, "auth.failure", "deny")
		if ev.ActorIdentity != "ops" || ev.ActorType != domain.IdentityKindAdmin {
			t.Errorf("actor = %q/%q, want ops/admin", ev.ActorIdentity, ev.ActorType)
		}
		assertMetadata(t, ev, `"reason":"admin_client_cert_required"`, `"method":"mtls"`)
	})

	t.Run("certificate and token admitted", func(t *testing.T) {
		s, _, cert, token := adminCertFixture(t, "ops")
		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: cert})
		if err != nil {
			t.Fatalf("ResolvePrincipal: %v", err)
		}
		if pr.Identity.Name != "ops" || !pr.IsAdmin() {
			t.Fatalf("identity = %+v, want admin ops", pr.Identity)
		}
		if pr.Method != domain.AuthMethodMTLS {
			t.Fatalf("method = %q, want mtls", pr.Method)
		}
		if pr.Token != token {
			t.Fatalf("token was not retained: %q", pr.Token)
		}
		if pr.Serial != CertSerial(cert) || pr.Fingerprint != CertFingerprint(cert) {
			t.Fatalf("certificate bindings = %q/%q, want %q/%q", pr.Serial, pr.Fingerprint, CertSerial(cert), CertFingerprint(cert))
		}
	})

	t.Run("admin certificate with a foreign token denied", func(t *testing.T) {
		s, store, cert, _ := adminCertFixture(t, "ops")
		_, clientToken := newClient(t, s, store, "app", nil, domain.AuthMethodToken)
		if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: clientToken, PeerCert: cert}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		assertMetadata(t, requireLastAudit(t, store, "auth.failure", "deny"), `"reason":"credential_mismatch"`)
	})
}

func TestResolvePrincipalAdminRequirementDisabled(t *testing.T) {
	ctx := context.Background()
	s, _, cert, token := adminCertFixture(t, "ops")
	s.SetAdminRequireClientCert(false)

	pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token})
	if err != nil {
		t.Fatalf("relaxed admin token login: %v", err)
	}
	if pr.Method != domain.AuthMethodToken || pr.Identity.Name != "ops" {
		t.Fatalf("principal = %+v, want token-authenticated ops", pr)
	}

	pr, err = s.ResolvePrincipal(ctx, CredentialInput{PeerCert: cert})
	if err != nil {
		t.Fatalf("relaxed admin certificate login: %v", err)
	}
	if pr.Method != domain.AuthMethodMTLS || pr.Token != "" {
		t.Fatalf("principal = %+v, want cert-only mtls ops", pr)
	}
}

func TestResolvePrincipalTrimsTokenAndCopiesRequestContext(t *testing.T) {
	ctx := context.Background()
	s, _, cert, token := adminCertFixture(t, "ops")

	in := CredentialInput{
		Token:      "  \t" + token + "\n",
		PeerCert:   cert,
		RemoteAddr: "192.0.2.7",
		UserAgent:  "console/1.0",
		RequestID:  "req-42",
	}
	pr, err := s.ResolvePrincipal(ctx, in)
	if err != nil {
		t.Fatalf("ResolvePrincipal with padded token: %v", err)
	}
	if pr.Token != token {
		t.Fatalf("token = %q, want the trimmed %q", pr.Token, token)
	}
	if pr.RemoteAddr != "192.0.2.7" || pr.UserAgent != "console/1.0" || pr.RequestID != "req-42" {
		t.Fatalf("request context not copied through: %+v", pr)
	}
}

// --- watch reauthorization --------------------------------------------------

func TestReauthorizeWatchAdminCertAndToken(t *testing.T) {
	ctx := context.Background()

	// resolve returns the admin principal exactly as a transport would build it.
	resolve := func(t *testing.T, s *Service, cert *x509.Certificate, token string) Principal {
		t.Helper()
		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: cert})
		if err != nil {
			t.Fatalf("ResolvePrincipal: %v", err)
		}
		return pr
	}

	t.Run("baseline", func(t *testing.T) {
		s, _, cert, token := adminCertFixture(t, "ops")
		if err := s.ReauthorizeWatch(ctx, resolve(t, s, cert, token)); err != nil {
			t.Fatalf("baseline reauth: %v", err)
		}
	})

	t.Run("token rotation closes the stream", func(t *testing.T) {
		s, _, cert, token := adminCertFixture(t, "ops")
		pr := resolve(t, s, cert, token)
		if _, err := s.RotateIdentityToken(ctx, adminPrincipal(), "ops"); err != nil {
			t.Fatalf("RotateIdentityToken: %v", err)
		}
		if err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("reauth after token rotation err = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("certificate revocation closes the stream", func(t *testing.T) {
		s, _, cert, token := adminCertFixture(t, "ops")
		pr := resolve(t, s, cert, token)
		if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "ops", CertSerial(cert)); err != nil {
			t.Fatalf("RevokeIdentityCertificate: %v", err)
		}
		if err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("reauth after certificate revocation err = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("disabling the identity closes the stream", func(t *testing.T) {
		s, _, cert, token := adminCertFixture(t, "ops")
		pr := resolve(t, s, cert, token)
		if err := s.RevokeIdentity(ctx, adminPrincipal(), "ops"); err != nil {
			t.Fatalf("RevokeIdentity: %v", err)
		}
		// The stream must fail on the credential, not merely on some later
		// authorization step: a disabled identity is no longer authenticated.
		if err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("reauth after disabling the admin identity err = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("relaxed requirement still re-checks the admin token", func(t *testing.T) {
		s, _, _, token := adminCertFixture(t, "ops")
		s.SetAdminRequireClientCert(false)
		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token})
		if err != nil {
			t.Fatalf("relaxed admin login: %v", err)
		}
		if err := s.ReauthorizeWatch(ctx, pr); err != nil {
			t.Fatalf("baseline relaxed reauth: %v", err)
		}
		if _, err := s.RotateIdentityToken(ctx, adminPrincipal(), "ops"); err != nil {
			t.Fatalf("RotateIdentityToken: %v", err)
		}
		if err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("relaxed reauth after rotation err = %v, want ErrUnauthenticated", err)
		}
	})
}

// A non-admin that happened to present both credentials is re-checked only on
// the certificate that admitted it, so rotating its token does not close the
// stream (today's semantics, preserved by the admin-only token re-check).
func TestReauthorizeWatchClientCertIgnoresTokenRotation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	leaf, token := newClient(t, s, store, "svc", nil, domain.AuthMethodMTLS, domain.AuthMethodToken)

	pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: leaf})
	if err != nil {
		t.Fatalf("ResolvePrincipal: %v", err)
	}
	if pr.Method != domain.AuthMethodMTLS {
		t.Fatalf("method = %q, want mtls", pr.Method)
	}
	if err := s.ReauthorizeWatch(ctx, pr); err != nil {
		t.Fatalf("baseline reauth: %v", err)
	}
	if _, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc"); err != nil {
		t.Fatalf("RotateIdentityToken: %v", err)
	}
	if err := s.ReauthorizeWatch(ctx, pr); err != nil {
		t.Fatalf("client mTLS reauth after token rotation = %v, want nil (the certificate admitted it)", err)
	}
}

// --- admin admission: a broken certificate is not half a credential ---------

// TestResolvePrincipalAdminBrokenCertificateWithValidToken covers the states an
// admin certificate degrades into. In each of them the bearer token is still
// perfectly valid, so what stops the login is the client-certificate
// requirement alone — exactly the case a stolen token has to hit.
func TestResolvePrincipalAdminBrokenCertificateWithValidToken(t *testing.T) {
	ctx := context.Background()

	t.Run("revoked certificate", func(t *testing.T) {
		s, store, cert, token := adminCertFixture(t, "ops")
		if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "ops", CertSerial(cert)); err != nil {
			t.Fatalf("RevokeIdentityCertificate: %v", err)
		}

		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: cert, RemoteAddr: "10.0.0.9"})
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		if pr.Identity.Name != "" {
			t.Fatalf("denied principal must be zero-valued, got %+v", pr)
		}
		// The revoked certificate is dropped, leaving a token-only admin; the
		// requirement is what refuses it, and the audit says so.
		ev := requireLastAudit(t, store, "auth.failure", "deny")
		if ev.ActorIdentity != "ops" {
			t.Errorf("actor = %q, want ops", ev.ActorIdentity)
		}
		assertMetadata(t, ev, `"reason":"admin_client_cert_required"`, `"method":"token"`)
	})

	t.Run("expired certificate", func(t *testing.T) {
		s, store, cert, token := adminCertFixture(t, "ops")
		rec, err := store.GetIdentityCertBySerial(ctx, CertSerial(cert))
		if err != nil {
			t.Fatalf("GetIdentityCertBySerial: %v", err)
		}
		s.now = func() time.Time { return rec.Cert.NotAfter.Add(time.Hour) }

		if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: cert}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		assertMetadata(t, requireLastAudit(t, store, "auth.failure", "deny"),
			`"reason":"admin_client_cert_required"`, `"method":"token"`)
	})

	t.Run("disabled admin holding both credentials", func(t *testing.T) {
		s, store, cert, token := adminCertFixture(t, "ops")
		if err := s.RevokeIdentity(ctx, adminPrincipal(), "ops"); err != nil {
			t.Fatalf("RevokeIdentity: %v", err)
		}

		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: cert})
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		if pr.Identity.Name != "" {
			t.Fatalf("denied principal must be zero-valued, got %+v", pr)
		}
		// Neither credential survives verification, so the identity was never
		// proven and the failure must not be attributed to it.
		ev := requireLastAudit(t, store, "auth.failure", "deny")
		if ev.ActorIdentity != "" || ev.ActorType != "unknown" {
			t.Errorf("actor = %q/%q, want unattributed", ev.ActorIdentity, ev.ActorType)
		}
		if strings.Contains(ev.Metadata, "admin_client_cert_required") {
			t.Errorf("a disabled identity was refused as a certificate-requirement failure: %s", ev.Metadata)
		}
	})
}

// TestReauthorizeWatchTokenAdminHonorsRequirementChange covers the token branch
// of ReauthorizeWatch: a stream opened while the requirement was off must not
// outlive it being turned on, because a token-only admin could never open one
// under enforcement.
func TestReauthorizeWatchTokenAdminHonorsRequirementChange(t *testing.T) {
	ctx := context.Background()
	s, _, _, token := adminCertFixture(t, "ops")
	s.SetAdminRequireClientCert(false)

	pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token})
	if err != nil {
		t.Fatalf("relaxed admin login: %v", err)
	}
	if pr.Method != domain.AuthMethodToken {
		t.Fatalf("method = %q, want token", pr.Method)
	}
	if err := s.ReauthorizeWatch(ctx, pr); err != nil {
		t.Fatalf("reauth with the requirement off = %v, want nil", err)
	}

	s.SetAdminRequireClientCert(true)
	if err := s.ReauthorizeWatch(ctx, pr); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("reauth with the requirement on err = %v, want ErrUnauthenticated", err)
	}
}

// TestResolvePrincipalIgnoredCredentialIsAuditedAsAllowed: when one presented
// credential fails to verify but the other admits the caller, the request
// succeeds and the audit log must say so — one auth.credential_ignored row
// with decision allow naming the admitted identity, and no auth.failure row
// that would make a successful request read as a failed login.
func TestResolvePrincipalIgnoredCredentialIsAuditedAsAllowed(t *testing.T) {
	ctx := context.Background()

	t.Run("stale token beside a valid certificate", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		leaf, _ := newClient(t, s, store, "svc", nil, domain.AuthMethodMTLS, domain.AuthMethodToken)

		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: "kms_rotated-away", PeerCert: leaf, RequestID: "req-1"})
		if err != nil {
			t.Fatalf("ResolvePrincipal: %v", err)
		}
		if pr.Method != domain.AuthMethodMTLS || pr.Token != "" {
			t.Fatalf("principal = method %q token %q, want mtls with no token", pr.Method, pr.Token)
		}
		if store.hasAudit("auth.failure", "deny") {
			t.Fatal("an admitted request was audited as auth.failure")
		}
		ev := requireLastAudit(t, store, "auth.credential_ignored", "allow")
		if ev.ActorIdentity != "svc" || ev.ActorType != domain.IdentityKindClient || ev.RequestID != "req-1" {
			t.Fatalf("credential_ignored row = %+v, want actor svc/client on req-1", ev)
		}
		assertMetadata(t, ev, `"ignored":"token"`, `"method":"mtls"`)
	})

	t.Run("revoked certificate beside a valid token", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		leaf, token := newClient(t, s, store, "svc", nil, domain.AuthMethodMTLS, domain.AuthMethodToken)
		if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "svc", CertSerial(leaf)); err != nil {
			t.Fatalf("RevokeIdentityCertificate: %v", err)
		}

		pr, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: leaf})
		if err != nil {
			t.Fatalf("ResolvePrincipal: %v", err)
		}
		if pr.Method != domain.AuthMethodToken {
			t.Fatalf("method = %q, want token", pr.Method)
		}
		if store.hasAudit("auth.failure", "deny") {
			t.Fatal("an admitted request was audited as auth.failure")
		}
		assertMetadata(t, requireLastAudit(t, store, "auth.credential_ignored", "allow"), `"ignored":"mtls"`, `"method":"token"`)
	})

	// Refused requests keep the classic rows: a lone invalid credential is an
	// auth.failure, and an admin whose certificate failed is denied with both
	// the certificate failure and the admission denial on record.
	t.Run("refused requests still audit the failure", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		leaf, token := issueAdminCert(t, s, store, "root")
		if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "root", CertSerial(leaf)); err != nil {
			t.Fatalf("RevokeIdentityCertificate: %v", err)
		}
		if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: token, PeerCert: leaf}); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		if store.hasAudit("auth.credential_ignored", "allow") {
			t.Fatal("a refused request was audited as credential_ignored")
		}
		assertMetadata(t, requireLastAudit(t, store, "auth.failure", "deny"), `"reason":"admin_client_cert_required"`)
	})
}
