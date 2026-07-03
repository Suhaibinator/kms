package policy

import (
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// rule builds a namespace-level policy rule: an operation on (env, app), where
// env and app are exact or "*". There is no key scope.
func rule(op, env, app string) domain.PolicyRule {
	return domain.PolicyRule{Operation: op, Env: env, App: app}
}

func policyWith(name string, allow, deny []domain.PolicyRule) domain.Policy {
	return domain.Policy{Name: name, Subject: "s", Allow: allow, Deny: deny}
}

func ns(env, app string) domain.NamespaceRef {
	return domain.NamespaceRef{Env: env, App: app}
}

// evaluate is the pure rule-only check (no implicit home grant): Authorize with
// a nil home namespace, which skips the implicit grant and leaves the plain
// deny > allow > default-deny decision.
func evaluate(policies []domain.Policy, operation string, n domain.NamespaceRef) bool {
	return Authorize(policies, nil, operation, n)
}

func TestEvaluateDefaultDeny(t *testing.T) {
	if evaluate(nil, domain.OpSecretRead, ns("prod", "gradethis")) {
		t.Fatal("empty policy set allowed access")
	}
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpParameterRead, "prod", "*")}, nil)}
	if evaluate(ps, domain.OpSecretRead, ns("prod", "gradethis")) {
		t.Fatal("unrelated allow rule granted access")
	}
}

func TestEvaluateAllow(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "payments")}, nil)}
	if !evaluate(ps, domain.OpSecretRead, ns("prod", "payments")) {
		t.Fatal("matching allow rule did not grant access")
	}
	// App must match too.
	if evaluate(ps, domain.OpSecretRead, ns("prod", "billing")) {
		t.Fatal("allow rule matched a different app")
	}
	// Operation must match too.
	if evaluate(ps, domain.OpSecretWrite, ns("prod", "payments")) {
		t.Fatal("read allow rule granted write")
	}
}

func TestEvaluateEnvAppWildcards(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpSecretRead, "*", "*")}, nil)}
	if !evaluate(ps, domain.OpSecretRead, ns("prod", "a")) {
		t.Fatal("env/app wildcard did not match prod/a")
	}
	if !evaluate(ps, domain.OpSecretRead, ns("staging", "b")) {
		t.Fatal("env/app wildcard did not match staging/b")
	}
	if evaluate(ps, domain.OpParameterRead, ns("prod", "a")) {
		t.Fatal("operation mismatch should not match")
	}
}

func TestEvaluateDenyPrecedence(t *testing.T) {
	allow := []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "*")}
	deny := []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "payments")}

	ps := []domain.Policy{policyWith("p", allow, deny)}
	if !evaluate(ps, domain.OpSecretRead, ns("prod", "billing")) {
		t.Fatal("non-denied namespace should be allowed")
	}
	if evaluate(ps, domain.OpSecretRead, ns("prod", "payments")) {
		t.Fatal("deny rule did not override allow")
	}

	// Deny in a separate policy still overrides an allow in another policy.
	split := []domain.Policy{
		policyWith("allow-all", allow, nil),
		policyWith("deny-payments", nil, deny),
	}
	if evaluate(split, domain.OpSecretRead, ns("prod", "payments")) {
		t.Fatal("cross-policy deny did not override allow")
	}
}

func TestEvaluateOperationWildcards(t *testing.T) {
	tests := []struct {
		pattern string
		op      string
		want    bool
	}{
		{"secret:*", domain.OpSecretRead, true},
		{"secret:*", domain.OpSecretDestroy, true},
		{"secret:*", domain.OpParameterRead, false},
		{"parameter:*", domain.OpParameterList, true},
		{"admin:*", domain.OpAdminKeyRotate, true},
		{"admin:*", domain.OpAdminNamespaceCreate, true}, // multi-segment op
		{"admin:*", domain.OpSecretRead, false},
		{"*", domain.OpSecretRead, true},
		{"*", domain.OpAdminAuditRead, true},
		{"secret:read", domain.OpSecretRead, true},
		{"secret:read", domain.OpSecretWrite, false},
	}
	for _, tt := range tests {
		ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(tt.pattern, "*", "*")}, nil)}
		if got := evaluate(ps, tt.op, ns("any", "any")); got != tt.want {
			t.Errorf("evaluate(op pattern %q, op %q) = %v, want %v", tt.pattern, tt.op, got, tt.want)
		}
	}
}

func TestEvaluateGlobalWildcard(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule("*", "*", "*")}, nil)}
	nss := []domain.NamespaceRef{ns("a", "b"), ns("prod", "payments"), ns("x", "y")}
	for _, n := range nss {
		if !evaluate(ps, domain.OpSecretDestroy, n) {
			t.Errorf("global allow did not match %v", n)
		}
	}
}

func TestEvaluateDenyWildcardBeatsSpecificAllow(t *testing.T) {
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "*")},
		[]domain.PolicyRule{rule("secret:*", "prod", "gradethis")},
	)}
	if evaluate(ps, domain.OpSecretRead, ns("prod", "gradethis")) {
		t.Fatal("category deny wildcard did not override specific allow")
	}
	if !evaluate(ps, domain.OpSecretRead, ns("prod", "other")) {
		t.Fatal("allow should still apply outside the denied namespace")
	}
}

