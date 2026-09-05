package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

func TestConnectionTransportOnly(t *testing.T) {
	uri, _ := url.Parse("kms://identity/example")
	cert := &x509.Certificate{Raw: []byte("leaf DER"), URIs: []*url.URL{uri}, NotAfter: time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)}
	// No service is wired: neither authentication nor readiness may be consulted.
	s := &server{}
	for _, tc := range []struct {
		name string
		tls  *tls.ConnectionState
		cert bool
	}{
		{"plain", nil, false},
		{"TLS only", &tls.ConnectionState{}, false},
		{"unverified", &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}, false},
		{"verified", &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/auth/connection?identity=someone-else", nil)
			r.TLS = tc.tls
			r.Header.Set("Authorization", "Bearer invalid")
			r.Header.Set("X-Forwarded-Client-Cert", "spoofed")
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("Origin", "https://evil.example")
			w := httptest.NewRecorder()
			s.serveAPI(w, r)
			mustStatus(t, w, 200)
			body := decodeBody(t, w)
			if body["tls_enabled"] != (tc.tls != nil) {
				t.Fatalf("TLS: %v", body)
			}
			if !tc.cert {
				if body["client_certificate"] != nil {
					t.Fatalf("unexpected certificate: %v", body)
				}
			} else {
				d := body["client_certificate"].(map[string]any)
				if d["identity_uri"] != uri.String() || d["fingerprint_sha256"] != fmt.Sprintf("%x", sha256.Sum256(cert.Raw)) || d["not_after"] != "2027-01-02T03:04:05Z" {
					t.Fatalf("details: %v", d)
				}
				if len(d) != 3 {
					t.Fatalf("extra fields: %v", d)
				}
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("headers: %v", w.Header())
			}
		})
	}
	for _, uris := range [][]*url.URL{nil, {uri, uri}} {
		leaf := *cert
		leaf.URIs = uris
		leaf.Subject.CommonName = "do-not-use"
		w := httptest.NewRecorder()
		s.serveAPI(w, withPeerCert(httptest.NewRequest("GET", "/api/v1/auth/connection", nil), &leaf))
		if d := decodeBody(t, w)["client_certificate"].(map[string]any); d["identity_uri"] != nil {
			t.Fatalf("ambiguous identity: %v", d)
		}
	}
	for _, method := range []string{"POST", "HEAD", "OPTIONS", "DELETE"} {
		w := httptest.NewRecorder()
		s.serveAPI(w, httptest.NewRequest(method, "/api/v1/auth/connection", nil))
		mustStatus(t, w, 405)
	}
}

func TestConnectionIgnoresRevocationAndToken(t *testing.T) {
	e, cert := newAdminCertEnv(t)
	srv, err := New(e.svc, Config{})
	if err != nil {
		t.Fatal(err)
	}
	read := func(cert *x509.Certificate, token string) string {
		r := httptest.NewRequest("GET", "/api/v1/auth/connection", nil)
		if cert != nil {
			r = withPeerCert(r, cert)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, r)
		mustStatus(t, w, 200)
		return w.Body.String()
	}
	before := read(cert, e.adminToken)
	pr := core.Principal{Identity: domain.Identity{Name: "cli", Kind: domain.IdentityKindAdmin}}
	if err := e.svc.RevokeIdentityCertificate(context.Background(), pr, "admin", core.CertSerial(cert)); err != nil {
		t.Fatal(err)
	}
	if after := read(cert, "invalid"); after != before {
		t.Fatalf("account state changed diagnostics: %s / %s", before, after)
	}
	if absent := read(nil, ""); strings.Contains(absent, "kms://identity/admin") {
		t.Fatal("certificate leaked across requests")
	}
}
