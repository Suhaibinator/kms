// Package kmsmetrics exposes a managed configuration store's status and
// counters as Prometheus metrics.
//
// The collector reads the store on every scrape and emits constant metrics,
// so it never drifts from configstore's own counters and needs no
// bookkeeping in the application:
//
//	registry.MustRegister(kmsmetrics.NewCollector("myapp", store))
//
// Metric names are <namespace>_kms_config_*; every value is a bounded
// number or a bounded category label. No alias, field path, or value is
// ever exported.
package kmsmetrics

import (
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

// Source is the part of a managed store the collector reads. *configstore.Manager
// and every generated Store satisfy it.
type Source interface {
	Status() configstore.Status
	Stats() configstore.Stats
}

// Subsystem is the fixed middle segment of every metric name.
const Subsystem = "kms_config"

// rejectionCategories are always exported, with a zero value when the store
// has never rejected a candidate for that reason, so dashboards and alerts
// can rely on the series existing.
var rejectionCategories = []configstore.RejectionCategory{
	configstore.RejectConfigContractMismatch,
	configstore.RejectConfigDecodeFailed,
	configstore.RejectConfigValidationFailed,
	configstore.RejectDefaultMismatch,
	configstore.RejectRestartRequired,
	configstore.RejectInternal,
}

type collector struct {
	source Source

	defaultDivergent   *prometheus.Desc
	ready              *prometheus.Desc
	releaseVersion     *prometheus.Desc
	activationRevision *prometheus.Desc
	candidates         *prometheus.Desc
	applied            *prometheus.Desc
	rejected           *prometheus.Desc
	reconnects         *prometheus.Desc
}

// NewCollector returns a Prometheus collector that reads source on each
// scrape. namespace prefixes every metric name and is sanitized to a valid
// metric identifier ([a-zA-Z_][a-zA-Z0-9_]*); an empty namespace yields bare
// kms_config_* names. It panics when source is nil because a collector that
// can never report anything is a programming error.
//
// Exported metrics:
//
//	<ns>_kms_config_default_divergent            gauge   1 while the applied generation differs from source defaults
//	<ns>_kms_config_ready                        gauge   1 once an initial generation has been applied
//	<ns>_kms_config_applied_release_version      gauge   version of the applied release
//	<ns>_kms_config_applied_activation_revision  gauge   activation revision of the applied release
//	<ns>_kms_config_candidates_total             counter release candidates received
//	<ns>_kms_config_candidates_applied_total     counter candidates applied
//	<ns>_kms_config_candidates_rejected_total    counter candidates rejected, by category
//	<ns>_kms_config_reconnects_total             counter release stream reconnects
func NewCollector(namespace string, source Source) prometheus.Collector {
	if source == nil {
		panic("kmsmetrics: NewCollector requires a non-nil Source")
	}
	namespace = SanitizeNamespace(namespace)
	name := func(suffix string) string { return prometheus.BuildFQName(namespace, Subsystem, suffix) }
	return &collector{
		source: source,
		defaultDivergent: prometheus.NewDesc(name("default_divergent"),
			"1 while the applied configuration generation differs from the application's source defaults.", nil, nil),
		ready: prometheus.NewDesc(name("ready"),
			"1 once an initial configuration generation has been applied.", nil, nil),
		releaseVersion: prometheus.NewDesc(name("applied_release_version"),
			"Version of the applied configuration release.", nil, nil),
		activationRevision: prometheus.NewDesc(name("applied_activation_revision"),
			"Activation revision of the applied configuration release.", nil, nil),
		candidates: prometheus.NewDesc(name("candidates_total"),
			"Configuration release candidates received.", nil, nil),
		applied: prometheus.NewDesc(name("candidates_applied_total"),
			"Configuration release candidates applied.", nil, nil),
		rejected: prometheus.NewDesc(name("candidates_rejected_total"),
			"Configuration release candidates rejected, by category.", []string{"category"}, nil),
		reconnects: prometheus.NewDesc(name("reconnects_total"),
			"Configuration release stream reconnects.", nil, nil),
	}
}

// SanitizeNamespace rewrites namespace into a valid Prometheus metric
// identifier: every character outside [a-zA-Z0-9_] becomes "_" and a leading
// digit is prefixed with "_". An empty namespace stays empty.
func SanitizeNamespace(namespace string) string {
	if namespace == "" {
		return ""
	}
	var out strings.Builder
	out.Grow(len(namespace) + 1)
	for i := 0; i < len(namespace); i++ {
		character := namespace[i]
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character == '_':
			out.WriteByte(character)
		case character >= '0' && character <= '9':
			if i == 0 {
				out.WriteByte('_')
			}
			out.WriteByte(character)
		default:
			out.WriteByte('_')
		}
	}
	return out.String()
}

// Describe implements prometheus.Collector.
func (c *collector) Describe(descs chan<- *prometheus.Desc) {
	descs <- c.defaultDivergent
	descs <- c.ready
	descs <- c.releaseVersion
	descs <- c.activationRevision
	descs <- c.candidates
	descs <- c.applied
	descs <- c.rejected
	descs <- c.reconnects
}

// Collect implements prometheus.Collector.
func (c *collector) Collect(metrics chan<- prometheus.Metric) {
	status := c.source.Status()
	stats := c.source.Stats()

	metrics <- prometheus.MustNewConstMetric(c.defaultDivergent, prometheus.GaugeValue, boolValue(status.DefaultDivergent))
	metrics <- prometheus.MustNewConstMetric(c.ready, prometheus.GaugeValue, boolValue(status.Ready))
	metrics <- prometheus.MustNewConstMetric(c.releaseVersion, prometheus.GaugeValue, float64(stats.AppliedReleaseVersion))
	metrics <- prometheus.MustNewConstMetric(c.activationRevision, prometheus.GaugeValue, float64(stats.AppliedActivationRevision))
	metrics <- prometheus.MustNewConstMetric(c.candidates, prometheus.CounterValue, float64(stats.Candidates))
	metrics <- prometheus.MustNewConstMetric(c.applied, prometheus.CounterValue, float64(stats.Applied))
	for _, category := range rejectedCategories(stats.Rejected) {
		metrics <- prometheus.MustNewConstMetric(c.rejected, prometheus.CounterValue, float64(stats.Rejected[category]), string(category))
	}
	metrics <- prometheus.MustNewConstMetric(c.reconnects, prometheus.CounterValue, float64(stats.Reconnects))
}

// rejectedCategories returns the fixed configstore categories followed by
// any further bounded loader categories present in rejected, sorted, so the
// per-category series set is stable across scrapes.
func rejectedCategories(rejected map[configstore.RejectionCategory]uint64) []configstore.RejectionCategory {
	categories := make([]configstore.RejectionCategory, 0, len(rejectionCategories)+len(rejected))
	seen := make(map[configstore.RejectionCategory]struct{}, len(rejectionCategories)+len(rejected))
	for _, category := range rejectionCategories {
		categories = append(categories, category)
		seen[category] = struct{}{}
	}
	var extra []configstore.RejectionCategory
	for category := range rejected {
		if _, known := seen[category]; known || category == "" {
			continue
		}
		extra = append(extra, category)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return append(categories, extra...)
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