func TestImplicitHomeGrant(t *testing.T) {
	home := &domain.NamespaceRef{Env: "prod", App: "gradethis"}

	// Read/list in the home namespace is allowed with no policies at all.
	for _, op := range []string{domain.OpParameterRead, domain.OpParameterList, domain.OpSecretRead, domain.OpSecretList} {
		if !Authorize(nil, home, op, ns("prod", "gradethis")) {
			t.Errorf("implicit grant did not allow %s in home namespace", op)
		}
	}

	// Writes and other mutations are NOT implicitly granted.
	for _, op := range []string{domain.OpParameterWrite, domain.OpSecretWrite, domain.OpSecretDestroy, domain.OpParameterDelete} {
		if Authorize(nil, home, op, ns("prod", "gradethis")) {
			t.Errorf("implicit grant wrongly allowed mutation %s", op)
		}
	}

	// A different namespace gets nothing implicitly.
	if Authorize(nil, home, domain.OpSecretRead, ns("prod", "other")) {
		t.Fatal("implicit grant leaked into another namespace")
	}

	// An unbound caller gets nothing implicitly.
	if Authorize(nil, nil, domain.OpSecretRead, ns("prod", "gradethis")) {
		t.Fatal("unbound caller got an implicit grant")
	}
}

func TestImplicitGrantDoesNotOverrideDeny(t *testing.T) {
	home := &domain.NamespaceRef{Env: "prod", App: "gradethis"}
	// An explicit deny on the home namespace must beat the implicit read grant.
	ps := []domain.Policy{policyWith("p", nil,
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis")},
	)}
	if Authorize(ps, home, domain.OpSecretRead, ns("prod", "gradethis")) {
		t.Fatal("deny did not override the implicit home grant")
	}
	// A non-denied operation in the home namespace still rides the implicit grant.
	if !Authorize(ps, home, domain.OpParameterRead, ns("prod", "gradethis")) {
		t.Fatal("implicit grant should still apply to non-denied operations")
	}
}

func TestAuthorizeFallsBackToExplicitAllow(t *testing.T) {
	home := &domain.NamespaceRef{Env: "prod", App: "gradethis"}
	// A write in the home namespace needs an explicit allow (no implicit grant).
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretWrite, "prod", "gradethis")}, nil)}
	if !Authorize(ps, home, domain.OpSecretWrite, ns("prod", "gradethis")) {
		t.Fatal("explicit allow for a write was not honored")
	}
	// Cross-namespace read needs an explicit allow too.
	cross := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "other")}, nil)}
	if !Authorize(cross, home, domain.OpSecretRead, ns("prod", "other")) {
		t.Fatal("cross-namespace explicit allow was not honored")
	}
}

func TestMayListUnder(t *testing.T) {
	tests := []struct {
		name  string
		allow []domain.PolicyRule
		op    string
		ns    domain.NamespaceRef
		want  bool
	}{
		{
			name:  "allow on namespace",
			allow: []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments")},
			op:    domain.OpSecretList,
			ns:    ns("prod", "payments"),
			want:  true,
		},
		{
			name:  "different namespace",
			allow: []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments")},
			op:    domain.OpSecretList,
			ns:    ns("staging", "payments"),
			want:  false,
		},
		{
			name:  "namespace wildcard",
			allow: []domain.PolicyRule{rule(domain.OpSecretList, "*", "*")},
			op:    domain.OpSecretList,
			ns:    ns("prod", "payments"),
			want:  true,
		},
		{
			name:  "category wildcard covers list op",
			allow: []domain.PolicyRule{rule("secret:*", "prod", "payments")},
			op:    domain.OpSecretList,
			ns:    ns("prod", "payments"),
			want:  true,
		},
		{
			name:  "operation does not match",
			allow: []domain.PolicyRule{rule(domain.OpParameterRead, "prod", "payments")},
			op:    domain.OpSecretList,
			ns:    ns("prod", "payments"),
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := []domain.Policy{policyWith("p", tt.allow, nil)}
			if got := MayListUnder(ps, tt.op, tt.ns); got != tt.want {
				t.Errorf("MayListUnder(%q, %v) = %v, want %v", tt.op, tt.ns, got, tt.want)
			}
		})
	}
}

// MayListUnder deliberately ignores deny rules (deny precedence is applied per
// item by Authorize).
func TestMayListUnderIgnoresDeny(t *testing.T) {
	n := ns("prod", "gradethis")
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretList, "prod", "gradethis")},
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis")},
	)}
	if !MayListUnder(ps, domain.OpSecretList, n) {
		t.Fatal("MayListUnder should ignore deny rules and still permit listing")
	}
}

func TestValidateRulesValid(t *testing.T) {
	in := domain.Policy{
		Name:    "gradethis-read",
		Subject: "gradethis-be",
		Allow: []domain.PolicyRule{
			rule(domain.OpSecretRead, "prod", "gradethis"),
			rule("secret:*", "prod", "other"),
			rule("*", "", ""), // empties normalize to "*"
		},
		Deny: []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis")},
	}
	out, err := ValidateRules(in)
	if err != nil {
		t.Fatalf("ValidateRules: %v", err)
	}
	if out.Allow[1].Env != "prod" || out.Allow[1].App != "other" {
		t.Fatalf("rule scope changed: %+v", out.Allow[1])
	}
	if out.Allow[2].Env != "*" || out.Allow[2].App != "*" {
		t.Fatalf("empty components not normalized to *: %+v", out.Allow[2])
	}
}

func TestValidateRulesRejects(t *testing.T) {
	base := func() domain.Policy {
		return domain.Policy{Name: "p", Subject: "s",
			Allow: []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis")}}
	}

	t.Run("missing name", func(t *testing.T) {
		p := base()
		p.Name = ""
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("missing subject", func(t *testing.T) {
		p := base()
		p.Subject = ""
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("unknown operation", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule("secret:teleport", "prod", "gradethis")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("bad operation wildcard category", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule("bogus:*", "prod", "gradethis")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid env", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule(domain.OpSecretRead, "Prod!", "gradethis")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid app", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "-bad")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
}
