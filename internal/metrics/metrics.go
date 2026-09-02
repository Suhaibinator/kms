// Package metrics is the Prometheus exporter for the parameter store. It owns
// a private registry, the /metrics handler, and every series the server
// publishes.
//
// A scrape is the one place operational data leaves the process without an
// authenticated caller behind it, so every label emitted here is a closed set
// defined in code (see labels.go): counts, latencies, and sizes, never a
// namespace, identity, key, client, instance, IP, or request ID. Values that
// arrive from a call site are mapped through the label helpers and collapse to
// a fallback when they are not in their set, so a mistake upstream costs one
// bucket rather than a disclosure.
//
// *Metrics satisfies core.Metrics. This package may import core for its label
// constants; core never imports this package, mirroring the hub seam.
package metrics

import (
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Suhaibinator/kms/internal/core"
)

// DefaultSampleInterval is how often RunSampler refreshes the sampled gauges
// when Options.SampleInterval is unset.
const DefaultSampleInterval = 60 * time.Second

// sampleTimeout bounds one sampler run so a wedged database read cannot stall
// the sampling loop for longer than a single interval.
const sampleTimeout = 10 * time.Second

// Reload results: the closed set of the result label on kms_reloads_total.
const (
	ReloadApplied  = "applied"
	ReloadRejected = "rejected"
)

// Options configures the exporter. The zero value is valid; New fills in the
// defaults.
type Options struct {
	// Version is the build version reported by kms_build_info. Empty is
	// reported as "unknown".
	Version string
	// SampleInterval is the cadence RunSampler refreshes the sampled gauges at.
	// Zero uses DefaultSampleInterval.
	SampleInterval time.Duration

	// now overrides the clock (tests). nil uses time.Now.
	now func() time.Time
}

// Metrics is the exporter: a private registry plus every series the server
// publishes. All methods are safe for concurrent use and non-blocking, because
// the core hooks sit on the authentication and authorization paths.
type Metrics struct {
	reg            *prometheus.Registry
	sampleInterval time.Duration
	now            func() time.Time

	// Build and posture.
	buildInfo  *prometheus.GaugeVec
	startTime  prometheus.Gauge
	ready      prometheus.Gauge
	tlsEnabled prometheus.Gauge
	adminCert  prometheus.Gauge
	reloads    *prometheus.CounterVec
	lastReload prometheus.Gauge

	// Security and audit signals (the core.Metrics seam).
	authFailures       *prometheus.CounterVec
	authzDenials       *prometheus.CounterVec
	authzMethodDenials *prometheus.CounterVec
	rateLimited        *prometheus.CounterVec
	auditEvents        *prometheus.CounterVec
	auditWriteFailures prometheus.Counter
	auditPruned        prometheus.Counter
	decryptFailures    prometheus.Counter
	releaseOutcomes    *prometheus.CounterVec

	// Transport.
	grpcRequests *prometheus.CounterVec
	grpcDuration *prometheus.HistogramVec
	grpcStreams  *prometheus.GaugeVec
	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	sseStreams   prometheus.Gauge

	// Sampled state (see sampler.go).
	changelogRows              prometheus.Gauge
	changelogLastRevision      prometheus.Gauge
	changelogOldestRevision    prometheus.Gauge
	identityCertsExpiringSoon  prometheus.Gauge
	secretVersionsExpiringSoon prometheus.Gauge
	kekGenerations             prometheus.Gauge
	kekActiveCreated           prometheus.Gauge
	adminCertsLacking          prometheus.Gauge
	adminCertsExpiringSoon     prometheus.Gauge
	dbFileBytes                *prometheus.GaugeVec
	opsLastSample              prometheus.Gauge
	opsSampleFailures          prometheus.Counter

	// watchCollectors are the scrape-time collectors installed by
	// SetWatchSource, retained so a second call can replace them.
	watchMu         sync.Mutex
	watchCollectors []prometheus.Collector
}

