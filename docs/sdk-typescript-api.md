# TypeScript SDK public API and compatibility policy

This document fixes the package boundaries and lifecycle contract for the first
public TypeScript SDK. It is intentionally framework-neutral at the core.

## Package and runtime boundaries

The distribution is `@suhaibinator/kms` and supports maintained Node.js
releases beginning with Node 22. The root export is Node-only. Browser bundlers
receive a fail-fast poison module instead of transport code.

| Export | Runtime | Responsibility |
|---|---|---|
| `@suhaibinator/kms` | Node.js | Client, TLS/mTLS, values, cache/watch, releases, publishing contracts |
| `@suhaibinator/kms/configstore` | Node.js | Optional strict managed-configuration runtime and generator contracts |
| `@suhaibinator/kms/next/server` | Next.js Node runtime | Process-owned client/release lifecycle, server reads, validation, Route Handler factory |
| `@suhaibinator/kms/next/client` | Browser/React | Public-policy wire decoding, refresh, and stale-policy recovery only |

The core never imports Next.js, React, HTTP routing, or application policy. The
client export never imports gRPC, TLS, credentials, secrets, or a full release
snapshot. The existing repository frontend remains a static export and is not a
host for this adapter; consumers need a serverful Next.js deployment.

## Naming and types

Public TypeScript names use `PascalCase` for types/classes and `camelCase` for
methods/properties. All protobuf `uint64` values are `bigint` at every internal
and Node API boundary. HTTP/JSON revisions are canonical unsigned decimal
strings and are range-checked before conversion back to `bigint`.

Resources accept relative keys resolved against the configured or discovered
home namespace. A leading `/env/app/key` is display-path sugar split by the SDK;
the server still receives an explicit namespace and opaque key. Interior key
slashes are preserved.

## Lifecycle and concurrency

- A `KmsClient` is safe to share for the process lifetime. Construction fails
  closed unless TLS/mTLS, explicit development-only cleartext, or an injected
  test transport is supplied.
- `close()` is idempotent. It cancels streams, reconciliation, callbacks, and
  transport work. New operations fail after close.
- Unary calls use a five-second default deadline unless the caller supplies an
  earlier deadline. Long-lived streams do not inherit that unary deadline.
- One client owns one shared parameter watch stream over an add-only union of
  namespaces. Callbacks never delay applying state.
- A release loader permits one run, bounds exact-version fetch concurrency,
  keeps at most one preparation and one replace-latest candidate, and preserves
  the last known good snapshot after the first commit.
- Next.js initialization coalesces concurrent starts, owns one process-local
  client/loader, and closes loader work before the client transport.

## Error and secret contract

`KmsError` exposes a bounded code suitable for branching. Known gRPC statuses
map to `not_found`, `permission_denied`, `unauthenticated`, and
`failed_precondition`; namespace/configuration failures use local bounded codes.
Unmapped transport status information is preserved without ever including
caller-supplied secret bytes.

`Secret` stores a private copy of plaintext bytes. `bytes()`, `text()`, and
`clone()` are the only plaintext access paths; returned byte arrays are fresh
copies. String conversion, JSON conversion, Node inspection, errors, release
snapshots, status, metrics, and logs redact. This defensive-copy behavior is
deliberately stronger than the Go SDK's mutable `Value()` slice.

## Stability, versioning, and deprecation

The public exports documented here follow semantic versioning. Removing or
changing a public method, error code, wire response, or TypeScript type in an
incompatible way requires a major release. New optional fields and new methods
may ship in a minor release. Security and correctness fixes that preserve the
contract ship as patches.

Deprecated APIs remain functional for at least one minor-release line and carry
both a TypeScript `@deprecated` annotation and migration guidance. Generated
protobuf symbols and modules below `src/generated` are internal and are not a
stable public surface.

## Intentional language differences

- JavaScript cannot make arbitrary third-party object reflection safe. Secret
  safety is guaranteed for supported `Secret` accessors, `toString`, `toJSON`,
  and Node's custom inspection hook; applications must not copy plaintext out
  and then log the copy.
- Declarative resolution walks own object properties and arrays with cycle
  detection. It does not traverse `Map`, weak collections, accessors, or class
  internals. Explicit initialization remains available for dynamic shapes.
- Synchronous CPU-bound callbacks can block the Node event loop. The SDK does
  not await callbacks or callback promises and catches callback failures.
- Public snapshots use frozen copies and read-only maps rather than Go value
  copies. Every secret accessor still returns an independent `Secret`.
