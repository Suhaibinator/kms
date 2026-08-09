# Vendored fonts

## Inter-Variable-latin.woff2

- **Family:** Inter (variable, weight axis 400–700), latin subset only.
- **Source:** the woff2 served by Google Fonts for
  `https://fonts.googleapis.com/css2?family=Inter:wght@400..700&display=swap`.
- **License:** SIL Open Font License 1.1 — <https://openfontlicense.org>.

It is committed rather than fetched by `next/font/google` on purpose. The
frontend is a static export embedded into the Go binary, so `make frontend`
must not depend on fonts.gstatic.com being reachable from the build machine.
Self-hosting also satisfies the server's `font-src 'self'` CSP
(`internal/server/httpserver/static.go`).

Only the latin subset is vendored to keep the binary small; the system font
stack in `--sans` (see `styles/globals.css`) covers any glyph outside it.
