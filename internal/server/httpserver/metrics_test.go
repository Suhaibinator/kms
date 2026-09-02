package httpserver

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Suhaibinator/kms/internal/metrics"
)

// newMetricsEnv is newTestEnvWith with an exporter attached, built through the
// real Config so the test exercises the same wiring serve uses.
func newMetricsEnv(t *testing.T, ready bool) (*testEnv, *metrics.Metrics) {
	t.Helper()
	env := newTestEnvWith(t, ready)
	m := metrics.New(metrics.Options{Version: "test-version"})
	srv, err := New(env.svc, Config{Addr: ":0", FrontendEnabled: false, Version: "test-version", Metrics: m})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env.handler = srv.Handler
	return env, m
}

// scrapeText renders the exporter's exposition directly, so the scrape itself
// is never counted among the requests a test is asserting about.
func scrapeText(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	return rec.Body.String()
}

// seriesValue reads one exposition line by its full name-and-labels prefix.
// Labels are rendered in alphabetical order by name, which is what the
// expected strings below spell out.
func seriesValue(t *testing.T, body, series string) (float64, bool) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || name != series {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return v, true
	}
	return 0, false
}

func mustSeries(t *testing.T, body, series string, want float64) {
	t.Helper()
	got, ok := seriesValue(t, body, series)
	if !ok {
		t.Fatalf("series %s is absent from the exposition:\n%s", series, body)
	}
	if got != want {
		t.Errorf("%s = %v, want %v", series, got, want)
	}
}

// TestMetricsEndpointIsUnauthenticatedAndUngated: a scrape carries no bearer
// token and must answer before the server is ready, or a restart looks like an
// outage of the monitoring itself.
func TestMetricsEndpointIsUnauthenticatedAndUngated(t *testing.T) {
	for _, ready := range []bool{true, false} {
		env, _ := newMetricsEnv(t, ready)
		w := env.do(http.MethodGet, "/metrics", nil, nil)
		mustStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), "kms_build_info{") {
			t.Errorf("ready=%t: exposition missing kms_build_info:\n%s", ready, w.Body.String())
		}
	}
}

// TestMetricsEndpointWithoutExporter: with metrics.enabled off there is no
// exporter, /metrics is not a special path, and the request falls through to
// the normal dispatch — a 404 from the API/frontend catch-all.
func TestMetricsEndpointWithoutExporter(t *testing.T) {
	env := newTestEnv(t)
	w := env.do(http.MethodGet, "/metrics", nil, nil)
	mustStatus(t, w, http.StatusNotFound)
}

// requestIdentifiers are the request-borne strings that must never reach a
// scrape: a namespace, a key name, a request ID, and a presented credential.
// "admin" is checked separately, by exact match on label values — it is a
// legitimate part of several metric names.
var requestIdentifiers = []string{"prod", "payments", "db_password", "req-secret-123", "not-a-real-token-9f3c"}

// TestMetricsCarryNoRequestIdentifiers is the security test for this
// transport: /metrics is served without a caller behind it, so nothing the
// request path can be handed may end up in a label.
func TestMetricsCarryNoRequestIdentifiers(t *testing.T) {
	env, m := newMetricsEnv(t, true)
	headers := map[string]string{
		"Authorization": "Bearer not-a-real-token-9f3c",
		"X-Request-ID":  "req-secret-123",
	}
	for _, target := range []string{
		"/api/v1/parameters?env=prod&app=payments&key=db_password",
		"/api/v1/secrets/metadata?env=prod&app=payments&key=db_password",
		"/api/v1/parameters/prod/payments/db_password",
		"/api/v1/whoami",
		"/prod/payments/db_password",
		"/healthz",
	} {
		env.do(http.MethodGet, target, nil, headers)
	}
	env.do(http.MethodPost, "/api/v1/auth/login",
		map[string]any{"name": "admin", "token": "not-a-real-token-9f3c"}, headers)
	env.admin(http.MethodGet, "/api/v1/whoami", nil)

	families, err := m.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	allowed := map[string]bool{}
	for _, name := range metrics.LabelNames {
		allowed[name] = true
	}
	for _, family := range families {
		// The label-name allowlist governs this exporter's series; the Go and
		// process collectors share the registry but their labels are fixed by
		// client_golang.
		isKMS := strings.HasPrefix(family.GetName(), "kms_")
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if isKMS && !allowed[label.GetName()] {
					t.Errorf("%s: label name %q is not in metrics.LabelNames", family.GetName(), label.GetName())
				}
				if label.GetValue() == "admin" {
					t.Errorf("%s: label %s carries the bare identity name", family.GetName(), label.GetName())
				}
			}
		}
	}

	body := scrapeText(t, m)
	for _, id := range requestIdentifiers {
		if strings.Contains(body, id) {
			t.Errorf("exposition contains %q:\n%s", id, body)
		}
	}
}

