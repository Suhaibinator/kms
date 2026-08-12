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
- Value-free watch health plus publisher, Route Handler, refresh, and recovery
  observer hooks with revision/correlation timestamps.
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
- Authoritative snapshots and explicit tombstones invalidate ordinary
  parameter-cache reads; expanded scopes also interrupt reconnect backoff, and
  late reconciliation pages cannot repopulate a released namespace.
- Callback promises settle serially behind the bounded dispatcher, and
  unsubscribe/dispose fences notifications that were already queued.
- Release and managed-config identities reject non-`bigint` or out-of-range
  protocol integers at runtime.
- Protected parameter reads now forward their per-resource token and bypass
  shared caching; namespace discovery applies caller-local deadlines and
  cancellation without allowing one caller to poison a coalesced lookup.
- Publisher observers receive an outcome only after the corresponding public
  result is safely constructed.
- A cancelled release run retains exclusivity until every owned preparation
  settles, preventing sequential runs from overlapping abort-insensitive work.
- Next process signal hooks are explicitly cleanup-only, reject uncatchable
  signals before installation, and invoke an explicit application-owned
  termination callback after cleanup settles.

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
