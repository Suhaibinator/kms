package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// parsePrivateKeyPEM decodes a one-time issuance key bundle (PKCS#8).
func parsePrivateKeyPEM(t *testing.T, keyPEM string) any {
	t.Helper()
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		t.Fatal("no PRIVATE KEY PEM block")
		return nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	return key
}

// --- CreateIdentity: admins never receive a certificate online --------------

func TestCreateIdentityAdminCredentials(t *testing.T) {
	ctx := context.Background()

	tokenOnly := func(t *testing.T, methods []domain.AuthMethod) {
		t.Helper()
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
			Name: "boss", Kind: domain.IdentityKindAdmin, AuthMethods: methods,
		})
		if err != nil {
			t.Fatalf("CreateIdentity(admin, %v): %v", methods, err)
		}
		if res.Token == "" {
			t.Error("admin identity did not receive a bearer token")
		}
		if res.Cert != nil {
			t.Error("admin identity received a client certificate online; admin certificates are offline-only")
		}
		if !res.Identity.HasToken {
			t.Error("identity view reports HasToken = false")
		}
		if res.Identity.Kind != domain.IdentityKindAdmin {
			t.Errorf("kind = %q, want admin", res.Identity.Kind)
		}
	}

	t.Run("empty auth methods mint a token only", func(t *testing.T) { tokenOnly(t, nil) })
	t.Run("explicit token mints a token only", func(t *testing.T) {
		tokenOnly(t, []domain.AuthMethod{domain.AuthMethodToken})
	})

	rejected := func(t *testing.T, methods []domain.AuthMethod) {
		t.Helper()
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		_, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
			Name: "boss", Kind: domain.IdentityKindAdmin, AuthMethods: methods,
		})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("CreateIdentity(admin, %v) err = %v, want ErrInvalidArgument", methods, err)
		}
		if _, ok := store.identitiesByName["boss"]; ok {
			t.Fatal("rejected admin request still created an identity")
		}
		if len(store.certsBySerial) != 0 {
			t.Fatal("rejected admin request still minted a certificate")
		}
	}

	t.Run("mtls rejected", func(t *testing.T) { rejected(t, []domain.AuthMethod{domain.AuthMethodMTLS}) })
	t.Run("token plus mtls rejected", func(t *testing.T) {
		rejected(t, []domain.AuthMethod{domain.AuthMethodToken, domain.AuthMethodMTLS})
	})
	t.Run("unknown method rejected", func(t *testing.T) {
		rejected(t, []domain.AuthMethod{domain.AuthMethod("carrier-pigeon")})
	})

	// Regression guard: the admin rule must not have changed the client default.
	t.Run("client with empty methods still defaults to mtls-only", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		res, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
			Name: "svc", Kind: domain.IdentityKindClient,
		})
		if err != nil {
			t.Fatalf("CreateIdentity(client): %v", err)
		}
		if res.Cert == nil {
			t.Error("client identity did not receive a certificate")
		}
		if res.Token != "" {
			t.Error("mTLS-only client identity received a bearer token")
		}
	})
}

// --- IssueIdentityCertificate: online path refuses admin targets ------------

