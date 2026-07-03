package policy

import (
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// rule builds a policy rule from an operation and a "env/app/key" scope, where
// env and app are exact or "*" and key is exact, "*", or "prefix/*".
func rule(op, env, app, key string) domain.PolicyRule {
	return domain.PolicyRule{Operation: op, Env: env, App: app, KeyPattern: key}
}

func policyWith(name string, allow, deny []domain.PolicyRule) domain.Policy {
	return domain.Policy{Name: name, Subject: "s", Allow: allow, Deny: deny}
}

func ref(env, app, key string) domain.Ref {
	return domain.Ref{NS: domain.NamespaceRef{Env: env, App: app}, Key: key}
}

func TestEvaluateDefaultDeny(t *testing.T) {
	if Evaluate(nil, domain.OpSecretRead, ref("prod", "gradethis", "x")) {
		t.Fatal("empty policy set allowed access")
	}
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpParameterRead, "prod", "*", "*")}, nil)}
	if Evaluate(ps, domain.OpSecretRead, ref("prod", "gradethis", "x")) {
		t.Fatal("unrelated allow rule granted access")
	}
}

func TestEvaluateAllow(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "payments", "stripe/*")}, nil)}
	if !Evaluate(ps, domain.OpSecretRead, ref("prod", "payments", "stripe/api-key")) {
		t.Fatal("matching allow rule did not grant access")
	}
	if Evaluate(ps, domain.OpSecretRead, ref("prod", "payments", "other/key")) {
		t.Fatal("allow rule matched outside its key subtree")
	}
	// App must match too.
	if Evaluate(ps, domain.OpSecretRead, ref("prod", "billing", "stripe/api-key")) {
		t.Fatal("allow rule matched a different app")
	}
	// Operation must match too.
	if Evaluate(ps, domain.OpSecretWrite, ref("prod", "payments", "stripe/api-key")) {
		t.Fatal("read allow rule granted write")
	}
}

func TestEvaluateEnvAppWildcards(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpSecretRead, "*", "*", "db")}, nil)}
	if !Evaluate(ps, domain.OpSecretRead, ref("prod", "a", "db")) {
		t.Fatal("env/app wildcard did not match prod/a")
	}
	if !Evaluate(ps, domain.OpSecretRead, ref("staging", "b", "db")) {
		t.Fatal("env/app wildcard did not match staging/b")
	}
	if Evaluate(ps, domain.OpSecretRead, ref("prod", "a", "other")) {
		t.Fatal("key mismatch should not match")
	}
}

func TestEvaluateDenyPrecedence(t *testing.T) {
	allow := []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "payments", "*")}
	deny := []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "payments", "admin/*")}

	ps := []domain.Policy{policyWith("p", allow, deny)}
	if !Evaluate(ps, domain.OpSecretRead, ref("prod", "payments", "stripe")) {
		t.Fatal("non-denied key should be allowed")
	}
	if Evaluate(ps, domain.OpSecretRead, ref("prod", "payments", "admin/root")) {
		t.Fatal("deny rule did not override allow")
	}

	// Deny in a separate policy still overrides an allow in another policy.
	split := []domain.Policy{
		policyWith("allow-all", allow, nil),
		policyWith("deny-admin", nil, deny),
	}
	if Evaluate(split, domain.OpSecretRead, ref("prod", "payments", "admin/root")) {
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
		ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(tt.pattern, "*", "*", "*")}, nil)}
		if got := Evaluate(ps, tt.op, ref("any", "any", "path")); got != tt.want {
			t.Errorf("Evaluate(op pattern %q, op %q) = %v, want %v", tt.pattern, tt.op, got, tt.want)
		}
	}
}

func TestEvaluateGlobalWildcard(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule("*", "*", "*", "*")}, nil)}
	refs := []domain.Ref{ref("a", "b", "c"), ref("prod", "payments", "admin"), ref("x", "y", "a/b/c")}
	for _, r := range refs {
		if !Evaluate(ps, domain.OpSecretDestroy, r) {
			t.Errorf("global allow did not match %v", r)
		}
	}
}

