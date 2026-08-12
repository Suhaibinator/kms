# `@suhaibinator/kms`

Framework-neutral Node.js SDK for the KMS parameter store and atomic
configuration releases. It provides TLS/mTLS transport, namespace-aware reads
and writes, declarative values, shared hot-reload watches, safe secret wrappers,
atomic release loading, public-policy publishing primitives, and an optional
serverful Next.js adapter.

The SDK requires Node.js 22 or newer and is ESM-only. The KMS transport is
server-only: never import the package root from browser code. The only browser
entry point is `@suhaibinator/kms/next/client`, which consumes an explicitly
allowlisted public HTTP projection and never connects to KMS.

## Installation

```bash
npm install @suhaibinator/kms
```

For the optional Next.js adapter, install compatible peer dependencies too:

```bash
npm install next react
```

## Create a client

Production clients should authenticate with mTLS. The CA file below must trust
the KMS **server** certificate; it is not the service's built-in
client-certificate issuer.

```ts
import { createClient, mtlsFromFiles } from "@suhaibinator/kms";

const client = createClient({
  endpoint: process.env.KMS_ENDPOINT ?? "kms.internal:8443",
  credentials: mtlsFromFiles(
    process.env.KMS_CLIENT_CERT!,
    process.env.KMS_CLIENT_KEY!,
    process.env.KMS_SERVER_CA!,
  ),
  // Optional when the authenticated identity is bound to a namespace.
  namespace: process.env.KMS_NAMESPACE,
});

try {
  const rateLimit = await client.getParameter("rate-limit");
  const signingKey = await client.getSecret("session-signing-key");

  console.log({ rateLimit, signingKey }); // signingKey renders as [REDACTED]
  useSigningKey(signingKey.bytes()); // plaintext access is always explicit
} finally {
  await client.close();
}
```

Use `tlsFromFiles(caFile)` for server-authenticated TLS with a bearer token.
Cleartext requires the explicit `insecure: true` option and is intended only
for a trusted local development server:

```ts
const local = createClient({
  endpoint: "127.0.0.1:8443",
  namespace: "dev/example",
  token: process.env.KMS_TOKEN,
  insecure: true,
});
```

`close()` is idempotent and cancels SDK-owned streams, reconciliation, and
queued callbacks. Unary calls have a five-second default deadline; pass an
earlier `deadline` or an `AbortSignal` per operation when needed.

## Keys, versions, and precision

Relative keys, such as `billing/stripe-key`, resolve in the configured or
identity-bound namespace. An absolute display path such as
`/staging/example/rate-limit` makes a cross-namespace request, subject to
authorization. Interior slashes remain part of the key.

Version, revision, and timestamp fields originating as protobuf `uint64` are
always `bigint`. Never coerce them to `number`. Public JSON/HTTP contracts use
canonical decimal strings; use `formatRevision` and `parseRevision` at that
boundary.

To request an exact version or label:

```ts
const pinned = await client.getParameter("rate-limit", { version: 7n });
const previous = await client.getSecret("session-signing-key", {
  label: "previous",
  secretToken: process.env.SIGNING_KEY_TOKEN,
});
```

## Declarative values and hot reload

Resolution order is a non-empty environment override, then KMS, then an
allowed non-empty default. Missing values and configuration errors are
reported together by `client.resolve`. Parameters hot-reload by default;
secrets are resolved once and remain redacted during implicit rendering.

```ts
import { ParameterValue, SecretValue } from "@suhaibinator/kms";

const config = {
  database: {
    password: new SecretValue("db-password", {
      token: process.env.DB_PASSWORD_TOKEN,
      envVar: "DB_PASSWORD",
    }),
  },
  requestTimeout: new ParameterValue("request-timeout-ms", {
    default: "3000",
  }),
};

await client.resolve(config);
const unsubscribe = config.requestTimeout.onChange((_oldValue, newValue) => {
  console.info("request timeout updated", newValue);
});

const timeoutMs = Number.parseInt(config.requestTimeout.get(), 10);
const passwordBytes = config.database.password.bytes();

// At application shutdown:
unsubscribe();
await config.requestTimeout.dispose();
await client.close();
```

Set `static: true` on a `ParameterValue` for a boot-time-only read. Defaults are
used for `not_found` by default. Broader fallback requires
`fallbackToDefaultsOnError: true`; namespace and wiring errors still fail
closed.

## Watches

`client.watch` subscribes to the client's namespace. The SDK shares one
bidirectional stream, resumes by revision, fences stale events, acknowledges
heartbeats, reconnects with jittered backoff, and periodically reconciles.