func TestIssueIdentityCertificateOnlineChannel(t *testing.T) {
	ctx := context.Background()

	t.Run("admin target refused and audited", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")

		if _, err := s.IssueIdentityCertificate(ctx, adminPrincipal(), "ops", 0); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		ev := requireLastAudit(t, store, "identity.cert.issue", "deny")
		if ev.ResourceKey != "ops" {
			t.Errorf("resource key = %q, want ops", ev.ResourceKey)
		}
		assertMetadata(t, ev, `"reason":"admin_target"`, `"channel":"online"`)
		if len(store.certsBySerial) != 0 {
			t.Fatal("a certificate was recorded for a refused admin target")
		}
	})

	t.Run("client target issues an ed25519 leaf", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		store.addIdentity("svc", domain.IdentityKindClient, "kms_svc")

		bundle, err := s.IssueIdentityCertificate(ctx, adminPrincipal(), "svc", 0)
		if err != nil {
			t.Fatalf("IssueIdentityCertificate: %v", err)
		}
		if _, ok := parsePrivateKeyPEM(t, bundle.KeyPEM).(ed25519.PrivateKey); !ok {
			t.Fatalf("client leaf key is %T, want ed25519.PrivateKey", parsePrivateKeyPEM(t, bundle.KeyPEM))
		}
		leaf := parseCertPEM(t, bundle.CertPEM)
		if leaf.PublicKeyAlgorithm != x509.Ed25519 {
			t.Errorf("public key algorithm = %v, want Ed25519", leaf.PublicKeyAlgorithm)
		}
		ev := requireLastAudit(t, store, "identity.cert.issue", "allow")
		assertMetadata(t, ev, `"serial":"`+bundle.Serial+`"`)
		if strings.Contains(ev.Metadata, `"channel":"local"`) {
			t.Error("online issuance was recorded as a local (offline) issuance")
		}
	})
}

// --- IssueLocalAdminCertificate: the offline path ---------------------------

func TestIssueLocalAdminCertificateGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("non-admin caller denied", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")

		if _, err := s.IssueLocalAdminCertificate(ctx, clientPrincipal("app"), "ops", 0); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		requireLastAudit(t, store, "identity.cert.issue", "deny")
		if len(store.certsBySerial) != 0 {
			t.Fatal("a certificate was minted for a denied caller")
		}
	})

	t.Run("unknown target", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		if _, err := s.IssueLocalAdminCertificate(ctx, cliAdminPrincipal(), "ghost", 0); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("client target rejected", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		store.addIdentity("svc", domain.IdentityKindClient, "kms_svc")
		if _, err := s.IssueLocalAdminCertificate(ctx, cliAdminPrincipal(), "svc", 0); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
		if len(store.certsBySerial) != 0 {
			t.Fatal("a certificate was minted for a client target")
		}
	})

	t.Run("disabled admin rejected", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withCA(t, s)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		if err := store.SetIdentityDisabled(ctx, "ops", true); err != nil {
			t.Fatalf("SetIdentityDisabled: %v", err)
		}
		if _, err := s.IssueLocalAdminCertificate(ctx, cliAdminPrincipal(), "ops", 0); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("err = %v, want ErrFailedPrecondition", err)
		}
		if len(store.certsBySerial) != 0 {
			t.Fatal("a certificate was minted for a disabled admin")
		}
	})
}