func TestEvaluateDenyWildcardBeatsSpecificAllow(t *testing.T) {
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "*", "*")},
		[]domain.PolicyRule{rule("secret:*", "prod", "gradethis", "secrets/*")},
	)}
	if Evaluate(ps, domain.OpSecretRead, ref("prod", "gradethis", "secrets/db")) {
		t.Fatal("category deny wildcard did not override specific allow")
	}
	if !Evaluate(ps, domain.OpSecretRead, ref("prod", "gradethis", "config/db")) {
		t.Fatal("allow should still apply outside the denied subtree")
	}
}

func TestImplicitHomeGrant(t *testing.T) {
	home := &domain.NamespaceRef{Env: "prod", App: "gradethis"}

	// Read/list in the home namespace is allowed with no policies at all.
	for _, op := range []string{domain.OpParameterRead, domain.OpParameterList, domain.OpSecretRead, domain.OpSecretList} {
		if !Authorize(nil, home, op, ref("prod", "gradethis", "rate-limit")) {
			t.Errorf("implicit grant did not allow %s in home namespace", op)
		}
	}

	// Writes and other mutations are NOT implicitly granted.
	for _, op := range []string{domain.OpParameterWrite, domain.OpSecretWrite, domain.OpSecretDestroy, domain.OpParameterDelete} {
		if Authorize(nil, home, op, ref("prod", "gradethis", "rate-limit")) {
			t.Errorf("implicit grant wrongly allowed mutation %s", op)
		}
	}

	// A different namespace gets nothing implicitly.
	if Authorize(nil, home, domain.OpSecretRead, ref("prod", "other", "k")) {
		t.Fatal("implicit grant leaked into another namespace")
	}

	// An unbound caller gets nothing implicitly.
	if Authorize(nil, nil, domain.OpSecretRead, ref("prod", "gradethis", "k")) {
		t.Fatal("unbound caller got an implicit grant")
	}
}

