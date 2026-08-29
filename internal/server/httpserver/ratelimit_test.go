package httpserver

import (
	"net/http"
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
