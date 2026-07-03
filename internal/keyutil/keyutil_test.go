package keyutil

import (
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestValidateEnvApp(t *testing.T) {
	valid := []string{"prod", "a", "gradethis", "us-east-1", "x-y-z", strings.Repeat("a", MaxLabelLength)}
	for _, s := range valid {
		if err := ValidateEnv(s); err != nil {
			t.Errorf("ValidateEnv(%q) unexpected error: %v", s, err)
		}
		if err := ValidateApp(s); err != nil {
			t.Errorf("ValidateApp(%q) unexpected error: %v", s, err)
		}
	}
	invalid := []string{
		"",
		"-prod",
		"prod-",
		"Prod",         // uppercase
		"pr_od",        // underscore not allowed in labels
		"pr.od",        // dot not allowed in labels
		"prod/x",       // slash not allowed
		"pr od",        // space
		strings.Repeat("a", MaxLabelLength+1),
	}
	for _, s := range invalid {
		if err := ValidateEnv(s); err == nil {
			t.Errorf("ValidateEnv(%q) = nil, want error", s)
		}
	}
}

func TestValidateKeyValid(t *testing.T) {
	valid := []string{
		"rate-limit",
		"a",
		"billing/stripe-key",
		"a/b/c/d",
		"log.format",
		"a_b.c-d/e_f",
		strings.Repeat("a", MaxKeyLength),
	}
	for _, k := range valid {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) unexpected error: %v", k, err)
		}
	}
}

func TestValidateKeyInvalid(t *testing.T) {
	invalid := []string{
		"",
		"/leading",
		"trailing/",
		"a//b",
		"a/./b",
		"a/../b",
		"UP/case",
		"a b",
		"a/b c",
		"a*",
		"a\x00",
		strings.Repeat("a", MaxKeyLength+1),
	}
	for _, k := range invalid {
		if err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) = nil, want error", k)
		}
	}
}

func TestValidateKeyPattern(t *testing.T) {
	valid := []string{"*", "billing/*", "billing/stripe-key", "a/b/*"}
	for _, p := range valid {
		if err := ValidateKeyPattern(p); err != nil {
			t.Errorf("ValidateKeyPattern(%q) unexpected error: %v", p, err)
		}
	}
	invalid := []string{"", "/*", "a//b/*", "a b/*"}
	for _, p := range invalid {
		if err := ValidateKeyPattern(p); err == nil {
			t.Errorf("ValidateKeyPattern(%q) = nil, want error", p)
		}
	}
}

func TestMatchKey(t *testing.T) {
	tests := []struct {
		pattern, key string
		want         bool
	}{
		{"", "anything", true},
		{"*", "anything", true},
		{"*", "a/b/c", true},
		{"rate-limit", "rate-limit", true},
		{"rate-limit", "rate-limit2", false},
		{"billing/*", "billing", true},
		{"billing/*", "billing/stripe", true},
		{"billing/*", "billing/stripe/sub", true},
		{"billing/*", "billingx", false},
		{"billing/*", "bill", false},
		{"a/b", "a/b/c", false},
	}
	for _, tt := range tests {
		if got := MatchKey(tt.pattern, tt.key); got != tt.want {
			t.Errorf("MatchKey(%q, %q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
		}
	}
}

func TestParseNamespace(t *testing.T) {
	ns, err := ParseNamespace("prod/gradethis")
	if err != nil {
		t.Fatalf("ParseNamespace: %v", err)
	}
	if ns.Env != "prod" || ns.App != "gradethis" {
		t.Fatalf("got %+v", ns)
	}
	for _, bad := range []string{"prod", "", "prod/", "/gradethis", "Prod/app", "prod/gradethis/extra"} {
		if _, err := ParseNamespace(bad); err == nil {
			t.Errorf("ParseNamespace(%q) = nil, want error", bad)
		}
	}
}

func TestSplitDisplayPath(t *testing.T) {
	ref, err := SplitDisplayPath("/prod/gradethis/billing/stripe-key")
	if err != nil {
		t.Fatalf("SplitDisplayPath: %v", err)
	}
	if ref.NS.Env != "prod" || ref.NS.App != "gradethis" || ref.Key != "billing/stripe-key" {
		t.Fatalf("got %+v", ref)
	}
	// Round-trips with String / DisplayPath.
	if got := DisplayPath(ref); got != "/prod/gradethis/billing/stripe-key" {
		t.Fatalf("DisplayPath round-trip = %q", got)
	}
	for _, bad := range []string{
		"",
		"prod/gradethis/key", // no leading slash
		"/prod",              // missing app + key
		"/prod/gradethis",    // missing key
		"/prod/gradethis/",   // empty key
		"//gradethis/key",    // empty env
		"/prod//key",         // empty app
		"/Prod/app/key",      // invalid env
		"/prod/app//key",     // key has empty segment
	} {
		if _, err := SplitDisplayPath(bad); err == nil {
			t.Errorf("SplitDisplayPath(%q) = nil, want error", bad)
		}
	}
}

func TestRefAndNamespaceString(t *testing.T) {
	ns := domain.NamespaceRef{Env: "prod", App: "gradethis"}
	if ns.String() != "prod/gradethis" {
		t.Fatalf("NamespaceRef.String() = %q", ns.String())
	}
	ref := domain.Ref{NS: ns, Key: "rate-limit"}
	if ref.String() != "/prod/gradethis/rate-limit" {
		t.Fatalf("Ref.String() = %q", ref.String())
	}
}

func FuzzSplitDisplayPath(f *testing.F) {
	for _, s := range []string{"/prod/app/key", "/a/b/c/d", "//", "/x", "/prod/app/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		ref, err := SplitDisplayPath(in)
		if err != nil {
			return
		}
		// A parsed ref must round-trip back through the display form.
		again, err := SplitDisplayPath(ref.String())
		if err != nil {
			t.Fatalf("valid ref %+v failed re-parse: %v", ref, err)
		}
		if again != ref {
			t.Fatalf("SplitDisplayPath not idempotent: %+v -> %+v", ref, again)
		}
	})
}
