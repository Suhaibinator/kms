package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// TestClosedSetsStartAtZero pins the pre-registration: an alert written as
// increase(kms_auth_failures_total[5m]) > 0 must fire on the first refusal
// after a restart, which it only does if the series already exists at zero.
func TestClosedSetsStartAtZero(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	for _, reason := range core.AuthFailureReasons {
		if got := testutil.ToFloat64(m.authFailures.WithLabelValues(reason)); got != 0 {
			t.Errorf("kms_auth_failures_total{reason=%q} = %v, want 0", reason, got)
		}
	}
	for _, outcome := range core.ReleaseOutcomes {
		if got := testutil.ToFloat64(m.releaseOutcomes.WithLabelValues(outcome)); got != 0 {
			t.Errorf("kms_release_outcomes_total{outcome=%q} = %v, want 0", outcome, got)
		}
	}
	for _, result := range ReloadResults {
		if got := testutil.ToFloat64(m.reloads.WithLabelValues(result)); got != 0 {
			t.Errorf("kms_reloads_total{result=%q} = %v, want 0", result, got)
		}
	}
	for _, limiter := range LimiterNames {
		if got := testutil.ToFloat64(m.rateLimited.WithLabelValues(limiter)); got != 0 {
			t.Errorf("kms_ratelimit_refusals_total{limiter=%q} = %v, want 0", limiter, got)
		}
	}
	for _, method := range core.AuthMethods {
		if got := testutil.ToFloat64(m.authzMethodDenials.WithLabelValues(method)); got != 0 {
			t.Errorf("kms_authz_method_denials_total{method=%q} = %v, want 0", method, got)
		}
	}

	// The transport limiters must be part of that set, not discovered at
	// runtime: they are the ones a login flood shows up on.
	if got := testutil.CollectAndCount(m.rateLimited); got != len(LimiterNames) {
		t.Errorf("pre-registered limiter series = %d, want %d", got, len(LimiterNames))
	}
}

func TestAuthFailure(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.AuthFailure(core.AuthFailureToken)
	m.AuthFailure(core.AuthFailureToken)
	m.AuthFailure(core.AuthFailureAdminClientCertRequired)
	m.AuthFailure("payments")

	for reason, want := range map[string]float64{
		core.AuthFailureToken:                   2,
		core.AuthFailureAdminClientCertRequired: 1,
		core.AuthFailureMTLS:                    0,
		ValueOther:                              1,
	} {
		if got := testutil.ToFloat64(m.authFailures.WithLabelValues(reason)); got != want {
			t.Errorf("kms_auth_failures_total{reason=%q} = %v, want %v", reason, got, want)
		}
	}
}

func TestAuthzDenials(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.AuthzDenied(domain.OpSecretRead)
	m.AuthzDenied("identity.cert.issue") // admin surface: denied under its event type
	m.AuthzDenied("prod/payments")

	for op, want := range map[string]float64{
		domain.OpSecretRead:   1,
		"identity.cert.issue": 1,
		ValueOther:            1,
	} {
		if got := testutil.ToFloat64(m.authzDenials.WithLabelValues(op)); got != want {
			t.Errorf("kms_authz_denials_total{operation=%q} = %v, want %v", op, got, want)
		}
	}
}

// TestAuthzMethodDenied keys on the credential, not the operation: the
// namespace auth-method gate refuses a token where mTLS is required, and the
// useful question is which credential was turned away.
func TestAuthzMethodDenied(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.AuthzMethodDenied(core.AuthFailureToken)
	m.AuthzMethodDenied(core.AuthFailureToken)
	m.AuthzMethodDenied(core.AuthFailureMTLS)
	// An operation name is not an auth method; it must be bucketed, not
	// silently accepted as one.
	m.AuthzMethodDenied(domain.OpParameterWrite)
	m.AuthzMethodDenied("db_password")

	for method, want := range map[string]float64{
		core.AuthFailureToken: 2,
		core.AuthFailureMTLS:  1,
		ValueOther:            2,
	} {
		if got := testutil.ToFloat64(m.authzMethodDenials.WithLabelValues(method)); got != want {
			t.Errorf("kms_authz_method_denials_total{method=%q} = %v, want %v", method, got, want)
		}
	}
}

func TestRateLimited(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.RateLimited(LimiterHTTPLogin)
	m.RateLimited(core.LimiterVerifyDefaultsMismatch)
	m.RateLimited("10.0.0.5")

	for limiter, want := range map[string]float64{
		LimiterHTTPLogin:                   1,
		core.LimiterVerifyDefaultsMismatch: 1,
		LimiterSSEGlobal:                   0,
		ValueOther:                         1,
	} {
		if got := testutil.ToFloat64(m.rateLimited.WithLabelValues(limiter)); got != want {
			t.Errorf("kms_ratelimit_refusals_total{limiter=%q} = %v, want %v", limiter, got, want)
		}
	}
}

func TestAuditHooks(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.AuditEvent("secret.reveal", core.DecisionAllow)
	m.AuditEvent("secret.reveal", core.DecisionDeny)
	m.AuditEvent("db_password", "maybe")
	m.AuditWriteFailed()
	m.AuditWriteFailed()
	m.AuditPruned(17)
	m.AuditPruned(0)
	m.AuditPruned(-5) // a counter never goes backwards

	if got := testutil.ToFloat64(m.auditEvents.WithLabelValues("secret.reveal", core.DecisionAllow)); got != 1 {
		t.Errorf("allow events = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.auditEvents.WithLabelValues("secret.reveal", core.DecisionDeny)); got != 1 {
		t.Errorf("deny events = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.auditEvents.WithLabelValues(ValueOther, ValueOther)); got != 1 {
		t.Errorf("bucketed events = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.auditWriteFailures); got != 2 {
		t.Errorf("kms_audit_write_failures_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.auditPruned); got != 17 {
		t.Errorf("kms_audit_pruned_total = %v, want 17", got)
	}
}

func TestDecryptAndReleaseHooks(t *testing.T) {
	t.Parallel()

	m := newTestMetrics(t)
	m.DecryptFailed()
	m.ReleaseOutcome(core.ReleaseOutcomeActivated)
	m.ReleaseOutcome(core.ReleaseOutcomeCASConflict)
	m.ReleaseOutcome("req-123")

	if got := testutil.ToFloat64(m.decryptFailures); got != 1 {
		t.Errorf("kms_secret_decrypt_failures_total = %v, want 1", got)
	}
	for outcome, want := range map[string]float64{
		core.ReleaseOutcomeActivated:   1,
		core.ReleaseOutcomeCASConflict: 1,
		core.ReleaseOutcomeRolledBack:  0,
		ValueOther:                     1,
	} {
		if got := testutil.ToFloat64(m.releaseOutcomes.WithLabelValues(outcome)); got != want {
			t.Errorf("kms_release_outcomes_total{outcome=%q} = %v, want %v", outcome, got, want)
		}
	}
}
