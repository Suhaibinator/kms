package metrics

import (
	"time"

	"google.golang.org/grpc/codes"
)

// ObserveGRPC records one completed unary gRPC call. Streaming RPCs are
// deliberately absent: their duration is a session length, not a latency, and
// would swamp the request histogram. GRPCStreamStarted/GRPCStreamEnded track
// those instead.
func (m *Metrics) ObserveGRPC(fullMethod string, code codes.Code, d time.Duration) {
	service, method := grpcLabels(fullMethod)
	m.grpcRequests.WithLabelValues(service, method, codeLabel(code)).Inc()
	m.grpcDuration.WithLabelValues(service, method).Observe(d.Seconds())
}

// GRPCStreamStarted records a gRPC stream opening.
func (m *Metrics) GRPCStreamStarted(fullMethod string) {
	service, method := grpcLabels(fullMethod)
	m.grpcStreams.WithLabelValues(service, method).Inc()
}

// GRPCStreamEnded records a gRPC stream closing. It must be paired with
// GRPCStreamStarted on every exit path, including panics and cancellations,
// or the gauge drifts upward for the life of the process.
func (m *Metrics) GRPCStreamEnded(fullMethod string) {
	service, method := grpcLabels(fullMethod)
	m.grpcStreams.WithLabelValues(service, method).Dec()
}

// ObserveHTTP records one completed HTTP request. route is the matched pattern
// (pass it through RouteLabel, or one of RouteStatic/RouteUnmatched) — never a
// request path, which may carry a namespace or a key name.
func (m *Metrics) ObserveHTTP(route, method string, status int, d time.Duration) {
	routeLabel := RouteLabel(route)
	methodLabel := httpMethodLabel(method)
	m.httpRequests.WithLabelValues(routeLabel, methodLabel, statusLabel(status)).Inc()
	m.httpDuration.WithLabelValues(routeLabel, methodLabel).Observe(d.Seconds())
}

// SSEStreamStarted records a server-sent-event stream opening.
func (m *Metrics) SSEStreamStarted() { m.sseStreams.Inc() }

// SSEStreamEnded records a server-sent-event stream closing.
func (m *Metrics) SSEStreamEnded() { m.sseStreams.Dec() }
