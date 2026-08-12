# TypeScript SDK security policy

## Reporting a vulnerability

Do not disclose a suspected vulnerability, secret value, credential, private
endpoint, or exploit details in a public issue. Use the repository's private
security-reporting channel or contact the maintainers privately. Include the
affected package version, Node.js version, a minimal reproduction with all
credentials and plaintext removed, and the security impact.

General bugs that do not contain sensitive information may be reported through
the repository issue tracker. The project-wide threat model and operational
controls are documented in [`../../docs/security.md`](../../docs/security.md).

## Supported versions

Security fixes are made on the latest published minor release. Consumers
should upgrade to the newest patch promptly. The package requires a maintained
Node.js release beginning with Node 22; end-of-life Node.js versions are not a
supported security configuration.

## Trust boundary

The core package and `@suhaibinator/kms/next/server` are Node-only. They may
hold KMS authentication credentials and secret plaintext and must never be
imported into Client Components, browser bundles, Edge runtime code, or a
static export. Browser code may import only `@suhaibinator/kms/next/client`,
which understands the explicitly allowlisted public-policy HTTP contract and
contains no KMS transport.

The package export map supplies a fail-fast browser module for server-only
entry points. This is defense in depth, not permission to share server source
with a client bundler.

## Deployment requirements

- Use TLS in every non-local deployment. Prefer mTLS with a namespace-bound
  identity. `insecure: true` is an explicit cleartext development mode.
- Treat the CA passed to `tlsFromFiles` or `mtlsFromFiles` as a trust anchor for
  the operator-provided KMS server certificate. Do not substitute the built-in
  CA used by KMS to issue client identities.
- Keep bearer tokens, private keys, per-secret access tokens, and client-bound
  key shares outside source control and browser-visible environment variables.
- Set bounded operation deadlines, close clients during graceful shutdown, and
  monitor watch/reconciliation and release-loader health.
- Publish only values selected by `definePublicProjection`. Client-side
  validation is a usability feature; always validate against the active
  server-side snapshot.
- Return `policy_changed` with the current public projection when a submitted
  revision is stale. Do not accept a request because it passed an older browser
  policy.
- Use `no-store` for sensitive deployment contexts. If private caching is
  enabled for public policy, keep it short-lived; shared/CDN caching is not
  emitted by the adapter.

## Secret handling

`Secret`, `SecretValue`, and release secrets redact supported string, JSON, and
Node inspection operations and return defensive byte copies. Redaction cannot
protect plaintext after application code explicitly calls `bytes()`, `text()`,
`value()`, or `stringValue()`. Avoid interpolating such copies into errors,
logs, metrics, traces, status responses, crash reports, or public configuration.

JavaScript cannot reliably erase all copies retained by the runtime. Keep
plaintext scope short, avoid unnecessary conversion to immutable strings, and
prefer byte-oriented consumers where possible.

## Dependency and release checks

Release candidates must pass `npm ci` and `npm run check` from
`sdk/typescript`, including generated-contract verification, strict type
checking, lint and formatting, unit tests, consumer type tests, and a clean
build. CI runs the SDK on every supported Node.js major listed in its matrix.
