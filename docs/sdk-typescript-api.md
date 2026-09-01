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
| `@suhaibinator/kms/configstore` | Node.js | Optional strict managed-configuration runtime and generated-binding contracts |
| `@suhaibinator/kms/configgen` | Node.js | Descriptor parser and deterministic binding/schema/contract generation library |
| `@suhaibinator/kms/next/server` | Next.js Node runtime | Process-owned client/release lifecycle, server reads, validation, Route Handler factory |
| `@suhaibinator/kms/next/client` | Browser/React | Public-policy wire decoding, refresh, and stale-policy recovery only |
| `@suhaibinator/kms/package.json` | Build tooling | Read-only package metadata; no runtime SDK API |

The core never imports Next.js, React, HTTP routing, or application policy. The
client export never imports gRPC, TLS, credentials, secrets, or a full release
snapshot. The existing repository frontend remains a static export and is not a
host for this adapter; consumers need a serverful Next.js deployment.

The package is ESM-only. Node 22 is the oldest supported runtime; CI runs the
complete release gate on Node 22, 24, and 26. The declaration syntax requires
TypeScript 5.2 or newer (`const` type parameters and `Symbol.asyncDispose`);
the release gate compiles built-package consumers with both TypeScript 5.2.2
and the pinned current compiler. Next.js and React are
optional peers and are needed only for their respective adapter entry points.
The peer ranges cover Next.js 14–16 and React 18–19; isolated exact-tuple builds
qualify Next.js 14/React 18, Next.js 15/React 18 and 19, and Next.js 16/React
18 and 19.

Only the entry points in this table are stable. Generated protobuf modules,
files below `dist/` that are not named by the package export map, and source
subpaths are implementation details even when their declarations are visible
in a repository checkout.

## Stable export inventory

The tables in this section are exhaustive for the package export map. “Type”
means a TypeScript-only export with no runtime value. Overloads and generic
parameters are summarized by family; the emitted declarations remain the
source of truth for exact inference.

### Package root: client and resource types

| Export | Kind and signature family | Purpose |
|---|---|---|
| `createClient`, `KmsClient` | Function `(KmsClientOptions) => KmsClient`; class constructor with the same options | Construct and own one process-shareable Node client. |
| `KmsClientOptions`, `Logger` | Types | Configure endpoint, authentication/transport, namespace, cache/deadlines, reconciliation, client identity, and bounded logging. |
| `CallOptions`, `GetOptions`, `ListOptions`, `PutParameterOptions`, `PutSecretOptions` | Types | Per-operation cancellation/deadline, selector, pagination, content metadata, and secret-specific options. |
| `ParameterMetadata`, `Parameter`, `SecretInfo`, `SecretVersion`, `PutResult`, `PutSecretResult`, `Page<T>`, `WhoAmI` | Types | Immutable public response models; every protobuf integer/timestamp/revision field is `bigint`. |
| `WatchOptions`, `WatchCallback`, `WatchEvent` | Types | Abortable watch registration and the discriminated `put`/`delete`/`secret_change` event union. |
| `WatchStatus`, `WatchConnectionState`, `ReconciliationHealth` | Types | Frozen, value-free point-in-time watch health: connection/reconciliation state, exact revision, reconnect/scope/tracked-parameter counts, and optional lifecycle timestamps. |
| `ClientReleaseLoaderOptions` | Type | Select a release and control identity, reconciliation, fetch concurrency, secret-token lookup, and manifest validation. |

`KmsClient` exposes the readonly properties `clientName`, `timeoutMs`,
`fallbackToDefaultsOnError`, `logger`, `closed`, `currentRevision`, and
`watchStatus`. The last is safe for health/metrics endpoints and returns a new
immutable snapshot rather than a live mutable object. Because
`currentRevision` is a `bigint`, JSON endpoints must replace it with
`formatRevision(status.currentRevision)` before serialization. Its stable
method families are:

| Methods | Result and contract |
|---|---|
| `whoAmI(options?)` | `Promise<WhoAmI>`; discovers bounded caller/authentication metadata. |
| `getParameter(key, options?)`, `getParameterInfo(key, options?)` | `Promise<string>` or `Promise<Parameter>` for a current, exact-version, or labeled immutable parameter. |
| `putParameter(key, value, options?)` | `Promise<PutResult>` and invalidates matching cached reads. |
| `listParameters(namespace?, options?)`, `getParameterMetadata(key, options?)`, `deleteParameter(key, options?)` | Immutable page, non-plaintext metadata/history, or exact revision. |
| `getSecret(key, options?)`, `putSecret(key, value, options?)` | Defensive `Secret` read or `PutSecretResult`; secret tokens are separate from bearer identity. |
| `listSecrets(namespace?, options?)`, `getSecretMetadata(key, options?)`, `deleteSecret(key, options?)` | Immutable non-plaintext inventory/metadata or exact mutation revision. |
| `setSecretEnabled(key, enabled, options?)`, `destroySecretVersion(key, version, options?)`, `promoteSecretVersion(key, version, options?)` | Authorized version-state mutations; promotion returns current/previous versions and revision as `bigint`. |
| `watch(callback, options?)`, `watchNamespace(namespace, callback, options?)` | Register on the shared process-client stream and return an idempotent local unsubscriber. Home-namespace discovery makes `watch` asynchronous. |
| `resolve(config, options?)` | Resolve all reachable declarative values concurrently. |
| `createReleaseLoader(options)` | Create an independently runnable loader that shares this client’s authenticated transport. |
| `verifyReleaseDefaults(options)` | `Promise<VerifyReleaseDefaultsResult>`; value-free comparison of canonical alias hashes against the active release. Validates aliases, lowercase-hex digests, verdict vocabulary, alias echo, and count consistency; `RESOURCE_EXHAUSTED` becomes `RateLimitedError`. |
| `close()`, `[Symbol.asyncDispose]()` | Idempotent async ownership boundary for transport, streams, reconciliation, and callbacks. |

### Package root: references, errors, secrets, and transport

| Export | Kind and purpose |
|---|---|
| `NamespaceRef`, `ResourceRef`, `VersionRef` | Types for explicit namespaces, fully qualified keys, and mutually normalized version/label selection. |
| `CURRENT_VERSION`, `UINT64_MAX` | Readonly current-selector and exact protobuf `uint64` upper bound. |
| `parseNamespace`, `splitDisplayPath`, `displayNamespace`, `displayPath`, `resolveRef`, `refOf`, `namespaceEquals`, `namespaceKey`, `normalizeVersionRef` | Pure reference parse/render/compare/normalization helpers. `resolveRef` requires a namespace for a relative key; `refOf` is the tolerant trusted-input parser. |
| `KmsError`, `ConfigError`, `NoNamespaceError`, `NotInitializedError`, `RateLimitedError` | Stable error class hierarchy for remote, configuration, relative-key, declarative-lifecycle, and exhausted per-identity budget (`resource_exhausted`) failures. |
| `KmsErrorCode`, `KmsErrorOptions` | Types for bounded programmatic codes and optional original gRPC status. |
| `isKmsError`, `mapGrpcError`, `normalizeError`, `wrapError` | Error narrowing, transport normalization (`normalizeError` aliases `mapGrpcError`), and context wrapping that preserves stable codes. |
| `Secret`, `newSecret`, `REDACTED`, `SecretMetadata` | Redacting, defensive plaintext wrapper; constructor/factory accept bytes or string plus non-sensitive metadata. |
| `tlsFromFiles`, `mtlsFromFiles`, `tlsFromBytes` | Node credential factories for server-authenticated TLS or mTLS from validated files/bytes. The CA argument always identifies server trust. |
| `UnaryMethod<Request, Response>`, `BidiMethod<Request, Response>`, `TransportCallOptions`, `DuplexRpc<Request, Response>`, `RpcTransport` | Type-only injectable protocol boundary. Generated service descriptors are intentionally not exported. |

### Package root: declarative values

| Export | Kind and signature family | Purpose |
|---|---|---|
| `SecretValue`, `SecretValueOptions`, `SecretReadOptions` | Class and types | Initialize once from env/KMS/default; explicit `value`/`text`/`stringValue`/`bytes`/`secret` access, redacted implicit rendering. |
| `ParameterValue`, `ParameterValueOptions`, `ValueReadOptions` | Class and types | Initialize a string parameter, subscribe unless `static`, read with `get`, register `onChange`, and end owned subscription with `dispose`. |
| `ValueResolver`, `SubscriptionHandle`, `ChangeCallback`, `DeclarativeValue` | Structural/callback/union types | Minimal injection surface used by declarative fields and tests without exposing transport internals. |
| `collectDeclarativeValues(config)` | Function | Discover supported values through own data properties and arrays without invoking getters. |
| `resolveValues(config, resolver, options?)` | Function | Resolve discovered values concurrently; successful siblings remain initialized if another fails. |
| `ResolutionError` | `AggregateError` subclass | Carries all field errors from a resolution pass. |

