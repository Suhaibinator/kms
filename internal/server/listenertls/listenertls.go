// Package listenertls derives the TLS configuration both network listeners
// (gRPC and HTTP) use from the operator-supplied server TLS config.
//
// It exists so the two listeners cannot drift: an admin authenticates with a
// client certificate plus a bearer token on either transport, so both must be
// willing to receive a certificate from the built-in CA.
package listenertls

import (
	"crypto/tls"
	"crypto/x509"

	"github.com/Suhaibinator/kms/internal/core"
)

// Build returns the listener TLS config derived from base: the built-in CA is
// added to the client-CA pool and ClientAuth becomes VerifyClientCertIfGiven.
//
// Presenting a certificate stays optional at the handshake — a browser or a
// token-only machine client with no certificate must still be able to connect
// and reach the login endpoint — but a certificate that *is* presented has been
// chain-verified by the TLS stack before any handler sees it. Admission is then
// core's decision (core.Service.ResolvePrincipal), not the handshake's.
//
// base is never mutated: any operator-supplied ClientCAs pool is cloned before
// the CA is added. A nil base (TLS disabled) yields (nil, nil): a plaintext
// development transport carries no client certificates at all.
func Build(base *tls.Config, svc *core.Service) (*tls.Config, error) {
	if base == nil {
		return nil, nil
	}
	caCert, err := svc.CACertificate()
	if err != nil {
		return nil, err
	}
	cfg := base.Clone()
	var pool *x509.CertPool
	if cfg.ClientCAs != nil {
		pool = cfg.ClientCAs.Clone()
	} else {
		pool = x509.NewCertPool()
	}
	pool.AddCert(caCert)
	cfg.ClientCAs = pool
	cfg.ClientAuth = tls.VerifyClientCertIfGiven
	return cfg, nil
}
