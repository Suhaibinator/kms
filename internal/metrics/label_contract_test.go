package metrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/Suhaibinator/kms/internal/core"
)

// identifiers are the shapes of data that must never reach a scrape: a
// namespace, an application, a key name, an identity, a client address, and a
// request ID. /metrics is served without an authenticated caller behind it, so
// a label carrying any of these would hand an operator's monitoring stack — and
// anyone who can reach it — the inventory the API is there to protect.
var identifiers = []string{
	"prod",
	"payments",
	"db_password",
	"10.0.0.5",
	"req-123",
	"deploy-bot",
	"7f3a9c1e",
}

// adminIdentifier is checked by exact match on label values rather than as a
// substring: "admin" is a legitimate part of metric names
// (kms_admin_certs_lacking) and of policy operations (admin:key:rotate), but no
// label may ever be the bare identity name.
const adminIdentifier = "admin"

// exerciseEveryHook drives every entry point on Metrics with the worst input a
// call site could pass: identifiers where a closed-set value belongs.
func exerciseEveryHook(t *testing.T, m *Metrics) {
	t.Helper()

	for _, id := range append(append([]string{}, identifiers...), adminIdentifier) {
		m.AuthFailure(id)
		m.AuthzDenied(id)
		m.AuthzMethodDenied(id)
		m.RateLimited(id)
		m.AuditEvent(id, id)
		m.ReleaseOutcome(id)
		m.ReloadResult(id)
		m.ObserveGRPC("/"+id+"/"+id, codes.Code(99), time.Millisecond)
		m.GRPCStreamStarted("/" + id + "/" + id)
		m.GRPCStreamEnded("/" + id + "/" + id)
		m.ObserveHTTP("/api/v1/parameters/"+id, id, 999, time.Millisecond)
		m.ObserveHTTP("GET /api/v1/secrets?name="+id, "GET", 200, time.Millisecond)
	}

	// The hooks without a label still have to run, so a future change that adds
	// one to them is caught here.
	m.AuditWriteFailed()
	m.AuditPruned(3)
	m.DecryptFailed()
	m.SSEStreamStarted()
	m.SSEStreamEnded()
	m.SetStartTime(time.Unix(1600000000, 0).UTC())
	m.SetReady(true)
	m.SetPosture(true, true)

	// A sampler is written by the wiring layer; Sample takes numbers only, but
	// the file label is a map key it could get wrong.
	m.Sample(t.Context(), func(context.Context) (Sample, error) {
		return Sample{
			ChangeLogRows: 1,
			DBFileBytes:   map[string]int64{DBFileMain: 1, "/srv/" + identifiers[0] + ".db": 2},
			Ready:         true,
		}, nil
	})
	m.Sample(t.Context(), func(context.Context) (Sample, error) {
		return Sample{}, context.DeadlineExceeded
	})
	m.SetWatchSource(func() WatchStats {
		return WatchStats{Subscribers: 1, ReleaseSubscribers: 1, LastDispatchedRevision: 2,
			MaxLagRevisions: 1, DroppedStale: 1, DroppedSlow: 1}
	})

	// The same hooks with legitimate values, so the assertions run against a
	// registry that also holds real series.
	m.AuthFailure(core.AuthFailureToken)
	m.AuthzDenied(core.DecisionDeny)
	m.RateLimited(LimiterHTTPLogin)
	m.AuditEvent("secret.reveal", core.DecisionAllow)
	m.ReleaseOutcome(core.ReleaseOutcomeActivated)
	m.ReloadResult(ReloadApplied)
	m.ObserveGRPC("/kms.v1.SecretService/GetSecret", codes.OK, time.Millisecond)
	m.ObserveHTTP("GET /api/v1/secrets", "GET", 200, time.Millisecond)
}

// TestLabelContract is the security test for this package: after every hook has
// been fed identifying data, no gathered series may carry a label name outside
// the allowlist, and no label value anywhere may contain one of those
// identifiers.
func TestLabelContract(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	exerciseEveryHook(t, m)

	allowed := stringSet(LabelNames)
	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(families) == 0 {
		t.Fatal("gathered nothing")
	}

	sawKMS := false
	for _, family := range families {
		name := family.GetName()
		isKMS := strings.HasPrefix(name, "kms_")
		sawKMS = sawKMS || isKMS
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				// The label-name allowlist governs this package's series. The
				// Go and process collectors are part of the same exposition but
				// their labels are fixed by client_golang.
				if isKMS {
					if _, ok := allowed[label.GetName()]; !ok {
						t.Errorf("%s: label name %q is not in LabelNames", name, label.GetName())
					}
				}
				for _, id := range identifiers {
					if strings.Contains(label.GetValue(), id) {
						t.Errorf("%s: label %s=%q leaks %q",
							name, label.GetName(), label.GetValue(), id)
					}
				}
				if label.GetValue() == adminIdentifier {
					t.Errorf("%s: label %s carries the bare identity %q",
						name, label.GetName(), adminIdentifier)
				}
			}
		}
	}
	if !sawKMS {
		t.Fatal("no kms_ series were gathered")
	}
}