### Package root: publishing

| Export | Kind and signature family | Purpose |
|---|---|---|
| `DecimalRevision`, `PublicJsonPrimitive`, `PublicJsonValue`, `PublicJsonObject` | Types | Branded canonical decimal revision and the exact safe JSON domain. |
| `PolicySnapshot<T>`, `SnapshotReader<T>` | Types | One immutable `{revision, value}` generation and its synchronous atomic read boundary. |
| `PublicConfig<T>`, `PublicConfigWire<T>` | Types | Internal `bigint` and HTTP/JSON decimal-string representations of the same public generation. |
| `PublicFieldSelector`, `PublicProjectionMap`, `PublicProjection` | Types | Explicit selector allowlist and its defensively cloned/frozen projection. |
| `definePublicProjection(allowlist)` | Generic overloaded function | Infer a typed projection while making every published top-level key explicit. |
| `freezePublicJson(value)`, `normalizePublicConfigWire(value, validateConfig?)` | Functions | Strictly validate, clone, and freeze public JSON or an untrusted wire envelope. |
| `formatRevision`, `parseRevision`, `formatPublicConfigEtag` | Functions | Lossless `bigint`/canonical-decimal conversion and strong revision ETag formatting. |
| `ValidationSuccess`, `ValidationFailure`, `ValidationDecision`, `AuthoritativeValidator` | Types | Application validation callback contract against one captured policy. |
| `PolicyValidationResult` | Type | `success`, `validation_failed`, `policy_changed`, or `unavailable` discriminated result. |
| `PolicyPublisherEvent`, `PolicyPublisherObserver` | Types | Frozen, value-free publication/unavailability/stale-rejection/validation events with decimal revisions and observation timestamps. Async or throwing observers are isolated from policy behavior. |
| `CreatePolicyPublisherOptions`, `PolicyPublisher` | Types | Publisher construction (including optional `onEvent`) and `read`, `readWire`, `etag`, and `validate` operations. |
| `createPolicyPublisher(options)` | Function | Couple one atomic source, allowlisted projection, and authoritative validator. |

### Package root: releases

