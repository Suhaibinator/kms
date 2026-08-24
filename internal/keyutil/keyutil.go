// Package keyutil validates and matches the components of a namespace-native
// resource address: an environment, an application, and a relative key. It is
// the single place those naming rules live (successor to internal/pathutil).
//
// Rules:
//   - env / app: 1–64 chars of [a-z0-9-], not starting or ending with '-'.
//   - key: 1–256 chars total; '/'-separated segments of [a-z0-9._-]; no
//     leading, trailing, empty, or "."/".." segment. Interior slashes are part
//     of the name, never namespace structure.
//
// The server addresses resources by explicit (env, app, key); the "/env/app/key"
// string survives only as a display format and an SDK/CLI escape hatch
// (SplitDisplayPath). The server never parses it.
package keyutil

import (
	"fmt"
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
)

const (
	// MaxLabelLength bounds an env or app value.
	MaxLabelLength = 64
	// MaxKeyLength bounds a relative key.
	MaxKeyLength = 256
)

// ValidateEnv reports whether env is a legal environment name.
func ValidateEnv(env string) error {
	if err := validateLabel(env); err != nil {
		return fmt.Errorf("env %q: %w", env, err)
	}
	return nil
}

// ValidateApp reports whether app is a legal application name.
func ValidateApp(app string) error {
	if err := validateLabel(app); err != nil {
		return fmt.Errorf("app %q: %w", app, err)
	}
	return nil
}

// ValidateNamespace validates both halves of a namespace reference.
func ValidateNamespace(ns domain.NamespaceRef) error {
	if err := ValidateEnv(ns.Env); err != nil {
		return err
	}
	return ValidateApp(ns.App)
}

// validateLabel enforces the shared env/app character rules.
func validateLabel(s string) error {
	if s == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(s) > MaxLabelLength {
		return fmt.Errorf("exceeds %d characters", MaxLabelLength)
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return fmt.Errorf("must not start or end with '-'")
	}
	for _, r := range s {
		if !isLabelRune(r) {
			return fmt.Errorf("contains invalid character %q", r)
		}
	}
	return nil
}

func isLabelRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-':
		return true
	}
	return false
}

// ValidateKey reports whether key is a legal relative key.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key must not be empty")
	}
	if len(key) > MaxKeyLength {
		return fmt.Errorf("key %q exceeds %d characters", key, MaxKeyLength)
	}
	if strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return fmt.Errorf("key %q must not have a leading or trailing slash", key)
	}
	for seg := range strings.SplitSeq(key, "/") {
		if err := validateKeySegment(seg); err != nil {
			return fmt.Errorf("key %q: %w", key, err)
		}
	}
	return nil
}

func validateKeySegment(s string) error {
	if s == "" {
		return fmt.Errorf("empty segment")
	}
	if s == "." || s == ".." {
		return fmt.Errorf("segment %q is not allowed", s)
	}
	for _, r := range s {
		if !isKeyRune(r) {
			return fmt.Errorf("segment %q contains invalid character %q", s, r)
		}
	}
	return nil
}

func isKeyRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == '.':
		return true
	}
	return false
}

// ParseNamespace parses the SDK/CLI display form "env/app" into a NamespaceRef.
// It is client-side sugar; the server takes explicit env/app fields.
func ParseNamespace(s string) (domain.NamespaceRef, error) {
	env, app, ok := strings.Cut(s, "/")
	if !ok {
		return domain.NamespaceRef{}, fmt.Errorf("namespace %q must be of the form env/app", s)
	}
	ns := domain.NamespaceRef{Env: env, App: app}
	if err := ValidateNamespace(ns); err != nil {
		return domain.NamespaceRef{}, err
	}
	return ns, nil
}

// SplitDisplayPath parses the absolute display form "/env/app/key" (the key may
// contain interior slashes) into a Ref. It is used only by SDKs and the CLI as
// an escape hatch for cross-namespace addressing; the server never calls it.
func SplitDisplayPath(p string) (domain.Ref, error) {
	if !strings.HasPrefix(p, "/") {
		return domain.Ref{}, fmt.Errorf("path %q must start with '/'", p)
	}
	parts := strings.SplitN(p[1:], "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return domain.Ref{}, fmt.Errorf("path %q must be of the form /env/app/key", p)
	}
	ref := domain.Ref{NS: domain.NamespaceRef{Env: parts[0], App: parts[1]}, Key: parts[2]}
	if err := ValidateNamespace(ref.NS); err != nil {
		return domain.Ref{}, err
	}
	if err := ValidateKey(ref.Key); err != nil {
		return domain.Ref{}, err
	}
	return ref, nil
}
