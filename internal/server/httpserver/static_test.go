package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>home</title>")},
		"about.html":           {Data: []byte("<!doctype html><title>about</title>")},
		"docs/index.html":      {Data: []byte("<!doctype html><title>docs</title>")},
		"_next/static/app.js":  {Data: []byte("console.log('hi')")},
		"_next/static/app.css": {Data: []byte("body{}")},
		"favicon.ico":          {Data: []byte("icon")},
	}
}

func getStatic(t *testing.T, h *staticHandler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	h.serve(w, req)
	return w
}

func TestStaticExactFile(t *testing.T) {
	h := newStaticHandler(testFS())

	w := getStatic(t, h, "/_next/static/app.js")
	mustStatus(t, w, http.StatusOK)
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("cache-control = %q, want immutable", cc)
	}
	if !strings.Contains(w.Body.String(), "console.log") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestStaticIndexAtRoot(t *testing.T) {
	h := newStaticHandler(testFS())
	w := getStatic(t, h, "/")
	mustStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "home") {
		t.Fatalf("body = %q", w.Body.String())
	}
	// HTML security headers.
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing X-Frame-Options")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("csp = %q", w.Header().Get("Content-Security-Policy"))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("cache-control = %q, want no-cache", cc)
	}
}

func TestStaticExtensionlessHTMLFallback(t *testing.T) {
	h := newStaticHandler(testFS())

	// /about -> about.html
	w := getStatic(t, h, "/about")
	mustStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "about") {
		t.Fatalf("body = %q", w.Body.String())
	}

	// /docs -> docs/index.html
	w = getStatic(t, h, "/docs")
	mustStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "docs") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestStaticSPADeepLink(t *testing.T) {
	h := newStaticHandler(testFS())
	// Unknown extensionless route falls back to the entry index.html (200).
	w := getStatic(t, h, "/secrets/prod/payments/stripe/api-key")
	mustStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "home") {
		t.Fatalf("expected SPA fallback to index, got %q", w.Body.String())
	}
}

func TestStaticMissingAssetIs404(t *testing.T) {
	h := newStaticHandler(testFS())
	// A missing file WITH an extension is a hard 404, not an SPA fallback.
	w := getStatic(t, h, "/missing-bundle.js")
	mustStatus(t, w, http.StatusNotFound)
}

func TestStaticPlaceholderWhenNoIndex(t *testing.T) {
	// A placeholder-only build (no index.html) serves an actionable 503.
	h := newStaticHandler(fstest.MapFS{})
	w := getStatic(t, h, "/")
	mustStatus(t, w, http.StatusServiceUnavailable)
	if !strings.Contains(w.Body.String(), "make frontend") {
		t.Fatalf("placeholder body = %q", w.Body.String())
	}
}

func TestServerServesFrontend(t *testing.T) {
	// Integration: New wired with a frontend FS routes non-API paths to static.
	store := newFakeStore()
	svc := newReadyService(t, store)
	srv, err := New(svc, Config{Addr: ":0", FrontendEnabled: true, Frontend: testFS(), Version: "v"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)
	mustStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), "home") {
		t.Fatalf("expected SPA index for deep link, got %q", w.Body.String())
	}
}
