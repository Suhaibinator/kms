package kmsclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLSFromFiles builds a *tls.Config that verifies the server against the CA
// bundle in caFile. Use it for one-way TLS where the server does not require a
// client certificate.
//
// It panics if the files cannot be read or parsed, so it is convenient to use
// inline in a Config literal at startup; use TLSConfig for an error-returning
// variant.
func TLSFromFiles(caFile string) *tls.Config {
	cfg, err := tlsConfig("", "", caFile)
	if err != nil {
		panic(err)
	}
	return cfg
}

// MTLSFromFiles builds a *tls.Config for mutual TLS: it presents the given
// client certificate/key and verifies the server against caFile.
//
// It panics on error so it can be used inline in a Config literal; use
// MTLSConfig for an error-returning variant.
func MTLSFromFiles(certFile, keyFile, caFile string) *tls.Config {
	cfg, err := tlsConfig(certFile, keyFile, caFile)
	if err != nil {
		panic(err)
	}
	return cfg
}

// TLSConfig is the error-returning form of TLSFromFiles.
func TLSConfig(caFile string) (*tls.Config, error) { return tlsConfig("", "", caFile) }

// MTLSConfig is the error-returning form of MTLSFromFiles.
func MTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	return tlsConfig(certFile, keyFile, caFile)
}

func tlsConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("kmsclient: read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("kmsclient: no certificates found in %s", caFile)
		}
		cfg.RootCAs = pool
	}

	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("kmsclient: load client key pair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}
