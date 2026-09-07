// Package httpserver implements the JSON HTTP API and embedded-frontend
// serving described in docs/http-api.md. Handlers are thin adapters over
// internal/core: they parse requests, call one service method, and render the
// result or a mapped error. No business logic lives here.
package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/metrics"
	"github.com/Suhaibinator/kms/internal/ratelimit"
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
	// in rate limiting and audit records, and X-Forwarded-Host as the host a
	// browser's Origin header is compared against. Enable ONLY when the server
	// sits behind a trusted reverse proxy that sets them; otherwise a client
	// can spoof the headers to evade the login/auth throttle, forge audit
	// source IPs, or pass the same-origin check. Default false: the real TCP
	// peer address and the Host header are always used.
	TrustProxyHeaders bool
	// GRPCAddr is the advertised gRPC listen address, reported by the health
	// endpoint so the console can show SDK connection details. Empty when the
	// gRPC server is not wired.
	GRPCAddr string
	// TLSEnabled records that the caller starts this server's listener with
	// TLS. The health endpoint reports it (or a TLS request connection) as
	// tls_enabled so the console can warn about cleartext listeners.
	TLSEnabled bool
	// AdminClientCertRequired reports whether admin identities must present a
	// client certificate alongside their bearer token on this listener. It is
	// the *effective* value computed by serve (the configured setting AND TLS
	// being on), retained in the health API for compatibility. The login page
	// uses transport-only diagnostics instead. Enforcement lives in core.
	AdminClientCertRequired bool
	// MTLSEnabled records that the listener was configured with a client CA
	// (security.mtls_enabled), so the TLS stack demands and verifies a client
	// certificate on every connection. Reported by the posture endpoint, which
	// is admin-only; unlike AdminClientCertRequired it is never shown to an
	// unauthenticated caller.
	MTLSEnabled bool
	// AuditEnabled, AuditRetainDuration and AuditArchiveEnabled describe the
	// configured audit posture for the posture endpoint: whether decisions are
	// recorded at all, how long rows are kept (zero keeps them forever), and
	// whether retired rows are archived before deletion. They are reported,
	// never enforced here — retention runs in serve and recording in core.
	AuditEnabled        bool
	AuditRetainDuration time.Duration
	AuditArchiveEnabled bool
	// Metrics is the Prometheus exporter. When set, the server serves its
	// exposition at /metrics — unauthenticated, like the probes — and records
	// one observation per request, per SSE stream, and per rate-limit refusal.
	// nil disables all of that and leaves /metrics to the normal dispatch.
	Metrics *metrics.Metrics
}

// maxBodyBytes bounds request bodies. A 1 MiB secret value base64-encodes to
// ~1.37 MiB; the ceiling leaves headroom for JSON framing.
const maxBodyBytes = 4 << 20

type server struct {
	svc *core.Service
	cfg Config
	log *zap.Logger
	// loginLimiter is the per-IP budget for credential-verification failures:
	// a login attempt that carries a token, or a request to any other endpoint
	// that presents a credential which does not verify.
	loginLimiter *ratelimit.Limiter
	// credentiallessLimiter is the separate, per-IP admission class for
	// requests with nothing to verify — no bearer token and no client
	// certificate, or a login whose body is malformed or carries no token.
	// Keeping them apart means traffic that never touches a credential cannot
	// spend the budget that throttles guessing (see serveAPI).
	credentiallessLimiter *ratelimit.Limiter
	static                *staticHandler
	apiMux                *http.ServeMux
	stream                streamLimits
	streams               streamRegistry
}

// New builds the HTTP server. The returned *http.Server has its Handler and
// Addr set; the caller starts it with ListenAndServe / Serve (and Shutdown for
// graceful stop).
func New(svc *core.Service, cfg Config) (*http.Server, error) {
	s := newServer(svc, cfg)
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}, nil
}