func TestIssueLocalAdminCertificateSuccess(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")

	bundle, err := s.IssueLocalAdminCertificate(ctx, cliAdminPrincipal(), "ops", 0)
	if err != nil {
		t.Fatalf("IssueLocalAdminCertificate: %v", err)
	}

	// The leaf key must be ECDSA P-256 so the credential can live in a browser
	// or OS keystore; the CA's signature over it stays Ed25519.
	key, ok := parsePrivateKeyPEM(t, bundle.KeyPEM).(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("admin leaf key is %T, want *ecdsa.PrivateKey", parsePrivateKeyPEM(t, bundle.KeyPEM))
	}
	if key.Curve != elliptic.P256() {
		t.Errorf("curve = %v, want P-256", key.Curve.Params().Name)
	}
	leaf := parseCertPEM(t, bundle.CertPEM)
	if leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("public key algorithm = %v, want ECDSA", leaf.PublicKeyAlgorithm)
	}
	if leaf.SignatureAlgorithm != x509.PureEd25519 {
		t.Errorf("signature algorithm = %v, want PureEd25519", leaf.SignatureAlgorithm)
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != "kms://identity/ops" {
		t.Fatalf("URI SANs = %v, want exactly kms://identity/ops", leaf.URIs)
	}

	// The certificate is enrolled, so the leaf authenticates as the admin.
	rec, err := store.GetIdentityCertBySerial(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("GetIdentityCertBySerial: %v", err)
	}
	if rec.IdentityName != "ops" {
		t.Errorf("cert row identity = %q, want ops", rec.IdentityName)
	}
	if rec.Cert.Fingerprint != bundle.Fingerprint || rec.Cert.Fingerprint != CertFingerprint(leaf) {
		t.Errorf("fingerprint mismatch: row %q, bundle %q, leaf %q", rec.Cert.Fingerprint, bundle.Fingerprint, CertFingerprint(leaf))
	}
	if bundle.Serial != CertSerial(leaf) {
		t.Errorf("serial = %q, want %q", bundle.Serial, CertSerial(leaf))
	}
	id, err := s.VerifyClientCert(ctx, leaf, "ip", "ua")
	if err != nil {
		t.Fatalf("VerifyClientCert on the freshly issued admin leaf: %v", err)
	}
	if id.Name != "ops" || id.Kind != domain.IdentityKindAdmin {
		t.Fatalf("verified identity = %+v, want admin ops", id)
	}

	ev := requireLastAudit(t, store, "identity.cert.issue", "allow")
	if ev.ResourceKey != "ops" {
		t.Errorf("resource key = %q, want ops", ev.ResourceKey)
	}
	assertMetadata(t, ev, `"channel":"local"`, `"serial":"`+bundle.Serial+`"`)
	if strings.Contains(ev.Metadata, bundle.KeyPEM) {
		t.Error("audit metadata leaked private key material")
	}

	// Online revocation of an admin certificate stays available.
	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "ops", bundle.Serial); err != nil {
		t.Fatalf("RevokeIdentityCertificate on an admin certificate: %v", err)
	}
	if _, err := s.VerifyClientCert(ctx, leaf, "ip", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked admin certificate err = %v, want ErrUnauthenticated", err)
	}
}

// --- AdminsWithoutValidCert -------------------------------------------------

func TestAdminsWithoutValidCert(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	if got, err := s.AdminsWithoutValidCert(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty store = %v, %v; want no names and no error", got, err)
	}

	// No certificate at all.
	store.addIdentity("bare", domain.IdentityKindAdmin, "kms_bare")

	// Only a revoked certificate.
	revokedCert, _ := issueAdminCert(t, s, store, "revoked")
	if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "revoked", CertSerial(revokedCert)); err != nil {
		t.Fatalf("RevokeIdentityCertificate: %v", err)
	}

	// Only an expired certificate.
	store.addIdentity("expired", domain.IdentityKindAdmin, "kms_expired")
	if err := store.InsertIdentityCert(ctx, "expired", domain.IdentityCert{
		Serial:      "e0e0",
		Fingerprint: strings.Repeat("a", 64),
		NotAfter:    time.Now().Add(-time.Hour),
		CreatedAt:   time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("InsertIdentityCert: %v", err)
	}

	// A valid certificate: covered.
	issueAdminCert(t, s, store, "covered")

	// A disabled admin cannot authenticate at all, so it is not reported.
	store.addIdentity("retired", domain.IdentityKindAdmin, "kms_retired")
	if err := store.SetIdentityDisabled(ctx, "retired", true); err != nil {
		t.Fatalf("SetIdentityDisabled: %v", err)
	}

	// Client identities are out of scope for the admin requirement.
	store.addIdentity("app", domain.IdentityKindClient, "kms_app")

	got, err := s.AdminsWithoutValidCert(ctx)
	if err != nil {
		t.Fatalf("AdminsWithoutValidCert: %v", err)
	}
	want := []string{"bare", "expired", "revoked"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("AdminsWithoutValidCert = %v, want %v", got, want)
	}
}

