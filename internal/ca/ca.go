// Package ca implements the KMS's built-in certificate authority. It mints the
// short-lived client certificates that machine clients present for mTLS
// authentication (plan-namespaces.md §7).
//
// The package is deliberately self-contained: it depends only on the standard
// library and knows nothing about the KMS's domain types, storage, or wire
// protocol. Persistence, KEK-wrapping of the CA private key, bootstrap at
// unseal, and mapping a verified peer certificate to a stored identity are all
// the caller's responsibility. This package only holds the X.509 material and
// signs.
//
// Keys are Ed25519. The CA certificate is long-lived and self-signed; issued
// client certificates are short-lived leaves carrying the identity name in
// both the CommonName and a URI SAN of the form "kms://identity/<name>". The
// URI SAN is authoritative for identity mapping; the CommonName is cosmetic.
package ca

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

const (
	// caCommonName is the Subject/Issuer CN of the built-in CA certificate.
	caCommonName = "kms-builtin-ca"
	// caValidity is how long the self-signed CA certificate is valid. It is
	// long-lived because there is no automatic renewal mechanism.
	caValidity = 10 * 365 * 24 * time.Hour
	// DefaultCertTTL is used by IssueClientCert when a non-positive ttl is
	// passed. It matches the plan's 90-day default.
	DefaultCertTTL = 90 * 24 * time.Hour
	// clockSkew backdates leaf NotBefore to tolerate small clock differences
	// between the KMS and verifying peers.
	clockSkew = 5 * time.Minute

	// identityURIPrefix is the scheme+authority prefix of the URI SAN that
	// names an identity. The identity name is the remainder.
	identityURIPrefix = "kms://identity/"

	pemTypeCertificate = "CERTIFICATE"
	pemTypePrivateKey  = "PRIVATE KEY" // PKCS#8
)

// Serial numbers are 128 random bits.
var serialLimit = new(big.Int).Lsh(big.NewInt(1), 128)

// ErrNoIdentitySAN is returned by IdentityFromCert when the certificate does
// not carry exactly one "kms://identity/<name>" URI SAN.
var ErrNoIdentitySAN = errors.New("certificate has no unique kms identity URI SAN")

// CA holds the built-in certificate authority: its certificate and the private
// key used to sign client certificates.
type CA struct {
	cert    *x509.Certificate
	signer  ed25519.PrivateKey
	certPEM []byte
}

// IssuedCert is the result of minting one client certificate. The private key
// is generated per issuance and returned exactly once; it is never retained by
// the CA.
type IssuedCert struct {
	CertPEM           []byte    // PEM-encoded leaf certificate
	KeyPEM            []byte    // PEM-encoded PKCS#8 Ed25519 private key
	Serial            string    // lowercase hex of the certificate serial number
	FingerprintSHA256 string    // lowercase hex of SHA-256 over the leaf DER
	NotAfter          time.Time // expiry (UTC, second precision)
}

// Generate creates a fresh built-in CA: a new Ed25519 key pair and a
// self-signed, long-lived CA certificate. It returns the ready CA plus the
// PEM-encoded certificate and PKCS#8 private key so the caller can persist
// them (the private key must be stored KEK-wrapped, never in plaintext). The
// returned keyPEM is the only copy the caller will get; the CA retains the key
// in memory for signing.
func Generate() (ca *CA, certPEM, keyPEM []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate CA serial: %w", err)
	}

	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: caCommonName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// Sign leaf certificates only, no intermediates.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("self-sign CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: der})
	keyPEM, err = marshalKeyPEM(priv)
	if err != nil {
		return nil, nil, nil, err
	}

	return &CA{cert: cert, signer: priv, certPEM: certPEM}, certPEM, keyPEM, nil
}

// Load reconstructs a CA from its persisted PEM material. The caller is
// responsible for having decrypted keyPEM (storage keeps it KEK-wrapped).
func Load(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, err
	}

	signer, err := parseKeyPEM(keyPEM)
	if err != nil {
		return nil, err
	}

	// Sanity check: the private key must correspond to the certificate.
	certPub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("ca: certificate public key is not Ed25519")
	}
	if !certPub.Equal(signer.Public()) {
		return nil, errors.New("ca: private key does not match certificate")
	}

	return &CA{cert: cert, signer: signer, certPEM: certPEM}, nil
}

