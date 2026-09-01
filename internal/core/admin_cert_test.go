package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
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
