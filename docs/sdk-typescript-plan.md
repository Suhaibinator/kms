# Framework-Neutral TypeScript SDK Plan

## Purpose

Build a Node.js TypeScript SDK with near-full behavioral feature parity with the Go SDK and the KMS contract, without server changes and without coupling the SDK to Next.js. The SDK is the generic configuration and release runtime; applications and framework adapters decide how to expose non-sensitive configuration to browsers.

## Go SDK feature-parity target

The target is near-full behavioral parity with the supported Go SDK surface, including its operational and failure-handling semantics—not merely generated gRPC client stubs. The few unavoidable language-level differences, such as JavaScript inspection behavior versus Go `fmt`, must preserve the same security and API guarantees wherever Node supports them.

The Node SDK will support TLS and mTLS, authentication, namespace discovery, reads and writes, caching, typed errors, and secret redaction. It will support declarative `SecretValue` and `ParameterValue` resolution, nested values, defaults, callbacks, and hot reload.

It will also reproduce shared bidirectional watch streams, heartbeats, acknowledgements, revision fencing, reconnect/resume, and reconciliation. Atomic release loading includes exact-version resolution, digest verification, supersession, prepare/commit/abort, acknowledgements, and last-known-good behavior.

If the scope includes the Go `configstore` package, it additionally requires strict decoding, drift and restart policy, immutable typed views, schema/contract generation, and a TypeScript-native generator.

## Platform boundary

This is a Node-only SDK. Node supports the bidirectional gRPC streams and TLS/mTLS needed by the protocol. Browsers do not call KMS directly: ordinary gRPC-Web does not support the required bidirectional client streaming, browser mTLS is not suitable for this use, and direct use could expose secrets.

All protobuf `uint64` revisions and versions are represented internally as TypeScript `bigint`, never JavaScript `number`.

Secrets are stored as private bytes with defensive copies and redacted serialization and inspection behavior. This matches the supported security behavior even though JavaScript cannot reproduce every Go formatting guarantee exactly.

## Architecture

### Core SDK

The core package is framework-neutral. It exposes typed KMS values, namespaces, cache and watch primitives, atomic release snapshots, revisions, reconciliation, and safe secret handling. It contains no password-policy, React, Next.js, HTTP-routing, or browser-specific logic.

### Application policy layer

Each application maps generic KMS values and release snapshots into its own domain configuration. For example, an application can derive a `PasswordPolicy` from a release. The application also explicitly defines a public projection: fields safe to disclose to an untrusted browser.

### Framework adapters

Adapters are optional packages on top of the core and application layers. Equivalent adapters can target Express, Fastify, Nest, or plain Node HTTP. Any frontend consumes the same ordinary HTTP public-policy contract.

### First-class Next.js adapter

Provide a supported Next.js adapter designed to make the importing application write as little integration code as possible. It is convenience infrastructure, not part of the core KMS contract.

The adapter should provide:

- one server-only initialization entry point that owns the Node SDK client, cache, watches, lifecycle, and graceful shutdown;
- typed helpers for reading an active release snapshot and its public projection in Server Components and Server Actions;
- a ready-made Route Handler factory for publishing an allowlisted public configuration view with revision-aware caching;
- helpers for standard stale-policy responses and server-side validation against the active snapshot; and
- a small Client Component hook that receives initial policy, refreshes safely, reacts to version changes, and recovers from `policy_changed` responses.

An application should configure its KMS connection, declare its typed policy mapping and public allowlist once, then import the adapter helpers where it renders or validates. The adapter must use the Node runtime, prevent accidental Client Component imports of server-only code, and never expose KMS credentials or unallowlisted values.

## Public hot-reloaded configuration

The password minimum length is an example of public configuration. It can be exposed for immediate client-side feedback, while the backend remains authoritative.

1. KMS publishes an atomic release containing the full server configuration.
2. The Node application watch applies that release atomically to one in-memory snapshot.
3. Password validation reads from that snapshot.
4. A public-config publisher derives a deliberately allowlisted policy view from the same snapshot.
5. Browser clients fetch the view and use it for user experience only.

The public response includes a revision and safe fields, for example:

```ts
type PublicPasswordPolicy = {
  revision: bigint;
  minLength: number;
};
```

On JSON/HTTP boundaries, represent the revision as a decimal string and convert it to `bigint` in TypeScript.

## Next.js example adapter behavior

A Next.js Server Component can use the adapter to obtain the active public policy and pass it as the initial prop to a Client Component. The Client Component hook validates locally and retains the policy in state. It never imports the KMS SDK.

The client can refresh policy on page load, navigation, focus, or before submit by calling a public-policy Route Handler. If immediate propagation is important, SSE or WebSocket can notify the client to refetch; the KMS stream remains solely on the server.

## Consistency and stale clients

Backend validation and the public-policy response must always derive from the same atomically swapped release snapshot. The SDK/application must not validate using one revision while publishing a separate revision.

An already-open browser tab can still hold an older policy. On submission, the backend validates against the active policy. If the client supplied an older revision, return a structured `policy_changed` result with the current public policy. The client replaces its local policy, revalidates, and explains the changed requirement. Ordinary validation errors apply when the revision is current but the password fails the rule.

## Caching and security

The public-policy endpoint may use short-lived caching or an ETag keyed to the policy revision. It must not permit a CDN to serve arbitrarily stale policy; strict deployments can use `no-store`. Stale submission recovery is always required.

Only explicitly allowlisted non-sensitive fields are exposed. Never publish KMS credentials, secrets, internal endpoints, private control values, or any value merely because it is present in a release. Client validation is a usability feature, not a security control; every submission is validated by the backend.

## Observability

Record the active release revision, watch/reconciliation health, public-policy publication, refresh failures, stale-policy rejections, successful client recovery, and propagation delay from release activation to client update.