// IssueClientCert mints a new client certificate for identityName, valid for
// ttl (DefaultCertTTL when ttl <= 0). The certificate carries identityName in
// its CommonName and a "kms://identity/<name>" URI SAN, and is marked for
// ExtKeyUsageClientAuth. A fresh Ed25519 key pair is generated per call; its
// private key is returned in the result and not retained.
func (c *CA) IssueClientCert(identityName string, ttl time.Duration) (IssuedCert, error) {
	if identityName == "" {
		return IssuedCert{}, errors.New("ca: identity name must not be empty")
	}
	if strings.ContainsAny(identityName, " \t\r\n") {
		return IssuedCert{}, fmt.Errorf("ca: identity name %q contains whitespace", identityName)
	}
	if ttl <= 0 {
		ttl = DefaultCertTTL
	}

	uri, err := url.Parse(identityURIPrefix + identityName)
	if err != nil {
		return IssuedCert{}, fmt.Errorf("ca: build identity URI: %w", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return IssuedCert{}, fmt.Errorf("ca: generate leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return IssuedCert{}, fmt.Errorf("ca: generate leaf serial: %w", err)
	}

	now := time.Now().UTC()
	notAfter := now.Add(ttl)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: identityName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  []*url.URL{uri},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, pub, c.signer)
	if err != nil {
		return IssuedCert{}, fmt.Errorf("ca: sign leaf certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: der})
	keyPEM, err := marshalKeyPEM(priv)
	if err != nil {
		return IssuedCert{}, err
	}
	sum := sha256.Sum256(der)

	return IssuedCert{
		CertPEM:           certPEM,
		KeyPEM:            keyPEM,
		Serial:            serialHex(serial),
		FingerprintSHA256: hex.EncodeToString(sum[:]),
		NotAfter:          notAfter,
	}, nil
}

// IdentityFromCert extracts the identity name from a verified peer certificate.
// It requires exactly one URI SAN of the form "kms://identity/<name>" and
// returns its <name>. The CommonName is intentionally NOT used as a fallback:
// the URI SAN is the single authoritative identity claim, so a certificate
// lacking it (or carrying more than one) is rejected with ErrNoIdentitySAN.
func IdentityFromCert(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", ErrNoIdentitySAN
	}
	var name string
	found := 0
	for _, u := range cert.URIs {
		s := u.String()
		if !strings.HasPrefix(s, identityURIPrefix) {
			continue
		}
		candidate := strings.TrimPrefix(s, identityURIPrefix)
		if candidate == "" {
			continue
		}
		found++
		name = candidate
	}
	if found != 1 {
		return "", ErrNoIdentitySAN
	}
	return name, nil
}

// CertPEM returns the PEM-encoded CA certificate (public; safe to serve).
func (c *CA) CertPEM() []byte {
	out := make([]byte, len(c.certPEM))
	copy(out, c.certPEM)
	return out
}

// Certificate returns the parsed CA certificate.
func (c *CA) Certificate() *x509.Certificate { return c.cert }

// CertPool returns a certificate pool containing only this CA, suitable for
// tls.Config.ClientCAs. Callers that also honor an operator-supplied client CA
// should AddCert this CA's Certificate() to their own pool instead.
func (c *CA) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}

func marshalKeyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("ca: marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemTypePrivateKey, Bytes: der}), nil
}

func parseCertPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != pemTypeCertificate {
		return nil, errors.New("ca: no CERTIFICATE PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse certificate: %w", err)
	}
	return cert, nil
}

func parseKeyPEM(keyPEM []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("ca: no PRIVATE KEY PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse private key: %w", err)
	}
	signer, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ca: private key is %T, want ed25519.PrivateKey", key)
	}
	return signer, nil
}

// serialHex renders a serial number as lowercase hex with no leading "0x".
func serialHex(serial *big.Int) string {
	return strings.ToLower(serial.Text(16))
}
