package grpcserver

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/metrics"
)

// newMetricsEnv mirrors newTestEnv with an exporter attached, built through the
// real Config so the test exercises the same wiring serve uses.
func newMetricsEnv(t *testing.T) (*testEnv, *metrics.Metrics) {
	t.Helper()
	store := newMemStore()
	store.addIdentity("admin", domain.IdentityKindAdmin, adminToken, nil)
	store.addIdentity("client", domain.IdentityKindClient, clientToken, nil)

	svc, hub := buildService(t, store, true)
	m := metrics.New(metrics.Options{Version: "test-version"})
	srv, lis := serveBufconn(t, svc, hub, Config{Metrics: m})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &testEnv{store: store, svc: svc, hub: hub, srv: srv, conn: conn}, m
}

// seriesValue reads one exposition line by its full name-and-labels prefix.
// Labels are rendered in alphabetical order by name.
func seriesValue(t *testing.T, m *metrics.Metrics, series string) (float64, bool) {
	t.Helper()
	families, err := m.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			var labels []string
			for _, l := range metric.GetLabel() {
				labels = append(labels, l.GetName()+`="`+l.GetValue()+`"`)
			}
			name := family.GetName()
			if len(labels) > 0 {
				name += "{" + strings.Join(labels, ",") + "}"
			}
			if name != series {
				continue
			}
			switch {
			case metric.Counter != nil:
				return metric.GetCounter().GetValue(), true
			case metric.Gauge != nil:
				return metric.GetGauge().GetValue(), true
			case metric.Histogram != nil:
				return float64(metric.GetHistogram().GetSampleCount()), true
			}
		}
	}
	return 0, false
}

func mustSeries(t *testing.T, m *metrics.Metrics, series string, want float64) {
	t.Helper()
	got, ok := seriesValue(t, m, series)
	if !ok {
		t.Fatalf("series %s is absent from the registry", series)
	}
	if got != want {
		t.Errorf("%s = %v, want %v", series, got, want)
	}
}

// grpcSeries spells one kms_grpc_requests_total series for the KMS services.
func grpcSeries(service, method string, code codes.Code) string {
	return `kms_grpc_requests_total{code="` + code.String() + `",method="` + method +
		`",service="kms.v1.` + service + `"}`
}

// TestUnaryCallsAreObservedWithTheirStatusCode: the code label is the one the
// caller received, whether the call succeeded or was refused.
func TestUnaryCallsAreObservedWithTheirStatusCode(t *testing.T) {
	env, m := newMetricsEnv(t)

	if _, err := env.admin().Health(context.Background(), &kmsv1.HealthRequest{}); err != nil {
		t.Fatalf("health: %v", err)
	}
	// No credentials: refused by the interceptor before the handler runs, and
	// still counted under the method the caller asked for.
	_, err := env.admin().ListNamespaces(context.Background(), &kmsv1.ListNamespacesRequest{})
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("unauthenticated ListNamespaces code = %v, want Unauthenticated", got)
	}

	mustSeries(t, m, grpcSeries("AdminService", "Health", codes.OK), 1)
	mustSeries(t, m, grpcSeries("AdminService", "ListNamespaces", codes.Unauthenticated), 1)
	// The latency histogram is keyed by service and method only — a duration
	// bucketed by status code would fragment the series for no operational gain.
	mustSeries(t, m,
		`kms_grpc_request_duration_seconds{method="Health",service="kms.v1.AdminService"}`, 1)
}

// TestStreamGaugeTracksAnOpenStream: the gauge must be up while the stream is
// open and back down once the client goes away, or it drifts upward for the
// life of the process.
func TestStreamGaugeTracksAnOpenStream(t *testing.T) {
	env, m := newMetricsEnv(t)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"})

	ctx, cancel := context.WithCancel(adminCtx())
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName: "app-1",
		Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")},
	}); err != nil {
		t.Fatalf("send registration: %v", err)
	}
	// The snapshot proves the handler is running, so the gauge is up.
	recvMatching(t, stream, isSnapshot)

	const gauge = `kms_grpc_streams_active{method="Subscribe",service="kms.v1.WatchService"}`
	mustSeries(t, m, gauge, 1)

	cancel()
	waitForSeries(t, m, gauge, 0)
}

// TestPanickingHandlerIsObservedAsInternal: the recovery defer turns a panic
// into Internal for the caller, and the observation must agree with what the
// caller was told. It drives the interceptor directly because no registered
// handler panics on purpose.
func TestPanickingHandlerIsObservedAsInternal(t *testing.T) {
	env, m := newMetricsEnv(t)

	const method = "/kms.v1.AdminService/Health" // public: no credentials needed
	info := &grpc.UnaryServerInfo{FullMethod: method}
	resp, err := env.srv.unaryInterceptor(context.Background(), &kmsv1.HealthRequest{}, info,
		func(context.Context, any) (any, error) { panic("boom") })
	if resp != nil || status.Code(err) != codes.Internal {
		t.Fatalf("panicked handler = (%v, %v), want (nil, Internal)", resp, err)
	}
	mustSeries(t, m, grpcSeries("AdminService", "Health", codes.Internal), 1)

	// The stream gauge is released by the same unwind, so a panicking stream
	// handler does not leave it stuck at 1.
	streamInfo := &grpc.StreamServerInfo{FullMethod: method}
	err = env.srv.streamInterceptor(nil, fakeServerStream{ctx: context.Background()}, streamInfo,
		func(any, grpc.ServerStream) error { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Fatalf("panicked stream handler = %v, want Internal", err)
	}
	mustSeries(t, m, `kms_grpc_streams_active{method="Health",service="kms.v1.AdminService"}`, 0)
}

// TestUnregisteredMethodsAreBucketed: a call for a method the proto does not
// define must not mint a series named after whatever the caller sent.
func TestUnregisteredMethodsAreBucketed(t *testing.T) {
	env, m := newMetricsEnv(t)

	// The method is not in publicMethods and the call carries no credentials,
	// so it is refused — which is exactly the shape a scanner produces.
	info := &grpc.UnaryServerInfo{FullMethod: "/prod.payments.Service/db_password"}
	_, err := env.srv.unaryInterceptor(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return nil, nil })
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("unregistered method code = %v, want Unauthenticated", got)
	}
	mustSeries(t, m,
		`kms_grpc_requests_total{code="Unauthenticated",method="unknown",service="unknown"}`, 1)

	families, gerr := m.Gatherer().Gather()
	if gerr != nil {
		t.Fatalf("Gather: %v", gerr)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				for _, id := range []string{"prod", "payments", "db_password"} {
					if strings.Contains(label.GetValue(), id) {
						t.Errorf("%s: label %s=%q leaks %q",
							family.GetName(), label.GetName(), label.GetValue(), id)
					}
				}
			}
		}
	}
}

// fakeServerStream is the minimal ServerStream the stream interceptor needs
// when it is driven directly.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s fakeServerStream) Context() context.Context { return s.ctx }

// waitForSeries polls until series reaches want. A stream ends on the server's
// own schedule once the client goes away, so the gauge falls shortly after the
// context is cancelled rather than synchronously with it.
func waitForSeries(t *testing.T, m *metrics.Metrics, series string, want float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, ok := seriesValue(t, m, series)
		if ok && got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s = %s (present=%t), want %v",
				series, strconv.FormatFloat(got, 'f', -1, 64), ok, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
