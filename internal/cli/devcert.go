package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"time"
)

// devTLSMaterial names the files `dev` generates so the server can speak TLS
// and a client on the same machine can verify it. The built-in CA that core
// bootstraps issues *client* certificates; nothing in the product issues the
// server's own keypair, which an operator normally supplies through
// security.server_cert_file. dev plays that operator: a throwaway CA, a leaf
// for the loopback names, and the CA certificate written out so --ca has
// something to trust.
type devTLSMaterial struct {
	CACertPath     string
	ServerCertPath string
	ServerKeyPath  string
}

// devTLSValidity is how long the generated dev certificates last. A dev store
// that outlives a month has stopped being a demo, and a short life keeps a
// stray copy of the private key from being useful for long.
const devTLSValidity = 30 * 24 * time.Hour

// generateDevTLS writes ca.crt, ca.key, server.crt and server.key into dir.
// Keys are ECDSA P-256 rather than the built-in CA's Ed25519: this leaf is
// presented to a browser opening the console, and mainstream browsers still
// refuse Ed25519 server certificates. extraHosts carries the bind addresses so
// a --allow-remote run is reachable under the address it was given.
func generateDevTLS(dir string, extraHosts []string) (devTLSMaterial, error) {
	var out devTLSMaterial
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return out, fmt.Errorf("generating dev CA key: %w", err)
	}
	now := time.Now().UTC()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "parameter-store dev CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(devTLSValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return out, fmt.Errorf("self-signing dev CA certificate: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return out, fmt.Errorf("parsing dev CA certificate: %w", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return out, fmt.Errorf("generating dev server key: %w", err)
	}
	dnsNames, ips := devServerNames(extraHosts)
	leafTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(devTLSValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return out, fmt.Errorf("signing dev server certificate: %w", err)
	}

	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return out, fmt.Errorf("marshalling dev CA key: %w", err)
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return out, fmt.Errorf("marshalling dev server key: %w", err)
	}
	files := []struct {
		path  string
		block pem.Block
	}{
		{devPath(dir, devCACertFile), pem.Block{Type: "CERTIFICATE", Bytes: caDER}},
		{devPath(dir, devCAKeyFile), pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER}},
		{devPath(dir, devServerCertFile), pem.Block{Type: "CERTIFICATE", Bytes: leafDER}},
		{devPath(dir, devServerKeyFile), pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}},
	}
	for _, f := range files {
		// The dev directory is fresh or reset by the time this runs, so
		// PublishNoReplace's refusal to overwrite is a guard rather than a
		// case to handle: an existing file here means something else is
		// writing into the store.
		block := f.block
		if err := writePrivateFile(f.path, false, func(w io.Writer) error {
			_, err := w.Write(pem.EncodeToMemory(&block))
			return err
		}); err != nil {
			return out, fmt.Errorf("writing %s: %w", f.path, err)
		}
	}
	out.CACertPath = files[0].path
	out.ServerCertPath = files[2].path
	out.ServerKeyPath = files[3].path
	return out, nil
}

// devServerNames is the SAN set the dev leaf carries: the loopback names a
// browser and the CLI use, plus any host the operator bound to explicitly so
// --allow-remote is usable under the address it was given.
func devServerNames(extraHosts []string) ([]string, []net.IP) {
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	seenDNS := map[string]bool{"localhost": true}
	seenIP := map[string]bool{"127.0.0.1": true, "::1": true}
	if host, err := os.Hostname(); err == nil && host != "" && !seenDNS[host] {
		dnsNames = append(dnsNames, host)
		seenDNS[host] = true
	}
	for _, h := range extraHosts {
		if h == "" || h == "0.0.0.0" || h == "::" {
			continue // a wildcard bind names no address to certify
		}
		if ip := net.ParseIP(h); ip != nil {
			if !seenIP[ip.String()] {
				ips = append(ips, ip)
				seenIP[ip.String()] = true
			}
			continue
		}
		if !seenDNS[h] {
			dnsNames = append(dnsNames, h)
			seenDNS[h] = true
		}
	}
	return dnsNames, ips
}