// A certificate that expires while the process runs is reported on the next
// scan: validity is evaluated against the service clock, not issuance time.
func TestAdminsWithoutValidCertHonorsServiceClock(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	cert, _ := issueAdminCert(t, s, store, "ops")

	if got, err := s.AdminsWithoutValidCert(ctx); err != nil || len(got) != 0 {
		t.Fatalf("with a valid certificate = %v, %v; want none", got, err)
	}

	rec, err := store.GetIdentityCertBySerial(ctx, CertSerial(cert))
	if err != nil {
		t.Fatalf("GetIdentityCertBySerial: %v", err)
	}
	s.now = func() time.Time { return rec.Cert.NotAfter.Add(time.Hour) }
	got, err := s.AdminsWithoutValidCert(ctx)
	if err != nil {
		t.Fatalf("AdminsWithoutValidCert: %v", err)
	}
	if !slices.Equal(got, []string{"ops"}) {
		t.Fatalf("after expiry = %v, want [ops]", got)
	}
}

// --- AdminCertReport --------------------------------------------------------

// reportFixture builds a service whose clock is pinned to `now`, so certificate
// expiry is exercised against an exact instant rather than wall-clock timing.
func reportFixture(t *testing.T, now time.Time) (*Service, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	s.now = func() time.Time { return now }
	return s, store
}

// addAdminCert enrolls a certificate row for an existing identity. It bypasses
// the CA so a test can name the exact serial and expiry it wants to assert on.
func addAdminCert(t *testing.T, store *fakeStore, name, serial string, notAfter time.Time) domain.IdentityCert {
	t.Helper()
	cert := domain.IdentityCert{
		Serial:      serial,
		Fingerprint: fmt.Sprintf("%064s", serial),
		NotAfter:    notAfter,
		CreatedAt:   time.Now(),
	}
	if err := store.InsertIdentityCert(context.Background(), name, cert); err != nil {
		t.Fatalf("InsertIdentityCert(%s, %s): %v", name, serial, err)
	}
	return cert
}

// expiringNames returns the reported identity names, so a test can assert the
// set without depending on store iteration order.
func expiringNames(expiring []ExpiringAdminCert) []string {
	out := make([]string, 0, len(expiring))
	for _, e := range expiring {
		out = append(out, e.Name)
	}
	slices.Sort(out)
	return out
}

const reportWindow = 14 * 24 * time.Hour