## Acceptance criteria

- A KMS release changes backend policy atomically.
- Public policy is derived from the same active release snapshot.
- The core SDK remains reusable outside Next.js.
- A first-class Next.js adapter minimizes application wiring while Express/Fastify/Nest and other adapters can expose the same public-policy contract.
- Client components receive only safe projections and never access KMS directly.
- A stale browser can recover after a server rejection without a full page reload.
- All KMS `uint64` values preserve precision as `bigint`.
- Secret data is redacted and defensively handled.

## Rough delivery estimate

Lower-level `kmsclient` parity and publishing is estimated at roughly seven to eleven engineer-weeks. Literal parity including managed `configstore` and a TypeScript-native generation path is roughly eleven to eighteen engineer-weeks.

## Staged delivery plan

The SDK is a public-facing product and must be delivered in bounded stages. No stage is complete merely when its implementation compiles; it must pass its defined parity, quality, reliability, and documentation gates before the next stage begins.

### Stage 0: Contract inventory and public API design

Inventory the supported Go SDK APIs, behavioral guarantees, error taxonomy, protobuf mappings, concurrency assumptions, secret-redaction rules, and configuration semantics. Publish the proposed TypeScript package boundaries, stable public types, naming conventions, lifecycle rules, and deprecation policy before implementation.

### Stage 1: Transport and foundational client

Implement generated protobuf bindings, Node gRPC transport, TLS/mTLS, authentication, namespace discovery, metadata handling, typed error normalization, `bigint` conversion, and secret-safe primitives.

### Stage 2: Values, caching, and resolution

Implement parameter and secret value abstractions, defaults, nested resolution, callbacks, caching, invalidation, and redacted inspection/serialization behavior.

### Stage 3: Watches and reconciliation

Implement shared watch streams, heartbeats, acknowledgements, revision fencing, reconnect/resume, backoff, cancellation, reconciliation, and bounded resource cleanup.

### Stage 4: Atomic releases

Implement exact-version resolution, digest verification, supersession, prepare/commit/abort, acknowledgements, last-known-good behavior, and atomic snapshot swaps.

### Stage 5: Framework-neutral publishing primitives

Implement typed public-projection helpers, revision-aware response contracts, stale-client recovery contracts, and server-side authoritative validation patterns without embedding any web framework dependency.

### Stage 6: Next.js adapter

Build the supported low-wiring Next.js adapter: server-only initialization, Server Component and Server Action helpers, Route Handler factories, Client Component policy hooks, lifecycle integration, and Node-runtime safeguards.

### Stage 7: Optional managed configstore parity

If included in release scope, implement strict decoding, immutable typed views, drift/restart policy, schema/contract generation, and a TypeScript/schema-native generator. Do not claim literal configstore parity until this stage is complete.

### Stage 8: Release readiness

Finalize API reference material, examples, migration guidance, semantic-versioning policy, package metadata, license/security notices, changelog process, support boundaries, and a stable release candidate.

## Mandatory stage gates

After every stage, run a separate adversarial review pass using multiple independent reviewers. Reviewers must challenge the implementation rather than merely confirm it, with focused passes for:

- correctness and parity gaps against the Go behavior;
- race conditions, cancellation, reconnect, ordering, and resource-lifetime failures;
- performance, memory use, backpressure, duplicate work, and connection sharing;
- security, secret handling, redaction, TLS/mTLS, authentication, and unsafe browser exposure;
- public API ergonomics, TypeScript type design, naming, compatibility, and documentation clarity; and
- framework-neutral boundaries, ensuring adapter convenience does not leak into the core SDK.

All findings require triage with an explicit resolution, test coverage where applicable, and a documented exception only when the difference is intentional and user-visible. High-risk or disputed areas receive a second independent implementation or test-design review before the stage is accepted.

## Parity verification

Maintain a living Go-to-TypeScript parity matrix. For every supported Go feature, record the TypeScript API, supported behavior, intentional differences, test location, and release status. Parity checks must exercise behavior, not only type or method presence.

Use shared protocol fixtures and, where practical, run the Go and TypeScript SDKs against the same controlled server scenarios. Compare observable outcomes: return values, typed errors, revision behavior, redaction, callbacks, cache state, watch recovery, release transitions, and cancellation semantics.

Every intentional difference must be documented in the public compatibility notes. The SDK must not advertise full parity for an area that is only partially implemented.

## Test strategy

Comprehensive automated testing is required from the first implementation stage:

- unit tests for all public types, conversion rules, errors, cache logic, resolution, redaction, and lifecycle behavior;
- integration tests against a real or protocol-faithful KMS server for transport, TLS/mTLS, authentication, namespaces, reads, writes, watches, and releases;
- deterministic fault-injection tests for dropped streams, duplicate events, out-of-order revisions, server restarts, timeouts, acknowledgement failures, bad digests, and interrupted commits;
- concurrency and stress tests for shared watches, atomic snapshot reads, reconnect storms, cancellation, leak resistance, and backpressure;
- compatibility tests across supported Node and TypeScript versions; and
- adapter tests covering server-only boundaries, public allowlists, stale-policy recovery, caching, and accidental browser-bundle exclusion.

Public APIs require documentation examples that run in continuous integration. Test failures, race reports, lint/type failures, and unmet parity-matrix entries block progression to the next stage.

## Public SDK interface standards

The published API must favor small, composable, typed interfaces with predictable lifecycle and error behavior. Public interfaces must avoid accidental framework dependencies, hidden global state, ambiguous ownership, mutable shared results, and unbounded background work.

Each public API needs clear documentation for construction, disposal, concurrency safety, error cases, secret behavior, revision semantics, and compatibility expectations. Breaking changes require deliberate versioning and migration guidance.