| Export | Kind and signature family | Purpose |
|---|---|---|
| `ReleaseLoader`, `ClientReleaseLoaderOptions` | Class and type | One-concurrent-run exact-version release lifecycle; inspect `instanceId`, `status()`/`stats()`, request `stop()`, and await `run(prepare, signal?)`. Sequential runs are permitted, matching Go. |
| `runTypedRelease(loader, decode, prepare, signal?)` | Generic function | Split fallible snapshot decoding from application resource preparation without weakening atomic commit. |
| `PreparedRelease`, `PrepareRelease`, `ReleaseDivergence` | Types | Candidate callback contract: synchronous infallible `commit` and at-most-once `abort`, each returning exactly `undefined`, plus cooperative `AbortSignal`. An optional `releaseDivergence()` puts a bounded divergence flag and field count on the applied acknowledgement only. |
| `VERIFY_VERDICTS`, `VerifyVerdict`, `VerifyDefaultsEntry`, `VerifyReleaseDefaultsOptions`, `VerifyDefaultsVerdict`, `VerifyReleaseDefaultsResult` | Readonly value/types | Bounded verdict vocabulary (`match`, `differs`, `missing_in_release`, `unknown_alias`, `secret_alias`, `unsupported_content_type`), the value-free verify request, and the frozen validated result with `passed()`. |
| `SecretTokenProvider`, `ValidateReleaseManifest` | Callback types | Fetch per-entry secret authorization locally and reject a manifest before resource resolution. |
| `ReleaseManifest`, `ReleaseManifestInit`, `ReleaseSnapshot`, `ReleaseSnapshotInit` | Immutable classes/types | Unresolved identity/entries and fully resolved exact candidate; serialization and inspection omit values. |
| `ReleaseEntryMetadata`, `ReleaseEntryMetadataInit`, `ReleaseEntryKind` | Immutable class/types | Non-secret alias, resource, version, content, digest, and protection metadata. |
| `ReleaseParameter`, `ReleaseSecret` | Immutable value classes | Exact candidate values; release-secret access returns copies and implicit rendering redacts. |
| `RELEASE_STATES`, `ReleaseState`, `RELEASE_REJECTION_CATEGORIES`, `ReleaseRejectionCategory` | Readonly values/types | Complete bounded service acknowledgement states and rejection taxonomy. |
| `ReleaseLoaderStatus`, `ReleaseLoaderStats` | Types | Copied, value-free health and counters including applied/observed revisions and reconnects. |
| `ClassifiedReleaseError`, `ReleaseCandidateError`, `classifiedReleaseCategory` | Classes/function | Deliberately bounded local classification and safe loader failure reporting. |

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
the caller identity. Parameter and secret reads carrying a `secretToken`
bypass the shared read cache and never populate it. Lazy namespace discovery
is coalesced as client-owned work under the default RPC deadline; each caller's
earlier deadline or cancellation independently bounds its wait without
poisoning concurrent callers; an already-cancelled caller starts no shared
discovery. `RpcTransport`, `UnaryMethod`, `BidiMethod`, and `DuplexRpc` are
exported for deterministic tests and advanced transport integration, but
generated protobuf symbols remain internal.

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
| `client.createReleaseLoader(options)` | Bind an atomic loader to the client's authenticated transport and configured/discovered namespace; exact fetches preserve and verify the server-returned resource identity without entering the ordinary read cache. |
| `ReleaseLoader.run(prepare, signal)` | Resolve one exact, verified candidate at a time; abort superseded work; synchronously commit prepared state; preserve last known good after later rejection. |
| `ReleaseLoader.status()`, `stats()` | Return copied non-secret loader health and bounded rejection categories. |
| `ReleaseSnapshot` | Provide immutable parameter access and defensive redacting secret access. JSON and inspection omit all values. |
| `ClassifiedReleaseError` | Let application decoding map a failure to an allowed acknowledgement category without sending raw error text. |

`prepare` may do fallible parsing, validation, and resource allocation. Its
returned `commit()` and `abort()` must complete synchronously, return exactly
`undefined`, and never throw; the declaration and runtime both reject Promise
or thenable callbacks. `abort()` releases candidate-owned resources. A release
containing protected secrets supplies a local `secretTokenProvider`. `stop()`
requests cooperative shutdown; preparation work must observe its `AbortSignal`,
and callers still await the `run()` promise before closing the client.

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

### Server entry point

`@suhaibinator/kms/next/server` exports the following complete stable surface:

| Export | Kind and signature family | Purpose |
|---|---|---|
| `runtime` | Constant `"nodejs"` | Re-export from a Route module to declare the required Next runtime. |
| `MAX_PRIVATE_PUBLIC_CONFIG_AGE_SECONDS` | Constant `300` | Hard upper bound accepted for private browser-cache age. |
| `createNextKms(options)`, `NextKms`, `CreateNextKmsOptions`, `NextKmsResource` | Function and types | Own a lazily/eagerly initialized process resource, its atomic snapshot source, projection, validator, optional cleanup, and optional publisher observer. |
| `NextKmsClosedError` | Error class | Signals any start/read/validation operation attempted after permanent close. |
| `createPublicConfigGET(provider, options?)`, `PublicConfigGET`, `PublicConfigProvider` | Function and types | Adapt a safe public-config provider to a Node Route Handler. |
| `PublicConfigCachePolicy`, `PublicConfigRouteOptions` | Types | Select `no-store` or bounded private-only browser caching. |
| `PublicConfigRouteEvent`, `PublicConfigRouteObserver` | Types | Frozen, value-free `served`, `not_modified`, or `unavailable` HTTP event with observation time/duration and a decimal revision where available. Throwing/async-rejecting observers are isolated. |
| `ProcessShutdownOptions` | Type | Select catchable process signals, an isolated cleanup-error callback, and an application-owned post-cleanup termination callback. Uncatchable signals are rejected before any listener is installed. |
| `DecimalRevision` | Re-exported type | Canonical decimal `uint64` used at this HTTP boundary. |

