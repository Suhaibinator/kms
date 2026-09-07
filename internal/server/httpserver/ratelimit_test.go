package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginRateLimit(t *testing.T) {
	e := newTestEnv(t)

	// The login limiter allows a burst of 10. Ten bad attempts return 401; the
	// eleventh is throttled to 429.
	for i := range 10 {
		w := e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "bad"}, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, w.Code)
		}
	}
	w := e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "bad"}, nil)
	mustStatus(t, w, http.StatusTooManyRequests)
	if errCode(t, w) != "rate_limited" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestForwardedForCannotEvadeLoginThrottle(t *testing.T) {
	e := newTestEnv(t) // TrustProxyHeaders defaults false

	// An attacker rotates X-Forwarded-For on every request to try to get a
	// fresh bucket each time. Because the proxy is not trusted, the header is
	// ignored and the real peer keeps hitting the same bucket: the throttle
	// still engages.
	saw429 := false
	for i := range 40 {
		hdr := map[string]string{"X-Forwarded-For": "203.0.113." + itoa(i)}
		w := e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "bad"}, hdr)
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Fatal("rotating X-Forwarded-For evaded the login throttle; spoofed header was trusted")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestFailedAuthThrottled(t *testing.T) {
	e := newTestEnv(t)
	// Bad tokens on a protected route also consume the login bucket; after the
	// burst is exhausted the response becomes 429.
	saw429 := false
	for i := range 12 {
		w := e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer bad"})
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d", i+1, w.Code)
		}
	}
	if !saw429 {
		t.Fatalf("expected a 429 once the bucket drained")
	}
}

func TestFailedAuthLimiterRunsBeforeAuthentication(t *testing.T) {
	e := newTestEnv(t)
	for i := range 10 {
		w := e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer bad"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, w.Code)
		}
	}
	// Even correct credentials are not verified while this IP's failed-auth
	// bucket is exhausted. The old post-auth limiter incorrectly returned 200.
	w := e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer " + e.adminToken})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("valid request after exhausted auth bucket = %d, want 429", w.Code)
	}
}

// TestCrossSiteCredentiallessRequestsDoNotExhaustAuthBudget is the
// confused-deputy case: a page on another site makes a victim's browser fire
// credentialless requests at a known KMS address. Every one is refused by the
// same-origin gate before any budget is charged, so the victim's own
// credential still authenticates afterwards instead of getting 429.
func TestCrossSiteCredentiallessRequestsDoNotExhaustAuthBudget(t *testing.T) {
	e := newTestEnv(t)
	crossSite := map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors"}
	for i := range 40 {
		w := e.do(http.MethodGet, "/api/v1/namespaces", nil, crossSite)
		if w.Code != http.StatusForbidden || errCode(t, w) != "permission_denied" {
			t.Fatalf("cross-site GET %d: status = %d code = %s, want 403 permission_denied", i+1, w.Code, w.Body.String())
		}
		w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "bad"}, crossSite)
		if w.Code != http.StatusForbidden {
			t.Fatalf("cross-site login %d: status = %d, want 403", i+1, w.Code)
		}
		// A browser without Fetch Metadata still names its origin on a POST.
		w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "bad"}, map[string]string{"Origin": "https://evil.example"})
		if w.Code != http.StatusForbidden {
			t.Fatalf("foreign-origin login %d: status = %d, want 403", i+1, w.Code)
		}
	}
	w := e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer " + e.adminToken})
	mustStatus(t, w, http.StatusOK)
	w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": e.adminToken}, nil)
	mustStatus(t, w, http.StatusOK)
}

