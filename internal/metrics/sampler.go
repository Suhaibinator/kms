package metrics

import (
	"context"
	"time"
)

// Sample is one reading of the state the exporter cannot observe from the
// request path: sizes, backlogs, and expiry counts that have to be queried.
// Every field is a plain number, so nothing that identifies a namespace, an
// identity, or a key can reach a gauge through it.
type Sample struct {
	ChangeLogRows           int64
	ChangeLogLastRevision   int64
	ChangeLogOldestRevision int64

	IdentityCertsExpiringSoon  int64
	SecretVersionsExpiringSoon int64

	KEKGenerations   int64
	KEKActiveCreated time.Time

	AdminCertsLacking      int64
	AdminCertsExpiringSoon int64

	// DBFileBytes is keyed by DBFileMain and DBFileWAL; any other key is
	// ignored, keeping the file label closed.
	DBFileBytes map[string]int64

	Ready bool
}

// Sampler produces one Sample. It is supplied by the wiring layer, which owns
// the store and the readiness check; the exporter only calls it.
type Sampler func(ctx context.Context) (Sample, error)

// Sample runs s once and publishes the result.
//
// A failed run bumps kms_ops_sample_failures_total and leaves every gauge
// exactly as it was: a transient database error must not read as "the change
// log emptied" on a dashboard. kms_ops_last_sample_timestamp_seconds is the
// companion signal — an alert on its age catches a sampler that keeps failing.
func (m *Metrics) Sample(ctx context.Context, s Sampler) {
	if s == nil {
		return
	}
	sample, err := s(ctx)
	if err != nil {
		m.opsSampleFailures.Inc()
		return
	}

	m.changelogRows.Set(float64(sample.ChangeLogRows))
	m.changelogLastRevision.Set(float64(sample.ChangeLogLastRevision))
	m.changelogOldestRevision.Set(float64(sample.ChangeLogOldestRevision))
	m.identityCertsExpiringSoon.Set(float64(sample.IdentityCertsExpiringSoon))
	m.secretVersionsExpiringSoon.Set(float64(sample.SecretVersionsExpiringSoon))
	m.kekGenerations.Set(float64(sample.KEKGenerations))
	m.kekActiveCreated.Set(unixSeconds(sample.KEKActiveCreated))
	m.adminCertsLacking.Set(float64(sample.AdminCertsLacking))
	m.adminCertsExpiringSoon.Set(float64(sample.AdminCertsExpiringSoon))
	for _, file := range DBFiles {
		if size, ok := sample.DBFileBytes[file]; ok {
			m.dbFileBytes.WithLabelValues(file).Set(float64(size))
		}
	}
	m.ready.Set(boolValue(sample.Ready))
	m.opsLastSample.Set(unixSeconds(m.now()))
}

// RunSampler refreshes the sampled gauges until ctx is done. It samples once
// immediately so the first scrape after startup carries real values, then on
// every tick of Options.SampleInterval. Each run is bounded by sampleTimeout,
// so a slow query delays one interval rather than stopping the loop.
func (m *Metrics) RunSampler(ctx context.Context, s Sampler) {
	if s == nil {
		return
	}
	m.sampleOnce(ctx, s)

	ticker := time.NewTicker(m.sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sampleOnce(ctx, s)
		}
	}
}

// sampleOnce runs one bounded sample.
func (m *Metrics) sampleOnce(ctx context.Context, s Sampler) {
	runCtx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	m.Sample(runCtx, s)
}