// TestHTTPRequestsAreLabelledByMatchedRoute: the route label is the mux
// pattern, not the request path — and a refusal that never reaches the mux is
// still attributed to the route the caller asked for.
func TestHTTPRequestsAreLabelledByMatchedRoute(t *testing.T) {
	env, m := newMetricsEnv(t, true)
	w := env.do(http.MethodGet, "/api/v1/whoami", nil,
		map[string]string{"Authorization": "Bearer not-a-real-token-9f3c"})
	mustStatus(t, w, http.StatusUnauthorized)

	if err := testutil.GatherAndCompare(m.Gatherer(), strings.NewReader(`
# HELP kms_http_requests_total Completed HTTP requests.
# TYPE kms_http_requests_total counter
kms_http_requests_total{method="GET",route="GET /api/v1/whoami",status="401"} 1
`), "kms_http_requests_total"); err != nil {
		t.Error(err)
	}
}

// TestUnmatchedPathsCollapseToOneLabel: an unrouted path is a scanner, a typo,
// or a probe for something that is not there. None of them may mint a series
// named after whatever they asked for.
func TestUnmatchedPathsCollapseToOneLabel(t *testing.T) {
	env, m := newMetricsEnv(t, true)
	for _, target := range []string{"/prod/payments/db_password", "/nope", "/also-not-here"} {
		mustStatus(t, env.do(http.MethodGet, target, nil, nil), http.StatusNotFound)
	}
	mustSeries(t, scrapeText(t, m), `kms_http_requests_total{method="GET",route="unmatched",status="404"}`, 3)
}

// TestProbeRoutesKeepTheirOwnLabels: the endpoints route dispatches by name
// are not in the API mux, so they need their own label mapping.
func TestProbeRoutesKeepTheirOwnLabels(t *testing.T) {
	env, m := newMetricsEnv(t, true)
	for _, target := range []string{"/healthz", "/readyz", "/api/v1/health", "/api/v1/ca", "/metrics"} {
		mustStatus(t, env.do(http.MethodGet, target, nil, nil), http.StatusOK)
	}
	body := scrapeText(t, m)
	for _, route := range []string{"/healthz", "/readyz", "/api/v1/health", "/api/v1/ca", "/metrics"} {
		mustSeries(t, body,
			`kms_http_requests_total{method="GET",route="`+route+`",status="200"}`, 1)
	}
}

// TestLoginAndAuthLimiterRefusalsAreCounted: the two HTTP-owned limiters
// report under their own names, so an operator can tell a login flood from a
// bad-credential flood.
func TestLoginAndAuthLimiterRefusalsAreCounted(t *testing.T) {
	env, m := newMetricsEnv(t, true)
	// The limiter allows a burst of 10 across both call sites.
	bad := map[string]string{"Authorization": "Bearer not-a-real-token-9f3c"}
	for range 12 {
		env.do(http.MethodGet, "/api/v1/whoami", nil, bad)
	}
	for range 3 {
		env.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"name": "x", "token": "y"}, nil)
	}

	body := scrapeText(t, m)
	authRefusals, _ := seriesValue(t, body, `kms_ratelimit_refusals_total{limiter="http_auth"}`)
	if authRefusals < 1 {
		t.Errorf("http_auth refusals = %v, want at least 1:\n%s", authRefusals, body)
	}
	loginRefusals, _ := seriesValue(t, body, `kms_ratelimit_refusals_total{limiter="http_login"}`)
	if loginRefusals < 1 {
		t.Errorf("http_login refusals = %v, want at least 1:\n%s", loginRefusals, body)
	}
}

