package metrics

import "github.com/Suhaibinator/kms/internal/core"

// *Metrics is the exporter behind the core.Metrics seam.
var _ core.Metrics = (*Metrics)(nil)

// AuthFailure records a rejected credential.
func (m *Metrics) AuthFailure(reason string) {
	m.authFailures.WithLabelValues(AuthFailureReasonLabel(reason)).Inc()
}

// AuthzDenied records a policy denial.
func (m *Metrics) AuthzDenied(operation string) {
	m.authzDenials.WithLabelValues(OperationLabel(operation)).Inc()
}

// AuthzMethodDenied records a request refused by a namespace's auth-method
// gate, independent of policy. method is the credential the caller
// authenticated with; the gate is about the credential, not the operation, so
// that is what the series is keyed on.
func (m *Metrics) AuthzMethodDenied(method string) {
	m.authzMethodDenials.WithLabelValues(AuthMethodLabel(method)).Inc()
}

// RateLimited records a refusal by the named limiter.
func (m *Metrics) RateLimited(limiter string) {
	m.rateLimited.WithLabelValues(LimiterLabel(limiter)).Inc()
}

// AuditEvent records a persisted audit row.
func (m *Metrics) AuditEvent(eventType, decision string) {
	m.auditEvents.WithLabelValues(EventTypeLabel(eventType), AuditDecisionLabel(decision)).Inc()
}

// AuditWriteFailed records an audit row that could not be persisted. The
// guarded operation fails closed, so this counter moving is an outage signal,
// not a warning.
func (m *Metrics) AuditWriteFailed() { m.auditWriteFailures.Inc() }

// AuditPruned records n audit rows removed by retention. A negative count is
// ignored: a counter never goes backwards.
func (m *Metrics) AuditPruned(n int) {
	if n <= 0 {
		return
	}
	m.auditPruned.Add(float64(n))
}

// DecryptFailed records a secret version that could not be opened with the
// current keyring.
func (m *Metrics) DecryptFailed() { m.decryptFailures.Inc() }

// ReleaseOutcome records the result of an activation or rollback.
func (m *Metrics) ReleaseOutcome(outcome string) {
	m.releaseOutcomes.WithLabelValues(ReleaseOutcomeLabel(outcome)).Inc()
}
