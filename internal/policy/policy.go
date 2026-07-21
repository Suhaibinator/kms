// Package policy implements namespace-native authorization with deny precedence.
//
// A policy binds a subject (identity name or "*") to allow and deny rules.
// Each rule pairs an operation pattern with a namespace scope (env, app):
//
//	operation: "secret:read" | "secret:*" | "parameter:*" | "configuration-release:*" | "admin:*" | "*"
//	env / app: an exact value or "*"
//
// Authorization is per-namespace: a grant covers a whole namespace, every key
// in it. There is no key-level scoping.
//
// Evaluation (default deny), over policies already filtered to the subject and
// "*" by the caller:
//  1. If any deny rule matches (operation, ns) -> deny.
//  2. Else if any allow rule matches -> allow.
//  3. Else -> deny.
//
// Authorize additionally honors the implicit home-namespace grant (plan §3,
// §6): a namespace-bound identity may perform read/list operations within its
// own namespace with no explicit allow rule. Deny rules still override the
// implicit grant.
package policy

import (
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// Authorize applies the full precedence for a namespaced operation:
// deny > implicit home grant > explicit allow > default deny. home is the
// caller's bound namespace, or nil when the caller is unbound (admin/tooling).
func Authorize(policies []domain.Policy, home *domain.NamespaceRef, operation string, ns domain.NamespaceRef) bool {
	if deniedBy(policies, operation, ns) {
		return false
	}
	if HasImplicitHomeGrant(home, operation, ns) {
		return true
	}
	return allowedBy(policies, operation, ns)
}

func deniedBy(policies []domain.Policy, operation string, ns domain.NamespaceRef) bool {
	for _, p := range policies {
		for _, r := range p.Deny {
			if ruleMatches(r, operation, ns) {
				return true
			}
		}
	}
	return false
}

func allowedBy(policies []domain.Policy, operation string, ns domain.NamespaceRef) bool {
	for _, p := range policies {
		for _, r := range p.Allow {
			if ruleMatches(r, operation, ns) {
				return true
			}
		}
	}
	return false
}

// implicitHomeOps are the operations a namespace-bound identity may perform in
// its own namespace with no explicit policy (plan §3, §6): parameter and secret
// reads and lists. Subscribe is authorized by core as a continuous
// parameter:read within the home namespace and therefore rides on the same
// grant. Writes, deletes, disables, destroys, and promotes always require an
// explicit allow rule.
var implicitHomeOps = map[string]bool{
	domain.OpParameterRead:             true,
	domain.OpParameterList:             true,
	domain.OpSecretRead:                true,
	domain.OpSecretList:                true,
	domain.OpConfigurationReleaseRead:  true,
	domain.OpConfigurationReleaseWatch: true,
}

// IsImplicitHomeOp reports whether operation is in the implicit home-namespace
// grant set.
func IsImplicitHomeOp(operation string) bool {
	return implicitHomeOps[operation]
}

// HasImplicitHomeGrant reports whether the implicit home-namespace grant alone
// (before considering deny rules) would permit operation on ns for a caller
// whose bound namespace is home. It is true only when the caller is bound, ns
// is the caller's own namespace, and the operation is in the implicit set.
// Callers must still apply deny precedence (Authorize does this).
func HasImplicitHomeGrant(home *domain.NamespaceRef, operation string, ns domain.NamespaceRef) bool {
	if home == nil {
		return false
	}
	if !IsImplicitHomeOp(operation) {
		return false
	}
	return *home == ns
}

func ruleMatches(r domain.PolicyRule, operation string, ns domain.NamespaceRef) bool {
	return operationMatches(r.Operation, operation) &&
		labelMatches(r.Env, ns.Env) &&
		labelMatches(r.App, ns.App)
}

// labelMatches matches an env or app rule component: exact or "*".
func labelMatches(pattern, value string) bool {
	return pattern == "*" || pattern == value
}

// operationMatches supports exact operations, category wildcards like
// "secret:*" (also matching multi-segment ops such as "admin:namespace:create"),
// and the global wildcard "*".
func operationMatches(pattern, op string) bool {
	if pattern == "*" {
		return true
	}
	if cat, ok := strings.CutSuffix(pattern, ":*"); ok {
		return strings.HasPrefix(op, cat+":")
	}
	return pattern == op
}

// MayListUnder reports whether at least one allow rule grants operation on ns
// for this subject. Because authorization is namespace-level, a subject that may
// list a namespace may see every key in it. Deny rules are intentionally not
// consulted here — core applies deny precedence per item via Authorize; this is
// only the "may the subject enumerate this namespace at all" gate. It does not
// consult the implicit home-namespace grant; core short-circuits home-namespace
// lists separately.
func MayListUnder(policies []domain.Policy, operation string, ns domain.NamespaceRef) bool {
	return allowedBy(policies, operation, ns)
}

// ValidateRules canonicalizes every rule in the policy (operation against the
// known set; env/app normalized and validated) and returns the normalized
// policy or an error naming the offending rule. An empty env or app component
// normalizes to "*" (unscoped).
func ValidateRules(p domain.Policy) (domain.Policy, error) {
	if p.Name == "" {
		return p, domain.Errorf(domain.ErrInvalidArgument, "policy name is required")
	}
	if p.Subject == "" {
		return p, domain.Errorf(domain.ErrInvalidArgument, "policy subject is required")
	}
	normalize := func(rules []domain.PolicyRule, kind string) ([]domain.PolicyRule, error) {
		out := make([]domain.PolicyRule, 0, len(rules))
		for i, r := range rules {
			nr, err := normalizeRule(r)
			if err != nil {
				return nil, domain.Errorf(domain.ErrInvalidArgument,
					"policy %q %s rule %d: %v", p.Name, kind, i, err)
			}
			out = append(out, nr)
		}
		return out, nil
	}
	var err error
	if p.Allow, err = normalize(p.Allow, "allow"); err != nil {
		return p, err
	}
	if p.Deny, err = normalize(p.Deny, "deny"); err != nil {
		return p, err
	}
	return p, nil
}

func normalizeRule(r domain.PolicyRule) (domain.PolicyRule, error) {
	if !validOperationPattern(r.Operation) {
		return r, domain.Errorf(domain.ErrInvalidArgument, "unknown operation %q", r.Operation)
	}
	env, err := normalizeLabel(r.Env, keyutil.ValidateEnv)
	if err != nil {
		return r, err
	}
	app, err := normalizeLabel(r.App, keyutil.ValidateApp)
	if err != nil {
		return r, err
	}
	return domain.PolicyRule{Operation: r.Operation, Env: env, App: app}, nil
}

// normalizeLabel normalizes an env/app rule component: "" or "*" become "*";
// anything else must pass the supplied validator.
func normalizeLabel(v string, validate func(string) error) (string, error) {
	if v == "" || v == "*" {
		return "*", nil
	}
	if err := validate(v); err != nil {
		return "", err
	}
	return v, nil
}

var knownOperations = map[string]bool{
	domain.OpParameterRead: true, domain.OpParameterWrite: true,
	domain.OpParameterList: true, domain.OpParameterDelete: true,
	domain.OpSecretRead: true, domain.OpSecretWrite: true,
	domain.OpSecretList: true, domain.OpSecretDisable: true,
	domain.OpSecretDestroy: true, domain.OpSecretPromote: true,
	domain.OpConfigurationReleaseCreate: true, domain.OpConfigurationReleaseRead: true,
	domain.OpConfigurationReleaseValidate: true, domain.OpConfigurationReleaseActivate: true,
	domain.OpConfigurationReleaseList: true, domain.OpConfigurationReleaseWatch: true,
	domain.OpAdminNamespaceCreate: true, domain.OpAdminNamespaceUpdate: true,
	domain.OpAdminNamespaceDelete: true, domain.OpAdminIdentityCert: true,
	domain.OpAdminPolicyWrite: true, domain.OpAdminAuditRead: true,
	domain.OpAdminKeyRotate: true,
}

func validOperationPattern(op string) bool {
	if op == "*" {
		return true
	}
	if cat, ok := strings.CutSuffix(op, ":*"); ok {
		switch cat {
		case "parameter", "secret", "configuration-release", "admin":
			return true
		}
		return false
	}
	return knownOperations[op]
}
