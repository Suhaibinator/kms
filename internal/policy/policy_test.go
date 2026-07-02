package policy

import (
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func rule(op, path string) domain.PolicyRule { return domain.PolicyRule{Operation: op, Path: path} }

func policyWith(name string, allow, deny []domain.PolicyRule) domain.Policy {
	return domain.Policy{Name: name, Subject: "s", Allow: allow, Deny: deny}
}

func TestEvaluateDefaultDeny(t *testing.T) {
	// No policies at all.
	if Evaluate(nil, domain.OpSecretRead, "/prod/x") {
		t.Fatal("empty policy set allowed access")
	}
	// A policy that matches nothing relevant.
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpParameterRead, "/prod/*")}, nil)}
	if Evaluate(ps, domain.OpSecretRead, "/prod/x") {
		t.Fatal("unrelated allow rule granted access")
	}
}

func TestEvaluateAllow(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(domain.OpSecretRead, "/prod/payments/*")}, nil)}
	if !Evaluate(ps, domain.OpSecretRead, "/prod/payments/stripe/api-key") {
		t.Fatal("matching allow rule did not grant access")
	}
	if Evaluate(ps, domain.OpSecretRead, "/prod/other/key") {
		t.Fatal("allow rule matched outside its subtree")
	}
	// Operation must match too.
	if Evaluate(ps, domain.OpSecretWrite, "/prod/payments/stripe/api-key") {
		t.Fatal("read allow rule granted write")
	}
}

func TestEvaluateDenyPrecedence(t *testing.T) {
	// Allow and deny both match => deny wins, regardless of policy/rule order.
	allow := []domain.PolicyRule{rule(domain.OpSecretRead, "/prod/payments/*")}
	deny := []domain.PolicyRule{rule(domain.OpSecretRead, "/prod/payments/admin/*")}

	ps := []domain.Policy{policyWith("p", allow, deny)}
	if Evaluate(ps, domain.OpSecretRead, "/prod/payments/stripe") == false {
		t.Fatal("non-denied path should be allowed")
	}
	if Evaluate(ps, domain.OpSecretRead, "/prod/payments/admin/root") {
		t.Fatal("deny rule did not override allow")
	}

	// Deny in a separate policy still overrides an allow in another policy.
	split := []domain.Policy{
		policyWith("allow-all", allow, nil),
		policyWith("deny-admin", nil, deny),
	}
	if Evaluate(split, domain.OpSecretRead, "/prod/payments/admin/root") {
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
		{"admin:*", domain.OpSecretRead, false},
		{"*", domain.OpSecretRead, true},
		{"*", domain.OpAdminAuditRead, true},
		{"secret:read", domain.OpSecretRead, true},
		{"secret:read", domain.OpSecretWrite, false},
	}
	for _, tt := range tests {
		ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule(tt.pattern, "/*")}, nil)}
		if got := Evaluate(ps, tt.op, "/any/path"); got != tt.want {
			t.Errorf("Evaluate(op pattern %q, op %q) = %v, want %v", tt.pattern, tt.op, got, tt.want)
		}
	}
}

func TestEvaluateGlobalPathWildcard(t *testing.T) {
	ps := []domain.Policy{policyWith("p", []domain.PolicyRule{rule("*", "/*")}, nil)}
	for _, p := range []string{"/a", "/a/b/c", "/prod/payments/admin"} {
		if !Evaluate(ps, domain.OpSecretDestroy, p) {
			t.Errorf("global allow did not match %q", p)
		}
	}
}

func TestEvaluateDenyWildcardBeatsSpecificAllow(t *testing.T) {
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretRead, "/prod/*")},
		[]domain.PolicyRule{rule("secret:*", "/prod/secrets/*")},
	)}
	if Evaluate(ps, domain.OpSecretRead, "/prod/secrets/db") {
		t.Fatal("category deny wildcard did not override specific allow")
	}
	if !Evaluate(ps, domain.OpSecretRead, "/prod/config/db") {
		t.Fatal("allow should still apply outside the denied subtree")
	}
}

