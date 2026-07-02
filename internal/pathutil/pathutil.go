// Package pathutil validates, normalizes, and matches resource paths.
//
// Canonical form: "/seg/seg/seg" — leading slash, no trailing slash, no empty
// segments, no "." or "..", lowercase-insensitive is NOT applied (paths are
// case-sensitive), each segment limited to safe characters.
package pathutil

import (
	"fmt"
	"strings"
)

const (
	// MaxPathLength bounds the canonical path string.
	MaxPathLength = 512
	// MaxSegments bounds path depth.
	MaxSegments = 32
)

// Normalize validates a resource path and returns its canonical form.
// It rejects rather than silently rewrites anything ambiguous; the only
// normalization applied is trimming a single trailing slash.
func Normalize(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must start with '/'", p)
	}
	if len(p) > MaxPathLength {
		return "", fmt.Errorf("path exceeds %d characters", MaxPathLength)
	}
	// Allow exactly one trailing slash to be trimmed ("/a/b/" -> "/a/b").
	trimmed := strings.TrimSuffix(p, "/")
	if trimmed == "" {
		return "", fmt.Errorf("path %q has no segments", p)
	}
	segs := strings.Split(trimmed[1:], "/")
	if len(segs) > MaxSegments {
		return "", fmt.Errorf("path exceeds %d segments", MaxSegments)
	}
	for _, s := range segs {
		if err := validateSegment(s); err != nil {
			return "", fmt.Errorf("path %q: %w", p, err)
		}
	}
	return "/" + strings.Join(segs, "/"), nil
}

// NormalizePrefix validates a path pattern that is either an exact canonical
// path or a prefix pattern ending in "/*". It returns the canonical pattern.
func NormalizePrefix(p string) (string, error) {
	if p == "*" || p == "/*" {
		return "/*", nil
	}
	if strings.HasSuffix(p, "/*") {
		base, err := Normalize(strings.TrimSuffix(p, "/*"))
		if err != nil {
			return "", err
		}
		return base + "/*", nil
	}
	return Normalize(p)
}

func validateSegment(s string) error {
	if s == "" {
		return fmt.Errorf("empty path segment")
	}
	if s == "." || s == ".." {
		return fmt.Errorf("path segment %q is not allowed", s)
	}
	for _, r := range s {
		if !isSegmentRune(r) {
			return fmt.Errorf("path segment %q contains invalid character %q", s, r)
		}
	}
	return nil
}

func isSegmentRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == '.':
		return true
	}
	return false
}

// Match reports whether path matches pattern. Pattern is either an exact
// canonical path, or a prefix pattern "base/*" which matches "base" itself
// and everything under it. "/*" matches every path. Both inputs are assumed
// canonical (see Normalize / NormalizePrefix).
func Match(pattern, path string) bool {
	if pattern == "/*" {
		return true
	}
	if base, ok := strings.CutSuffix(pattern, "/*"); ok {
		return path == base || strings.HasPrefix(path, base+"/")
	}
	return pattern == path
}

// HasPrefix reports whether path is prefix itself or lies under it.
// Both must be canonical paths (no "/*" suffix). An empty prefix matches all.
func HasPrefix(path, prefix string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// Parent returns the parent path of p ("" for top-level paths).
func Parent(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i]
}