// New builds an exporter over a private registry. The registry carries the
// standard Go runtime and process collectors alongside the KMS series, so a
// single scrape covers both.
func New(opts Options) *Metrics {
	if opts.SampleInterval <= 0 {
		opts.SampleInterval = DefaultSampleInterval
	}
	if opts.now == nil {
		opts.now = func() time.Time { return time.Now().UTC() }
	}
	version := opts.Version
	if version == "" {
		version = ValueUnknown
	}

	m := &Metrics{
		reg:            prometheus.NewRegistry(),
		sampleInterval: opts.SampleInterval,
		now:            opts.now,

		buildInfo:  gaugeVec("kms_build_info", "Build information, always 1.", labelVersion, labelGoVersion),
		startTime:  gauge("kms_start_time_seconds", "Unix time the server started."),
		ready:      gauge("kms_ready", "Whether the server is ready to serve requests (1) or not (0)."),
		tlsEnabled: gauge("kms_tls_enabled", "Whether the listeners are configured with TLS (1) or not (0)."),
		adminCert: gauge("kms_admin_client_cert_required",
			"Whether admin identities must present a client certificate (1) or not (0)."),
		reloads:    counterVec("kms_reloads_total", "Configuration reloads by result.", labelResult),
		lastReload: gauge("kms_last_reload_timestamp_seconds", "Unix time of the last configuration reload attempt."),

		authFailures: counterVec("kms_auth_failures_total", "Rejected credentials by reason.", labelReason),
		authzDenials: counterVec("kms_authz_denials_total", "Policy denials by operation.", labelOperation),
		authzMethodDenials: counterVec("kms_authz_method_denials_total",
			"Requests refused by a namespace's auth-method gate, by the method the caller authenticated with.",
			labelMethod),
		rateLimited:        counterVec("kms_ratelimit_refusals_total", "Requests refused by a rate limiter.", labelLimiter),
		auditEvents:        counterVec("kms_audit_events_total", "Audit rows persisted.", labelEventType, labelDecision),
		auditWriteFailures: counter("kms_audit_write_failures_total", "Audit rows that could not be persisted."),
		auditPruned:        counter("kms_audit_pruned_total", "Audit rows removed by retention."),
		decryptFailures: counter("kms_secret_decrypt_failures_total",
			"Secret versions whose ciphertext could not be opened with the current keyring."),
		releaseOutcomes: counterVec("kms_release_outcomes_total",
			"Release activations and rollbacks by outcome.", labelOutcome),

		grpcRequests: counterVec("kms_grpc_requests_total", "Completed unary gRPC requests.",
			labelService, labelMethod, labelCode),
		grpcDuration: histogramVec("kms_grpc_request_duration_seconds", "Unary gRPC request latency.",
			labelService, labelMethod),
		grpcStreams: gaugeVec("kms_grpc_streams_active", "gRPC streams currently open.", labelService, labelMethod),
		httpRequests: counterVec("kms_http_requests_total", "Completed HTTP requests.",
			labelRoute, labelMethod, labelStatus),
		httpDuration: histogramVec("kms_http_request_duration_seconds", "HTTP request latency.",
			labelRoute, labelMethod),
		sseStreams: gauge("kms_http_sse_streams_active", "Server-sent-event streams currently open."),

		changelogRows:           gauge("kms_changelog_rows", "Rows currently retained in the change log."),
		changelogLastRevision:   gauge("kms_changelog_last_revision", "Highest revision assigned in the change log."),
		changelogOldestRevision: gauge("kms_changelog_oldest_revision", "Oldest revision still retained in the change log."),
		identityCertsExpiringSoon: gauge("kms_identity_certs_expiring_soon",
			"Unrevoked identity certificates expiring within the alerting window."),
		secretVersionsExpiringSoon: gauge("kms_secret_versions_expiring_soon",
			"Enabled secret versions expiring within the alerting window."),
		kekGenerations:   gauge("kms_kek_generations", "Key-encryption-key generations in the keyring."),
		kekActiveCreated: gauge("kms_kek_active_created_timestamp_seconds", "Unix time the active KEK was created."),
		adminCertsLacking: gauge("kms_admin_certs_lacking",
			"Admin identities without a usable client certificate."),
		adminCertsExpiringSoon: gauge("kms_admin_certs_expiring_soon",
			"Admin identities whose client certificate expires within the alerting window."),
		dbFileBytes:   gaugeVec("kms_db_file_bytes", "Size of each database file on disk.", labelFile),
		opsLastSample: gauge("kms_ops_last_sample_timestamp_seconds", "Unix time of the last successful operational sample."),
		opsSampleFailures: counter("kms_ops_sample_failures_total",
			"Operational sampler runs that failed; the sampled gauges are stale by that many runs."),
	}

	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),

		m.buildInfo, m.startTime, m.ready, m.tlsEnabled, m.adminCert, m.reloads, m.lastReload,

		m.authFailures, m.authzDenials, m.authzMethodDenials, m.rateLimited,
		m.auditEvents, m.auditWriteFailures, m.auditPruned, m.decryptFailures, m.releaseOutcomes,

		m.grpcRequests, m.grpcDuration, m.grpcStreams,
		m.httpRequests, m.httpDuration, m.sseStreams,

		m.changelogRows, m.changelogLastRevision, m.changelogOldestRevision,
		m.identityCertsExpiringSoon, m.secretVersionsExpiringSoon,
		m.kekGenerations, m.kekActiveCreated,
		m.adminCertsLacking, m.adminCertsExpiringSoon,
		m.dbFileBytes, m.opsLastSample, m.opsSampleFailures,
	)

	m.buildInfo.WithLabelValues(version, runtime.Version()).Set(1)
	m.initClosedSets()
	return m
}