// TestExpositionCarriesNoIdentifiers is the same guarantee checked end to end,
// on the bytes a scraper actually receives — help text and metric names
// included.
func TestExpositionCarriesNoIdentifiers(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	exerciseEveryHook(t, m)

	body := gather(t, m)
	for _, id := range identifiers {
		if strings.Contains(body, id) {
			t.Errorf("exposition contains %q", id)
		}
	}
	// Sanity: the bucketed values are what the identifiers turned into.
	for _, want := range []string{
		`reason="other"`,
		`operation="other"`,
		`limiter="other"`,
		`outcome="other"`,
		`event_type="other"`,
		`decision="other"`,
		`result="other"`,
		`service="unknown"`,
		`method="unknown"`,
		`route="unmatched"`,
		`status="other"`,
		`code="Unknown"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing the bucketed series %s", want)
		}
	}
}

// TestClosedSetsCoverTheirSources keeps the label allowlists tied to the
// vocabularies they mirror, so a value core can emit is never silently
// bucketed as "other".
func TestClosedSetsCoverTheirSources(t *testing.T) {
	t.Parallel()

	for _, reason := range core.AuthFailureReasons {
		if got := AuthFailureReasonLabel(reason); got != reason {
			t.Errorf("AuthFailureReasonLabel(%q) = %q", reason, got)
		}
	}
	for _, outcome := range core.ReleaseOutcomes {
		if got := ReleaseOutcomeLabel(outcome); got != outcome {
			t.Errorf("ReleaseOutcomeLabel(%q) = %q", outcome, got)
		}
	}
	for _, decision := range core.AuditDecisions {
		if got := AuditDecisionLabel(decision); got != decision {
			t.Errorf("AuditDecisionLabel(%q) = %q", decision, got)
		}
	}
	for _, limiter := range []string{core.LimiterVerifyDefaultsRequests, core.LimiterVerifyDefaultsMismatch, core.LimiterBindingCohortPreview} {
		if got := LimiterLabel(limiter); got != limiter {
			t.Errorf("LimiterLabel(%q) = %q", limiter, got)
		}
	}
	for _, method := range core.AuthMethods {
		if got := AuthMethodLabel(method); got != method {
			t.Errorf("AuthMethodLabel(%q) = %q", method, got)
		}
	}
	for _, op := range PolicyOperations {
		if got := OperationLabel(op); got != op {
			t.Errorf("OperationLabel(%q) = %q", op, got)
		}
	}
	// Admin denials are recorded under an audit event type, so the operation
	// label has to accept those too.
	for _, eventType := range AuditEventTypes {
		if got := EventTypeLabel(eventType); got != eventType {
			t.Errorf("EventTypeLabel(%q) = %q", eventType, got)
		}
		if got := OperationLabel(eventType); got != eventType {
			t.Errorf("OperationLabel(%q) = %q", eventType, got)
		}
	}
}

// TestClosedSetsAreWellFormed guards the lists themselves: a duplicate or an
// empty entry would quietly weaken a set.
func TestClosedSetsAreWellFormed(t *testing.T) {
	t.Parallel()

	for name, values := range map[string][]string{
		"LabelNames":       LabelNames,
		"RouteLabels":      RouteLabels,
		"LimiterNames":     LimiterNames,
		"AuditEventTypes":  AuditEventTypes,
		"PolicyOperations": PolicyOperations,
		"ReloadResults":    ReloadResults,
		"DBFiles":          DBFiles,
		"AuthMethods":      core.AuthMethods,
	} {
		seen := map[string]bool{}
		for _, v := range values {
			if v == "" {
				t.Errorf("%s contains an empty value", name)
			}
			if seen[v] {
				t.Errorf("%s contains duplicate %q", name, v)
			}
			seen[v] = true
		}
	}
	// The fallbacks must not collide with a real value, or a bucketed event
	// would be indistinguishable from a genuine one.
	for _, set := range []map[string]struct{}{
		authFailureReasonSet, releaseOutcomeSet, auditDecisionSet, authMethodSet,
		limiterSet, operationSet, auditEventTypeSet, reloadResultSet, routeLabelSet,
	} {
		if _, ok := set[ValueOther]; ok {
			t.Errorf("a closed set contains the fallback %q", ValueOther)
		}
	}
}
