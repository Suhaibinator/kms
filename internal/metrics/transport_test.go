package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
)

func TestSplitFullMethod(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		full        string
		wantService string
		wantMethod  string
	}{
		{"unary", "/kms.v1.KMSService/GetSecret", "kms.v1.KMSService", "GetSecret"},
		{"no leading slash", "kms.v1.AdminService/WhoAmI", "kms.v1.AdminService", "WhoAmI"},
		{"empty method", "/kms.v1.AdminService/", "kms.v1.AdminService", ""},
		{"no separator", "/GetSecret", "", ""},
		{"empty", "", "", ""},
		{"slash only", "/", "", ""},
		{"nested path", "/a.B/C/D", "a.B", "C/D"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, method := splitFullMethod(tc.full)
			if service != tc.wantService || method != tc.wantMethod {
				t.Fatalf("splitFullMethod(%q) = (%q, %q), want (%q, %q)",
					tc.full, service, method, tc.wantService, tc.wantMethod)
			}
		})
	}
}

// TestGRPCLabelsAreDerivedFromDescriptors keeps the service and method label
// sets tied to the proto: they are read from the generated file descriptor, so
// a renamed RPC changes the labels instead of silently emitting a stale name.
func TestGRPCLabelsAreDerivedFromDescriptors(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"kms.v1.ParameterService", "kms.v1.SecretService", "kms.v1.WatchService",
		"kms.v1.ConfigurationReleaseService", "kms.v1.ConfigurationSchemaService", "kms.v1.AdminService",
	} {
		if _, ok := grpcServices[want]; !ok {
			t.Errorf("service %s missing from the descriptor-derived set", want)
		}
	}

	for _, tc := range []struct {
		name        string
		full        string
		wantService string
		wantMethod  string
	}{
		{"registered", "/kms.v1.SecretService/GetSecret", "kms.v1.SecretService", "GetSecret"},
		{"streaming", "/kms.v1.WatchService/Subscribe", "kms.v1.WatchService", "Subscribe"},
		{"unknown method", "/kms.v1.SecretService/ExfiltrateEverything", "kms.v1.SecretService", ValueUnknown},
		{"unknown service", "/evil.v1.Service/Method", ValueUnknown, ValueUnknown},
		{"malformed", "not-a-method", ValueUnknown, ValueUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, method := grpcLabels(tc.full)
			if service != tc.wantService || method != tc.wantMethod {
				t.Fatalf("grpcLabels(%q) = (%q, %q), want (%q, %q)",
					tc.full, service, method, tc.wantService, tc.wantMethod)
			}
		})
	}
}

func TestObserveGRPC(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.ObserveGRPC("/kms.v1.SecretService/GetSecret", codes.OK, 20*time.Millisecond)
	m.ObserveGRPC("/kms.v1.SecretService/GetSecret", codes.PermissionDenied, 5*time.Millisecond)
	m.ObserveGRPC("/kms.v1.SecretService/GetSecret", codes.Code(99), time.Millisecond)

	for code, want := range map[string]float64{
		"OK":               1,
		"PermissionDenied": 1,
		"Unknown":          1,
	} {
		got := testutil.ToFloat64(m.grpcRequests.WithLabelValues("kms.v1.SecretService", "GetSecret", code))
		if got != want {
			t.Errorf("kms_grpc_requests_total{code=%q} = %v, want %v", code, got, want)
		}
	}
	if got := testutil.CollectAndCount(m.grpcDuration); got != 1 {
		t.Errorf("duration series = %d, want 1 (service+method only)", got)
	}
	if !strings.Contains(gather(t, m), `kms_grpc_request_duration_seconds_count{method="GetSecret",service="kms.v1.SecretService"} 3`) {
		t.Errorf("all three calls should share one duration series")
	}
}

func TestGRPCStreams(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	subscribe := "/kms.v1.WatchService/Subscribe"
	m.GRPCStreamStarted(subscribe)
	m.GRPCStreamStarted(subscribe)
	m.GRPCStreamEnded(subscribe)

	got := testutil.ToFloat64(m.grpcStreams.WithLabelValues("kms.v1.WatchService", "Subscribe"))
	if got != 1 {
		t.Fatalf("kms_grpc_streams_active = %v, want 1", got)
	}
}