// initClosedSets materialises every series whose label set is fully known up
// front at zero. Without it an alert written as increase(...[5m]) > 0 stays
// silent until the first event creates the series, so the very first refusal
// after a restart would be missed.
func (m *Metrics) initClosedSets() {
	for _, reason := range core.AuthFailureReasons {
		m.authFailures.WithLabelValues(reason)
	}
	for _, outcome := range core.ReleaseOutcomes {
		m.releaseOutcomes.WithLabelValues(outcome)
	}
	for _, result := range ReloadResults {
		m.reloads.WithLabelValues(result)
	}
	for _, limiter := range LimiterNames {
		m.rateLimited.WithLabelValues(limiter)
	}
	for _, method := range core.AuthMethods {
		m.authzMethodDenials.WithLabelValues(method)
	}
}

// Handler serves the exposition. Scrapes are bounded — at most two in flight
// and five seconds each — so a slow or duplicated Prometheus cannot pile work
// onto a server that is already struggling; a partially broken registry still
// serves the series that do gather.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		ErrorHandling:       promhttp.ContinueOnError,
		MaxRequestsInFlight: 2,
		Timeout:             5 * time.Second,
	})
}

// Gatherer exposes the registry for tests and for callers that render the
// exposition themselves (the healthcheck command).
func (m *Metrics) Gatherer() prometheus.Gatherer { return m.reg }

// SetStartTime records when the process started. A zero time reports 0.
func (m *Metrics) SetStartTime(t time.Time) { m.startTime.Set(unixSeconds(t)) }

// SetReady records whether the server is serving requests. The sampler
// refreshes it too, so a database that goes away is visible without a restart.
func (m *Metrics) SetReady(ready bool) { m.ready.Set(boolValue(ready)) }

// SetPosture records the transport-security posture the server came up with,
// so a dashboard can tell a deliberately plaintext deployment from one that
// lost its certificates.
func (m *Metrics) SetPosture(tlsEnabled, adminClientCertRequired bool) {
	m.tlsEnabled.Set(boolValue(tlsEnabled))
	m.adminCert.Set(boolValue(adminClientCertRequired))
}

// ReloadResult records a configuration reload attempt. result is ReloadApplied
// or ReloadRejected; the timestamp advances either way, because a reload that
// was rejected is still the last one that happened.
func (m *Metrics) ReloadResult(result string) {
	m.reloads.WithLabelValues(ReloadResultLabel(result)).Inc()
	m.lastReload.Set(unixSeconds(m.now()))
}

// --- collector constructors ------------------------------------------------

func counter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
}

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
}

func gauge(name, help string) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
}

func gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
}

func histogramVec(name, help string, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: name, Help: help, Buckets: prometheus.DefBuckets},
		labels,
	)
}

// --- value helpers ---------------------------------------------------------

// unixSeconds renders a timestamp as fractional Unix seconds, the convention
// every *_timestamp_seconds gauge follows. A zero time reports 0 rather than
// the year-1 epoch offset.
func unixSeconds(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixNano()) / float64(time.Second)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