func TestImplicitGrantDoesNotOverrideDeny(t *testing.T) {
	home := &domain.NamespaceRef{Env: "prod", App: "gradethis"}
	// An explicit deny on a sub-key must beat the implicit read grant.
	ps := []domain.Policy{policyWith("p", nil,
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis", "billing/*")},
	)}
	if Authorize(ps, home, domain.OpSecretRead, ref("prod", "gradethis", "billing/stripe")) {
		t.Fatal("deny did not override the implicit home grant")
	}
	// Non-denied keys in the home namespace still ride the implicit grant.
	if !Authorize(ps, home, domain.OpSecretRead, ref("prod", "gradethis", "rate-limit")) {
		t.Fatal("implicit grant should still apply to non-denied keys")
	}
}

func TestAuthorizeFallsBackToExplicitAllow(t *testing.T) {
	home := &domain.NamespaceRef{Env: "prod", App: "gradethis"}
	// A write in the home namespace needs an explicit allow (no implicit grant).
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretWrite, "prod", "gradethis", "*")}, nil)}
	if !Authorize(ps, home, domain.OpSecretWrite, ref("prod", "gradethis", "k")) {
		t.Fatal("explicit allow for a write was not honored")
	}
	// Cross-namespace read needs an explicit allow too.
	cross := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "other", "*")}, nil)}
	if !Authorize(cross, home, domain.OpSecretRead, ref("prod", "other", "k")) {
		t.Fatal("cross-namespace explicit allow was not honored")
	}
}

func TestMayListUnder(t *testing.T) {
	ns := func(env, app string) domain.NamespaceRef { return domain.NamespaceRef{Env: env, App: app} }
	tests := []struct {
		name   string
		allow  []domain.PolicyRule
		op     string
		ns     domain.NamespaceRef
		prefix string
		want   bool
	}{
		{
			name:   "allow subtree contains prefix root",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments", "stripe/*")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "",
			want:   true,
		},
		{
			name:   "prefix is deeper than allow subtree",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments", "stripe/*")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "stripe/keys",
			want:   true,
		},
		{
			name:   "disjoint key subtrees",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments", "stripe/*")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "billing",
			want:   false,
		},
		{
			name:   "different namespace",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments", "*")},
			op:     domain.OpSecretList,
			ns:     ns("staging", "payments"),
			prefix: "",
			want:   false,
		},
		{
			name:   "namespace wildcard",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "*", "*", "*")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "anything",
			want:   true,
		},
		{
			name:   "category wildcard covers list op",
			allow:  []domain.PolicyRule{rule("secret:*", "prod", "payments", "stripe/*")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "",
			want:   true,
		},
		{
			name:   "exact-key rule inside prefix",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments", "billing/key")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "billing",
			want:   true,
		},
		{
			name:   "exact-key rule outside prefix",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "prod", "payments", "other/key")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "billing",
			want:   false,
		},
		{
			name:   "operation does not match",
			allow:  []domain.PolicyRule{rule(domain.OpParameterRead, "prod", "payments", "*")},
			op:     domain.OpSecretList,
			ns:     ns("prod", "payments"),
			prefix: "",
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := []domain.Policy{policyWith("p", tt.allow, nil)}
			if got := MayListUnder(ps, tt.op, tt.ns, tt.prefix); got != tt.want {
				t.Errorf("MayListUnder(%q, %v, %q) = %v, want %v", tt.op, tt.ns, tt.prefix, got, tt.want)
			}
		})
	}
}

// MayListUnder deliberately ignores deny rules (a deny only prunes items).
func TestMayListUnderIgnoresDeny(t *testing.T) {
	ns := domain.NamespaceRef{Env: "prod", App: "gradethis"}
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretList, "prod", "gradethis", "*")},
		[]domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis", "*")},
	)}
	if !MayListUnder(ps, domain.OpSecretList, ns, "") {
		t.Fatal("MayListUnder should ignore deny rules and still permit listing")
	}
}

func TestValidateRulesValid(t *testing.T) {
	in := domain.Policy{
		Name:    "gradethis-read",
		Subject: "gradethis-be",
		Allow: []domain.PolicyRule{
			rule(domain.OpSecretRead, "prod", "gradethis", "billing/*"),
			rule("secret:*", "prod", "other", "config"), // exact key preserved
			rule("*", "", "", ""),                        // empties normalize to "*"
		},
		Deny: []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis", "billing/admin/*")},
	}
	out, err := ValidateRules(in)
	if err != nil {
		t.Fatalf("ValidateRules: %v", err)
	}
	if out.Allow[1].KeyPattern != "config" {
		t.Fatalf("exact key changed: %q", out.Allow[1].KeyPattern)
	}
	if out.Allow[2].Env != "*" || out.Allow[2].App != "*" || out.Allow[2].KeyPattern != "*" {
		t.Fatalf("empty components not normalized to *: %+v", out.Allow[2])
	}
	if out.Deny[0].KeyPattern != "billing/admin/*" {
		t.Fatalf("deny key changed unexpectedly: %q", out.Deny[0].KeyPattern)
	}
}

func TestValidateRulesRejects(t *testing.T) {
	base := func() domain.Policy {
		return domain.Policy{Name: "p", Subject: "s",
			Allow: []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis", "*")}}
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
		p.Allow = []domain.PolicyRule{rule("secret:teleport", "prod", "gradethis", "*")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("bad operation wildcard category", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule("bogus:*", "prod", "gradethis", "*")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid env", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule(domain.OpSecretRead, "Prod!", "gradethis", "*")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid app", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "-bad", "*")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid key pattern", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis", "a/../b")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid deny key", func(t *testing.T) {
		p := base()
		p.Deny = []domain.PolicyRule{rule(domain.OpSecretRead, "prod", "gradethis", "/leading")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
}
