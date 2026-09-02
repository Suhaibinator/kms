package metrics

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

var watchMetricNames = []string{
	"kms_watch_subscribers",
	"kms_watch_release_subscribers",
	"kms_watch_last_dispatched_revision",
	"kms_watch_subscriber_lag_revisions_max",
	"kms_watch_subscribers_dropped_total",
}

const watchGolden = `
# HELP kms_watch_last_dispatched_revision Highest change-log revision the hub has dispatched.
# TYPE kms_watch_last_dispatched_revision gauge
kms_watch_last_dispatched_revision 4096
# HELP kms_watch_release_subscribers Release watch subscribers currently connected.
# TYPE kms_watch_release_subscribers gauge
kms_watch_release_subscribers 2
# HELP kms_watch_subscriber_lag_revisions_max Revisions the furthest-behind subscriber trails the dispatch cursor by.
# TYPE kms_watch_subscriber_lag_revisions_max gauge
kms_watch_subscriber_lag_revisions_max 12
# HELP kms_watch_subscribers Parameter watch subscribers currently connected.
# TYPE kms_watch_subscribers gauge
kms_watch_subscribers 7
# HELP kms_watch_subscribers_dropped_total Watch subscribers dropped by the hub, by reason.
# TYPE kms_watch_subscribers_dropped_total counter
kms_watch_subscribers_dropped_total{reason="slow"} 5
kms_watch_subscribers_dropped_total{reason="stale"} 3
`

func TestSetWatchSource(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.SetWatchSource(func() WatchStats {
		return WatchStats{
			Subscribers:            7,
			ReleaseSubscribers:     2,
			LastDispatchedRevision: 4096,
			MaxLagRevisions:        12,
			DroppedStale:           3,
			DroppedSlow:            5,
		}
	})

	if err := testutil.GatherAndCompare(m.Gatherer(), strings.NewReader(watchGolden), watchMetricNames...); err != nil {
		t.Fatal(err)
	}
}

// TestWatchSourceReadAtScrapeTime is the point of the seam: nothing is stored
// between scrapes, so a hub that moves on is reflected without the exporter
// being told.
func TestWatchSourceReadAtScrapeTime(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	var subscribers atomic.Int64
	m.SetWatchSource(func() WatchStats { return WatchStats{Subscribers: int(subscribers.Load())} })

	if !strings.Contains(gather(t, m), "kms_watch_subscribers 0") {
		t.Fatal("expected zero subscribers on the first scrape")
	}
	subscribers.Store(4)
	if !strings.Contains(gather(t, m), "kms_watch_subscribers 4") {
		t.Fatal("the second scrape should read the source again")
	}
}

// TestSetWatchSourceReplaces covers a hub rebuilt by a reload: the second call
// must not panic on a duplicate registration, and the old source must stop
// being read.
func TestSetWatchSourceReplaces(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	var oldReads atomic.Int64
	m.SetWatchSource(func() WatchStats {
		oldReads.Add(1)
		return WatchStats{Subscribers: 1}
	})
	m.SetWatchSource(func() WatchStats { return WatchStats{Subscribers: 9} })

	body := gather(t, m)
	if !strings.Contains(body, "kms_watch_subscribers 9") {
		t.Errorf("second source not installed:\n%s", body)
	}
	before := oldReads.Load()
	_ = gather(t, m)
	if oldReads.Load() != before {
		t.Errorf("the replaced source is still being scraped")
	}

	// A nil source removes the series entirely rather than freezing them at
	// their last value.
	m.SetWatchSource(nil)
	for _, name := range watchMetricNames {
		if strings.Contains(gather(t, m), name) {
			t.Errorf("%s still exposed after the source was removed", name)
		}
	}
}
