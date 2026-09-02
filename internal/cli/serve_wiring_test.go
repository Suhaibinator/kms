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
	"strings"
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

// writeServerCert writes server.crt/server.key into dir with the given serial —
// the operator-supplied TLS material serve loads through
// security.server_cert_file/server_key_file — and returns their paths, a pool
// trusting the certificate, and the parsed leaf. Distinct serials are how a
// test tells one keypair from another across a rotation; calling it twice on
// the same directory replaces the pair in place, which is what an operator does
// before sending SIGHUP.
func writeServerCert(t *testing.T, dir string, serial int64) (certFile, keyFile string, pool *x509.CertPool, leaf *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
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

	certFile = filepath.Join(dir, "server.crt")
	keyFile = filepath.Join(dir, "server.key")
	writeFileAtomically(t, certFile, certPEM)
	writeFileAtomically(t, keyFile, keyPEM)
	pool = x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("trust the generated server certificate")
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse server certificate: %v", err)
	}
	return certFile, keyFile, pool, leaf
}

// writeFileAtomically replaces path through a temporary file in the same
// directory, the rotation procedure operations.md prescribes: a reload can land
// at any moment, and a half-written certificate would be a reload failure.
func writeFileAtomically(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename %s to %s: %v", tmp, path, err)
	}
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
	addr    string
	token   string
	certDir string
	pool    *x509.CertPool
	// configPath is the YAML file serve reads through --config. A test rewrites
	// it and fires reloadCh to drive a reload the way SIGHUP does.
	configPath string
	// tlsDir holds server.crt/server.key when TLS is on, so a test can rotate
	// the keypair in place under the running listener.
	tlsDir   string
	certFile string
	keyFile  string

	reloadCh chan struct{}
	stop     chan struct{}
	done     chan int
	stopOnce sync.Once
	waited   bool
	exitCode int
}

// startServe initialises a database with one admin identity holding both
// credentials, then runs `serve` against it on a loopback port. The server
// always reads a config file (`--config`), so a test can rewrite it and reload.
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
	configPath := filepath.Join(dir, "config.yaml")
	writeConfigFile(t, configPath, "log:\n  level: info\n")
	args := []string{
		"--config", configPath,
		"--sqlite-path", db,
		"--kek-file", kek,
		"--http-addr", addr,
		"--grpc-addr", "127.0.0.1:0",
	}
	served := &servedKMS{
		logs:       &safeBuffer{},
		addr:       addr,
		token:      token,
		certDir:    certDir,
		configPath: configPath,
		reloadCh:   make(chan struct{}),
		stop:       make(chan struct{}),
		done:       make(chan int, 1),
	}
	if tlsEnabled {
		served.tlsDir = t.TempDir()
		certFile, keyFile, pool, _ := writeServerCert(t, served.tlsDir, 1)
		served.pool, served.certFile, served.keyFile = pool, certFile, keyFile
		scheme = "https"
		args = append(args, "--tls-enabled", "--server-cert-file", certFile, "--server-key-file", keyFile)
	} else {
		args = append(args, "--tls-enabled=false")
	}
	args = append(args, extraArgs...)
	served.baseURL = scheme + "://" + addr

	c := newTestCLI()
	c.Stderr = served.logs
	c.stopServe = served.stop
	c.reloadSignal = served.reloadCh
	go func() { served.done <- c.cmdServe(args) }()
	t.Cleanup(func() { served.stopAndWait(t) })
	return served
}