// TestSSEStreamGaugeReturnsToZero: the active-stream gauge is only useful if
// it comes back down. It also pins the SSE limiter label on a refused stream.
func TestSSEStreamGaugeReturnsToZero(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	if shipped := e.ship("dev", "rate_limits", "7", false); shipped["status"] != "activated" {
		t.Fatalf("ship = %v", shipped)
	}
	m := metrics.New(metrics.Options{Version: "test-version"})
	s := newServer(e.svc, Config{Addr: ":0", Version: "test-version", Metrics: m})
	s.stream = streamLimits{debounce: 20 * time.Millisecond, requery: time.Hour,
		keepAlive: 30 * time.Millisecond, maxLifetime: time.Minute, perIdentity: 1, global: 64}
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	open := func(ctx context.Context) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			ts.URL+"/api/v1/release-subscribers/stream?env=dev&app=gradethis&name=runtime", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+e.adminToken)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := open(ctx)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	// Read the first frame so the handler is demonstrably inside the stream.
	if frame, err := readFrame(t, bufio.NewReader(resp.Body)); err != nil || frame.event != "snapshot" {
		t.Fatalf("first frame = %+v err=%v", frame, err)
	}
	mustSeries(t, scrapeText(t, m), "kms_http_sse_streams_active", 1)

	// A second stream for the same identity exceeds the per-identity cap and
	// is counted against that limiter, not the global one.
	second := open(ctx)
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second stream status = %d", second.StatusCode)
	}
	if err := second.Body.Close(); err != nil {
		t.Fatalf("close refused stream body: %v", err)
	}
	body := scrapeText(t, m)
	mustSeries(t, body, `kms_ratelimit_refusals_total{limiter="sse_identity"}`, 1)
	mustSeries(t, body, `kms_ratelimit_refusals_total{limiter="sse_global"}`, 0)
	mustSeries(t, body, "kms_http_sse_streams_active", 1)

	cancel()
	_ = resp.Body.Close()
	waitForSeries(t, m, "kms_http_sse_streams_active", 0)
}

// waitForSeries polls the exposition until series reaches want. A stream ends
// on the server's own schedule once the client goes away, so the gauge falls
// shortly after the request is cancelled rather than synchronously with it.
func waitForSeries(t *testing.T, m *metrics.Metrics, series string, want float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		body := scrapeText(t, m)
		got, ok := seriesValue(t, body, series)
		if ok && got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s = %v (present=%t), want %v:\n%s", series, got, ok, want, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// muxPattern matches one route registration in handlers.go. The mux exposes no
// way to list what was registered on it, so the source is the authority.
var muxPattern = regexp.MustCompile(`mux\.HandleFunc\("([^"]+)"`)

// TestEveryRegisteredRouteHasALabel is this transport's half of the route
// contract. The exporter cannot import the HTTP server (that would invert the
// dependency), so metrics.RouteLabels is a hand-maintained list; without this
// test a new mux pattern would be silently recorded as "unmatched" and its
// traffic would disappear into the catch-all bucket.
func TestEveryRegisteredRouteHasALabel(t *testing.T) {
	src, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("read handlers.go: %v", err)
	}
	matches := muxPattern.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 50 {
		t.Fatalf("found only %d mux registrations; the regexp has drifted from the source", len(matches))
	}

	labels := map[string]bool{}
	for _, l := range metrics.RouteLabels {
		labels[l] = true
	}
	registered := map[string]bool{}
	for _, m := range matches {
		registered[m[1]] = true
		if !labels[m[1]] {
			t.Errorf("mux pattern %q is missing from metrics.RouteLabels", m[1])
		}
	}
	for path := range exemptRoutes {
		registered[path] = true
		if !labels[path] {
			t.Errorf("exempt route %q is missing from metrics.RouteLabels", path)
		}
	}
	// And the other direction: a label for a route that no longer exists is a
	// stale entry that can never be emitted again.
	for _, l := range metrics.RouteLabels {
		if l == metrics.RouteStatic || l == metrics.RouteUnmatched {
			continue
		}
		if !registered[l] {
			t.Errorf("metrics.RouteLabels carries %q, which this server does not serve", l)
		}
	}
}
