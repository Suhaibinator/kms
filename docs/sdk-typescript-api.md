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

The package is ESM-only. Node 22 is the oldest supported runtime; CI also runs
the release gate on Node 26. The declaration syntax requires TypeScript 5.2 or
newer (`const` type parameters and `Symbol.asyncDispose`); the current release
gate directly qualifies the pinned TypeScript 7 compiler, so minimum-compiler
matrix coverage remains outstanding. Next.js and React are optional peers and
are needed only for their respective adapter entry points. The peer ranges express
intended Next.js 14–16 and React 18–19 compatibility, while the current adapter
suite directly qualifies Next.js 16 with React 19. Earlier accepted peer majors
remain a compatibility-qualification gap rather than a matrix-tested claim.

## Core API reference

### Construction, transport, and lifecycle

| API | Contract |
|---|---|
| `createClient(options)`, `new KmsClient(options)` | Create one process-shareable client. Require `credentials`, explicit development-only `insecure: true`, or an injected `RpcTransport`. |
| `tlsFromFiles(ca)`, `mtlsFromFiles(cert, key, ca)`, `tlsFromBytes(...)` | Build gRPC channel credentials. The CA trusts the operator-provided server certificate. |
| `client.whoAmI()` | Return bounded identity, namespace, and authentication-method metadata. Namespace discovery is lazy and retryable. |
| `client.close()` | Idempotently cancel watches, reconciliation, callbacks, and transport work. `Symbol.asyncDispose` delegates to it. |
| `CallOptions` | Per-call `AbortSignal` and earlier absolute deadline. The default unary deadline is five seconds. |

`KmsClientOptions.token` supplies bearer authentication. `GetOptions` and
secret mutation options accept a separate `secretToken`; it is never used as
the caller identity. `RpcTransport`, `UnaryMethod`, `BidiMethod`, and
`DuplexRpc` are exported for deterministic tests and advanced transport
integration, but generated protobuf symbols remain internal.

### Resources and declarative values

| API | Contract |
|---|---|
| `getParameter`, `getSecret` | Resolve a relative or absolute key at `version` or `label`; return exact `bigint` metadata and a redacting `Secret`. |
| `putParameter`, `putSecret` | Write an immutable version, invalidate matching cache entries, and return `bigint` version/revision fields. |
| `listParameters`, `listSecrets` | Return immutable `Page<T>` values with bounded pagination inputs. |
| `getParameterMetadata`, `getSecretMetadata` | Return non-plaintext history, labels, state, and content metadata. |
| `deleteParameter`, `deleteSecret`, `setSecretEnabled`, `destroySecretVersion`, `promoteSecretVersion` | Perform the corresponding authorized mutation and return exact revision/version values. |
| `SecretValue` | Resolve an env override, KMS secret, or allowed default. Plaintext access is explicit and implicit rendering redacts. |
| `ParameterValue` | Resolve the same precedence and subscribe by default. `static: true` opts out; `onChange` and `dispose` own callback lifecycle. |
| `client.resolve(object)` / `resolveValues` | Find declarative values through own properties and arrays, detect cycles, and report failures together in `ResolutionError`. |

`parseNamespace`, `splitDisplayPath`, `resolveRef`, and display helpers expose
the namespace/path rules without a network call. `CURRENT_VERSION` represents
the server's current selector. All explicit versions must be `bigint` values
within the protobuf `uint64` range.

### Watches and releases

| API | Contract |
|---|---|
| `client.watch(callback)` | Observe the home namespace through the shared bidirectional stream and return an unsubscribe function. |
| `client.watchNamespace(namespace, callback)` | Observe an explicit namespace, subject to authorization. |
| `client.createReleaseLoader(options)` | Bind an atomic loader to the client's authenticated transport and configured/discovered namespace. |
| `ReleaseLoader.run(prepare, signal)` | Resolve one exact, verified candidate at a time; abort superseded work; synchronously commit prepared state; preserve last known good after later rejection. |
| `ReleaseLoader.status()`, `stats()` | Return copied non-secret loader health and bounded rejection categories. |
| `ReleaseSnapshot` | Provide immutable parameter access and defensive redacting secret access. JSON and inspection omit all values. |
| `ClassifiedReleaseError` | Let application decoding map a failure to an allowed acknowledgement category without sending raw error text. |

