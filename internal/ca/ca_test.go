package ca

import (
	"crypto/x509"
	"testing"
	"time"
)

func mustGenerate(t *testing.T) *CA {
	t.Helper()
	c, certPEM, keyPEM, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("Generate returned empty PEM material")
	}
	return c
}

func TestGenerateProducesCACert(t *testing.T) {
	c := mustGenerate(t)
	cert := c.Certificate()
	if !cert.IsCA {
		t.Error("CA certificate IsCA = false")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA certificate missing KeyUsageCertSign")
	}
	if cert.Subject.CommonName != caCommonName {
		t.Errorf("CA CN = %q, want %q", cert.Subject.CommonName, caCommonName)
	}
	// Long-lived: at least ~9 years out.
	if got := time.Until(cert.NotAfter); got < 9*365*24*time.Hour {
		t.Errorf("CA validity %v too short", got)
	}
}

func TestGenerateLoadRoundTrip(t *testing.T) {
	c1, certPEM, keyPEM, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	c2, err := Load(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// A cert issued after reload must still verify against the original CA
	// pool, proving the reloaded signer is the same key.
	issued, err := c2.IssueClientCert("gradethis-be", time.Hour)
	if err != nil {
		t.Fatalf("IssueClientCert after Load: %v", err)
	}
	verifyChain(t, c1, issued)
}

func TestLoadRejectsMismatchedKey(t *testing.T) {
	_, certPEM, _, err := Generate()
	if err != nil {
		t.Fatalf("Generate a: %v", err)
	}
	_, _, otherKeyPEM, err := Generate()
	if err != nil {
		t.Fatalf("Generate b: %v", err)
	}
	if _, err := Load(certPEM, otherKeyPEM); err == nil {
		t.Fatal("Load accepted a key that does not match the certificate")
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	_, certPEM, keyPEM, _ := Generate()
	if _, err := Load([]byte("not pem"), keyPEM); err == nil {
		t.Error("Load accepted non-PEM certificate")
	}
	if _, err := Load(certPEM, []byte("not pem")); err == nil {
		t.Error("Load accepted non-PEM key")
	}
}

func TestIssueClientCertContents(t *testing.T) {
	c := mustGenerate(t)
	before := time.Now()
	issued, err := c.IssueClientCert("gradethis-be", 2*time.Hour)
	if err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}

	if issued.Serial == "" {
		t.Error("empty serial")
	}
	if len(issued.FingerprintSHA256) != 64 {
		t.Errorf("fingerprint hex len = %d, want 64", len(issued.FingerprintSHA256))
	}

	leaf := parseLeaf(t, issued.CertPEM)
	name, err := IdentityFromCert(leaf)
	if err != nil {
		t.Fatalf("IdentityFromCert: %v", err)
	}
	if name != "gradethis-be" {
		t.Errorf("identity = %q, want gradethis-be", name)
	}
	if leaf.Subject.CommonName != "gradethis-be" {
		t.Errorf("leaf CN = %q, want gradethis-be", leaf.Subject.CommonName)
	}
	if !hasClientAuth(leaf) {
		t.Error("leaf missing ExtKeyUsageClientAuth")
	}
	if leaf.IsCA {
		t.Error("leaf must not be a CA")
	}

	// NotAfter ~ now + ttl.
	wantMin := before.Add(2 * time.Hour).Add(-time.Minute)
	wantMax := time.Now().Add(2 * time.Hour).Add(time.Minute)
	if issued.NotAfter.Before(wantMin) || issued.NotAfter.After(wantMax) {
		t.Errorf("NotAfter %v out of expected window [%v, %v]", issued.NotAfter, wantMin, wantMax)
	}
}

func TestIssueClientCertDefaultTTL(t *testing.T) {
	c := mustGenerate(t)
	issued, err := c.IssueClientCert("svc", 0)
	if err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}
	got := time.Until(issued.NotAfter)
	if got < DefaultCertTTL-time.Hour || got > DefaultCertTTL+time.Hour {
		t.Errorf("default TTL NotAfter in %v, want ~%v", got, DefaultCertTTL)
	}
}

func TestIssueClientCertRejectsBadName(t *testing.T) {
	c := mustGenerate(t)
	if _, err := c.IssueClientCert("", time.Hour); err == nil {
		t.Error("accepted empty identity name")
	}
	if _, err := c.IssueClientCert("has space", time.Hour); err == nil {
		t.Error("accepted identity name with whitespace")
	}
}

func TestIssuedCertVerifiesAgainstCAPool(t *testing.T) {
	c := mustGenerate(t)
	issued, err := c.IssueClientCert("app", time.Hour)
	if err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}
	verifyChain(t, c, issued)
}

func TestCertSignedByDifferentCARejected(t *testing.T) {
	signer := mustGenerate(t)
	other := mustGenerate(t)
	issued, err := signer.IssueClientCert("app", time.Hour)
	if err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}
	leaf := parseLeaf(t, issued.CertPEM)
	opts := x509.VerifyOptions{
		Roots:     other.CertPool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err == nil {
		t.Fatal("leaf verified against an unrelated CA pool")
	}
}

func TestSerialsAreUnique(t *testing.T) {
	c := mustGenerate(t)
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		issued, err := c.IssueClientCert("app", time.Hour)
		if err != nil {
			t.Fatalf("IssueClientCert %d: %v", i, err)
		}
		if seen[issued.Serial] {
			t.Fatalf("duplicate serial %s", issued.Serial)
		}
		seen[issued.Serial] = true
	}
}

func TestIdentityFromCertRequiresSAN(t *testing.T) {
	// A certificate with no URI SAN at all is rejected.
	c := mustGenerate(t)
	if _, err := IdentityFromCert(c.Certificate()); err == nil {
		t.Error("IdentityFromCert accepted a cert with no identity URI SAN")
	}
	if _, err := IdentityFromCert(nil); err == nil {
		t.Error("IdentityFromCert accepted nil")
	}
}

// --- helpers ---

func verifyChain(t *testing.T, c *CA, issued IssuedCert) {
	t.Helper()
	leaf := parseLeaf(t, issued.CertPEM)
	opts := x509.VerifyOptions{
		Roots:     c.CertPool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		t.Fatalf("leaf failed to verify against CA pool: %v", err)
	}
}

func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return cert
}

func hasClientAuth(cert *x509.Certificate) bool {
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}
