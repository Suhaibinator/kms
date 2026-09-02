package metrics

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func fullSample() Sample {
	return Sample{
		ChangeLogRows:              120,
		ChangeLogLastRevision:      4096,
		ChangeLogOldestRevision:    3977,
		IdentityCertsExpiringSoon:  2,
		SecretVersionsExpiringSoon: 3,
		KEKGenerations:             4,
		KEKActiveCreated:           time.Unix(1650000000, 0).UTC(),
		AdminCertsLacking:          1,
		AdminCertsExpiringSoon:     5,
		DBFileBytes:                map[string]int64{DBFileMain: 65536, DBFileWAL: 4096},
		Ready:                      true,
	}
}

func TestSamplePublishesEveryGauge(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.Sample(t.Context(), func(context.Context) (Sample, error) { return fullSample(), nil })

	for name, tc := range map[string]struct {
		gauge prometheus.Collector
		want  float64
	}{
		"kms_changelog_rows":                       {m.changelogRows, 120},
		"kms_changelog_last_revision":              {m.changelogLastRevision, 4096},
		"kms_changelog_oldest_revision":            {m.changelogOldestRevision, 3977},
		"kms_identity_certs_expiring_soon":         {m.identityCertsExpiringSoon, 2},
		"kms_secret_versions_expiring_soon":        {m.secretVersionsExpiringSoon, 3},
		"kms_kek_generations":                      {m.kekGenerations, 4},
		"kms_kek_active_created_timestamp_seconds": {m.kekActiveCreated, 1650000000},
		"kms_admin_certs_lacking":                  {m.adminCertsLacking, 1},
		"kms_admin_certs_expiring_soon":            {m.adminCertsExpiringSoon, 5},
		"kms_ready":                                {m.ready, 1},
		"kms_ops_last_sample_timestamp_seconds":    {m.opsLastSample, 1700000000},
	} {
		if got := testutil.ToFloat64(tc.gauge); got != tc.want {
			t.Errorf("%s = %v, want %v", name, got, tc.want)
		}
	}
	for file, want := range map[string]float64{DBFileMain: 65536, DBFileWAL: 4096} {
		if got := testutil.ToFloat64(m.dbFileBytes.WithLabelValues(file)); got != want {
			t.Errorf("kms_db_file_bytes{file=%q} = %v, want %v", file, got, want)
		}
	}
	if got := testutil.ToFloat64(m.opsSampleFailures); got != 0 {
		t.Errorf("kms_ops_sample_failures_total = %v, want 0", got)
	}
}

// TestSampleKeepsUnknownFilesOut closes the file label: a sampler that reports
// an extra key must not create a series for it.
func TestSampleKeepsUnknownFilesOut(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.Sample(t.Context(), func(context.Context) (Sample, error) {
		return Sample{DBFileBytes: map[string]int64{DBFileMain: 1, "/srv/kms/prod.db": 2}}, nil
	})
	if got := testutil.CollectAndCount(m.dbFileBytes); got != 1 {
		t.Fatalf("kms_db_file_bytes series = %d, want 1", got)
	}
}

// TestSampleFailureLeavesGaugesAlone is the reason failures are counted rather
// than zeroed: a database hiccup must not read as "the change log emptied" or
// "the KEK vanished" on a dashboard.
func TestSampleFailureLeavesGaugesAlone(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.Sample(t.Context(), func(context.Context) (Sample, error) { return fullSample(), nil })

	before := gather(t, m)
	m.Sample(t.Context(), func(context.Context) (Sample, error) {
		return Sample{}, errors.New("database is locked")
	})

	if got := testutil.ToFloat64(m.opsSampleFailures); got != 1 {
		t.Errorf("kms_ops_sample_failures_total = %v, want 1", got)
	}
	for name, tc := range map[string]struct {
		gauge prometheus.Collector
		want  float64
	}{
		"kms_changelog_rows":                    {m.changelogRows, 120},
		"kms_kek_generations":                   {m.kekGenerations, 4},
		"kms_ready":                             {m.ready, 1},
		"kms_ops_last_sample_timestamp_seconds": {m.opsLastSample, 1700000000},
	} {
		if got := testutil.ToFloat64(tc.gauge); got != tc.want {
			t.Errorf("%s = %v after a failed sample, want %v (unchanged)", name, got, tc.want)
		}
	}
	if got := testutil.ToFloat64(m.dbFileBytes.WithLabelValues(DBFileMain)); got != 65536 {
		t.Errorf("kms_db_file_bytes{file=main} = %v after a failed sample, want 65536", got)
	}
	if before == "" {
		t.Fatal("expected a non-empty exposition before the failure")
	}
}

func TestSampleNilSampler(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.Sample(t.Context(), nil)
	m.RunSampler(t.Context(), nil)
	if got := testutil.ToFloat64(m.opsSampleFailures); got != 0 {
		t.Fatalf("a nil sampler must not count as a failure, got %v", got)
	}
}

// TestRunSamplerTicksAndStops covers the loop: it samples once up front so the
// first scrape after startup is useful, keeps ticking, and returns when the
// context is cancelled.
func TestRunSamplerTicksAndStops(t *testing.T) {
	t.Parallel()

	m := New(Options{SampleInterval: time.Millisecond, now: fixedClock(time.Unix(1700000000, 0).UTC())})
	ctx, cancel := context.WithCancel(t.Context())

	var runs atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.RunSampler(ctx, func(ctx context.Context) (Sample, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Errorf("sampler run should be bounded by a deadline")
			}
			runs.Add(1)
			return fullSample(), nil
		})
	}()

	for runs.Load() < 3 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSampler did not return after the context was cancelled")
	}
	if got := testutil.ToFloat64(m.changelogRows); got != 120 {
		t.Errorf("kms_changelog_rows = %v, want 120", got)
	}
}