// TestAdminCertReportExpiring covers the look-ahead serve uses at startup: an
// expired certificate is rejected by the TLS handshake itself, so the operator
// has to hear about it before that happens.
func TestAdminCertReportExpiring(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	t.Run("within the window is flagged with its serial and expiry", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		notAfter := now.Add(3 * 24 * time.Hour)
		addAdminCert(t, store, "ops", "abc1", notAfter)

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil {
			t.Fatalf("AdminCertReport: %v", err)
		}
		if len(lacking) != 0 {
			t.Fatalf("lacking = %v, want none (the certificate is still valid)", lacking)
		}
		if len(expiring) != 1 {
			t.Fatalf("expiring = %+v, want exactly one", expiring)
		}
		if expiring[0].Name != "ops" || expiring[0].Serial != "abc1" || !expiring[0].NotAfter.Equal(notAfter) {
			t.Fatalf("expiring[0] = %+v, want ops/abc1/%v", expiring[0], notAfter)
		}
	})

	t.Run("beyond the window is not flagged", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		addAdminCert(t, store, "ops", "abc1", now.Add(30*24*time.Hour))

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil || len(lacking) != 0 || len(expiring) != 0 {
			t.Fatalf("AdminCertReport = %v, %+v, %v; want nothing reported", lacking, expiring, err)
		}
	})

	// Mid-rollover an admin holds the certificate it is retiring and the one it
	// has just imported. Only the later expiry matters; warning on the older one
	// would train operators to ignore the warning.
	t.Run("the newest valid certificate decides", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		addAdminCert(t, store, "ops", "old1", now.Add(2*24*time.Hour))
		addAdminCert(t, store, "ops", "new1", now.Add(60*24*time.Hour))

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil || len(lacking) != 0 || len(expiring) != 0 {
			t.Fatalf("AdminCertReport = %v, %+v, %v; want nothing reported (the new certificate covers the admin)", lacking, expiring, err)
		}
	})

	t.Run("a never-expiring certificate is neither lacking nor expiring", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		addAdminCert(t, store, "ops", "forever", time.Time{})

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil || len(lacking) != 0 || len(expiring) != 0 {
			t.Fatalf("AdminCertReport = %v, %+v, %v; want nothing reported", lacking, expiring, err)
		}
	})

	// A never-expiring certificate must also win against an expiring one, so an
	// admin holding both is not warned about the one it no longer needs.
	t.Run("a never-expiring certificate outranks an expiring one", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		addAdminCert(t, store, "ops", "soon", now.Add(time.Hour))
		addAdminCert(t, store, "ops", "forever", time.Time{})

		_, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil || len(expiring) != 0 {
			t.Fatalf("expiring = %+v, %v; want none", expiring, err)
		}
	})

	// within == 0 is the AdminsWithoutValidCert mode: report who cannot
	// authenticate at all, never who will stop being able to.
	t.Run("within zero disables the expiry check", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		addAdminCert(t, store, "ops", "abc1", now.Add(time.Minute))

		lacking, expiring, err := s.AdminCertReport(ctx, 0)
		if err != nil || len(lacking) != 0 || len(expiring) != 0 {
			t.Fatalf("AdminCertReport(0) = %v, %+v, %v; want nothing reported", lacking, expiring, err)
		}
	})

	t.Run("an already expired certificate is lacking, not expiring", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("ops", domain.IdentityKindAdmin, "kms_ops")
		addAdminCert(t, store, "ops", "gone", now.Add(-time.Hour))

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil {
			t.Fatalf("AdminCertReport: %v", err)
		}
		if !slices.Equal(lacking, []string{"ops"}) || len(expiring) != 0 {
			t.Fatalf("AdminCertReport = %v, %+v; want lacking [ops] and no expiring entry", lacking, expiring)
		}
	})

	t.Run("a revoked certificate leaves the admin lacking", func(t *testing.T) {
		s, store := reportFixture(t, now)
		cert, _ := issueAdminCert(t, s, store, "ops")
		if err := s.RevokeIdentityCertificate(ctx, adminPrincipal(), "ops", CertSerial(cert)); err != nil {
			t.Fatalf("RevokeIdentityCertificate: %v", err)
		}

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil {
			t.Fatalf("AdminCertReport: %v", err)
		}
		if !slices.Equal(lacking, []string{"ops"}) || len(expiring) != 0 {
			t.Fatalf("AdminCertReport = %v, %+v; want lacking [ops]", lacking, expiring)
		}
	})

	// A disabled admin cannot authenticate by any credential, and a client
	// identity is outside the admin requirement entirely: neither is the
	// operator's problem at startup.
	t.Run("disabled admins and client identities are ignored", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("retired", domain.IdentityKindAdmin, "kms_retired")
		addAdminCert(t, store, "retired", "ret1", now.Add(time.Hour))
		if err := store.SetIdentityDisabled(ctx, "retired", true); err != nil {
			t.Fatalf("SetIdentityDisabled: %v", err)
		}
		store.addIdentity("app", domain.IdentityKindClient, "kms_app")
		addAdminCert(t, store, "app", "app1", now.Add(time.Hour))
		store.addIdentity("bare-client", domain.IdentityKindClient, "kms_bare_client")

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil || len(lacking) != 0 || len(expiring) != 0 {
			t.Fatalf("AdminCertReport = %v, %+v, %v; want nothing reported", lacking, expiring, err)
		}
	})

	t.Run("lacking and expiring are reported together", func(t *testing.T) {
		s, store := reportFixture(t, now)
		store.addIdentity("bare", domain.IdentityKindAdmin, "kms_bare")
		store.addIdentity("soon", domain.IdentityKindAdmin, "kms_soon")
		addAdminCert(t, store, "soon", "soon1", now.Add(24*time.Hour))
		store.addIdentity("covered", domain.IdentityKindAdmin, "kms_covered")
		addAdminCert(t, store, "covered", "cov1", now.Add(90*24*time.Hour))

		lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
		if err != nil {
			t.Fatalf("AdminCertReport: %v", err)
		}
		slices.Sort(lacking)
		if !slices.Equal(lacking, []string{"bare"}) {
			t.Fatalf("lacking = %v, want [bare]", lacking)
		}
		if !slices.Equal(expiringNames(expiring), []string{"soon"}) {
			t.Fatalf("expiring = %+v, want only soon", expiring)
		}
	})
}