// reload drives one reload through the same seam SIGHUP uses. The send
// completes when the serve loop picks the signal up; the reload itself happens
// afterwards, so callers still poll for its effect.
func (s *servedKMS) reload(t *testing.T) {
	t.Helper()
	select {
	case s.reloadCh <- struct{}{}:
	case code := <-s.done:
		s.waited, s.exitCode = true, code
		t.Fatalf("serve exited with code %d instead of reloading; log:\n%s", code, s.logs.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("serve did not accept a reload signal within 10s; log:\n%s", s.logs.String())
	}
}

// rewriteConfig replaces the config file serve reads on reload.
func (s *servedKMS) rewriteConfig(t *testing.T, body string) {
	t.Helper()
	writeConfigFile(t, s.configPath, body)
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
			defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()
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

// get fetches an unauthenticated endpoint on the running server and returns
// the status and body — the shape a Prometheus scrape and a container health
// check both take.
func (s *servedKMS) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := s.client(t, false).Get(s.baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s body: %v", path, err)
	}
	return resp.StatusCode, string(raw)
}

// TestServeExposesMetrics is the end-to-end statement of the exporter's
// wiring: the exposition is served on the HTTP listener without a credential,
// and it carries the posture serve actually came up with rather than the zero
// every gauge starts at.
func TestServeExposesMetrics(t *testing.T) {
	s := startServe(t, true)
	s.health(t) // wait for the listener

	code, body := s.get(t, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", code)
	}
	for _, want := range []string{
		"kms_build_info{",
		"kms_ready 1",
		"kms_tls_enabled 1",
		"kms_admin_client_cert_required 1",
		"kms_watch_subscribers 0",
		"kms_kek_generations 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
	// The synchronous startup sample means the database-backed gauges carry
	// real numbers before the first scrape, not zero until the first tick.
	if strings.Contains(body, "kms_ops_last_sample_timestamp_seconds 0\n") {
		t.Error("the startup sample did not run before the listener opened")
	}
	if strings.Contains(body, `kms_db_file_bytes{file="main"} 0`) {
		t.Error("the database file size was never sampled")
	}

	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
}

// TestServeMetricsDisabled: metrics.enabled=false attaches no exporter, so
// /metrics is not a route at all and falls through to the normal dispatch —
// here a 404, with the frontend off so the catch-all is not the SPA entry
// document (which answers 503 in a test binary that embeds no built assets).
func TestServeMetricsDisabled(t *testing.T) {
	s := startServe(t, false, "--metrics-enabled=false", "--frontend-enabled=false")
	s.health(t)

	code, body := s.get(t, "/metrics")
	if code != http.StatusNotFound {
		t.Fatalf("GET /metrics with the exporter off = %d, want 404; body=%s", code, body)
	}
	if strings.Contains(body, "kms_build_info") {
		t.Error("the exposition is served while metrics.enabled is false")
	}
}

// TestHealthcheckCommand is the container HEALTHCHECK path. The command
// resolves the listen address and the TLS posture the same way serve does, so
// the flags that started the server also describe how to reach it.
func TestHealthcheckCommand(t *testing.T) {
	s := startServe(t, true)
	s.health(t)

	args := []string{"healthcheck", "--http-addr", strings.TrimPrefix(s.baseURL, "https://"), "--tls-enabled"}
	c := newTestCLI()
	if code := c.Run(args); code != 0 {
		t.Fatalf("healthcheck = %d, want 0; stderr=%s", code, c.stderr())
	}
	ready := newTestCLI()
	if code := ready.Run(append(append([]string{}, args...), "--ready")); code != 0 {
		t.Fatalf("healthcheck --ready = %d, want 0; stderr=%s", code, ready.stderr())
	}

	// A closed port is a failure, with the reason on one line.
	closed := newTestCLI()
	closedCode := closed.Run([]string{"healthcheck", "--http-addr", freeLoopbackAddr(t),
		"--tls-enabled=false", "--timeout", "2s"})
	if closedCode != 1 {
		t.Fatalf("healthcheck against a closed port = %d, want 1; stderr=%s", closedCode, closed.stderr())
	}
	if !strings.HasPrefix(closed.stderr(), "error: http://127.0.0.1:") {
		t.Errorf("closed-port message = %q", closed.stderr())
	}

	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
}

// --- SIGHUP reload over the running listeners -------------------------------

// servedCert completes a fresh TLS handshake against the HTTP listener and
// reports the serial of the certificate it presented plus the negotiated ALPN
// protocol. Verification is skipped deliberately: the question is which
// certificate the listener serves after a rotation, not whether this test
// trusts it.
func (s *servedKMS) servedCert(t *testing.T) (serial, alpn string) {
	t.Helper()
	conn, err := tls.Dial("tcp", s.addr, &tls.Config{
		InsecureSkipVerify: true, // the test inspects the certificate itself
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatalf("dial the TLS listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("the listener presented no certificate")
	}
	return state.PeerCertificates[0].SerialNumber.String(), state.NegotiatedProtocol
}

// awaitServedSerial polls fresh handshakes until the listener presents want. A
// reload runs asynchronously with respect to the test's dial, so this is the
// only honest way to observe it.
func (s *servedKMS) awaitServedSerial(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := s.servedCert(t)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener still presents serial %s after 10s, want %s; log:\n%s", got, want, s.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// rotateServerCert writes a fresh keypair with the given serial over the
// configured paths — atomically, through a temporary file in the same
// directory, the way operations.md tells an operator to — and moves the test's
// own trust anchor along with it so the HTTP client still verifies the server
// after the reload.
func (s *servedKMS) rotateServerCert(t *testing.T, serial int64) {
	t.Helper()
	_, _, pool, _ := writeServerCert(t, s.tlsDir, serial)
	s.pool = pool
}

// awaitLog polls the server's log until msg appears.
func (s *servedKMS) awaitLog(t *testing.T, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if logContains(s.logs.String(), msg) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never appeared in the server log within 10s; log:\n%s", msg, s.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestServeReloadRotatesServerCertificate is the operator-facing statement of
// the feature: write a new keypair over the configured paths, signal a reload,
// and the running listener presents it to the next handshake — no restart, no
// edit to the config file, and h2 still negotiated afterwards.
//
// GRPCFactory is nil in this test binary, so serve runs HTTP-only and there is
// no gRPC listener to dial; the per-slot ALPN the gRPC listener depends on is
// covered in internal/server/listenertls and internal/server/grpcserver.
func TestServeReloadRotatesServerCertificate(t *testing.T) {
	s := startServe(t, true)
	s.health(t)

	serial, alpn := s.servedCert(t)
	if serial != "1" {
		t.Fatalf("serial before the reload = %s, want 1", serial)
	}
	if alpn != "h2" {
		t.Fatalf("negotiated %q before the reload, want h2", alpn)
	}

	s.rotateServerCert(t, 2)
	s.reload(t)
	s.awaitServedSerial(t, "2")

	if _, alpn := s.servedCert(t); alpn != "h2" {
		t.Errorf("negotiated %q after the reload, want h2", alpn)
	}
	// The reload swaps material under the running listener rather than
	// restarting it: the same process still answers.
	s.health(t)
	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
}

// TestServeReloadKeepsCertificateWhenTheNewOneIsCorrupt: the new material is
// loaded before anything running is touched, so a bad file costs one error line
// and nothing else. The listener keeps serving the certificate it had.
func TestServeReloadKeepsCertificateWhenTheNewOneIsCorrupt(t *testing.T) {
	s := startServe(t, true)
	s.health(t)

	s.rotateServerCert(t, 2)
	s.reload(t)
	s.awaitServedSerial(t, "2")

	if err := os.WriteFile(s.certFile, []byte("-----BEGIN CERTIFICATE-----\ntruncated\n"), 0o600); err != nil {
		t.Fatalf("corrupt the certificate file: %v", err)
	}
	s.reload(t)
	s.awaitLog(t, configReloadFailedMsg)

	if serial, _ := s.servedCert(t); serial != "2" {
		t.Errorf("listener serves serial %s after a failed reload, want the certificate it had (2)", serial)
	}
	s.health(t)
	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
}

// TestServeReloadAppliesLogLevelFromTheFile: the level is re-read from the file
// with the startup precedence and applied to the live logger. The debug-only
// "configuration sources" line is the visible proof — absent while the server
// runs at info, present once the reload takes.
func TestServeReloadAppliesLogLevelFromTheFile(t *testing.T) {
	const configSourcesMsg = "configuration sources"
	s := startServe(t, false)
	s.health(t)

	if logContains(s.logs.String(), configSourcesMsg) {
		t.Fatalf("the debug-only configuration-sources line appeared at info level:\n%s", s.logs.String())
	}
	s.rewriteConfig(t, "log:\n  level: debug\n")
	s.reload(t)
	s.awaitLog(t, configReloadedMsg)
	s.awaitLog(t, configSourcesMsg)

	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
}
