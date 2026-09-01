package cli

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json/v2"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// These tests run the real `serve` wiring in-process — config resolution, the
// unseal, the built-in CA, the effective admin client-certificate decision, the
// HTTP listener and its TLS config — and drive it over a real socket. The unit
// tests around logAdminCertPosture pin the wording; this pins that the setting
// actually reaches the listener, which is the part an operator experiences.
//
// GRPCFactory is nil in this test binary, so serve runs HTTP-only.

// safeBuffer collects the server's log output while the test reads it from
// another goroutine.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serveTestServerCert writes a self-signed loopback server certificate and its
// key, and returns their paths plus a pool trusting it — the operator-supplied
// TLS material serve loads through security.server_cert_file/server_key_file.
func serveTestServerCert(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	dir := t.TempDir()
	certFile = filepath.Join(dir, "server.crt")
	keyFile = filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write server certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write server key: %v", err)
	}
	pool = x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("trust the generated server certificate")
	}
	return certFile, keyFile, pool
}

// freeLoopbackAddr reserves and releases a loopback port, so serve can bind a
// port the test knows in advance.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return addr
}

// servedKMS is one in-process `serve` run and the client-side material needed
// to talk to it.
type servedKMS struct {
	logs    *safeBuffer
	baseURL string
	token   string
	certDir string
	pool    *x509.CertPool

	stop     chan struct{}
	done     chan int
	stopOnce sync.Once
	waited   bool
	exitCode int
}

// startServe initialises a database with one admin identity holding both
// credentials, then runs `serve` against it on a loopback port.
func startServe(t *testing.T, tlsEnabled bool, extraArgs ...string) *servedKMS {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	kek := filepath.Join(dir, "master.key")
	certDir := t.TempDir()

	initCLI := newTestCLI()
	if code := initCLI.Run([]string{"init", "--sqlite-path", db, "--kek-file", kek,
		"--admin", "ops", "--cert-dir", certDir}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, initCLI.stderr())
	}
	token := tokenFromCLIOutput(t, initCLI.stdout())

	addr := freeLoopbackAddr(t)
	scheme := "http"
	args := []string{
		"--sqlite-path", db,
		"--kek-file", kek,
		"--http-addr", addr,
		"--grpc-addr", "127.0.0.1:0",
	}
	served := &servedKMS{
		logs:    &safeBuffer{},
		token:   token,
		certDir: certDir,
		stop:    make(chan struct{}),
		done:    make(chan int, 1),
	}
	if tlsEnabled {
		certFile, keyFile, pool := serveTestServerCert(t)
		served.pool = pool
		scheme = "https"
		args = append(args, "--tls-enabled", "--server-cert-file", certFile, "--server-key-file", keyFile)
	} else {
		args = append(args, "--tls-enabled=false")
	}
	args = append(args, extraArgs...)
	served.baseURL = scheme + "://" + addr

	c := newTestCLI()
	c.CLI.Stderr = served.logs
	c.stopServe = served.stop
	go func() { served.done <- c.cmdServe(args) }()
	t.Cleanup(func() { served.stopAndWait(t) })
	return served
}

// client returns an HTTP client for this server, presenting the admin client
// certificate when withCert is set — the CLI equivalent of an operator passing
// --cert/--key, and the browser equivalent of an imported credential.
func (s *servedKMS) client(t *testing.T, withCert bool) *http.Client {
	t.Helper()
	transport := &http.Transport{}
	if s.pool != nil {
		cfg := &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS12}
		if withCert {
			pair, err := tls.LoadX509KeyPair(filepath.Join(s.certDir, "ops.crt"), filepath.Join(s.certDir, "ops.key"))
			if err != nil {
				t.Fatalf("load the admin client certificate: %v", err)
			}
			cfg.Certificates = []tls.Certificate{pair}
		}
		transport.TLSClientConfig = cfg
	} else if withCert {
		t.Fatal("a plaintext listener cannot carry a client certificate")
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: transport}
}

// health polls the unauthenticated health endpoint until the listener answers,
// which is also how the test knows serve finished starting up.
func (s *servedKMS) health(t *testing.T) map[string]any {
	t.Helper()
	client := s.client(t, false)
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case code := <-s.done:
			s.waited, s.exitCode = true, code
			t.Fatalf("serve exited during startup with code %d; log:\n%s", code, s.logs.String())
		default:
		}
		resp, err := client.Get(s.baseURL + "/api/v1/health")
		if err == nil {
			return decodeJSONBody(t, resp)
		}
		if time.Now().After(deadline) {
			t.Fatalf("health endpoint never answered: %v; log:\n%s", err, s.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// whoami calls the authenticated identity endpoint with the admin bearer token,
// optionally over a connection presenting the admin client certificate.
func (s *servedKMS) whoami(t *testing.T, withCert bool) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.baseURL+"/api/v1/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.client(t, withCert).Do(req)
	if err != nil {
		t.Fatalf("whoami (cert=%t): %v", withCert, err)
	}
	return resp.StatusCode, decodeJSONBody(t, resp)
}

func decodeJSONBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response body %q: %v", raw, err)
	}
	return body
}