// TestSameOriginGateOnlyRefusesBrowserCrossSiteRequests pins the gate's
// boundary: Fetch Metadata decides when present, a foreign or opaque Origin
// is refused only when it is not, and a request carrying neither header (the
// CLI, the SDKs, curl) is not a browser request and passes.
func TestSameOriginGateOnlyRefusesBrowserCrossSiteRequests(t *testing.T) {
	e := newTestEnv(t)
	auth := "Bearer " + e.adminToken
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no browser headers", map[string]string{"Authorization": auth}, http.StatusOK},
		{"same-origin fetch", map[string]string{"Authorization": auth, "Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"user-initiated navigation", map[string]string{"Authorization": auth, "Sec-Fetch-Site": "none"}, http.StatusOK},
		{"matching Origin without Fetch Metadata", map[string]string{"Authorization": auth, "Origin": "http://example.com"}, http.StatusOK},
		{"cross-site fetch", map[string]string{"Authorization": auth, "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site sibling", map[string]string{"Authorization": auth, "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"foreign Origin", map[string]string{"Authorization": auth, "Origin": "http://evil.example"}, http.StatusForbidden},
		{"opaque Origin", map[string]string{"Authorization": auth, "Origin": "null"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		// httptest.NewRequest addresses example.com, the host a matching
		// Origin must name.
		w := e.do(http.MethodGet, "/api/v1/whoami", nil, tc.headers)
		if w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

// TestCrossSiteRequestHonoursForwardedHostOnlyBehindTrustedProxy: a proxy
// that rewrites Host to its upstream address would make every browser Origin
// look foreign, so X-Forwarded-Host stands in for Host — but only when the
// operator has declared the proxy trusted, since otherwise the header is the
// attacker's to set.
func TestCrossSiteRequestHonoursForwardedHostOnlyBehindTrustedProxy(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("{}"))
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Origin", "https://kms.example.com")
	r.Header.Set("X-Forwarded-Host", "kms.example.com, proxy.internal")
	if !crossSiteRequest(r, false) {
		t.Fatal("untrusted X-Forwarded-Host was used for the origin comparison")
	}
	if crossSiteRequest(r, true) {
		t.Fatal("trusted X-Forwarded-Host did not stand in for the rewritten Host")
	}
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if crossSiteRequest(r, false) {
		t.Fatal("Fetch Metadata must decide before the Origin fallback")
	}
}

// TestCredentiallessRequestsDoNotConsumeAuthBudget: requests presenting no
// credential at all — a misconfigured client, a bare "Bearer", a Basic
// header — have nothing to verify. They are bounded by their own admission
// class and never touch the verification budget, so a valid credential from
// the same IP is not throttled behind them.
func TestCredentiallessRequestsDoNotConsumeAuthBudget(t *testing.T) {
	e := newTestEnv(t)
	variants := []map[string]string{nil, {"Authorization": "Bearer"}, {"Authorization": "Basic Zm9vOmJhcg=="}}
	saw429 := false
	for i := range 40 {
		w := e.do(http.MethodGet, "/api/v1/namespaces", nil, variants[i%len(variants)])
		switch w.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			saw429 = true
		default:
			t.Fatalf("credentialless request %d: status = %d, want 401 or 429", i+1, w.Code)
		}
	}
	if !saw429 {
		t.Fatal("credentialless class is not bounded: 40 requests never hit 429")
	}
	w := e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer " + e.adminToken})
	mustStatus(t, w, http.StatusOK)
}

func TestExhaustedAuthBudgetDoesNotConsumeCredentiallessBudget(t *testing.T) {
	e := newTestEnv(t)
	// The reverse holds too: a drained verification budget does not lock the
	// credentialless class, which reports under its own metrics label.
	for range 10 {
		w := e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer bad"})
		mustStatus(t, w, http.StatusUnauthorized)
	}
	w := e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer bad"})
	mustStatus(t, w, http.StatusTooManyRequests)
	w = e.do(http.MethodGet, "/api/v1/namespaces", nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)
	w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": ""}, nil)
	mustStatus(t, w, http.StatusUnauthorized)
}

// TestLoginShapeIsEnforcedBeforeAnyBudget: the wrong method, a non-JSON
// content type, a malformed body, and an empty token are not login attempts.
// None of them spends the verification budget, so a real attempt from the
// same IP still gets evaluated; and malformed bodies are bounded by the
// credentialless class rather than being free.
func TestLoginShapeIsEnforcedBeforeAnyBudget(t *testing.T) {
	e := newTestEnv(t)
	for i := range 40 {
		w := e.do(http.MethodGet, "/api/v1/auth/login", nil, nil)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET login %d: status = %d, want 405", i+1, w.Code)
		}
		w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "bad"}, map[string]string{"Content-Type": "text/plain"})
		if w.Code != http.StatusUnsupportedMediaType || errCode(t, w) != "invalid_argument" {
			t.Fatalf("text/plain login %d: status = %d body = %s, want 415 invalid_argument", i+1, w.Code, w.Body.String())
		}
	}
	saw429 := false
	for i := range 40 {
		var w *httptest.ResponseRecorder
		if i%2 == 0 {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("{not json"))
			req.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			e.handler.ServeHTTP(w, req)
		} else {
			w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "  "}, nil)
		}
		switch w.Code {
		case http.StatusBadRequest, http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			saw429 = true
		default:
			t.Fatalf("malformed login %d: status = %d, want 400/401 or 429", i+1, w.Code)
		}
	}
	if !saw429 {
		t.Fatal("malformed logins are not bounded: 40 requests never hit 429")
	}
	// The verification budget is untouched: a genuine attempt is evaluated.
	w := e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": e.adminToken}, nil)
	mustStatus(t, w, http.StatusOK)
}