`prepare` may do fallible parsing, validation, and resource allocation. Its
returned `commit()` must be synchronous and infallible; `abort()` must release
candidate-owned resources. A release containing protected secrets supplies a
local `secretTokenProvider`. `stop()` requests bounded shutdown; callers still
await the `run()` promise before closing the client.

### Public configuration

| API | Contract |
|---|---|
| `definePublicProjection(allowlist)` | Declare explicit public field selectors and recursively clone, validate, and freeze their JSON results. |
| `createPolicyPublisher(options)` | Read one atomic `PolicySnapshot`, publish its projection, and validate a request against that same generation. |
| `formatRevision`, `parseRevision` | Convert between internal `bigint` and canonical decimal-string HTTP revisions without precision loss. |
| `formatPublicConfigEtag` | Produce the revision-keyed strong validator used by the Next.js Route Handler. |
| `normalizePublicConfigWire` | Strictly validate untrusted public response shape, revision range, keys, and JSON values. |

The authoritative validator returns `success` or `validation_failed` only when
the client revision is current. A missing or stale revision returns
`policy_changed` with the public projection from the same captured snapshot.
An unavailable source returns `unavailable`.

## Next.js adapter reference

`@suhaibinator/kms/next/server` begins with `import "server-only"` and rejects
Edge runtime execution. `createNextKms` accepts one application-owned
initializer, projection, and validator and returns:

- `start()` and `close()` for coalesced process-local lifecycle;
- `readPolicy()` for server-only policy access and `readPublicPolicy()` for a
  serializable Server Component prop;
- `validateAtRevision()` for authoritative Server Action/Route validation;
- `createPublicConfigGET()` for an ETag-aware App Router Route Handler; and
- `installProcessShutdown()` for SIGINT/SIGTERM cleanup.

The Route Handler defaults to `Cache-Control: no-store`. The only alternative
is bounded private browser caching; it never emits shared/CDN caching.

`@suhaibinator/kms/next/client` exports `usePublicConfig(initial, options)`.
The hook retains a last-known-good projection, conditionally refreshes with an
ETag, fences out-of-order responses, refreshes on mount/focus by default, and
installs a validated `policy_changed` result through `applyServerResult`.
It has no KMS credentials, secret types, or transport dependency.

The client hook requires a modern browser with `bigint`, `fetch`,
`AbortController`, and standard focus events. Current automated coverage uses
a simulated DOM; it does not yet constitute a real-browser compatibility
matrix.

The complete compile-checked integration is in the
[`next-serverful` example](../sdk/typescript/examples/next-serverful).

## Managed configuration reference

`@suhaibinator/kms/configstore` is the optional Stage 7 layer used by generated
bindings. `startManagedConfig(client, options, prepare, signal)` accepts an
ordinary `KmsClient` through its public `createReleaseLoader` method; transport
and generated protobuf types remain private. The options declare the release
name and exact generated alias/content-type contract, require a synchronous
default-drift reporter, and may allow an explicitly reported startup mismatch.

Generated preparation performs strict decode and application validation, then
returns a synchronous `publish` swap plus optional `abort`, complete
source-default differences, and the canonical restart-required fields that
changed. The manager:

- validates the exact manifest before fetching entries;
- blocks startup until the first candidate is atomically publishable;
- fails closed on unapproved startup default drift and always reports approved
  drift;
- rejects a whole runtime candidate if any restart-required field changed;
- retains the last-known-good snapshot and detects later default restoration;
- exposes redacted copied status, metrics, mismatch, and rejection reports; and
- stops through `manager.stop()` followed by `await manager.wait()`.

`codecs`, `field`, `group`, `decodeGroup`, and `encodeGroup` provide
duplicate-aware, unknown/missing-field rejecting JSON codecs with exact range
checks. `ConfigSnapshot` and `immutableSnapshot` keep the stored root private
and defensively clone composite and secret-bearing reads. This deliberately
differs from allocation-free Go generated scalar/view getters: JavaScript
managed views favor defensive ownership, represent exact 64-bit
integers/durations as `bigint`, and use explicit `null` where the generated
contract permits absence.

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

Before `1.0.0`, a minor version may make a documented breaking change. Every
user-visible change is recorded under `Unreleased` in the package changelog;
release entries include an ISO date and migration guidance for breaking
changes. Security fixes are supported on the latest published minor line.

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
