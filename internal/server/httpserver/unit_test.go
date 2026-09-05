package httpserver

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
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
		{fmt.Errorf("post-commit cleanup: %w", storage.ErrPurgeCleanupPending), 503, "purge_cleanup_pending"},
	}
	for _, c := range cases {
		status, code, _ := mapError(c.err)
		if status != c.wantStatus || code != c.wantCode {
			t.Errorf("mapError(%v) = (%d,%s), want (%d,%s)", c.err, status, code, c.wantStatus, c.wantCode)
		}
	}
}

func TestMapErrorPurgeCleanupPendingSaysPurgeCommitted(t *testing.T) {
	status, code, msg := mapError(fmt.Errorf("wrapped: %w", storage.ErrPurgeCleanupPending))
	if status != http.StatusServiceUnavailable || code != "purge_cleanup_pending" || msg != storage.ErrPurgeCleanupPending.Error() {
		t.Fatalf("mapError = (%d,%s,%q)", status, code, msg)
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

func TestWriteJSONDisablesCaching(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"value": "sensitive"})
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestWriteJSONUsesV2MinimalEscapingWithoutTrailingNewline(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"value": "<tag>&"})
	if got, want := w.Body.String(), `{"value":"<tag>&"}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestDecodeJSONUsesStrictSingleDocumentV2Semantics(t *testing.T) {
	type requestBody struct {
		Name string `json:"name"`
	}
	tests := map[string]struct {
		body    []byte
		want    string
		wantErr bool
	}{
		"empty":           {body: nil},
		"whitespace only": {body: []byte(" \n\t")},
		"exact case":      {body: []byte(`{"name":"ok"}`), want: "ok"},
		"wrong case":      {body: []byte(`{"Name":"ignored"}`), wantErr: true},
		"unknown member":  {body: []byte(`{"name":"ok","extra":true}`), wantErr: true},
		"duplicate":       {body: []byte(`{"name":"one","name":"two"}`), wantErr: true},
		"trailing value":  {body: []byte(`{"name":"one"} {}`), wantErr: true},
		"invalid UTF-8":   {body: []byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}'}, wantErr: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(test.body))
			w := httptest.NewRecorder()
			var got requestBody
			err := decodeJSON(w, r, &got)
			if (err != nil) != test.wantErr {
				t.Fatalf("decodeJSON error = %v, wantErr %t", err, test.wantErr)
			}
			if !test.wantErr && got.Name != test.want {
				t.Fatalf("Name = %q, want %q", got.Name, test.want)
			}
		})
	}
}

func BenchmarkHTTPDTOJSONV2(b *testing.B) {
	value := errorEnvelope{Error: errorBody{
		Code:    "invalid_argument",
		Message: "invalid JSON body",
		ValidationErrors: []releaseValidationErrorDTO{
			{Alias: "runtime", Code: "required", SchemaPointer: "/properties/limit", Message: "missing property"},
		},
	}}
	b.ReportAllocs()
	for b.Loop() {
		if err := json.MarshalWrite(io.Discard, value); err != nil {
			b.Fatal(err)
		}
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

func TestStatusRecorderUnwrapAndFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	if sr.Unwrap() != rec {
		t.Fatal("Unwrap must return the underlying writer")
	}
	// http.ResponseController reaches Flush through Unwrap.
	if err := http.NewResponseController(sr).Flush(); err != nil {
		t.Fatalf("ResponseController.Flush: %v", err)
	}
	if !rec.Flushed {
		t.Fatal("flush was not forwarded to the underlying writer")
	}
	// Flushing commits the (default) status: a later WriteHeader must not
	// rewrite the recorded status.
	sr.WriteHeader(http.StatusInternalServerError)
	if sr.status != http.StatusOK {
		t.Fatalf("status after flush+WriteHeader = %d, want 200", sr.status)
	}

	// Direct Flush on a writer without Flusher support is a no-op.
	plain := &statusRecorder{ResponseWriter: nopWriter{}, status: http.StatusOK}
	plain.Flush()
	if plain.wroteHeader {
		t.Fatal("flush on a non-flusher must not mark the header written")
	}
}

type nopWriter struct{}

func (nopWriter) Header() http.Header         { return http.Header{} }
func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) WriteHeader(int)             {}

func TestConsoleRoutesMethodNotAllowed(t *testing.T) {
	e := newTestEnv(t)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/applications/get"},
		{http.MethodPost, "/api/v1/applications/overview"},
		{http.MethodGet, "/api/v1/applications/ship"},
		{http.MethodGet, "/api/v1/applications/environments/clone"},
		{http.MethodGet, "/api/v1/releases/rollback"},
		{http.MethodPost, "/api/v1/release-subscribers/stream"},
		{http.MethodDelete, "/api/v1/applications/overview"},
	}
	for _, c := range cases {
		w := e.admin(c.method, c.path, nil)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, w.Code)
		}
	}
}