The returned `NextKms` operations are `start`, `close`, `readPolicy`,
`readPublicPolicy`, `validateAtRevision`, `createPublicConfigGET`, and
`installProcessShutdown`. Reads start lazily; concurrent starts share one
attempt, a failed attempt may retry, and concurrent closes share permanent
cleanup. `installProcessShutdown` returns a listener uninstaller and, by
design, performs cleanup only. Installing a Node signal listener suppresses
Node's default handling, so the application or process supervisor must own its
termination policy; the adapter neither calls `process.exit()` nor re-sends a
signal whose behavior could be changed by other application listeners. A
long-lived server must supply `onCleanupComplete` (as the bundled example does)
or arrange equivalent supervisor-owned termination.

### Client entry point

`@suhaibinator/kms/next/client` exports exactly:

| Export | Kind and signature family | Purpose |
|---|---|---|
| `usePublicConfig(initial, options?)` | React hook | Hold one last-known-good public generation, refresh conditionally, and install a validated newer `policy_changed` result. |
| `UsePublicConfigOptions<TConfig>` | Type | Configure endpoint/fetch injection, application shape validation, mount/focus/navigation refresh, navigation identity, and an optional safe observer. |
| `UsePublicConfigResult<TConfig>` | Type | Read frozen `config`, exact `revision`, refresh/error state, invoke `refresh`, or apply a structured server result. |
| `PublicConfigClientEvent`, `PublicConfigClientObserver` | Types | Frozen, value-free refresh success/failure and policy-recovery success/rejection events. Revisions are canonical decimal strings; success events include duration/change metadata where applicable. Observer failures are isolated. |

`@suhaibinator/kms/next/server` begins with `import "server-only"` and rejects
Edge runtime execution. `createNextKms` accepts one application-owned
initializer, projection, and validator and returns:

- `start()` and `close()` for coalesced process-local lifecycle;
- `readPolicy()` for server-only policy access and `readPublicPolicy()` for a
  serializable Server Component prop;
- `validateAtRevision()` for authoritative Server Action/Route validation;
- `createPublicConfigGET()` for an ETag-aware App Router Route Handler; and
- `installProcessShutdown()` for cleanup-only SIGINT/SIGTERM hooks; the
  application or supervisor remains responsible for termination.

The Route Handler defaults to `Cache-Control: no-store`. The only alternative
is bounded private browser caching; it never emits shared/CDN caching.

`CreateNextKmsOptions.onPublisherEvent`, `PublicConfigRouteOptions.onEvent`,
and `UsePublicConfigOptions.onEvent` are independent instrumentation points.
They receive revision/timestamp metadata but never policy fields, validation
input/errors, HTTP bodies, secret values, credentials, endpoints, or thrown
error objects. Their timestamps let an application correlate release
activation, server publication, and client observation without placing that
telemetry inside the SDK's trust boundary.

`@suhaibinator/kms/next/client` exports `usePublicConfig(initial, options)`.
The hook retains a last-known-good projection, conditionally refreshes with an
ETag, fences out-of-order responses, refreshes on mount/focus by default, and
installs a validated `policy_changed` result through `applyServerResult`.
It has no KMS credentials, secret types, or transport dependency.

The client hook requires a modern browser with `bigint`, `fetch`,
`AbortController`, and standard focus events. Unit coverage uses a simulated
DOM, while a real Chromium release gate verifies hydration, refresh,
out-of-safe-integer revisions, and stale-policy recovery without a reload.
Chromium is the explicitly qualified browser target.

The complete compile-checked integration is in the
[`next-serverful` example](../sdk/typescript/examples/next-serverful).

## Managed configuration reference

### Configstore entry-point inventory

`@suhaibinator/kms/configstore` exports the following complete stable surface:

| Export | Kind and signature family | Purpose |
|---|---|---|
| `cloneConfig(value)` | Generic function `<T>(T) => T` | Secret-aware defensive deep clone used at managed ownership boundaries. |
| `ValueCodec`, `FieldCodec`, `GroupCodec`, `IntegerCodecOptions`, `BigIntCodecOptions`, `FloatCodecOptions` | Types | Describe strict generated value/group codecs and numeric range policy. |
| `codecs`, `field`, `group`, `decodeGroup`, `encodeGroup` | Values/functions | Compose boolean/string/integer/bigint/float/duration/bytes/object/array/fixed-array/record/nullable codecs and strictly decode or deterministically encode one whole group. |
| `ConfigDecodeError` | Error class | Value-free strict-decoding failure whose message contains only a generated canonical path and fixed diagnostic. |
| `ContractKind`, `ContractEntry`, `validateContract`, `createManifestValidator` | Types/functions | Validate and copy an alias/content-type contract, then build its pre-fetch manifest hook. |
| `REJECTION_CATEGORIES`, `RejectionCategory`, `CandidateError`, `reject`, `rejectDecode` | Readonly value, types, class, functions | Bounded managed-candidate classification; decode wrapping retains only safe generated paths. |
| `FieldDifference`, `FieldChange`, `Phase`, `MismatchPhase`, `MismatchSeverity` | Types | Non-secret source-default comparison fields, previous/current change records (secrets path-only), the startup/runtime phase (`MismatchPhase` is an alias), and the single `"error"` severity. |
| `DefaultMismatchReport`, `AppliedReport`, `CandidateRejectionReport` | Immutable classes | Secret-aware copied drift reporting, the per-generation applied view (`changed()` and `groups()` return fresh redacted copies; `toString` lists paths only), and value-free local rejection diagnostics. |
| `Callbacks`, `ManagedConfigOptions`, `ManagedReleaseClient`, `ManagedPreparedCandidate`, `PrepareManagedCandidate` | Types | Application observers (`onDefaultMismatch` required, `onApplied` and `onCandidateRejected` optional; all synchronous, failures isolated), structural client, generated preparation (including `changed` and `groups`), contract, and release-loader options. |
| `consoleCallbacks(logger, options?)`, `ConsoleLogger`, `ConsoleCallbacksOptions` | Function/types | Ready-made `Callbacks` rendering fixed structured log records (`kms config diverges from source defaults`, `kms config applied`, `kms config group`, `kms config reloaded`, `kms config field changed`, `kms config candidate rejected`) with an optional `component` attribute and `startupSnapshot`/`reloadChanges`/`reloadSnapshot` toggles (every applied generation is dumped in full by default). |
| `startManagedConfig(client, options, prepare, signal?)`, `ManagedConfigManager` | Function and class | Validate before fetch, block until initial atomic publication, then expose `stop`, `wait`, `status`, and `stats`. |
| `ManagedConfigStatus`, `ManagedConfigStats` | Types | Fresh redacted identity/health and bounded counter snapshots. |
| `ReleaseIdentityInit`, `ReleaseIdentity` | Type and immutable class | Value-free copied release identity; use `ReleaseIdentity.from`, `isZero`, and safe serialization. |
| `ConfigSnapshot<T>`, `immutableSnapshot(config, release?)` | Class/function | Private immutable generation with defensive `config()` and typed `get(key)` reads. |
| `canonicalParameterValue(contentType, value)`, `parameterHash(contentType, value)` | Functions | Canonical bytes and lowercase-hex SHA-256 shared with the Go SDK and the server: strict single-document JSON with UTF-8-byte-sorted keys, verbatim number literals, minimal string escaping, duplicate-key and invalid-UTF-8 rejection; other content types byte-for-byte. |
| `DEFAULTS_ARTIFACT_FORMAT`, `MAX_DEFAULTS_ARTIFACT_BYTES`, `MAX_DEFAULT_PARAMETER_VALUE_BYTES`, `DefaultsArtifact`, `DefaultsArtifactContractEntry`, `DefaultsArtifactParameter`, `EncodeDefaultsArtifactInput`, `encodeDefaultsArtifact(input)`, `parseDefaultsArtifact(document)`, `DefaultsArtifactError` | Constants/types/functions/error class | The `kms-config-defaults/v1` parameter-only defaults artifact: deterministic sorted encoding, strict parsing with size bounds, and its value-free error. |
| `verifyDefaults(client, input, options)`, `VerifyClient`, `VerifyInput`, `VerifyOptions`, `VerifyEntryResult`, `VerifyResult` | Function/types/class | Hash every parameter group of a generated contract, call `verifyReleaseDefaults`, and return `passed()`, `failures()`, and a value-free CI `report()` (header, sorted `VERDICT ALIAS CONTENT_TYPE` table, summary counts including unverified aliases). Secret entries are never sent. |

`@suhaibinator/kms/configstore` is the optional Stage 7 layer used by generated
bindings. `startManagedConfig(client, options, prepare, signal)` accepts an
ordinary `KmsClient` through its public `createReleaseLoader` method; transport
and generated protobuf types remain private. The options declare the release
name and exact generated alias/content-type contract, require a synchronous
default-drift reporter, and may add `onApplied` and `onCandidateRejected`
observers; `consoleCallbacks` supplies all three.

