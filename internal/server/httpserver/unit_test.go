package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestMapError(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{domain.Errorf(domain.ErrInvalidArgument, "bad"), 400, "invalid_argument"},
		{domain.Errorf(domain.ErrUnauthenticated, "no"), 401, "unauthenticated"},
		{domain.Errorf(domain.ErrPermissionDenied, "no"), 403, "permission_denied"},
		{domain.Errorf(domain.ErrNotFound, "no"), 404, "not_found"},
		{domain.Errorf(domain.ErrAlreadyExists, "dup"), 409, "already_exists"},
		{domain.Errorf(domain.ErrFailedPrecondition, "state"), 412, "failed_precondition"},
		{domain.Errorf(domain.ErrNotReady, "wait"), 503, "unavailable"},
	}
	for _, c := range cases {
		status, code, _ := mapError(c.err)
		if status != c.wantStatus || code != c.wantCode {
			t.Errorf("mapError(%v) = (%d,%s), want (%d,%s)", c.err, status, code, c.wantStatus, c.wantCode)
		}
	}
}

func TestMapErrorDecryptIsGeneric(t *testing.T) {
	// Decryption failures must collapse to a generic internal error so nothing
	// distinguishes a wrong token, corrupt ciphertext, or missing key.
	wrapped := fmt.Errorf("secret /x v2: %w", domain.ErrDecryptFailed)
	status, code, msg := mapError(wrapped)
	if status != http.StatusInternalServerError || code != "internal" || msg != "internal error" {
		t.Fatalf("mapError = (%d,%s,%q)", status, code, msg)
	}
}

func TestMapErrorUnknownIsInternal(t *testing.T) {
	status, code, msg := mapError(fmt.Errorf("some storage explosion"))
	if status != 500 || code != "internal" || msg != "internal error" {
		t.Fatalf("mapError = (%d,%s,%q)", status, code, msg)
	}
}

func TestClientIP(t *testing.T) {
	t.Run("forwarded-for honored only when proxy trusted", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "198.51.100.9:44321"
		r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
		if got := clientIP(r, true); got != "203.0.113.7" {
			t.Fatalf("trusted clientIP = %q, want forwarded value", got)
		}
	})
	t.Run("forwarded-for ignored when proxy not trusted", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "198.51.100.9:44321"
		r.Header.Set("X-Forwarded-For", "203.0.113.7")
		if got := clientIP(r, false); got != "198.51.100.9" {
			t.Fatalf("untrusted clientIP = %q, want peer address (no XFF spoofing)", got)
		}
	})
	t.Run("remote addr host", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "198.51.100.9:44321"
		if got := clientIP(r, false); got != "198.51.100.9" {
			t.Fatalf("clientIP = %q", got)
		}
	})
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc123":  "abc123",
		"bearer abc123":  "abc123",
		"BEARER  spaced": "spaced",
		"Basic xyz":      "",
		"":               "",
	}
	for header, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		if got := bearerToken(r); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestHealthzReadyz(t *testing.T) {
	e := newTestEnv(t)
	w := e.do(http.MethodGet, "/healthz", nil, nil)
	mustStatus(t, w, http.StatusOK)
	if w.Body.String() != "ok" {
		t.Fatalf("healthz body = %q", w.Body.String())
	}
	w = e.do(http.MethodGet, "/readyz", nil, nil)
	mustStatus(t, w, http.StatusOK)

	// Not-ready service reports 503 on readyz.
	ne := newTestEnvWith(t, false)
	w = ne.do(http.MethodGet, "/readyz", nil, nil)
	mustStatus(t, w, http.StatusServiceUnavailable)
}
