# `@suhaibinator/kms`

Framework-neutral Node.js SDK for the KMS parameter store and atomic
configuration releases. It provides TLS/mTLS transport, namespace-aware reads
and writes, declarative values, shared hot-reload watches, safe secret wrappers,
atomic release loading, public-policy publishing primitives, and an optional
serverful Next.js adapter.

The SDK requires Node.js 22 or newer and is ESM-only. CI qualifies the complete
release gate on Node.js 22, 24, and 26. Its declarations use
TypeScript features available in TypeScript 5.2 and newer; the repository
release gate currently runs the pinned TypeScript 7 compiler. The KMS transport
is server-only: never import the package root from browser code. The only
browser entry point is `@suhaibinator/kms/next/client`, which consumes an
explicitly allowlisted public HTTP projection and never connects to KMS.

The [complete public API and compatibility reference](https://github.com/Suhaibinator/kms/blob/main/docs/sdk-typescript-api.md)
lists every stable export, lifecycle boundary, and intentional language
difference.

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
  const signingKeyBytes = signingKey.bytes(); // plaintext access is explicit
  try {
    // Initialize the application signer from signingKeyBytes.
  } finally {
    signingKeyBytes.fill(0);
  }
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
  if (event.type === "put") {
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

See the
[`next-serverful` example](https://github.com/Suhaibinator/kms/tree/main/sdk/typescript/examples/next-serverful)
for a minimal App
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

## Generated managed configuration

The optional `@suhaibinator/kms/configstore` entry point lets generated
bindings strictly decode a complete release, compare source-owned defaults,
publish an immutable typed generation, and reject runtime changes to
restart-required fields. Declare the application-owned root type beside a
versioned descriptor; the descriptor contains structure and policy, never
defaults, secret values, or physical KMS paths:

```ts
// src/config.ts
import type { Secret } from "@suhaibinator/kms";

export interface RuntimeConfig {
  requestTimeoutMs: number;
  databasePassword: Secret;
}
```

```json
{
  "format": "kms-config-descriptor/v1",
  "source": { "module": "./config.js", "type": "RuntimeConfig" },
  "groups": [
    {
      "alias": "runtime",
      "fields": [
        {
          "property": "requestTimeoutMs",
          "jsonName": "request_timeout_ms",
          "reload": "hot",
          "views": ["worker"],
          "type": { "kind": "integer", "bits": 32 }
        }
      ]
    }
  ],
  "secrets": [
    {
      "property": "databasePassword",
      "alias": "database_password",
      "reload": "restart",
      "views": ["worker"]
    }
  ]
}
```

Generate and commit the binding, Draft 2020-12 parameter schema, and machine
contract:

```bash
kms-config-gen-ts \
  --descriptor config.kms.json \
  --binding-output src/config.generated.ts \
  --schema-output config/runtime.schema.json \
  --contract-output config/runtime.contract.json
```

The generated `Store` accepts source-owned defaults and application validation.
A normal `KmsClient` owns its release lifecycle:

```ts
import { Secret } from "@suhaibinator/kms";
import { Store } from "./config.generated.js";

const store = new Store(
  {
    requestTimeoutMs: 3000,
    // Secret defaults must be the zero Secret; plaintext never belongs here.
    databasePassword: new Secret(),
  },
  (candidate) => {
    if (candidate.requestTimeoutMs <= 0) {
      throw new Error("requestTimeoutMs must be positive");
    }
  },
);

const manager = await store.start(
  client,
  {
    release: "runtime",
    onDefaultMismatch(report) {
      // The report is secret-aware and path-bounded. Forward it to local telemetry.
      console.error(String(report));
    },
  },
);

const active = store.current();
console.info(active.worker().requestTimeoutMs, active.release.version);

// At shutdown, stop the manager before its client transport.
manager.stop();
await manager.wait();
await client.close();
```

Generated preparation must do all fallible decode/validation work before its
synchronous `publish` callback. Startup drift fails closed unless
`allowDefaultMismatch: true` is set, and even an allowed mismatch always calls
`onDefaultMismatch`. A runtime candidate changing any restart-required field
is rejected as a whole while the last-known-good snapshot remains active.

The generated binding, schema, and contract are application artifacts. Run the
same command with `--check` (or `--verify`) in CI so the descriptor cannot drift
from committed output. The library API for custom tooling is available at
`@suhaibinator/kms/configgen`.

## Package boundaries and support

| Import | Supported runtime | Purpose |
|---|---|---|
| `@suhaibinator/kms` | Node.js 22+ | Core client, values, watches, releases, publishing |
| `@suhaibinator/kms/configstore` | Node.js 22+ | Optional generated managed-configuration runtime |
| `@suhaibinator/kms/configgen` | Node.js 22+ | Descriptor parser and deterministic artifact generator |
| `@suhaibinator/kms/next/server` | Next.js Node runtime | Server-only lifecycle and Route Handlers |
| `@suhaibinator/kms/next/client` | React browser bundle | Public-policy refresh and stale recovery |

The package is ESM-only. Next.js 14 through 16 and React 18 through 19 are the
intended optional peer ranges. The repository currently qualifies the adapter
with Next.js 16 and React 19; the older accepted peer majors are not yet an
independent CI matrix. Generated protobuf modules are internal and are not a
compatibility surface.

The `next/client` hook targets modern browsers with native `bigint`, `fetch`,
`AbortController`, and standard focus events. Its automated tests run in a DOM
simulation and the release gate builds a real Next.js App Router fixture,
inspects its browser chunks for server-only dependencies/markers, and rejects a
Client Component import of `next/server`. A cross-browser behavior matrix is
still a release qualification gap.

The public exports follow semantic versioning. Before `1.0.0`, minor releases
may still contain breaking API changes and will document them in the package
changelog. See the repository's
[public API policy](https://github.com/Suhaibinator/kms/blob/main/docs/sdk-typescript-api.md),
[parity ledger](https://github.com/Suhaibinator/kms/blob/main/docs/sdk-typescript-parity.md),
and [migration guide](https://github.com/Suhaibinator/kms/blob/main/docs/migration.md)
for compatibility details.

## Development

From `sdk/typescript`:

```bash
npm ci
npm run check
```

`npm run check` verifies generated protobufs, types, lint, formatting, tests,
consumer type contracts, the distributable build, a real Next.js trust-boundary
build, and the publishable package manifest. The repository Makefile also
exposes `typescript`, `test-typescript`, and `check-typescript` targets.

Security reports and deployment guidance are in the package security notice.
This package is licensed under the included [MIT license](LICENSE).
