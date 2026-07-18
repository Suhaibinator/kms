// Package httpserver implements the JSON HTTP API and embedded-frontend
// serving described in docs/http-api.md. Handlers are thin adapters over
// internal/core: they parse requests, call one service method, and render the
// result or a mapped error. No business logic lives here.
package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// Config configures the HTTP server.
type Config struct {
	// Addr is the listen address (host:port). It is stored on the returned
	// *http.Server; the caller owns starting the listener (plain or TLS).
	Addr string
	// FrontendEnabled toggles static frontend serving. When false, non-API
	// routes return 404.
	FrontendEnabled bool
	// Frontend is the web root (typically fs.Sub of the embedded export at
	// frontend/out). May be nil when FrontendEnabled is false.
	Frontend fs.FS
	// Version is reported by the health endpoint and available to the UI.
	Version string
	// TrustProxyHeaders enables honoring X-Forwarded-For for the client IP used
	// in rate limiting and audit records. Enable ONLY when the server sits
	// behind a trusted reverse proxy that sets it; otherwise a client can spoof
	// the header to evade the login/auth throttle and forge audit source IPs.
	// Default false: the real TCP peer address is always used.
	TrustProxyHeaders bool
}

// maxBodyBytes bounds request bodies. A 1 MiB secret value base64-encodes to
// ~1.37 MiB; the ceiling leaves headroom for JSON framing.
const maxBodyBytes = 4 << 20

type server struct {
	svc          *core.Service
	cfg          Config
	log          *zap.Logger
	loginLimiter *rateLimiter
	static       *staticHandler
	apiMux       *http.ServeMux
}

// New builds the HTTP server. The returned *http.Server has its Handler and
// Addr set; the caller starts it with ListenAndServe / Serve (and Shutdown for
// graceful stop).
func New(svc *core.Service, cfg Config) (*http.Server, error) {
	s := &server{
		svc:          svc,
		cfg:          cfg,
		log:          svc.Logger(),
		loginLimiter: newRateLimiter(5, 10),
	}
	if cfg.FrontendEnabled && cfg.Frontend != nil {
		s.static = newStaticHandler(cfg.Frontend)
	}
	s.apiMux = s.newAPIMux()

	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.logging(http.HandlerFunc(s.route)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}, nil
}

// route is the top-level dispatcher: health/readiness probes, the JSON API, or
// the static frontend.
func (s *server) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		s.handleLiveness(w, r)
		return
	case "/readyz":
		s.handleReadiness(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.serveAPI(w, r)
		return
	}
	if s.static != nil {
		s.static.serve(w, r)
		return
	}
	http.NotFound(w, r)
}

// serveAPI applies the readiness gate, authentication, and rate limiting for
// the JSON API, then dispatches to the route handlers.
func (s *server) serveAPI(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, s.cfg.TrustProxyHeaders)
	requestID := requestIDFrom(r.Context())
	ctx := context.WithValue(r.Context(), metaKey, reqMeta{ip: ip, ua: r.UserAgent(), requestID: requestID})
	r = r.WithContext(ctx)

	// Exempt routes: no auth, no readiness gate.
	switch r.URL.Path {
	case "/api/v1/health":
		s.handleHealth(w, r)
		return
	case "/api/v1/ca":
		if r.Method != http.MethodGet {
			writeErrorCode(w, http.StatusMethodNotAllowed, "invalid_argument", "method not allowed")
			return
		}
		s.handleCA(w, r)
		return
	case "/api/v1/auth/login":
		if !s.loginLimiter.allow(ip) {
			writeErrorCode(w, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
			return
		}
		s.apiMux.ServeHTTP(w, r) // enforces POST + handles login
		return
	}

	// Everything else requires readiness and authentication.
	if err := s.svc.Ready(r.Context()); err != nil {
		s.writeError(w, r, err)
		return
	}
	// Reserve from the failed-auth bucket before doing credential work. A
	// successful authentication refunds the reservation; a failed one keeps it.
	// This makes throttling effective even while the bucket is exhausted.
	if !s.loginLimiter.allow(ip) {
		writeErrorCode(w, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
		return
	}
	pr, err := s.authenticate(r, ip)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.loginLimiter.refund(ip)
	ctx = context.WithValue(r.Context(), principalKey, pr)
	s.apiMux.ServeHTTP(w, r.WithContext(ctx))
}

// authenticate resolves the bearer token to a Principal, attaching request
// context (remote addr, user agent, request id, per-secret token header).
func (s *server) authenticate(r *http.Request, ip string) (core.Principal, error) {
	token := bearerToken(r)
	id, err := s.svc.Authenticate(r.Context(), token, ip, r.UserAgent())
	if err != nil {
		return core.Principal{}, err
	}
	return core.Principal{
		Identity: id,
		// HTTP callers authenticate with a bearer token by definition. The
		// per-namespace method gate lives in core; admin-kind identities bypass it.
		Method:      domain.AuthMethodToken,
		Token:       token,
		SecretToken: r.Header.Get("X-KMS-Secret-Token"),
		RemoteAddr:  ip,
		UserAgent:   r.UserAgent(),
		RequestID:   requestIDFrom(r.Context()),
	}, nil
}

// --- request context -------------------------------------------------------

type ctxKey int

const (
	requestIDKey ctxKey = iota
	principalKey
	metaKey
)

type reqMeta struct {
	ip        string
	ua        string
	requestID string
}

func principalFrom(ctx context.Context) core.Principal {
	pr, _ := ctx.Value(principalKey).(core.Principal)
	return pr
}

func metaFrom(ctx context.Context) reqMeta {
	m, _ := ctx.Value(metaKey).(reqMeta)
	return m
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// --- middleware ------------------------------------------------------------

// logging assigns a request id and records method, path, status, and duration
// for API and page requests. Static assets are skipped to keep logs readable.
func (s *server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", requestID)

		if s.skipLog(r) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// The query string is intentionally omitted: it may carry resource
		// paths and must never grow to include anything sensitive in a log.
		s.log.Info("http request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", rec.status),
			zap.Int64("duration_ms", time.Since(start).Milliseconds()),
			zap.String("request_id", requestID),
		)
	})
}

// skipLog suppresses logging for static assets and health probes.
func (s *server) skipLog(r *http.Request) bool {
	p := r.URL.Path
	if p == "/healthz" || p == "/readyz" {
		return true
	}
	if strings.HasPrefix(p, "/_next/") {
		return true
	}
	if strings.HasPrefix(p, "/api/") {
		return false
	}
	// Non-API request with a file extension is a static asset.
	return strings.Contains(lastSegment(p), ".")
}

func lastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	return r.ResponseWriter.Write(b)
}

// --- helpers ---------------------------------------------------------------

// clientIP returns the address used for rate-limit keying and audit source IP.
// It uses the real TCP peer by default; X-Forwarded-For is honored only when
// the operator has declared the deployment sits behind a trusted proxy
// (trustProxy), because otherwise the header is attacker-controlled and would
// let a client evade the auth throttle and forge audit IPs.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := xff
			if i := strings.IndexByte(xff, ','); i >= 0 {
				first = xff[:i]
			}
			if v := strings.TrimSpace(first); v != "" {
				return v
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) >= len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "r-0"
	}
	return "r-" + hex.EncodeToString(b[:])
}

// decodeJSON reads and decodes a JSON request body under the size limit. A
// missing/empty body is treated as an empty object, leaving v at its zero
// value; per-handler field validation then reports any required-field errors.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty body: leave v zero-valued
		}
		return invalidArg("invalid JSON body")
	}
	return nil
}
