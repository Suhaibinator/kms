// Package kms exposes build-time embedded assets. The frontend is a Next.js
// static export produced by `make frontend` into frontend/out and compiled
// into the binary here (plan 6.3). The Go build fails if frontend/out is
// missing entirely; the HTTP server detects a placeholder-only export and
// serves an actionable error page instead of a broken UI.
package kms

import "embed"

// FrontendFS holds the static export. Use fs.Sub(FrontendFS, "frontend/out")
// to obtain the web root.
//
//go:embed all:frontend/out
var FrontendFS embed.FS