// TestAdminsWithoutValidCertWrapsReport pins that the narrower helper is the
// report with the look-ahead switched off, so the two cannot drift.
func TestAdminsWithoutValidCertWrapsReport(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s, store := reportFixture(t, now)
	store.addIdentity("bare", domain.IdentityKindAdmin, "kms_bare")
	store.addIdentity("soon", domain.IdentityKindAdmin, "kms_soon")
	addAdminCert(t, store, "soon", "soon1", now.Add(time.Hour))

	got, err := s.AdminsWithoutValidCert(ctx)
	if err != nil {
		t.Fatalf("AdminsWithoutValidCert: %v", err)
	}
	if !slices.Equal(got, []string{"bare"}) {
		t.Fatalf("AdminsWithoutValidCert = %v, want [bare] (an expiring certificate is still a valid one)", got)
	}
}

// pagedIdentityStore serves ListIdentities one page at a time. The unpaged
// fakeStore would let a report that ignored the continuation token pass, and
// the real SQLite store only pages past 1000 identities — far too slow to seed
// in a unit test.
type pagedIdentityStore struct {
	*fakeStore
	pageSize int
	pages    int
}

func (p *pagedIdentityStore) ListIdentities(ctx context.Context, page storage.ListPage) ([]domain.Identity, string, error) {
	all, _, err := p.fakeStore.ListIdentities(ctx, storage.ListPage{})
	if err != nil {
		return nil, "", err
	}
	start := 0
	if page.Token != "" {
		for i, id := range all {
			if id.Name == page.Token {
				start = i + 1
				break
			}
		}
	}
	end := min(start+p.pageSize, len(all))
	p.pages++
	if end < len(all) {
		return all[start:end], all[end-1].Name, nil
	}
	return all[start:end], "", nil
}

// TestAdminCertReportPagesThroughIdentities proves the scan follows the
// continuation token: an admin on the last page is reported exactly like one on
// the first.
func TestAdminCertReportPagesThroughIdentities(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	backing := newFakeStore()
	store := &pagedIdentityStore{fakeStore: backing, pageSize: 2}
	s := New(store, zap.NewNop(), "test")
	withCA(t, s)
	s.now = func() time.Time { return now }

	// Sorted by name: a1 (lacking), a2 (covered), a3 (expiring), a4 (lacking).
	// With pageSize 2 each lands on a different page.
	backing.addIdentity("a1", domain.IdentityKindAdmin, "kms_a1")
	backing.addIdentity("a2", domain.IdentityKindAdmin, "kms_a2")
	addAdminCert(t, backing, "a2", "a2cert", now.Add(90*24*time.Hour))
	backing.addIdentity("a3", domain.IdentityKindAdmin, "kms_a3")
	addAdminCert(t, backing, "a3", "a3cert", now.Add(24*time.Hour))
	backing.addIdentity("a4", domain.IdentityKindAdmin, "kms_a4")

	lacking, expiring, err := s.AdminCertReport(ctx, reportWindow)
	if err != nil {
		t.Fatalf("AdminCertReport: %v", err)
	}
	slices.Sort(lacking)
	if !slices.Equal(lacking, []string{"a1", "a4"}) {
		t.Fatalf("lacking = %v, want [a1 a4]", lacking)
	}
	if !slices.Equal(expiringNames(expiring), []string{"a3"}) {
		t.Fatalf("expiring = %+v, want only a3", expiring)
	}
	if store.pages < 2 {
		t.Fatalf("ListIdentities was called %d time(s); the fixture must serve more than one page", store.pages)
	}
}
