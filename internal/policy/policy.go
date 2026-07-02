// Package policy implements path-based authorization with deny precedence.
//
// A policy binds a subject (identity name or "*") to allow and deny rules.
// Each rule pairs an operation pattern with a path pattern:
//
//	operation: "secret:read" | "secret:*" | "parameter:*" | "admin:*" | "*"
//	path:      exact canonical path, "base/*" prefix pattern, or "/*"
//
// Evaluation (default deny):
//  1. If any deny rule in any applicable policy matches (operation, path) -> deny.
//  2. Else if any allow rule matches -> allow.
//  3. Else -> deny.
package policy

import (
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/pathutil"
)

// Evaluate reports whether the given operation on path is allowed by the
// supplied policies (already filtered to the subject and "*" by the caller).
func Evaluate(policies []domain.Policy, operation, path string) bool {
	for _, p := range policies {
		for _, r := range p.Deny {
			if ruleMatches(r, operation, path) {
				return false
			}
		}
	}
	for _, p := range policies {
		for _, r := range p.Allow {
			if ruleMatches(r, operation, path) {
				return true
			}
		}
	}
	return false
}

func ruleMatches(r domain.PolicyRule, operation, path string) bool {
	return operationMatches(r.Operation, operation) && pathutil.Match(r.Path, path)
}

// operationMatches supports exact operations, category wildcards like
// "secret:*", and the global wildcard "*".
func operationMatches(pattern, op string) bool {
	if pattern == "*" {
		return true
	}
	if cat, ok := strings.CutSuffix(pattern, ":*"); ok {
		return strings.HasPrefix(op, cat+":")
	}
	return pattern == op
}

// MayListUnder reports whether a list operation rooted at prefix could
// possibly return anything for this subject: true when at least one allow
// rule's path pattern intersects the prefix subtree. Callers must still
// filter individual results with Evaluate — deny rules are intentionally
// ignored here because a deny on a sub-path only prunes items, it does not
// forbid listing the rest of the subtree.
func MayListUnder(policies []domain.Policy, operation, prefix string) bool {
	for _, p := range policies {
		for _, r := range p.Allow {
			if !operationMatches(r.Operation, operation) {
				continue
			}
			if patternIntersectsSubtree(r.Path, prefix) {
				return true
			}
		}
	}
	return false
}

// patternIntersectsSubtree reports whether the rule path pattern can match
// any path inside the subtree rooted at prefix ("" or "/" = whole tree).
func patternIntersectsSubtree(pattern, prefix string) bool {
	if prefix == "" || prefix == "/" || pattern == "/*" {
		return true
	}
	if base, ok := strings.CutSuffix(pattern, "/*"); ok {
		// Subtrees intersect when one root contains the other.
		return pathutil.HasPrefix(base, prefix) || pathutil.HasPrefix(prefix, base)
	}
	// Exact-path rule: it must live inside the prefix subtree.
	return pathutil.HasPrefix(pattern, prefix)
}

// ValidateRules canonicalizes every rule in the policy (paths through
// pathutil.NormalizePrefix, operations against the known set) and returns the
// normalized policy or an error naming the offending rule.
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
			if !validOperationPattern(r.Operation) {
				return nil, domain.Errorf(domain.ErrInvalidArgument,
					"policy %q %s rule %d: unknown operation %q", p.Name, kind, i, r.Operation)
			}
			np, err := pathutil.NormalizePrefix(r.Path)
			if err != nil {
				return nil, domain.Errorf(domain.ErrInvalidArgument,
					"policy %q %s rule %d: %v", p.Name, kind, i, err)
			}
			out = append(out, domain.PolicyRule{Operation: r.Operation, Path: np})
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

var knownOperations = map[string]bool{
	domain.OpParameterRead: true, domain.OpParameterWrite: true,
	domain.OpParameterList: true, domain.OpParameterDelete: true,
	domain.OpSecretRead: true, domain.OpSecretWrite: true,
	domain.OpSecretList: true, domain.OpSecretDisable: true,
	domain.OpSecretDestroy: true, domain.OpSecretPromote: true,
	domain.OpAdminNamespaceCreate: true, domain.OpAdminPolicyWrite: true,
	domain.OpAdminAuditRead: true, domain.OpAdminKeyRotate: true,
}

func validOperationPattern(op string) bool {
	if op == "*" {
		return true
	}
	if cat, ok := strings.CutSuffix(op, ":*"); ok {
		switch cat {
		case "parameter", "secret", "admin":
			return true
		}
		return false
	}
	return knownOperations[op]
}
