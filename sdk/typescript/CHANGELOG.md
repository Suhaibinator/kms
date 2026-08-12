# Changelog

All notable changes to `@suhaibinator/kms` are recorded here. The project uses
[Semantic Versioning](https://semver.org/); while the package is below `1.0.0`,
a minor release may contain documented breaking changes.

## [Unreleased]

### Added

- Stable root, Next.js server, Next.js client, optional configstore, and
  TypeScript-native config generator package entry points.
- Node gRPC transport with TLS/mTLS, bearer and per-secret metadata, bounded
  deadlines, namespace discovery, and exact `bigint` protocol values.
- Typed errors, defensive redacting secrets, bounded read caching, declarative
  values, shared watches, reconciliation, and atomic release loading.
- Explicit public-policy projections, stale-client recovery contracts, and a
  first-class serverful Next.js adapter.
- Runnable core and Next.js integration examples, package security guidance,
  consumer type tests, and full release gates on Node.js 22, 24, and 26.
- A real Next.js App Router build gate that rejects server-adapter imports from
  Client Components and inspects browser chunks for server-only dependencies.
- Exact Next.js 14–16/React 18–19 peer-tuple builds, TypeScript 5.2.2
  declaration consumption, and a real Chromium public-policy recovery gate.
- `kms-config-gen-ts` deterministic binding, Draft 2020-12 schema, and machine
  contract generation with non-writing `--check`/`--verify` modes.

### Fixed

- Managed float codecs canonicalize signed zero so `-0` and `0` cannot cause a
  false source-default drift report.
- Default-mismatch report and error JSON encode nested `bigint` values as
  canonical decimal strings while retaining whole-value secret redaction.
- Disposed declarative values unregister their live parameter handlers, and
  watch shutdown removes external abort listeners.
- Expanded watch scopes retry a full snapshot until it arrives, preserve
  per-key revision fences, and immediately invalidate newly watched secret
  caches; disposal cannot race a late parameter subscription.

### Compatibility notes

- The core SDK is Node-only and ESM-only. Importing it in a browser or Edge
  runtime is unsupported and fails closed.
- HTTP revisions are decimal strings; Node SDK revisions and versions are
  `bigint`.
- Managed configstore parity is advertised only for capabilities marked
  `complete` in the repository parity ledger.

## Changelog process

Every pull request that changes user-visible behavior adds an entry under
`Unreleased` in one of `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, or
`Security`. At release time, move those entries beneath a version and ISO date,
then add a fresh `Unreleased` section. Breaking changes include migration
guidance and are called out in the release notes.