Generated preparation performs strict decode and application validation, then
returns a synchronous `publish` swap plus optional `abort`; both must return
exactly `undefined`. It also returns complete source-default differences, the
canonical restart-required fields that changed, the fields that changed since
the previously applied generation, and the canonical non-secret group
documents. The manager:

- validates the exact manifest before fetching entries;
- blocks startup until the first candidate is atomically publishable;
- applies and reports every default divergence (severity `"error"`, startup
  and runtime) instead of refusing startup, and puts only a divergence flag and
  field count on the applied acknowledgement;
- fires `onApplied` after each publication with an immutable, redacted
  `AppliedReport` whose change list is empty for the initial generation;
- rejects a whole runtime candidate if any restart-required field changed;
- retains the last-known-good snapshot and detects later default restoration;
- exposes redacted copied status, metrics, mismatch, and rejection reports; and
- stops through `manager.stop()` followed by `await manager.wait()`.

Generated bindings additionally export `verifyReleaseDefaults(client, config,
options)`, which wires the generated schema digest, contract, and
`encodeParameterGroups(config)` into `verifyDefaults`. Only canonical content
hashes travel over the wire in either direction.

`codecs`, `field`, `group`, `decodeGroup`, and `encodeGroup` provide
duplicate-aware, unknown/missing-field rejecting JSON codecs with exact range
checks. `ConfigSnapshot` and `immutableSnapshot` keep the stored root private
and defensively clone composite and secret-bearing reads. This deliberately
differs from allocation-free Go generated scalar/view getters: JavaScript
managed views favor defensive ownership, represent exact 64-bit
integers/durations as `bigint`, and use explicit `null` where the generated
contract permits absence.

### TypeScript-native generator

`@suhaibinator/kms/configgen` exports the following complete stable library
surface (the `kms-config-gen-ts` executable is the corresponding CLI):

| Export | Kind and signature family | Purpose |
|---|---|---|
| `DESCRIPTOR_FORMAT`, `MAX_RELEASE_ENTRIES` | Constants | Required `kms-config-descriptor/v1` discriminator and 256-entry structural bound. |
| `ReloadPolicy`, `TypeDescriptor`, `NestedFieldDescriptor`, `FieldDescriptor`, `GroupDescriptor`, `SecretDescriptor`, `ConfigDescriptor` | Types | Complete versioned descriptor model for source type, parameter encodings, reload policy, views, and secret aliases. |
| `parseDescriptor(document)`, `normalizeDescriptor(value)` | Functions | Duplicate-aware JSON parsing or unknown-value validation followed by deterministic sorting and deep freezing. |
| `DescriptorError` | Error class | Descriptor syntax, shape, naming, collision, nesting, and range failure. |
| `CONTRACT_FORMAT`, `MAX_SCHEMA_BYTES` | Constants | Generated machine-contract discriminator and 256 KiB generated-schema bound. The server and Go generator accept up to 1 MiB. |
| `GenerateOptions`, `GeneratedArtifacts`, `generate(input, options?)` | Types/function | Deterministically produce binding, schema, contract, and schema SHA-256; import specifiers are explicitly configurable. |
| `OutputPaths`, `verifyArtifacts(paths, artifacts)`, `writeArtifacts(paths, artifacts)` | Type/functions | Name three explicit destinations, compare without mutation, or stage/fsync/replace changed members. Writers must not run concurrently because the three renames are not one filesystem transaction. |
| `StaleArtifactsError` | Error class | Reports the copied list of generated outputs that differ in verify/check mode. |
| `runDefaultsExporter(args, provider, encoder, io?)`, `DefaultsProvider`, `DefaultsEncoder`, `DefaultsExporterIO` | Function/types | Command-line runner that resolves a profile's defaults through the application-owned provider, encodes the defaults artifact with the generated encoder, and writes it to stdout or a file. |

`@suhaibinator/kms/configgen` parses a versioned
`kms-config-descriptor/v1` document and deterministically produces a generated
managed store, Draft 2020-12 parameter schema, and machine contract. The
`kms-config-gen-ts` executable writes all three explicit outputs or compares
them without modification in `--check`/`--verify` mode. The descriptor records
the application root type, parameter groups and field encodings, secret
aliases, hot/restart policy, and consumer views; it must not contain default or
secret values.

