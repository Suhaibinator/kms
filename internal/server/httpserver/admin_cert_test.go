package httpserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// withPeerCert marks req as having arrived over a TLS connection on which cert
// was presented and chain-verified, which is what the listener's
// VerifyClientCertIfGiven mode guarantees for a certificate that reaches a
// handler.
func withPeerCert(req *http.Request, cert *x509.Certificate) *http.Request {
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	return req
}

// issueAdminCert mints a certificate for the seeded admin through the
// offline-only path (`parameter-store admin-cert issue`) and returns the parsed
// leaf, ready to attach to a request.
func (e *testEnv) issueAdminCert(t *testing.T) *x509.Certificate {
	t.Helper()
	cliPr := core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
	bundle, err := e.svc.IssueLocalAdminCertificate(context.Background(), cliPr, "admin", 0)
	if err != nil {
		t.Fatalf("issue admin cert: %v", err)
	}
	block, _ := pem.Decode([]byte(bundle.CertPEM))
	if block == nil {
		t.Fatalf("no PEM block in issued certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	return cert
}

// request builds an API request, optionally carrying a JSON body, a bearer
// token, and a peer certificate, and runs it through the handler. It exists
// because testEnv.do cannot set r.TLS.
func (e *testEnv) request(t *testing.T, method, target, token string, body any, cert *x509.Certificate) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		req = httptest.NewRequest(method, target, bytes.NewReader(b))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if cert != nil {
		req = withPeerCert(req, cert)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w
}

// newAdminCertEnv returns an environment with the admin client-certificate
// requirement in force and a certificate issued for the seeded admin.
func newAdminCertEnv(t *testing.T) (*testEnv, *x509.Certificate) {
	t.Helper()
	e := newTestEnv(t)
	cert := e.issueAdminCert(t)
	e.svc.SetAdminRequireClientCert(true)
	return e, cert
}

// TestAdminCertHTTP_BearerAloneRejected: the console's own token, replayed
// without the certificate, does not authenticate an admin.
func TestAdminCertHTTP_BearerAloneRejected(t *testing.T) {
	e, _ := newAdminCertEnv(t)

	w := e.request(t, http.MethodGet, "/api/v1/whoami", e.adminToken, nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)
	if got := errCode(t, w); got != "unauthenticated" {
		t.Fatalf("code = %s, want unauthenticated", got)
	}
}

// TestAdminCertHTTP_BearerWithCertAccepted: both credentials on the same
// request authenticate the admin, reported as the stronger method.
func TestAdminCertHTTP_BearerWithCertAccepted(t *testing.T) {
	e, cert := newAdminCertEnv(t)

	w := e.request(t, http.MethodGet, "/api/v1/whoami", e.adminToken, nil, cert)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["name"] != "admin" || body["auth_method"] != string(domain.AuthMethodMTLS) {
		t.Fatalf("whoami = %v, want the admin with auth_method mtls", body)
	}
}

// TestAdminCertHTTP_UnverifiedChainIgnored: a certificate the TLS layer did not
// chain-verify is not a credential. Trusting mere presence would let anything a
// proxy or a misconfigured listener attached count as proof of possession.
func TestAdminCertHTTP_UnverifiedChainIgnored(t *testing.T) {
	e, cert := newAdminCertEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+e.adminToken)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}} // no VerifiedChains
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)

	mustStatus(t, w, http.StatusUnauthorized)
	if got := errCode(t, w); got != "unauthenticated" {
		t.Fatalf("code = %s, want unauthenticated", got)
	}
}

// TestAdminCertHTTP_ClientIdentityUnaffected: the requirement is admin-only. A
// token-method client identity still authenticates with its token alone.
func TestAdminCertHTTP_ClientIdentityUnaffected(t *testing.T) {
	e, _ := newAdminCertEnv(t)

	w := e.request(t, http.MethodGet, "/api/v1/whoami", e.clientToken, nil, nil)
	mustStatus(t, w, http.StatusOK)
	if body := decodeBody(t, w); body["name"] != "client" || body["auth_method"] != string(domain.AuthMethodToken) {
		t.Fatalf("whoami = %v, want the client with auth_method token", body)
	}
}

// TestAdminCertHTTP_Login covers the console's sign-in probe under the
// requirement: the token alone is refused with the same generic 401 as a bad
// token (no oracle for which half was wrong), and token plus certificate
// succeeds and reports the method.
func TestAdminCertHTTP_Login(t *testing.T) {
	e, cert := newAdminCertEnv(t)

	w := e.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"token": e.adminToken}, nil)
	mustStatus(t, w, http.StatusUnauthorized)
	if got := errCode(t, w); got != "unauthenticated" {
		t.Fatalf("token-only login code = %s, want unauthenticated", got)
	}

	w = e.request(t, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"token": e.adminToken}, cert)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	id, _ := body["identity"].(map[string]any)
	if id["name"] != "admin" || id["kind"] != domain.IdentityKindAdmin {
		t.Fatalf("identity = %v", id)
	}
	if body["auth_method"] != string(domain.AuthMethodMTLS) {
		t.Fatalf("auth_method = %v, want mtls", body["auth_method"])
	}
}

// TestAdminCertHTTP_HealthReportsPosture: the login page decides whether to
// warn from these two unauthenticated fields, so they must track both the
// server's configuration and the certificate on this particular connection.
func TestAdminCertHTTP_HealthReportsPosture(t *testing.T) {
	e, cert := newAdminCertEnv(t)
	srv, err := New(e.svc, Config{Addr: ":0", Version: "test-version", TLSEnabled: true, AdminClientCertRequired: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	health := func(cert *x509.Certificate) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		if cert != nil {
			req = withPeerCert(req, cert)
		}
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)
		mustStatus(t, w, http.StatusOK)
		return decodeBody(t, w)
	}

	body := health(nil)
	if body["admin_client_cert_required"] != true || body["client_cert_presented"] != false {
		t.Fatalf("health without a certificate = %v", body)
	}
	body = health(cert)
	if body["admin_client_cert_required"] != true || body["client_cert_presented"] != true {
		t.Fatalf("health with a certificate = %v", body)
	}
}