```ts
const unwatch = await client.watch((event) => {
  if (event.kind === "parameter_changed") {
    console.info(event.path, event.revision);
  }
});

// Stop only this observer; close the client to stop all SDK work.
unwatch();
```

Callbacks run outside the stream-apply path. They should still remain small:
synchronous CPU work blocks the Node.js event loop.

## Atomic configuration releases

`ReleaseLoader` resolves every entry at its exact version, verifies content and
the deterministic release digest, and calls `prepare` before an infallible
synchronous `commit`. A newer candidate aborts superseded work. After the first
successful commit, a rejected candidate preserves the last-known-good value.

```ts
import type { PreparedRelease, ReleaseSnapshot } from "@suhaibinator/kms";

let active: Readonly<{ requestTimeoutMs: number }> | undefined;
const loader = await client.createReleaseLoader({ name: "runtime" });

await loader.run(async (snapshot: ReleaseSnapshot): Promise<PreparedRelease> => {
  const raw = snapshot.parameter("request_timeout")?.value();
  const requestTimeoutMs = Number(raw);
  if (!Number.isInteger(requestTimeoutMs) || requestTimeoutMs <= 0) {
    throw new Error("request_timeout must be a positive integer");
  }
  const candidate = Object.freeze({ requestTimeoutMs });

  return {
    commit() {
      active = candidate;
    },
    abort() {},
  };
});
```

Supply `secretTokenProvider` when a release contains token-protected or
client-bound secrets. Preparation errors that may contain application data are
reported to the service as bounded rejection categories, not raw text.

## Public policy and Next.js

The framework-neutral `definePublicProjection` and `createPolicyPublisher`
helpers derive browser-safe JSON from one atomic application snapshot. The
allowlist is explicit: adding a private policy field cannot publish it.
Backend validation remains authoritative, and a stale browser receives a
structured `policy_changed` result containing the current public projection.

The optional adapters are split by trust boundary:

- `@suhaibinator/kms/next/server` requires the Next.js Node runtime and owns
  process-local lifecycle, server reads, validation, and the public Route
  Handler.
- `@suhaibinator/kms/next/client` is a Client Component hook that reads only
  the public HTTP response and can install a `policy_changed` response without
  reloading the page.

See [`examples/next-serverful`](examples/next-serverful) for a minimal App
Router integration. It intentionally lives outside this repository's embedded
static-export frontend, which has no Node server runtime.

## Errors and secrets

Branch on bounded error codes rather than transport text:

```ts
import { isKmsError } from "@suhaibinator/kms";

try {
  await client.getParameter("optional-feature");
} catch (error) {
  if (isKmsError(error, "not_found")) {
    // Handle the documented absence case.
  } else {
    throw error;
  }
}
```

`Secret`, `SecretValue`, and release secrets redact string conversion, JSON,
and Node inspection. Their byte accessors return defensive copies. Once an
application explicitly extracts plaintext, that copy is ordinary application
memory and must not be logged, serialized, sent to a browser, or retained
longer than necessary.

## Package boundaries and support

| Import | Supported runtime | Purpose |
|---|---|---|
| `@suhaibinator/kms` | Node.js 22+ | Core client, values, watches, releases, publishing |
| `@suhaibinator/kms/configstore` | Node.js 22+ | Optional generated managed-configuration runtime |
| `@suhaibinator/kms/next/server` | Next.js Node runtime | Server-only lifecycle and Route Handlers |
| `@suhaibinator/kms/next/client` | React browser bundle | Public-policy refresh and stale recovery |

The package is ESM-only. Next.js 14 through 16 and React 18 through 19 are the
declared optional peer ranges. Generated protobuf modules are internal and are
not a compatibility surface.

The public exports follow semantic versioning. Before `1.0.0`, minor releases
may still contain breaking API changes and will document them in the package
changelog. See the repository's
[public API policy](../../docs/sdk-typescript-api.md),
[parity ledger](../../docs/sdk-typescript-parity.md), and
[migration guide](../../docs/migration.md) for compatibility details.

## Development

From `sdk/typescript`:

```bash
npm ci
npm run check
```

`npm run check` verifies generated protobufs, types, lint, formatting, tests,
consumer type contracts, and the distributable build. The repository Makefile
also exposes `typescript`, `test-typescript`, and `check-typescript` targets.

Security reports and deployment guidance are in the package security notice.
This package is licensed under the repository's [MIT license](../../LICENSE).
