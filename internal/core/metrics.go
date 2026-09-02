package core

// Metrics is the operational-signal seam. The service reports security and
// health events through it with closed-set label values only — never a
// namespace, identity, key, client, IP, or request ID — so that a metrics
// backend can expose them without leaking who touched what. The Prometheus
// implementation lives in internal/metrics; core never imports it, mirroring
// the Hub seam above.
//
// Every method must be cheap, non-blocking, and safe to call concurrently:
// hooks sit on the authentication and authorization paths.
type Metrics interface {
	// AuthFailure records a rejected credential. reason is one of the
	// AuthFailure* constants.
	AuthFailure(reason string)
	// AuthzDenied records a policy denial for operation (a domain.Op* value
	// or an admin event type).
	AuthzDenied(operation string)
	// AuthzMethodDenied records a request refused by a namespace's
	// auth-method gate (token vs mTLS), independent of policy.
	AuthzMethodDenied(operation string)
	// RateLimited records a refusal by the named limiter (a Limiter*
	// constant, or a transport-owned name such as "http_login").
	RateLimited(limiter string)
	// AuditEvent records a persisted audit row by event type and decision
	// ("allow", "deny", "error").
	AuditEvent(eventType, decision string)
	// AuditWriteFailed records an audit row that could not be persisted. The
	// guarded operation fails closed; this is the signal that it did.
	AuditWriteFailed()
	// AuditPruned records n audit rows removed by retention.
	AuditPruned(n int)
	// DecryptFailed records a secret version whose ciphertext could not be
	// opened with the current keyring.
	DecryptFailed()
	// ReleaseOutcome records the result of an activation or rollback
	// (a ReleaseOutcome* constant).
	ReleaseOutcome(outcome string)
}

// Auth-failure reasons: the closed set of AuthFailure label values.
const (
	AuthFailureToken                   = "token"
	AuthFailureMTLS                    = "mtls"
	AuthFailureMissing                 = "missing"
	AuthFailureCredentialMismatch      = "credential_mismatch"
	AuthFailureAdminClientCertRequired = "admin_client_cert_required"
)

// AuthFailureReasons lists every AuthFailure label value, for exporters that
// pre-register series and for the label-contract tests.
var AuthFailureReasons = []string{
	AuthFailureToken,
	AuthFailureMTLS,
	AuthFailureMissing,
	AuthFailureCredentialMismatch,
	AuthFailureAdminClientCertRequired,
}

// Release outcomes: the closed set of ReleaseOutcome label values.
const (
	ReleaseOutcomeActivated        = "activated"
	ReleaseOutcomeRolledBack       = "rolled_back"
	ReleaseOutcomeCASConflict      = "cas_conflict"
	ReleaseOutcomeValidationFailed = "validation_failed"
	ReleaseOutcomeError            = "error"
)

// ReleaseOutcomes lists every ReleaseOutcome label value.
var ReleaseOutcomes = []string{
	ReleaseOutcomeActivated,
	ReleaseOutcomeRolledBack,
	ReleaseOutcomeCASConflict,
	ReleaseOutcomeValidationFailed,
	ReleaseOutcomeError,
}

// Limiters owned by core. Transports add their own names (see
// internal/metrics for the full closed set).
const (
	LimiterVerifyDefaultsRequests = "verify_defaults_requests"
	LimiterVerifyDefaultsMismatch = "verify_defaults_mismatch"
)

// Audit decisions as written to domain.AuditEvent.Decision.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
	DecisionError = "error"
)

// AuditDecisions lists every audit decision value.
var AuditDecisions = []string{DecisionAllow, DecisionDeny, DecisionError}

// noopMetrics is used until (or unless) a real exporter is attached.
type noopMetrics struct{}

func (noopMetrics) AuthFailure(string)        {}
func (noopMetrics) AuthzDenied(string)        {}
func (noopMetrics) AuthzMethodDenied(string)  {}
func (noopMetrics) RateLimited(string)        {}
func (noopMetrics) AuditEvent(string, string) {}
func (noopMetrics) AuditWriteFailed()         {}
func (noopMetrics) AuditPruned(int)           {}
func (noopMetrics) DecryptFailed()            {}
func (noopMetrics) ReleaseOutcome(string)     {}

// metricsHolder wraps the interface value so it can live in an
// atomic.Pointer the same way the hub does.
type metricsHolder struct{ m Metrics }

func (s *Service) initMetrics() { s.metrics.Store(&metricsHolder{m: noopMetrics{}}) }

// SetMetrics attaches an exporter. Safe to call at any time; nil restores
// the no-op default. Call it before the listeners start so no event is lost.
func (s *Service) SetMetrics(m Metrics) {
	if m == nil {
		m = noopMetrics{}
	}
	s.metrics.Store(&metricsHolder{m: m})
}

// m returns the attached exporter (never nil).
func (s *Service) m() Metrics { return s.metrics.Load().m }
