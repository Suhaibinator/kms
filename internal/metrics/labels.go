package metrics

import (
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"

	"github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// Label names used anywhere in this package. LabelNames is the allowlist the
// label-contract test checks every gathered series against: a name outside it
// is a bug, because only these have a closed, non-identifying value set.
const (
	labelVersion   = "version"
	labelGoVersion = "go_version"
	labelResult    = "result"
	labelReason    = "reason"
	labelOperation = "operation"
	labelLimiter   = "limiter"
	labelOutcome   = "outcome"
	labelDecision  = "decision"
	labelEventType = "event_type"
	labelService   = "service"
	labelMethod    = "method"
	labelCode      = "code"
	labelRoute     = "route"
	labelStatus    = "status"
	labelFile      = "file"
)

// LabelNames is every label name this exporter may emit.
var LabelNames = []string{
	labelVersion, labelGoVersion, labelResult, labelReason, labelOperation,
	labelLimiter, labelOutcome, labelDecision, labelEventType, labelService,
	labelMethod, labelCode, labelRoute, labelStatus, labelFile,
}

// Fallback label values. Every helper below maps an unrecognised input onto one
// of these instead of passing it through, so a wrong value at a call site costs
// one bucket rather than leaking whatever string was passed.
const (
	// ValueOther is the fallback for a value that is not in its closed set.
	ValueOther = "other"
	// ValueUnknown is the fallback for an unregistered gRPC service or method.
	ValueUnknown = "unknown"
	// RouteUnmatched is the route label for a request no registered pattern
	// handled (404s and anything the mux did not claim).
	RouteUnmatched = "unmatched"
	// RouteStatic is the single route label covering every embedded frontend
	// asset: their paths are a build artifact, not a bounded API surface.
	RouteStatic = "static"
)

// RouteLabels is the closed set of values the route label may take: the static
// method-qualified patterns registered by the HTTP server's API mux, the
// unauthenticated probes and exempt endpoints it dispatches by path, plus the
// two catch-alls above.
//
// The exporter cannot import the HTTP server (that would invert the
// dependency), so this list is the contract: a transport that adds a route adds
// it here in the same change, and its own tests assert every pattern it
// registers is present. It is read once at package initialisation to build the
// lookup set, so it must not be mutated at runtime.
var RouteLabels = []string{
	"/healthz",
	"/readyz",
	"/metrics",
	"/api/v1/health",
	"/api/v1/ca",

	"POST /api/v1/auth/login",
	"GET /api/v1/whoami",

	"GET /api/v1/applications",
	"POST /api/v1/applications",
	"PATCH /api/v1/applications",
	"DELETE /api/v1/applications",
	"POST /api/v1/applications/archive",
	"POST /api/v1/applications/unarchive",
	"GET /api/v1/applications/get",
	"GET /api/v1/applications/dashboard",
	"GET /api/v1/applications/overview",
	"POST /api/v1/applications/ship",
	"POST /api/v1/applications/defaults",
	"POST /api/v1/applications/environments/clone",
	"PUT /api/v1/applications/parameters",

	"GET /api/v1/namespaces",
	"POST /api/v1/namespaces",
	"PATCH /api/v1/namespaces",
	"DELETE /api/v1/namespaces",

	"GET /api/v1/parameters",
	"GET /api/v1/parameters/get",
	"GET /api/v1/parameters/metadata",
	"PUT /api/v1/parameters",
	"DELETE /api/v1/parameters",

	"GET /api/v1/secrets",
	"GET /api/v1/secrets/metadata",
	"POST /api/v1/secrets",
	"POST /api/v1/secrets/reveal",
	"POST /api/v1/secrets/disable",
	"POST /api/v1/secrets/destroy",
	"POST /api/v1/secrets/promote",
	"POST /api/v1/secrets/bind",
	"POST /api/v1/secrets/unbind",
	"POST /api/v1/secrets/binding-cohort/preview",
	"POST /api/v1/secrets/binding-key/rotate",
	"POST /api/v1/secrets/binding-cohort/purge",
	"DELETE /api/v1/secrets",

	"GET /api/v1/policies",
	"POST /api/v1/policies",
	"PUT /api/v1/policies",
	"DELETE /api/v1/policies",

	"GET /api/v1/identities",
	"POST /api/v1/identities",
	"POST /api/v1/identities/rotate",
	"POST /api/v1/identities/issue-cert",
	"POST /api/v1/identities/revoke-cert",
	"POST /api/v1/identities/revoke",

	"GET /api/v1/audit",
	"GET /api/v1/posture",
	"GET /api/v1/subscribers",
	"GET /api/v1/release-subscribers",
	"GET /api/v1/release-subscribers/stream",

	"GET /api/v1/releases",
	"POST /api/v1/releases",
	"GET /api/v1/releases/get",
	"GET /api/v1/releases/active",
	"POST /api/v1/releases/validate",
	"POST /api/v1/releases/activate",
	"POST /api/v1/releases/rollback",

	"GET /api/v1/configuration-schemas",
	"POST /api/v1/configuration-schemas",
	"GET /api/v1/keys",

	RouteStatic,
	RouteUnmatched,
}

var routeLabelSet = stringSet(RouteLabels)

// RouteLabel maps a request's matched pattern onto the route label. Anything
// not in RouteLabels becomes RouteUnmatched, so a request path — which may
// carry a namespace or a key — can never reach the exposition.
func RouteLabel(pattern string) string {
	if _, ok := routeLabelSet[pattern]; ok {
		return pattern
	}
	return RouteUnmatched
}

// Transport-owned rate limiters. Core owns the rest (see core.Limiter*).
const (
	LimiterHTTPLogin   = "http_login"
	LimiterHTTPAuth    = "http_auth"
	LimiterSSEIdentity = "sse_identity"
	LimiterSSEGlobal   = "sse_global"
)

// LimiterNames is the closed set of limiter label values, transport-owned and
// core-owned together. Every name is pre-registered at zero so an alert on
// increase() fires from the first scrape rather than the second.
var LimiterNames = []string{
	LimiterHTTPLogin,
	LimiterHTTPAuth,
	LimiterSSEIdentity,
	LimiterSSEGlobal,
	core.LimiterVerifyDefaultsRequests,
	core.LimiterVerifyDefaultsMismatch,
}

var limiterSet = stringSet(LimiterNames)

// LimiterLabel maps a limiter name onto its label value.
func LimiterLabel(limiter string) string {
	if _, ok := limiterSet[limiter]; ok {
		return limiter
	}
	return ValueOther
}

var (
	authFailureReasonSet = stringSet(core.AuthFailureReasons)
	releaseOutcomeSet    = stringSet(core.ReleaseOutcomes)
	auditDecisionSet     = stringSet(core.AuditDecisions)
)

// authMethodSet is the closed set of the method label on
// kms_authz_method_denials_total: the credential a caller authenticated with,
// which is what a namespace's auth-method gate refuses on. core owns the list
// so the hook and the exporter cannot drift.
var authMethodSet = stringSet(core.AuthMethods)

// AuthMethodLabel maps an authentication method onto its label value.
func AuthMethodLabel(method string) string {
	if _, ok := authMethodSet[method]; ok {
		return method
	}
	return ValueOther
}

// AuditEventTypes is the closed set of the event_type label: every event type
// core writes to the audit log.
//
// Core spells these inline at the call sites rather than as constants, so this
// list is maintained alongside them; an event type missing from it is recorded
// under ValueOther and shows up as a jump in that bucket.
var AuditEventTypes = []string{
	"application.archive",
	"application.create",
	"application.defaults",
	"application.defaults.apply",
	"application.defaults.preview",
	"application.delete",
	"application.environment_clone",
	"application.list",
	"application.read",
	"application.release.create",
	"application.release.preview",
	"application.ship",
	"application.unarchive",
	"application.update",
	"audit.read",
	"auth.credential_ignored",
	"auth.failure",
	"authz.denial",
	"authz.method_denied",
	"configuration_release.acknowledge",
	"configuration_release.activate",
	"configuration_release.cas_conflict",
	"configuration_release.create",
	"configuration_release.reference_blocked",
	"configuration_release.rollback",
	"configuration_release.subscribers",
	"configuration_release.subscribers_stream",
	"configuration_release.validate",
	"configuration_release.verify_defaults",
	"configuration_schema.create",
	"configuration_schema.list",
	"configuration_schema.read",
	"console.ship",
	"identity.cert.issue",
	"identity.cert.revoke",
	"identity.read",
	"identity.write",
	"key.read",
	"key.rotate",
	"namespace.create",
	"namespace.delete",
	"namespace.update",
	"parameter.delete",
	"parameter.write",
	"policy.read",
	"policy.write",
	"posture.read",
	"secret.bind",
	"secret.binding_cohort.preview",
	"secret.binding_cohort.purge",
	"secret.binding_key.rotate",
	"secret.delete",
	"secret.destroy",
	"secret.disable",
	"secret.enable",
	"secret.promote",
	"secret.read",
	"secret.reveal",
	"secret.unbind",
	"secret.write",
	"subscribers.read",
}

var auditEventTypeSet = stringSet(AuditEventTypes)

// PolicyOperations is the closed set of policy operations, referenced from the
// domain constants so the compiler keeps it honest.
var PolicyOperations = []string{
	domain.OpParameterRead,
	domain.OpParameterWrite,
	domain.OpParameterList,
	domain.OpParameterDelete,
	domain.OpSecretRead,
	domain.OpSecretWrite,
	domain.OpSecretList,
	domain.OpSecretDisable,
	domain.OpSecretDestroy,
	domain.OpSecretPromote,
	domain.OpConfigurationReleaseCreate,
	domain.OpConfigurationReleaseRead,
	domain.OpConfigurationReleaseValidate,
	domain.OpConfigurationReleaseActivate,
	domain.OpConfigurationReleaseList,
	domain.OpConfigurationReleaseWatch,
	domain.OpConfigurationReleaseVerifyDefaults,
	domain.OpAdminNamespaceCreate,
	domain.OpAdminNamespaceUpdate,
	domain.OpAdminNamespaceDelete,
	domain.OpAdminIdentityCert,
	domain.OpAdminPolicyWrite,
	domain.OpAdminAuditRead,
	domain.OpAdminKeyRotate,
}

// operationSet is the operation label's closed set: a policy operation or, for
// the admin surface that has no policy operation of its own, the audit event
// type core denies under.
var operationSet = func() map[string]struct{} {
	set := stringSet(PolicyOperations)
	for _, t := range AuditEventTypes {
		set[t] = struct{}{}
	}
	return set
}()

// OperationLabel maps a denied operation onto its label value.
func OperationLabel(operation string) string {
	if _, ok := operationSet[operation]; ok {
		return operation
	}
	return ValueOther
}

// EventTypeLabel maps an audit event type onto its label value.
func EventTypeLabel(eventType string) string {
	if _, ok := auditEventTypeSet[eventType]; ok {
		return eventType
	}
	return ValueOther
}

// AuthFailureReasonLabel maps an authentication-failure reason onto its label
// value.
func AuthFailureReasonLabel(reason string) string {
	if _, ok := authFailureReasonSet[reason]; ok {
		return reason
	}
	return ValueOther
}

// ReleaseOutcomeLabel maps a release outcome onto its label value.
func ReleaseOutcomeLabel(outcome string) string {
	if _, ok := releaseOutcomeSet[outcome]; ok {
		return outcome
	}
	return ValueOther
}

// AuditDecisionLabel maps an audit decision onto its label value.
func AuditDecisionLabel(decision string) string {
	if _, ok := auditDecisionSet[decision]; ok {
		return decision
	}
	return ValueOther
}

// ReloadResults is the closed set of the result label on kms_reloads_total.
var ReloadResults = []string{ReloadApplied, ReloadRejected}

var reloadResultSet = stringSet(ReloadResults)

// ReloadResultLabel maps a reload result onto its label value.
func ReloadResultLabel(result string) string {
	if _, ok := reloadResultSet[result]; ok {
		return result
	}
	return ValueOther
}

// Database files whose size is sampled. The file label is closed to these.
const (
	DBFileMain = "main"
	DBFileWAL  = "wal"
)

// DBFiles is the closed set of the file label on kms_db_file_bytes. Sample
// publishes exactly these keys and ignores any other, so a sampler that
// reports a path cannot turn it into a series.
var DBFiles = []string{DBFileMain, DBFileWAL}

// httpMethods is the closed set of the HTTP method label: the standard verbs.
// Anything else — including the garbage a scanner sends — becomes ValueOther.
var httpMethods = stringSet([]string{
	"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "CONNECT", "OPTIONS", "TRACE",
})

// httpMethodLabel maps a request method onto its label value.
func httpMethodLabel(method string) string {
	if _, ok := httpMethods[method]; ok {
		return method
	}
	return ValueOther
}

// statusLabel renders an HTTP status code. Anything outside the range a
// response may legally carry is reported as ValueOther rather than as itself.
func statusLabel(status int) string {
	if status < 100 || status > 599 {
		return ValueOther
	}
	return strconv.Itoa(status)
}

// codeLabel renders a gRPC status code. Codes outside the defined range are
// reported as Unknown, keeping the label set finite.
func codeLabel(code codes.Code) string {
	if code > codes.Unauthenticated {
		return codes.Unknown.String()
	}
	return code.String()
}

// grpcServices maps each registered service's full name to its method names.
// It is derived from the generated file descriptor, so it cannot drift from
// the proto: a method that exists is labelled, a method that does not is
// ValueUnknown.
var grpcServices = func() map[string]map[string]struct{} {
	services := kmsv1.File_kms_v1_kms_proto.Services()
	out := make(map[string]map[string]struct{}, services.Len())
	for i := range services.Len() {
		svc := services.Get(i)
		methods := svc.Methods()
		names := make(map[string]struct{}, methods.Len())
		for j := range methods.Len() {
			names[string(methods.Get(j).Name())] = struct{}{}
		}
		out[string(svc.FullName())] = names
	}
	return out
}()

// splitFullMethod splits a gRPC full method name ("/kms.v1.KMSService/
// GetSecret") into its service and method parts. A name in any other shape
// yields two empty strings, which grpcLabels maps to ValueUnknown.
func splitFullMethod(fullMethod string) (service, method string) {
	trimmed := strings.TrimPrefix(fullMethod, "/")
	i := strings.Index(trimmed, "/")
	if i < 0 {
		return "", ""
	}
	return trimmed[:i], trimmed[i+1:]
}

// grpcLabels resolves a full method name to the service and method labels.
// An unregistered service collapses both to ValueUnknown; an unregistered
// method on a registered service keeps the service.
func grpcLabels(fullMethod string) (service, method string) {
	svc, mth := splitFullMethod(fullMethod)
	methods, ok := grpcServices[svc]
	if !ok {
		return ValueUnknown, ValueUnknown
	}
	if _, ok := methods[mth]; !ok {
		return svc, ValueUnknown
	}
	return svc, mth
}

// stringSet builds a lookup set from a closed label-value list.
func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}
