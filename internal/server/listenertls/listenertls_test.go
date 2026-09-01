package listenertls

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/storage"
)

// newService returns a ready core.Service with the built-in CA bootstrapped.
func newService(t *testing.T) *core.Service {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := core.New(store, zap.NewNop(), "v-test")
	kek, err := crypto.NewKEKFromMaterial("kek-1", make([]byte, 32))
	if err != nil {
		t.Fatalf("build kek: %v", err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	if err := svc.BootstrapCA(context.Background()); err != nil {
		t.Fatalf("bootstrap CA: %v", err)
	}
	return svc
}

// verifiesAgainst reports whether cert chains to the pool. A self-signed CA
// verifies against a pool that contains it, which is how these tests assert
// pool membership without the deprecated Subjects accessor.
func verifiesAgainst(t *testing.T, cert *x509.Certificate, pool *x509.CertPool) bool {
	t.Helper()
	_, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err == nil
}

// selfSignedCA returns a throwaway CA certificate standing in for an
// operator-supplied client CA bundle.
func selfSignedCA(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

func TestBuildNilBase(t *testing.T) {
	// TLS disabled: no listener config to derive, and no certificate can reach
	// the server anyway.
	cfg, err := Build(nil, newService(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cfg != nil {
		t.Fatalf("Build(nil) = %v, want nil", cfg)
	}
}

func TestBuildAddsBuiltInCA(t *testing.T) {
	svc := newService(t)
	caCert, err := svc.CACertificate()
	if err != nil {
		t.Fatalf("CACertificate: %v", err)
	}
	base := &tls.Config{MinVersion: tls.VersionTLS12}

	cfg, err := Build(base, svc)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cfg.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Errorf("ClientAuth = %v, want VerifyClientCertIfGiven", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("ClientCAs is nil, want a pool holding the built-in CA")
	}
	if !verifiesAgainst(t, caCert, cfg.ClientCAs) {
		t.Error("built-in CA does not chain to the derived ClientCAs pool")
	}
	if cfg.MinVersion != base.MinVersion {
		t.Errorf("MinVersion = %v, want the base's %v", cfg.MinVersion, base.MinVersion)
	}
	// The base belongs to the caller (it is shared by both listeners): deriving
	// one listener's config must not change it.
	if base.ClientCAs != nil || base.ClientAuth != tls.NoClientCert {
		t.Errorf("Build mutated the base config: ClientCAs=%v ClientAuth=%v", base.ClientCAs, base.ClientAuth)
	}
}

func TestBuildKeepsOperatorClientCAs(t *testing.T) {
	// mtls_enabled loads an operator CA bundle into ClientCAs. The built-in CA
	// is added to it, not substituted for it, so certificates from either
	// authority still complete the handshake.
	svc := newService(t)
	caCert, err := svc.CACertificate()
	if err != nil {
		t.Fatalf("CACertificate: %v", err)
	}
	operatorCA := selfSignedCA(t, "operator-ca")
	operatorPool := x509.NewCertPool()
	operatorPool.AddCert(operatorCA)
	base := &tls.Config{MinVersion: tls.VersionTLS12, ClientCAs: operatorPool, ClientAuth: tls.RequireAndVerifyClientCert}

	cfg, err := Build(base, svc)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !verifiesAgainst(t, operatorCA, cfg.ClientCAs) {
		t.Error("operator CA missing from the derived ClientCAs pool")
	}
	if !verifiesAgainst(t, caCert, cfg.ClientCAs) {
		t.Error("built-in CA missing from the derived ClientCAs pool")
	}
	if verifiesAgainst(t, caCert, operatorPool) {
		t.Error("Build mutated the operator's ClientCAs pool")
	}
	if cfg.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Errorf("ClientAuth = %v, want VerifyClientCertIfGiven", cfg.ClientAuth)
	}
}

func TestBuildWithoutCA(t *testing.T) {
	// No CA bootstrapped yet: report it rather than silently serving a listener
	// that cannot verify any client certificate.
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := core.New(store, zap.NewNop(), "v-test")

	if _, err := Build(&tls.Config{MinVersion: tls.VersionTLS12}, svc); err == nil {
		t.Fatal("Build succeeded without a certificate authority, want an error")
	}
}