// newServer wires the request pipeline without binding it to an *http.Server,
// so tests can tune the server (stream limits) before taking its handler.
func newServer(svc *core.Service, cfg Config) *server {
	s := &server{
		svc:                   svc,
		cfg:                   cfg,
		log:                   svc.Logger(),
		loginLimiter:          ratelimit.New(5, 10),
		credentiallessLimiter: ratelimit.New(30, 30),
		stream:                defaultStreamLimits(),
		streams:               newStreamRegistry(),
	}
	if cfg.FrontendEnabled && cfg.Frontend != nil {
		s.static = newStaticHandler(cfg.Frontend)
	}
	s.apiMux = s.newAPIMux()
	return s
}

// handler is the full middleware chain around the dispatcher.
func (s *server) handler() http.Handler { return s.observe(http.HandlerFunc(s.route)) }

// route is the top-level dispatcher: health/readiness probes, the metrics
// exposition, the JSON API, or the static frontend.
func (s *server) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		s.handleLiveness(w, r)
		return
	case "/readyz":
		s.handleReadiness(w, r)
		return
	case "/metrics":
		// Unauthenticated, exactly like the two probes above: a scrape carries
		// no credential, and the exposition is a closed set of counts (see
		// internal/metrics). With the exporter off the path is not special at
		// all and falls through to the API/frontend dispatch below.
		if s.cfg.Metrics != nil {
			s.cfg.Metrics.Handler().ServeHTTP(w, r)
			return
		}
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

// serveAPI applies the same-origin gate, the readiness gate, authentication,
// and rate limiting for the JSON API, then dispatches to the route handlers.
func (s *server) serveAPI(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, s.cfg.TrustProxyHeaders)
	requestID := requestIDFrom(r.Context())
	ctx := context.WithValue(r.Context(), metaKey, reqMeta{ip: ip, ua: r.UserAgent(), requestID: requestID})
	r = r.WithContext(ctx)

	// Exempt routes: no auth, no readiness gate, no admission budget — and so
	// no same-origin gate either. They answer any caller alike and CORS keeps
	// their responses unreadable to another site.
	switch r.URL.Path {
	case "/api/v1/auth/connection":
		s.handleConnection(w, r)
		return
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
	}

	// Everything below spends an admission budget, so it is same-origin only.
	// The console is served from this origin and machine clients send neither
	// Fetch Metadata nor Origin, so a request a browser marks as started by
	// another site is refused here, before the readiness gate and before any
	// budget is touched. Otherwise a page anywhere on the web could spend a
	// victim's per-IP budgets with credentialless requests whose responses it
	// can never read.
	if crossSiteRequest(r, s.cfg.TrustProxyHeaders) {
		writeErrorCode(w, http.StatusForbidden, "permission_denied", "cross-site request refused")
		return
	}

	if r.URL.Path == "/api/v1/auth/login" {
		// Method and content type are enforced before any budget is spent:
		// neither says anything about a credential, so a request that fails
		// them is not an attempt. handleLogin charges the admission class the
		// body turns out to belong to.
		if r.Method != http.MethodPost {
			writeErrorCode(w, http.StatusMethodNotAllowed, "invalid_argument", "method not allowed")
			return
		}
		if !hasJSONContentType(r) {
			writeErrorCode(w, http.StatusUnsupportedMediaType, "invalid_argument", "Content-Type must be application/json")
			return
		}
		s.apiMux.ServeHTTP(w, r)
		return
	}

	// Everything else requires readiness and authentication.
	if err := s.svc.Ready(r.Context()); err != nil {
		s.writeError(w, r, err)
		return
	}
	// Reserve from the request's admission class before doing credential
	// work, so an exhausted class avoids the authentication path entirely; a
	// successful authentication refunds the reservation, a failed one keeps
	// it. A request that presents a credential is charged to the
	// verification-failure budget shared with login. One that presents none
	// has nothing to verify and is charged to the separate credentialless
	// class, so a flood of requests with no usable credential can never drain
	// the budget that protects against guessing.
	limiter, label := s.loginLimiter, metrics.LimiterHTTPAuth
	if !credentialPresented(r) {
		limiter, label = s.credentiallessLimiter, metrics.LimiterHTTPCredentialless
	}
	if !limiter.Allow(ip) {
		s.rateLimited(label)
		writeErrorCode(w, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
		return
	}
	pr, err := s.authenticate(r, ip)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	limiter.Refund(ip)
	ctx = context.WithValue(r.Context(), principalKey, pr)
	s.apiMux.ServeHTTP(w, r.WithContext(ctx))
}