The TypeScript generator currently rejects schemas larger than 256 KiB before
writing any artifact. The server and Go generator accept up to 1 MiB.

Generated bindings depend only on the stable package root and `configstore`
entry point. Generated application artifacts are committed and type-checked;
CI reruns the generator in check mode so descriptor and output drift fails the
build.

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
- One client owns one shared parameter watch stream over the reference-counted
  union of namespaces with active local owners. Callbacks never delay applying
  state.
- A release loader permits one concurrent run (and sequential reuse), bounds
  exact-version fetch concurrency, keeps at most one preparation and one
  replace-latest candidate, and preserves the last known good snapshot after
  the first commit.
- Next.js initialization coalesces concurrent starts, owns one process-local
  client/loader, and closes loader work before the client transport.

## Error and secret contract

`KmsError` exposes a bounded code suitable for branching. Known gRPC statuses
map to `not_found`, `permission_denied`, `unauthenticated`, and
`failed_precondition`; namespace/configuration failures use local bounded codes.
Unmapped transport status information is preserved without ever including
caller-supplied secret bytes.

The complete `KmsErrorCode` union is `not_found`, `permission_denied`,
`unauthenticated`, `failed_precondition`, `not_initialized`, `no_namespace`,
`invalid_argument`, `cancelled`, `deadline_exceeded`, `already_exists`,
`resource_exhausted`, `aborted`, `out_of_range`, `unimplemented`, `internal`,
`unavailable`, `data_loss`, and `unknown`. `grpcCode` is present only when a
wire status exists; branch on `code`, not message text.

`Secret` stores a private copy of plaintext bytes. `bytes()`, `text()`, and
`clone()` are the only plaintext access paths; returned byte arrays are fresh
copies. String conversion, JSON conversion, Node inspection, errors, release
snapshots, status, metrics, and logs redact. This defensive-copy behavior is
deliberately stronger than the Go SDK's mutable `Value()` slice.

## Stability, versioning, and deprecation

The public exports documented here follow semantic versioning. Before `1.0.0`,
a minor version may make a documented breaking change. Beginning with `1.0.0`,
removing or incompatibly changing a public method, error code, wire response,
or TypeScript type requires a major release. New backward-compatible fields and
methods may ship in a minor release; compatible security and correctness fixes
ship as patches.

Deprecated APIs remain functional for at least one minor-release line and carry
both a TypeScript `@deprecated` annotation and migration guidance. Generated
protobuf symbols and modules below `src/generated` are internal and are not a
stable public surface.

Every user-visible change is recorded under `Unreleased` in the package
changelog; release entries include an ISO date and migration guidance for
breaking changes. Security fixes are supported on the latest published minor
line.

## Intentional language differences

- JavaScript cannot make arbitrary third-party object reflection safe. Secret
  safety is guaranteed for supported `Secret` accessors, `toString`, `toJSON`,
  and Node's custom inspection hook; applications must not copy plaintext out
  and then log the copy.
- Declarative resolution walks own object properties and arrays with cycle
  detection. It does not traverse `Map`, weak collections, accessors, or class
  internals. Explicit initialization remains available for dynamic shapes.
- The TypeScript watch narrows its namespace scope when the last local owner
  unsubscribes and requests a full snapshot before exposing a newly added
  scope. The current Go watch keeps an add-only process-lifetime union.
- Scope growth and authoritative full snapshots invalidate matching read-cache
  entries, including values that were read before any watch registration. A
  first unknown tombstone invalidates its point-read cache without inventing a
  value-change callback.
- A `ParameterValue` that registers after a newer live update is seeded from
  that fenced state rather than overwriting it with its earlier point read.
- Unsubscribe and `ParameterValue.dispose()` fence callbacks already queued by
  the dispatcher. This is a stronger post-unsubscribe guarantee than the Go
  dispatcher currently provides.
- Synchronous CPU-bound callbacks can block the Node event loop. The SDK
  serializes callback settlements behind a bounded queue, catches callback
  failures, and never blocks stream state application; a never-settling
  application callback causes later notifications to fill and then be dropped
  from that bounded queue.
- Public snapshots use frozen copies and read-only maps rather than Go value
  copies. Every secret accessor still returns an independent `Secret`.
