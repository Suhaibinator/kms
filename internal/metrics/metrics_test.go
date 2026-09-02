package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fixedClock returns a deterministic clock for the timestamp gauges.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// newTestMetrics builds an exporter with a pinned version and clock.
func newTestMetrics(t *testing.T) *Metrics {
	t.Helper()
	return New(Options{
		Version:        "1.2.3",
		SampleInterval: time.Hour,
		now:            fixedClock(time.Unix(1700000000, 0).UTC()),
	})
}

// TestNewPassesPromlint is the guard on the exposition itself: names, units,
// help text, and counter suffixes all have to satisfy the Prometheus
// conventions, because a scrape target that violates them is silently
// mis-rendered by dashboards rather than rejected.
func TestNewPassesPromlint(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.SetWatchSource(func() WatchStats { return WatchStats{} })

	problems, err := testutil.GatherAndLint(m.Gatherer())
	if err != nil {
		t.Fatalf("GatherAndLint: %v", err)
	}
	for _, p := range problems {
		t.Errorf("promlint: %s: %s", p.Metric, p.Text)
	}
}

func TestBuildInfo(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	if got := testutil.ToFloat64(m.buildInfo); got != 1 {
		t.Fatalf("kms_build_info = %v, want 1", got)
	}
	body := gather(t, m)
	if !strings.Contains(body, `version="1.2.3"`) {
		t.Errorf("build info missing version label:\n%s", body)
	}
	if !strings.Contains(body, `go_version="go`) {
		t.Errorf("build info missing go_version label:\n%s", body)
	}
}

func TestBuildInfoUnsetVersion(t *testing.T) {
	t.Parallel()

	m := New(Options{})
	if !strings.Contains(gather(t, m), `version="unknown"`) {
		t.Errorf("empty Options.Version should report %q", ValueUnknown)
	}
}

func TestDefaultSampleInterval(t *testing.T) {
	t.Parallel()

	if got := New(Options{}).sampleInterval; got != DefaultSampleInterval {
		t.Fatalf("sampleInterval = %v, want %v", got, DefaultSampleInterval)
	}
	if got := New(Options{SampleInterval: time.Second}).sampleInterval; got != time.Second {
		t.Fatalf("sampleInterval = %v, want 1s", got)
	}
}

func TestStartTimeReadyAndPosture(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	// Every posture gauge starts at 0, so an absent SetPosture reads as the
	// weakest posture rather than as "no data".
	for name, g := range map[string]prometheus.Collector{
		"kms_ready":                             m.ready,
		"kms_tls_enabled":                       m.tlsEnabled,
		"kms_admin_client_cert_required":        m.adminCert,
		"kms_start_time_seconds":                m.startTime,
		"kms_last_reload_timestamp_seconds":     m.lastReload,
		"kms_ops_last_sample_timestamp_seconds": m.opsLastSample,
	} {
		if got := testutil.ToFloat64(g); got != 0 {
			t.Errorf("%s = %v, want 0 before it is set", name, got)
		}
	}

	m.SetStartTime(time.Unix(1600000000, 0).UTC())
	if got := testutil.ToFloat64(m.startTime); got != 1600000000 {
		t.Errorf("kms_start_time_seconds = %v", got)
	}
	m.SetStartTime(time.Time{})
	if got := testutil.ToFloat64(m.startTime); got != 0 {
		t.Errorf("zero start time = %v, want 0", got)
	}

	m.SetReady(true)
	if got := testutil.ToFloat64(m.ready); got != 1 {
		t.Errorf("kms_ready = %v, want 1", got)
	}
	m.SetReady(false)
	if got := testutil.ToFloat64(m.ready); got != 0 {
		t.Errorf("kms_ready = %v, want 0", got)
	}

	m.SetPosture(true, false)
	if got := testutil.ToFloat64(m.tlsEnabled); got != 1 {
		t.Errorf("kms_tls_enabled = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.adminCert); got != 0 {
		t.Errorf("kms_admin_client_cert_required = %v, want 0", got)
	}
}

func TestReloadResult(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.ReloadResult(ReloadApplied)
	m.ReloadResult(ReloadRejected)
	m.ReloadResult(ReloadRejected)
	// A result outside the closed set is bucketed, never passed through.
	m.ReloadResult("/etc/kms/config.yaml")

	for result, want := range map[string]float64{
		ReloadApplied:  1,
		ReloadRejected: 2,
		ValueOther:     1,
	} {
		if got := testutil.ToFloat64(m.reloads.WithLabelValues(result)); got != want {
			t.Errorf("kms_reloads_total{result=%q} = %v, want %v", result, got, want)
		}
	}
	// A rejected reload is still the last reload that happened.
	if got := testutil.ToFloat64(m.lastReload); got != 1700000000 {
		t.Errorf("kms_last_reload_timestamp_seconds = %v", got)
	}
}

func TestHandlerServesExposition(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE kms_build_info gauge",
		"kms_build_info{",
		"# HELP kms_auth_failures_total",
		"# TYPE go_goroutines gauge",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}

// gather renders the whole registry as text.
func gather(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	return rec.Body.String()
}
