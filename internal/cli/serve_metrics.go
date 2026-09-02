package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/metrics"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

// metricsExpiryWindow is how far ahead the sampler counts expiring identity
// certificates and secret versions. A month is long enough that an operator
// alerting on the gauge has time to act through a normal change process, and
// short enough that the number means "soon" rather than "eventually".
const metricsExpiryWindow = 30 * 24 * time.Hour

// serveSampler builds the sampler serve hands the exporter: one bounded read
// of the state no request path produces.
//
// It is all-or-nothing on purpose. Any error returns without a partial Sample,
// so the exporter bumps its failure counter and leaves the previous gauge
// values in place — a transient database error must not read as "the change
// log emptied" on a dashboard. store is type-asserted for the optional
// operational-stats capability; a store without it simply contributes no
// change-log or expiry gauges.
func serveSampler(svc *core.Service, store storage.Store, sqlitePath string) metrics.Sampler {
	return func(ctx context.Context) (metrics.Sample, error) {
		sample := metrics.Sample{Ready: svc.Ready(ctx) == nil}

		if ops, ok := store.(storage.OperationalStatsStore); ok {
			stats, err := ops.OperationalStats(ctx, time.Time{}, metricsExpiryWindow, metricsExpiryWindow)
			if err != nil {
				return metrics.Sample{}, err
			}
			sample.ChangeLogRows = stats.ChangeLogRows
			sample.ChangeLogLastRevision = stats.ChangeLogLastRevision
			sample.ChangeLogOldestRevision = stats.ChangeLogOldestRevision
			sample.IdentityCertsExpiringSoon = stats.IdentityCertsExpiringSoon
			sample.SecretVersionsExpiringSoon = stats.SecretVersionsExpiringSoon
		}

		// The same window serve's startup posture check warns on, so the gauge
		// and the log agree about which admin certificates count as expiring.
		report, err := svc.OperationalReport(ctx, adminCertExpiryWarning)
		if err != nil {
			return metrics.Sample{}, err
		}
		sample.KEKGenerations = int64(report.KEKGenerations)
		sample.KEKActiveCreated = report.ActiveKEKCreatedAt
		sample.AdminCertsLacking = int64(report.AdminCertsLacking)
		sample.AdminCertsExpiringSoon = int64(report.AdminCertsExpiringSoon)

		sizes, err := dbFileSizes(sqlitePath)
		if err != nil {
			return metrics.Sample{}, err
		}
		sample.DBFileBytes = sizes
		return sample, nil
	}
}

// dbFileSizes reports the size of the SQLite database and of its write-ahead
// log. A missing WAL is 0 rather than an error: SQLite creates it lazily and
// removes it on a clean close, so its absence is a normal state, not a fault.
func dbFileSizes(path string) (map[string]int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{metrics.DBFileMain: info.Size(), metrics.DBFileWAL: 0}
	walInfo, err := os.Stat(path + "-wal")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return sizes, nil
	}
	sizes[metrics.DBFileWAL] = walInfo.Size()
	return sizes, nil
}

// watchStats adapts the hub's statistics to the exporter's shape. The
// adaptation lives here, in the wiring layer, so internal/watch never imports
// internal/metrics — the same seam core keeps.
func watchStats(s watch.Stats) metrics.WatchStats {
	return metrics.WatchStats{
		Subscribers:            s.Subscribers,
		ReleaseSubscribers:     s.ReleaseSubscribers,
		LastDispatchedRevision: s.LastDispatchedRevision,
		MaxLagRevisions:        s.MaxLagRevisions,
		DroppedStale:           s.DroppedStale,
		DroppedSlow:            s.DroppedSlow,
	}
}
