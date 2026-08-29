package kmsmetrics

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

type fakeSource struct {
	status configstore.Status
	stats  configstore.Stats
	reads  atomic.Int32
}

func (s *fakeSource) Status() configstore.Status {
	s.reads.Add(1)
	return s.status
}

func (s *fakeSource) Stats() configstore.Stats {
	s.reads.Add(1)
	return s.stats
}

func TestNewCollectorPanicsOnNilSource(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewCollector(nil) did not panic")
		}
	}()
	NewCollector("app", nil)
}

func TestCollectorExportsStatusAndStatsEachScrape(t *testing.T) {
	source := &fakeSource{
		status: configstore.Status{Ready: true, DefaultDivergent: true},
		stats: configstore.Stats{
			Candidates: 7, Applied: 5, Reconnects: 2,
			Rejected: map[configstore.RejectionCategory]uint64{
				configstore.RejectConfigValidationFailed:    1,
				configstore.RejectRestartRequired:           1,
				configstore.RejectionCategory("superseded"): 3,
			},
			DefaultDivergent:          true,
			AppliedReleaseVersion:     12,
			AppliedActivationRevision: 34,
		},
	}
	collector := NewCollector("my-app", source)
	expected := `
# HELP my_app_kms_config_applied_activation_revision Activation revision of the applied configuration release.
# TYPE my_app_kms_config_applied_activation_revision gauge
my_app_kms_config_applied_activation_revision 34
# HELP my_app_kms_config_applied_release_version Version of the applied configuration release.
# TYPE my_app_kms_config_applied_release_version gauge
my_app_kms_config_applied_release_version 12
# HELP my_app_kms_config_candidates_applied_total Configuration release candidates applied.
# TYPE my_app_kms_config_candidates_applied_total counter
my_app_kms_config_candidates_applied_total 5
# HELP my_app_kms_config_candidates_rejected_total Configuration release candidates rejected, by category.
# TYPE my_app_kms_config_candidates_rejected_total counter
my_app_kms_config_candidates_rejected_total{category="config_contract_mismatch"} 0
my_app_kms_config_candidates_rejected_total{category="config_decode_failed"} 0
my_app_kms_config_candidates_rejected_total{category="config_validation_failed"} 1
my_app_kms_config_candidates_rejected_total{category="default_mismatch"} 0
my_app_kms_config_candidates_rejected_total{category="internal"} 0
my_app_kms_config_candidates_rejected_total{category="restart_required"} 1
my_app_kms_config_candidates_rejected_total{category="superseded"} 3
# HELP my_app_kms_config_candidates_total Configuration release candidates received.
# TYPE my_app_kms_config_candidates_total counter
my_app_kms_config_candidates_total 7
# HELP my_app_kms_config_default_divergent 1 while the applied configuration generation differs from the application's source defaults.
# TYPE my_app_kms_config_default_divergent gauge
my_app_kms_config_default_divergent 1
# HELP my_app_kms_config_ready 1 once an initial configuration generation has been applied.
# TYPE my_app_kms_config_ready gauge
my_app_kms_config_ready 1
# HELP my_app_kms_config_reconnects_total Configuration release stream reconnects.
# TYPE my_app_kms_config_reconnects_total counter
my_app_kms_config_reconnects_total 2
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected)); err != nil {
		t.Fatal(err)
	}
	if problems, err := testutil.CollectAndLint(collector); err != nil || len(problems) != 0 {
		t.Fatalf("lint problems = %v (%v)", problems, err)
	}
	if source.reads.Load() == 0 {
		t.Fatal("collector did not read the source")
	}

	// The next scrape reflects the store's current state without any
	// bookkeeping between scrapes.
	source.status = configstore.Status{Ready: true, DefaultDivergent: false}
	source.stats = configstore.Stats{Candidates: 8, Applied: 6, AppliedReleaseVersion: 13, AppliedActivationRevision: 35, Rejected: nil}
	expected = `
# HELP my_app_kms_config_applied_release_version Version of the applied configuration release.
# TYPE my_app_kms_config_applied_release_version gauge
my_app_kms_config_applied_release_version 13
# HELP my_app_kms_config_candidates_rejected_total Configuration release candidates rejected, by category.
# TYPE my_app_kms_config_candidates_rejected_total counter
my_app_kms_config_candidates_rejected_total{category="config_contract_mismatch"} 0
my_app_kms_config_candidates_rejected_total{category="config_decode_failed"} 0
my_app_kms_config_candidates_rejected_total{category="config_validation_failed"} 0
my_app_kms_config_candidates_rejected_total{category="default_mismatch"} 0
my_app_kms_config_candidates_rejected_total{category="internal"} 0
my_app_kms_config_candidates_rejected_total{category="restart_required"} 0
# HELP my_app_kms_config_default_divergent 1 while the applied configuration generation differs from the application's source defaults.
# TYPE my_app_kms_config_default_divergent gauge
my_app_kms_config_default_divergent 0
# HELP my_app_kms_config_reconnects_total Configuration release stream reconnects.
# TYPE my_app_kms_config_reconnects_total counter
my_app_kms_config_reconnects_total 0
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"my_app_kms_config_applied_release_version",
		"my_app_kms_config_candidates_rejected_total",
		"my_app_kms_config_default_divergent",
		"my_app_kms_config_reconnects_total",
	); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorZeroSourceBeforeReadiness(t *testing.T) {
	collector := NewCollector("", &fakeSource{stats: configstore.Stats{Rejected: map[configstore.RejectionCategory]uint64{}}})
	expected := `
# HELP kms_config_default_divergent 1 while the applied configuration generation differs from the application's source defaults.
# TYPE kms_config_default_divergent gauge
kms_config_default_divergent 0
# HELP kms_config_ready 1 once an initial configuration generation has been applied.
# TYPE kms_config_ready gauge
kms_config_ready 0
# HELP kms_config_applied_release_version Version of the applied configuration release.
# TYPE kms_config_applied_release_version gauge
kms_config_applied_release_version 0
# HELP kms_config_candidates_total Configuration release candidates received.
# TYPE kms_config_candidates_total counter
kms_config_candidates_total 0
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"kms_config_default_divergent", "kms_config_ready", "kms_config_applied_release_version", "kms_config_candidates_total",
	); err != nil {
		t.Fatal(err)
	}
	if got := testutil.CollectAndCount(collector); got != 7+len(rejectionCategories) {
		t.Fatalf("CollectAndCount() = %d, want %d", got, 7+len(rejectionCategories))
	}
}

func TestSanitizeNamespace(t *testing.T) {
	for input, want := range map[string]string{
		"":            "",
		"app":         "app",
		"my-app.v2":   "my_app_v2",
		"1st_app":     "_1st_app",
		"App_Name":    "App_Name",
		"with space":  "with_space",
		"ünïcode/app": "__n__code_app",
	} {
		if got := SanitizeNamespace(input); got != want {
			t.Fatalf("SanitizeNamespace(%q) = %q, want %q", input, got, want)
		}
	}
	collector := NewCollector("1st-app", &fakeSource{})
	if err := testutil.CollectAndCompare(collector, strings.NewReader(`
# HELP _1st_app_kms_config_ready 1 once an initial configuration generation has been applied.
# TYPE _1st_app_kms_config_ready gauge
_1st_app_kms_config_ready 0
`), "_1st_app_kms_config_ready"); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorRegistersWithPedanticRegistry(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	source := &fakeSource{
		status: configstore.Status{Ready: true},
		stats:  configstore.Stats{Rejected: map[configstore.RejectionCategory]uint64{configstore.RejectInternal: 2}},
	}
	if err := registry.Register(NewCollector("svc", source)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, family.GetName())
	}
	want := []string{
		"svc_kms_config_applied_activation_revision",
		"svc_kms_config_applied_release_version",
		"svc_kms_config_candidates_applied_total",
		"svc_kms_config_candidates_rejected_total",
		"svc_kms_config_candidates_total",
		"svc_kms_config_default_divergent",
		"svc_kms_config_ready",
		"svc_kms_config_reconnects_total",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("gathered families = %v, want %v", names, want)
	}
	// Two collectors for different namespaces coexist in one registry.
	if err := registry.Register(NewCollector("other", source)); err != nil {
		t.Fatalf("second Register() error = %v", err)
	}
	if err := registry.Register(NewCollector("svc", source)); err == nil {
		t.Fatal("duplicate namespace registration succeeded")
	}
}
