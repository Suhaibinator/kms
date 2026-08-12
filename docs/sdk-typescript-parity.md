# Go-to-TypeScript SDK parity matrix

This is the living behavioral parity ledger required by the TypeScript SDK
delivery plan. `complete` means the behavior has an automated TypeScript test;
`partial` is never advertised as full parity.

| Go capability | TypeScript API/module | Required behavior | Test location | Status |
|---|---|---|---|---|
| Secure construction and close | `KmsClient`, `tlsFromFiles`, `mtlsFromFiles` | Fail-closed transport, TLS/mTLS, idempotent bounded cleanup | `sdk/typescript/tests/client.test.ts`, `transport.test.ts` | partial — real TLS/mTLS handshake coverage pending |
| Namespace discovery and display paths | `KmsClient.whoAmI`, ref helpers | Lazy retryable discovery, cached unbound result, interior key slashes | `sdk/typescript/tests/refs.test.ts`, `client.test.ts` | complete |
| Reads/writes and auth metadata | `get/put/list/delete` methods | Bearer and per-secret metadata, deadlines, exact `bigint` values | `sdk/typescript/tests/client.test.ts` | complete |
| Typed errors | `KmsError` | Bounded status mapping without plaintext | `sdk/typescript/tests/errors.test.ts` | complete |
| Secret redaction and copying | `Secret` | Explicit access only; string/JSON/inspect redaction; no shared buffers | `sdk/typescript/tests/secret.test.ts` | complete |
| Bounded TTL cache | internal `ReadCache` | `(path,version,label)` keys, 4096 caps, invalidation, secret clones, token bypass | `sdk/typescript/tests/cache.test.ts` | complete |
| Declarative values and nested resolution | `SecretValue`, `ParameterValue`, `resolve` | env/store/default order, strict fallback, callbacks, hot/static values, cycles | `sdk/typescript/tests/values.test.ts` | complete |
| Shared parameter watch | `watch`, `watchNamespace` | One bidi stream, union/restart, heartbeats, resume, revision fencing | `sdk/typescript/tests/watch.test.ts` | partial — reconnect/resume fault test pending |
| Reconciliation | internal subscription manager | Five-minute full sync, page cap, no false deletes, tombstone fences | `sdk/typescript/tests/watch.test.ts` | partial — pagination/tombstone fault matrix pending |
| Immutable release snapshot | `ReleaseManifest`, `ReleaseSnapshot` | Redacted serialization and defensive accessors | `sdk/typescript/tests/releases/types.test.ts` | complete |
| Release exact resolution and digest | `ReleaseLoader` | Deterministic protobuf digest; ref/version/content/digest verification | `sdk/typescript/tests/releases/digest.test.ts`, `loader.test.ts` | complete |
| Atomic release lifecycle | `ReleaseLoader.run` | Supersession, prepare/commit/abort, active fence, LKG, reliable acks | `sdk/typescript/tests/releases/loader.test.ts` | complete |
| Public projection | `definePublicProjection`, `createPolicyPublisher` | Allowlist-only JSON, one captured snapshot, decimal revisions, stale result | `sdk/typescript/tests/publishing.test.ts` | complete |
| Next.js adapter | `next/server`, `next/client` | Server-only ownership, safe Route Handler, refresh and stale recovery | `sdk/typescript/tests/next-server.test.ts`, `next-client.test.tsx` | complete |
| Managed strict decoding | `configstore` | Duplicate/unknown/missing/type/range checks; transactional decoding | `sdk/typescript/tests/configstore-codecs.test.ts` | complete |
| Managed drift/restart policy | `ManagedConfigManager` | Startup drift, runtime restore, whole-candidate restart rejection | `sdk/typescript/tests/configstore-manager.test.ts` | planned |
| TypeScript-native generation | `kms-config-gen-ts` | Stable binding/schema/contract output and check mode | `sdk/typescript/tests/configgen.test.ts` | planned |
| Generated protobuf contract | `src/generated/kms.ts` | Complete service/messages, `uint64` as `bigint`, stale generation check | `npm run check:generated` | complete |
| Published entry points and examples | package root, `next/server`, `next/client`, `configstore` | Declaration build plus compile-checked consumer and serverful Next.js examples | `sdk/typescript/tests/types`, `npm run build` | complete |
| Supported Node majors | package release gate | Typecheck, lint, tests, consumer types, and build on Node 22 and 26 | `.github/workflows/ci.yml` | complete |
| Real-server interoperability | core transport, watches, releases | TLS/mTLS, auth, unary methods, bidi recovery, and releases against a protocol-faithful KMS server | not implemented | planned |

Intentional differences are documented in
[`sdk-typescript-api.md`](sdk-typescript-api.md). The SDK must not claim managed
configuration parity until all Stage 7 rows are complete.

The remaining `partial` and `planned` rows are release-visible qualification
gaps. Unit coverage and a declaration build do not substitute for the required
real-server, reconnect/fault, stress, and compatibility scenarios. Until those
rows complete, the package may be published as a prerelease candidate but must
not advertise literal full Go parity or a generally available stable release.