// credentialPresented reports whether r carries anything authenticate could
// verify: a bearer token or a chain-verified client certificate. A missing or
// malformed Authorization header ("Basic …", a bare "Bearer") counts as no
// credential, which is exactly the traffic the credentialless class exists to
// keep away from the verification budget.
func credentialPresented(r *http.Request) bool {
	return bearerToken(r) != "" || peerCertFromRequest(r) != nil
}

// authenticate resolves the request's credentials — the bearer token and any
// client certificate the TLS layer verified on this connection — to a
// Principal, attaching request context (remote addr, user agent, request id).
// Combination rules and the admin
// client-certificate requirement live in core, not here.
func (s *server) authenticate(r *http.Request, ip string) (core.Principal, error) {
	return s.svc.ResolvePrincipal(r.Context(), core.CredentialInput{
		Token:      bearerToken(r),
		PeerCert:   peerCertFromRequest(r),
		RemoteAddr: ip,
		UserAgent:  r.UserAgent(),
		RequestID:  requestIDFrom(r.Context()),
	})
}

// peerCertFromRequest returns the leaf client certificate presented on this
// connection, or nil. It insists on a verified chain rather than mere presence:
// under tls.VerifyClientCertIfGiven the TLS stack has already validated a
// presented certificate against the listener's client-CA pool, and requiring
// VerifiedChains keeps that guarantee if the listener ever moves to a mode that
// accepts a certificate without verifying it.
func peerCertFromRequest(r *http.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
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

// observe assigns a request id, records every request in the metrics exporter,
// and logs method, path, status, and duration for API and page requests. The
// two are separate concerns: static assets, probes, and scrapes are skipped by
// the log to keep it readable, but they are still counted — a probe that
// starts failing is exactly what a dashboard needs to show.
func (s *server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", requestID)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)

		if m := s.cfg.Metrics; m != nil {
			m.ObserveHTTP(s.routeLabel(r), r.Method, rec.status, elapsed)
		}
		if s.skipLog(r) {
			return
		}
		// The query string is intentionally omitted: it may carry resource
		// paths and must never grow to include anything sensitive in a log.
		s.log.Info("http request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", rec.status),
			zap.Int64("duration_ms", elapsed.Milliseconds()),
			zap.String("request_id", requestID),
		)
	})
}

// exemptRoutes are the paths route and serveAPI dispatch by name rather than
// through the API mux, so the mux cannot name them for the route label.
var exemptRoutes = map[string]bool{
	"/healthz":                true,
	"/readyz":                 true,
	"/metrics":                true,
	"/api/v1/health":          true,
	"/api/v1/auth/connection": true,
	"/api/v1/ca":              true,
}

// routeLabel resolves a request to its metrics route label: the path itself
// for the endpoints above, the mux's matched pattern for the JSON API, and one
// of the two catch-alls otherwise — RouteStatic for whatever the frontend
// serves (asset filenames are a build artifact, not an API surface) and
// RouteUnmatched for the rest, which includes a 404 and a 405, neither of
// which the mux attributes to a pattern. Everything goes through
// metrics.RouteLabel, which collapses an unknown value to unmatched, so a
// request path carrying a namespace or a key name can never become a series.
func (s *server) routeLabel(r *http.Request) string {
	p := r.URL.Path
	if exemptRoutes[p] {
		return metrics.RouteLabel(p)
	}
	if _, pattern := s.apiMux.Handler(r); pattern != "" {
		return metrics.RouteLabel(pattern)
	}
	if s.static != nil && !strings.HasPrefix(p, "/api/") {
		return metrics.RouteStatic
	}
	return metrics.RouteUnmatched
}