func TestObserveHTTP(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.ObserveHTTP("GET /api/v1/secrets", "GET", 200, 3*time.Millisecond)
	m.ObserveHTTP("GET /api/v1/secrets", "GET", 500, time.Millisecond)
	// A raw path with a namespace and key in it must never become a label.
	m.ObserveHTTP("/api/v1/parameters/prod/payments/db_password", "GET", 404, time.Millisecond)
	// A non-standard verb and an impossible status are both bucketed.
	m.ObserveHTTP("/healthz", "PROPFIND", 700, time.Millisecond)

	for _, tc := range []struct {
		route, method, status string
		want                  float64
	}{
		{"GET /api/v1/secrets", "GET", "200", 1},
		{"GET /api/v1/secrets", "GET", "500", 1},
		{RouteUnmatched, "GET", "404", 1},
		{"/healthz", ValueOther, ValueOther, 1},
	} {
		got := testutil.ToFloat64(m.httpRequests.WithLabelValues(tc.route, tc.method, tc.status))
		if got != tc.want {
			t.Errorf("kms_http_requests_total{route=%q,method=%q,status=%q} = %v, want %v",
				tc.route, tc.method, tc.status, got, tc.want)
		}
	}
	// The duration histogram is keyed on route and method only, so both
	// statuses land in one series.
	want := `kms_http_request_duration_seconds_count{method="GET",route="GET /api/v1/secrets"} 2`
	if body := gather(t, m); !strings.Contains(body, want) {
		t.Errorf("exposition missing %q", want)
	}
}

func TestSSEStreams(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.SSEStreamStarted()
	m.SSEStreamStarted()
	m.SSEStreamStarted()
	m.SSEStreamEnded()

	if got := testutil.ToFloat64(m.sseStreams); got != 2 {
		t.Fatalf("kms_http_sse_streams_active = %v, want 2", got)
	}
}

// TestRouteLabel pins the route allowlist. Anything not registered — a raw
// path, a probe that does not exist, a traversal attempt — collapses to
// RouteUnmatched, so route cardinality is bounded by this package, not by
// whatever a client asks for.
func TestRouteLabel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"GET /api/v1/secrets", "GET /api/v1/secrets"},
		{"POST /api/v1/auth/login", "POST /api/v1/auth/login"},
		{"/healthz", "/healthz"},
		{"/readyz", "/readyz"},
		{"/metrics", "/metrics"},
		{"/api/v1/health", "/api/v1/health"},
		{"/api/v1/ca", "/api/v1/ca"},
		{RouteStatic, RouteStatic},
		{RouteUnmatched, RouteUnmatched},
		{"/api/v1/secrets", RouteUnmatched}, // patterns carry their method
		{"GET /api/v1/secrets/prod/payments", RouteUnmatched},
		{"", RouteUnmatched},
	} {
		if got := RouteLabel(tc.in); got != tc.want {
			t.Errorf("RouteLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRouteLabelsAreUniqueAndMethodQualified(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, route := range RouteLabels {
		if seen[route] {
			t.Errorf("duplicate route label %q", route)
		}
		seen[route] = true
		if strings.HasPrefix(route, "/api/v1/") {
			// Only the auth-exempt endpoints the server dispatches by path are
			// registered without a method.
			switch route {
			case "/api/v1/health", "/api/v1/ca":
			default:
				t.Errorf("API route %q should be method-qualified", route)
			}
		}
	}
}

func TestStatusAndMethodLabels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   int
		want string
	}{
		{200, "200"}, {404, "404"}, {500, "500"}, {100, "100"}, {599, "599"},
		{0, ValueOther}, {99, ValueOther}, {600, ValueOther}, {-1, ValueOther},
	} {
		if got := statusLabel(tc.in); got != tc.want {
			t.Errorf("statusLabel(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"GET", "GET"}, {"DELETE", "DELETE"}, {"PATCH", "PATCH"},
		{"get", ValueOther}, {"PROPFIND", ValueOther}, {"", ValueOther},
	} {
		if got := httpMethodLabel(tc.in); got != tc.want {
			t.Errorf("httpMethodLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   codes.Code
		want string
	}{
		{codes.OK, "OK"},
		{codes.Unauthenticated, "Unauthenticated"},
		{codes.Code(17), "Unknown"},
		{codes.Code(4294967295), "Unknown"},
	} {
		if got := codeLabel(tc.in); got != tc.want {
			t.Errorf("codeLabel(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
