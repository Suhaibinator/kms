package pathutil

import (
	"strings"
	"testing"
)

func TestNormalizeValid(t *testing.T) {
	cases := map[string]string{
		"/prod/payments/stripe/api-key": "/prod/payments/stripe/api-key",
		"/a":                            "/a",
		"/a/b/":                         "/a/b",
		"/A/B-1/c_2/d.3":                "/A/B-1/c_2/d.3",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil {
			t.Errorf("Normalize(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeInvalid(t *testing.T) {
	invalid := []string{
		"",
		"/",
		"//",
		"a/b",
		"/a//b",
		"/a/../b",
		"/a/./b",
		"/a/b c",
		"/a/b*",
		"/a/b\x00",
		"/a/" + strings.Repeat("x", MaxPathLength),
		"/" + strings.Repeat("a/", MaxSegments) + "a",
	}
	for _, in := range invalid {
		if got, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) = %q, want error", in, got)
		}
	}
}

func TestNormalizePrefix(t *testing.T) {
	cases := map[string]string{
		"*":          "/*",
		"/*":         "/*",
		"/a/b/*":     "/a/b/*",
		"/a/b":       "/a/b",
		"/prod/x/*":  "/prod/x/*",
		"/prod/x/**": "", // invalid: ** is not a valid segment
		"/a//b/*":    "",
	}
	for in, want := range cases {
		got, err := NormalizePrefix(in)
		if want == "" {
			if err == nil {
				t.Errorf("NormalizePrefix(%q) = %q, want error", in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizePrefix(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"/*", "/anything/at/all", true},
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", false},
		{"/a/b/*", "/a/b", true},
		{"/a/b/*", "/a/b/c", true},
		{"/a/b/*", "/a/b/c/d", true},
		{"/a/b/*", "/a/bc", false},
		{"/a/b/*", "/a", false},
		{"/prod/payments/*", "/prod/payments/stripe/api-key", true},
		{"/prod/payments/*", "/prod/paymentsx/stripe", false},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.path); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestHasPrefix(t *testing.T) {
	tests := []struct {
		path, prefix string
		want         bool
	}{
		{"/a/b/c", "", true},
		{"/a/b/c", "/", true},
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
	}
	for _, tt := range tests {
		if got := HasPrefix(tt.path, tt.prefix); got != tt.want {
			t.Errorf("HasPrefix(%q, %q) = %v, want %v", tt.path, tt.prefix, got, tt.want)
		}
	}
}

func TestParent(t *testing.T) {
	cases := map[string]string{
		"/a/b/c": "/a/b",
		"/a":     "",
	}
	for in, want := range cases {
		if got := Parent(in); got != want {
			t.Errorf("Parent(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzNormalize(f *testing.F) {
	seeds := []string{"/a/b", "//", "/a/../b", "/*", "/a/b/", strings.Repeat("/a", 100)}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := Normalize(in)
		if err != nil {
			return
		}
		// Canonical outputs must round-trip.
		again, err := Normalize(out)
		if err != nil {
			t.Fatalf("canonical path %q failed re-normalization: %v", out, err)
		}
		if again != out {
			t.Fatalf("Normalize not idempotent: %q -> %q", out, again)
		}
		if strings.Contains(out, "//") || strings.HasSuffix(out, "/") || !strings.HasPrefix(out, "/") {
			t.Fatalf("non-canonical output %q from input %q", out, in)
		}
	})
}