// rateLimited records a refusal by the named limiter when an exporter is
// attached. Core reports its own limiters directly; this covers the two the
// transport owns.
func (s *server) rateLimited(limiter string) {
	if m := s.cfg.Metrics; m != nil {
		m.RateLimited(limiter)
	}
}

// admitCredentialless charges one request to the credentialless admission
// class for ip. When the class is exhausted it writes the 429 itself and
// reports false.
func (s *server) admitCredentialless(w http.ResponseWriter, ip string) bool {
	if s.credentiallessLimiter.Allow(ip) {
		return true
	}
	s.rateLimited(metrics.LimiterHTTPCredentialless)
	writeErrorCode(w, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
	return false
}

// skipLog suppresses logging for static assets, health probes, and scrapes.
func (s *server) skipLog(r *http.Request) bool {
	p := r.URL.Path
	if p == "/healthz" || p == "/readyz" || p == "/metrics" {
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

// Unwrap exposes the underlying writer so http.ResponseController can reach
// optional interfaces (SetWriteDeadline, Flush) through the recorder.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush forwards to the underlying writer when it supports flushing, which
// streaming responses (server-sent events) rely on. Flushing commits the
// status, so it is recorded as written.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		r.wroteHeader = true
		f.Flush()
	}
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
			if before, _, ok := strings.Cut(xff, ","); ok {
				first = before
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

// crossSiteRequest reports whether the browser that sent r says the request
// was started by another site. Browsers attach Fetch Metadata to every
// request: Sec-Fetch-Site is "same-origin" for anything the console does and
// "none" for a URL the user typed or a bookmark, while "cross-site" and
// "same-site" mark a request some other page initiated — which this API never
// serves, so the header decides when present. Browsers without Fetch Metadata
// still send Origin on cross-origin and non-GET requests, so an Origin naming
// another host, or the opaque "null" of a sandboxed frame, is refused too.
// Requests carrying neither header (the CLI, the SDKs, curl, probes) are not
// browser requests and pass.
func crossSiteRequest(r *http.Request, trustProxy bool) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "same-origin", "none":
		return false
	case "cross-site", "same-site":
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true // "null", or not an origin at all
	}
	return !strings.EqualFold(u.Host, requestHost(r, trustProxy))
}

// requestHost is the host the client addressed, as the Origin comparison in
// crossSiteRequest needs it: the Host header, or the first X-Forwarded-Host
// when the operator has declared a trusted proxy (trustProxy). A proxy that
// rewrites Host to its upstream address would otherwise make every browser
// Origin look foreign.
func requestHost(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
			first := xfh
			if before, _, ok := strings.Cut(xfh, ","); ok {
				first = before
			}
			if v := strings.TrimSpace(first); v != "" {
				return v
			}
		}
	}
	return r.Host
}

// hasJSONContentType reports whether r declares a JSON body. The login route
// requires it so that a request which is not even shaped like a login is
// refused before any admission budget is charged.
func hasJSONContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
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

// decodeJSON reads and strictly decodes a JSON request body under the size
// limit. Unknown members are rejected so misspelled protection or CAS fields
// cannot silently become their unsafe zero values. A missing/empty body is
// treated as an empty object, leaving v at its zero value; per-handler field
// validation then reports any required-field errors.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body := &jsonBodyReader{Reader: r.Body}
	if err := json.UnmarshalRead(body, v, json.RejectUnknownMembers(true)); err != nil {
		if !body.sawNonWhitespace && errors.Is(err, io.ErrUnexpectedEOF) {
			return nil // empty body: leave v zero-valued
		}
		return invalidArg("invalid JSON body")
	}
	return nil
}

// jsonBodyReader distinguishes an absent JSON document from a truncated one.
// UnmarshalRead reports both as unexpected EOF, while this API intentionally
// continues to accept empty or whitespace-only request bodies.
type jsonBodyReader struct {
	io.Reader
	sawNonWhitespace bool
}

func (r *jsonBodyReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	for _, b := range p[:n] {
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			r.sawNonWhitespace = true
			break
		}
	}
	return n, err
}