func TestMayListUnder(t *testing.T) {
	tests := []struct {
		name   string
		allow  []domain.PolicyRule
		op     string
		prefix string
		want   bool
	}{
		{
			name:   "allow subtree contains prefix root",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "/prod/payments/*")},
			op:     domain.OpSecretList,
			prefix: "/prod",
			want:   true,
		},
		{
			name:   "prefix is deeper than allow subtree",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "/prod/payments/*")},
			op:     domain.OpSecretList,
			prefix: "/prod/payments/stripe",
			want:   true,
		},
		{
			name:   "disjoint subtrees",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "/prod/payments/*")},
			op:     domain.OpSecretList,
			prefix: "/staging",
			want:   false,
		},
		{
			name:   "category wildcard covers list op",
			allow:  []domain.PolicyRule{rule("secret:*", "/prod/payments/*")},
			op:     domain.OpSecretList,
			prefix: "/prod",
			want:   true,
		},
		{
			name:   "exact-path rule inside prefix",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "/prod/payments/key")},
			op:     domain.OpSecretList,
			prefix: "/prod",
			want:   true,
		},
		{
			name:   "exact-path rule outside prefix",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "/other/key")},
			op:     domain.OpSecretList,
			prefix: "/prod",
			want:   false,
		},
		{
			name:   "empty prefix matches any allow",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "/deep/nested/path/*")},
			op:     domain.OpSecretList,
			prefix: "",
			want:   true,
		},
		{
			name:   "global path wildcard",
			allow:  []domain.PolicyRule{rule(domain.OpSecretList, "/*")},
			op:     domain.OpSecretList,
			prefix: "/prod/payments",
			want:   true,
		},
		{
			name:   "operation does not match",
			allow:  []domain.PolicyRule{rule(domain.OpParameterRead, "/prod/*")},
			op:     domain.OpSecretList,
			prefix: "/prod",
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := []domain.Policy{policyWith("p", tt.allow, nil)}
			if got := MayListUnder(ps, tt.op, tt.prefix); got != tt.want {
				t.Errorf("MayListUnder(%q, %q) = %v, want %v", tt.op, tt.prefix, got, tt.want)
			}
		})
	}
}

// MayListUnder deliberately ignores deny rules (a deny only prunes items).
func TestMayListUnderIgnoresDeny(t *testing.T) {
	ps := []domain.Policy{policyWith("p",
		[]domain.PolicyRule{rule(domain.OpSecretList, "/prod/*")},
		[]domain.PolicyRule{rule(domain.OpSecretRead, "/prod/*")},
	)}
	if !MayListUnder(ps, domain.OpSecretList, "/prod") {
		t.Fatal("MayListUnder should ignore deny rules and still permit listing")
	}
}

func TestValidateRulesValid(t *testing.T) {
	in := domain.Policy{
		Name:    "gradethis-read",
		Subject: "gradethis-be",
		Allow: []domain.PolicyRule{
			rule(domain.OpSecretRead, "/prod/gradethis/*"),
			rule("secret:*", "/prod/other/"), // trailing slash trimmed
			rule("*", "*"),                   // "*" normalizes to "/*"
		},
		Deny: []domain.PolicyRule{rule(domain.OpSecretRead, "/prod/gradethis/admin/*")},
	}
	out, err := ValidateRules(in)
	if err != nil {
		t.Fatalf("ValidateRules: %v", err)
	}
	if out.Allow[1].Path != "/prod/other" {
		t.Fatalf("trailing slash not trimmed to exact path: %q", out.Allow[1].Path)
	}
	if out.Allow[2].Path != "/*" {
		t.Fatalf(`"*" not normalized to "/*": %q`, out.Allow[2].Path)
	}
	if out.Deny[0].Path != "/prod/gradethis/admin/*" {
		t.Fatalf("deny path changed unexpectedly: %q", out.Deny[0].Path)
	}
}

func TestValidateRulesRejects(t *testing.T) {
	base := func() domain.Policy {
		return domain.Policy{Name: "p", Subject: "s",
			Allow: []domain.PolicyRule{rule(domain.OpSecretRead, "/prod/*")}}
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
		p.Allow = []domain.PolicyRule{rule("secret:teleport", "/prod/*")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("bad operation wildcard category", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule("bogus:*", "/prod/*")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid path", func(t *testing.T) {
		p := base()
		p.Allow = []domain.PolicyRule{rule(domain.OpSecretRead, "prod/no-leading-slash")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
	t.Run("invalid deny path", func(t *testing.T) {
		p := base()
		p.Deny = []domain.PolicyRule{rule(domain.OpSecretRead, "/a/../b")}
		if _, err := ValidateRules(p); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})
}
