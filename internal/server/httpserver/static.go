package httpserver

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// staticHandler serves the embedded Next.js static export with SPA deep-link
// support. Routing rules (plan 6.3 / 17.4):
//
//   - exact file match  -> serve with content-type and cache headers
//   - extensionless path -> try <path>/index.html, then <path>.html, then fall
//     back to the entry index.html so client-side routing resolves the page
//   - missing file WITH an extension -> 404
//
// When the entry index.html is absent (a placeholder-only build), it serves an
// actionable 503 page rather than a broken UI.
type staticHandler struct {
	fsys fs.FS
}

func newStaticHandler(fsys fs.FS) *staticHandler {
	return &staticHandler{fsys: fsys}
}

func (h *staticHandler) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clean := path.Clean(r.URL.Path)
	if clean == "/" || clean == "." {
		h.serveEntry(w, r)
		return
	}
	name := strings.TrimPrefix(clean, "/")

	if data, ok := h.read(name); ok {
		h.serveFile(w, r, clean, name, data)
		return
	}

	if path.Ext(clean) == "" {
		if data, ok := h.read(name + "/index.html"); ok {
			h.serveFile(w, r, clean, name+"/index.html", data)
			return
		}
		if data, ok := h.read(name + ".html"); ok {
			h.serveFile(w, r, clean, name+".html", data)
			return
		}
		// Unknown route: let the client-side router resolve it from the URL.
		h.serveEntry(w, r)
		return
	}

	http.NotFound(w, r)
}

// serveEntry serves the SPA entry document, or the "not built" placeholder when
// it is missing.
func (h *staticHandler) serveEntry(w http.ResponseWriter, r *http.Request) {
	data, ok := h.read("index.html")
	if !ok {
		h.servePlaceholder(w)
		return
	}
	h.serveFile(w, r, r.URL.Path, "index.html", data)
}

// serveFile writes data with content-type (derived from name's extension),
// cache headers, and security headers for HTML documents.
func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, cleanPath, name string, data []byte) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	isHTML := strings.HasSuffix(name, ".html")

	switch {
	case strings.HasPrefix(cleanPath, "/_next/"):
		// Next.js content-hashes these asset filenames, so they are immutable.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case isHTML:
		w.Header().Set("Cache-Control", "no-cache")
	}
	if isHTML {
		setHTMLSecurityHeaders(w)
	}
	// Zero modtime: content-hashed assets are immutable and HTML is no-cache,
	// so a Last-Modified header would add nothing.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (h *staticHandler) servePlaceholder(w http.ResponseWriter) {
	setHTMLSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(placeholderHTML))
}

func (h *staticHandler) read(name string) ([]byte, bool) {
	if name == "" || !fs.ValidPath(name) {
		return nil, false
	}
	data, err := fs.ReadFile(h.fsys, name)
	if err != nil {
		return nil, false
	}
	return data, true
}

// setHTMLSecurityHeaders applies the response headers for HTML documents. The
// CSP is compatible with a Next.js static export (inline hydration script/style
// allowed) and permits no external origins.
func setHTMLSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"font-src 'self' data:; "+
			"connect-src 'self'; "+
			"base-uri 'self'; "+
			"form-action 'self'; "+
			"frame-ancestors 'none'")
}

const placeholderHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Parameter Store — frontend not built</title>
<style>
body{font-family:system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem;line-height:1.5;color:#111}
code{background:#f2f2f2;padding:.15rem .35rem;border-radius:.25rem}
</style>
</head>
<body>
<h1>Frontend not built</h1>
<p>The API is running, but the embedded web UI has not been built into this
binary. Build it and recompile:</p>
<pre><code>make frontend
make backend</code></pre>
<p>The JSON API under <code>/api/v1</code> and the <code>/healthz</code> /
<code>/readyz</code> probes are available regardless.</p>
</body>
</html>
`
