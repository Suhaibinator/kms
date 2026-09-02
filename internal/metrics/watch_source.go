package metrics

import "github.com/prometheus/client_golang/prometheus"

// WatchStats is the hub's fan-out state, read at scrape time. It mirrors
// watch.Stats without importing it: the watch package must not depend on the
// exporter, so the wiring layer adapts between the two.
type WatchStats struct {
	Subscribers            int
	ReleaseSubscribers     int
	LastDispatchedRevision uint64
	// MaxLagRevisions is how far the slowest subscriber trails the dispatch
	// cursor. It is the number that says a client is about to be dropped.
	MaxLagRevisions uint64
	DroppedStale    uint64
	DroppedSlow     uint64
}

// SetWatchSource publishes the hub's fan-out state. source is called during a
// scrape rather than on a timer, so the numbers are current and nothing is
// stored between scrapes; it must therefore be cheap and must not block.
//
// Calling it again replaces the previous source, and a nil source removes it —
// so a hub that is torn down and rebuilt (a reload) does not leave a collector
// reading a dead one.
func (m *Metrics) SetWatchSource(source func() WatchStats) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	for _, c := range m.watchCollectors {
		m.reg.Unregister(c)
	}
	m.watchCollectors = nil
	if source == nil {
		return
	}

	collectors := []prometheus.Collector{
		watchGauge("kms_watch_subscribers", "Parameter watch subscribers currently connected.",
			func(s WatchStats) float64 { return float64(s.Subscribers) }, source),
		watchGauge("kms_watch_release_subscribers", "Release watch subscribers currently connected.",
			func(s WatchStats) float64 { return float64(s.ReleaseSubscribers) }, source),
		watchGauge("kms_watch_last_dispatched_revision", "Highest change-log revision the hub has dispatched.",
			func(s WatchStats) float64 { return float64(s.LastDispatchedRevision) }, source),
		watchGauge("kms_watch_subscriber_lag_revisions_max",
			"Revisions the furthest-behind subscriber trails the dispatch cursor by.",
			func(s WatchStats) float64 { return float64(s.MaxLagRevisions) }, source),
		watchDropped("stale", func(s WatchStats) float64 { return float64(s.DroppedStale) }, source),
		watchDropped("slow", func(s WatchStats) float64 { return float64(s.DroppedSlow) }, source),
	}
	for _, c := range collectors {
		m.reg.MustRegister(c)
	}
	m.watchCollectors = collectors
}

// watchGauge builds a scrape-time gauge over one field of WatchStats.
func watchGauge(name, help string, field func(WatchStats) float64, source func() WatchStats) prometheus.Collector {
	return prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{Name: name, Help: help},
		func() float64 { return field(source()) },
	)
}

// watchDropped builds one series of kms_watch_subscribers_dropped_total. The
// reason is a constant label rather than a vector dimension because the value
// is read from the source at scrape time, not accumulated here.
func watchDropped(reason string, field func(WatchStats) float64, source func() WatchStats) prometheus.Collector {
	return prometheus.NewCounterFunc(
		prometheus.CounterOpts{
			Name:        "kms_watch_subscribers_dropped_total",
			Help:        "Watch subscribers dropped by the hub, by reason.",
			ConstLabels: prometheus.Labels{labelReason: reason},
		},
		func() float64 { return field(source()) },
	)
}