// stopAndWait ends the server as a SIGTERM would and returns its exit code.
func (s *servedKMS) stopAndWait(t *testing.T) int {
	t.Helper()
	s.stopOnce.Do(func() { close(s.stop) })
	if !s.waited {
		select {
		case code := <-s.done:
			s.waited, s.exitCode = true, code
		case <-time.After(10 * time.Second):
			s.waited = true
			t.Errorf("serve did not shut down within 10s; log:\n%s", s.logs.String())
		}
	}
	return s.exitCode
}

// TestServeEnforcesAdminClientCertificate is the end-to-end statement of the
// feature: with TLS on and the setting at its default, the admin bearer token
// alone is refused by the running server, and the same token over a connection
// presenting the admin certificate is admitted as mTLS.
func TestServeEnforcesAdminClientCertificate(t *testing.T) {
	s := startServe(t, true)

	if got := s.health(t)["admin_client_cert_required"]; got != true {
		t.Fatalf("health admin_client_cert_required = %v, want true", got)
	}

	if code, body := s.whoami(t, false); code != http.StatusUnauthorized {
		t.Fatalf("token-only whoami = %d %v, want 401", code, body)
	}
	code, body := s.whoami(t, true)
	if code != http.StatusOK {
		t.Fatalf("token+certificate whoami = %d %v, want 200", code, body)
	}
	if body["name"] != "ops" || body["auth_method"] != "mtls" {
		t.Fatalf("whoami = %v, want ops authenticated by mtls", body)
	}

	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
	if logs := s.logs.String(); !logContains(logs, adminCertEnforcedMsg) {
		t.Fatalf("startup log does not state the enforced posture:\n%s", logs)
	}
}

// TestServeWithoutTLSRelaxesAdminClientCertificate: no handshake means no
// certificate can ever reach the server, so enforcing the requirement would
// lock every admin out of a plaintext dev run. serve relaxes it and says so,
// and the health endpoint reports the relaxed value so the login page does not
// promise a certificate prompt that will never come.
func TestServeWithoutTLSRelaxesAdminClientCertificate(t *testing.T) {
	s := startServe(t, false)

	if got := s.health(t)["admin_client_cert_required"]; got != false {
		t.Fatalf("health admin_client_cert_required = %v, want false", got)
	}
	code, body := s.whoami(t, false)
	if code != http.StatusOK {
		t.Fatalf("token-only whoami over plaintext = %d %v, want 200", code, body)
	}
	if body["name"] != "ops" || body["auth_method"] != "token" {
		t.Fatalf("whoami = %v, want ops authenticated by token", body)
	}

	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
	// Loopback bind: the un-escalated wording, and never the enforced notice.
	logs := s.logs.String()
	if !logContains(logs, adminCertUnenforceableMsg) {
		t.Fatalf("startup log does not warn that the requirement is unenforceable:\n%s", logs)
	}
	if logContains(logs, adminCertEnforcedMsg) {
		t.Fatalf("startup log claims enforcement without TLS:\n%s", logs)
	}
}

// TestServeAdminClientCertificateDisabled: an operator who explicitly opts out
// gets the old behaviour on a TLS listener, and a warning naming what they gave
// up.
func TestServeAdminClientCertificateDisabled(t *testing.T) {
	s := startServe(t, true, "--admin-require-client-cert=false")

	if got := s.health(t)["admin_client_cert_required"]; got != false {
		t.Fatalf("health admin_client_cert_required = %v, want false", got)
	}
	code, body := s.whoami(t, false)
	if code != http.StatusOK {
		t.Fatalf("token-only whoami = %d %v, want 200", code, body)
	}
	if body["name"] != "ops" || body["auth_method"] != "token" {
		t.Fatalf("whoami = %v, want ops authenticated by token", body)
	}

	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
	logs := s.logs.String()
	if !logContains(logs, adminCertDisabledMsg) {
		t.Fatalf("startup log does not warn that the requirement is disabled:\n%s", logs)
	}
	if logContains(logs, adminCertEnforcedMsg) {
		t.Fatalf("startup log claims enforcement while the setting is off:\n%s", logs)
	}
}

// contains reports whether the JSON log stream carries msg. The logger escapes
// its message field, so the constants (which contain quotes) are compared
// against the escaped form as well.
func logContains(logs, msg string) bool {
	if bytes.Contains([]byte(logs), []byte(msg)) {
		return true
	}
	escaped, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	return bytes.Contains([]byte(logs), bytes.Trim(escaped, `"`))
}
