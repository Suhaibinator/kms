# Testing and CI

The repository keeps fast package tests and end-to-end integration tests as
separate CI statuses. Both run for pull requests and pushes to `main`; branch
protection should require both statuses. Every CI job has read-only repository
permissions, a wall-clock timeout, and a dependency cache.

## Go suites

Run the unit and component packages (everything except
`internal/integration`):

```bash
make test-unit
```

Run the hermetic integration suite:

```bash
make test-integration
```

The integration target instruments the production packages matched by
`INTEGRATION_COVERPKG` (by default `./internal/...` and `./sdk/go/...`). This is
intentional: `internal/integration` contains external-package tests but no
non-test statements of its own, so package-local coverage would always be a
misleading 0.0%. To reproduce the CI coverage assertion locally:

```bash
make test-integration GO_TEST_FLAGS='-covermode=atomic -coverprofile=integration-coverage.out'
make check-integration-coverage
```

The check fails when the profile is absent, has no total, or reports 0.0%
statement coverage for the instrumented production packages.

`internal/integration` is the canonical integration-test package. Its harness
uses temporary SQLite databases and key files and may bind only ephemeral
loopback listeners. Tests must not require a pre-existing server, service
container, cloud account, fixed port, operator credential, or access to the
public internet. Each test owns and cleans up its state, which makes the same
command suitable for laptops and hosted CI runners.

Race-enabled variants are available for focused work and the complete suite:

```bash
make test-integration-race
make test
```

Generated managed-configuration artifacts are committed. In this repository,
regenerate the fixture when its root contract changes and run the same
read-only verification used by CI:

```bash
go generate ./internal/configstorefixture/config
make check-configgen
```

The Make target passes `-package ./internal/configstorefixture/config` and
checks the generated binding, schema, and contract without writing them. A
downstream application invoking the generator from its module root must
likewise pass its root package with `-package`; relative output paths are
resolved from the command's working directory. See the
[generation guide](managed-go-configuration.md#generate-bindings-and-artifacts)
for the generic command and required separate-package import topology.

Generator package tests cover deterministic output and stale verification.
Binding tests additionally gate snapshot consistency, defensive composite
access, zero allocations for `Current`/view/scalar getters, a duplicate-aware
strict-decode gauntlet, nil-versus-empty drift, transient retry, callback
redaction/panic isolation, rapid concurrent activations, and the equivalent
real-KMS TLS/gRPC/SQLite matrix. See the
[managed configuration testing section](managed-go-configuration.md#testing).

All Make targets use `-count=1` to prevent Go's result cache from masking a
regression and apply a 10-minute package timeout by default. Override that
limit for local diagnosis with, for example,
`make test-integration GO_TEST_TIMEOUT=15m`.

Filesystem permission behavior is operating-system-specific. Run the native
checks for the current host with:

```bash
make test-platform-security
```

CI executes the file utility, crypto, storage, and CLI packages on Linux,
macOS, and Windows. That is important: the macOS ACL and Windows DACL tests are
build-tagged and the database, backup, restore, certificate, and key workflows
must use those native primitives. They cannot be meaningfully validated by a
Linux-only run or cross-compilation. The Windows job also invokes the
reparse-entry DACL regression by exact test name and requires its JSON event
stream to contain a pass event and no skip event. Its setup deterministically
creates an unprivileged directory junction and fails if it cannot do so; a
renamed test, build-tag drift, or lack of symlink privilege cannot silently
turn that security check green.
Windows system roots may be owned by the exact TrustedInstaller service SID;
other SIDs in the NT SERVICE namespace remain untrusted.

## Python SDK

The Python SDK tests start only an in-process fake gRPC server. OpenSSL must be
available on `PATH` because the TLS transport regressions mint ephemeral test
certificates; GitHub-hosted Ubuntu CI runners provide it by default.

```bash
cd sdk/python
python -m venv .venv
.venv/bin/python -m pip install -e '.[dev]'
.venv/bin/python -m mypy
.venv/bin/python -m pytest -q
```

CI runs mypy and pytest on the oldest and newest supported Python versions.

## TypeScript SDK

The Node SDK has strict type checking, Biome lint/format gates, deterministic
protobuf generation verification, unit and fault-injection tests, compile-only
consumer/example contracts, and a declaration build:

```bash
cd sdk/typescript
npm ci
npm run check
```

The equivalent repository targets are `make typescript` for a clean package
build, `make test-typescript` for runtime and consumer-type tests, and
`make check-typescript` for the complete release gate. All three install from
the committed lockfile first.

CI runs `npm run check` on Node 22, Node 24, and Node 26. Examples under
`sdk/typescript/examples` are included in `npm run test:types`, so changes to
the documented core and serverful Next.js integrations cannot silently drift
from the public exports. The gate also builds declarations, compiles a consumer
against the built package, and runs `npm pack --dry-run` to verify the
publishable manifest. `npm run test:next` performs a real Next.js 16 App Router
build, rejects a Client Component import of `next/server`, and scans browser
chunks for server transport, TLS, generated-protocol, and credential markers.
Generated managed-configuration fixtures must match the descriptor and
exercise the emitted `Store`; applications should run their
`kms-config-gen-ts ... --check` command in CI for the same stale-artifact gate.
The compatibility jobs compile the built package with the exact TypeScript
5.2.2 minimum, build isolated Next.js 14/React 18, Next.js 15/React 18 and 19,
and Next.js 16/React 18 and 19 peer tuples, and launch Chromium against the serverful
fixture. The browser gate verifies initial hydration, HTTP refresh, exact
`bigint` revisions, and `policy_changed` recovery without a reload.

## Frontend

The frontend has compile-time checks, lint/format gates, component tests,
browser smoke tests, and a production export:

```bash
cd frontend
npm ci
npm run typecheck
npm run lint
npm run format:check
npm run test
npx playwright install chromium # first run only
npm run test:e2e
npm run build
test -f out/index.html
```

`npm ci` consumes the committed lockfile, and CI rejects a build that does not
produce the embedded entry point.

## CI jobs

The workflow in `.github/workflows/ci.yml` runs these independent checks:

- `Go build & unit tests (race)` — module tidiness, vet, build, unit/component
  tests, the race detector, and coverage.
- `Go integration tests (race)` — only `./internal/integration` as the test
  driver, with fresh execution, the race detector, a test timeout, production
  package instrumentation, and a mandatory non-zero coverage profile.
- `Native filesystem security` — `./internal/fileutil`, `./internal/crypto`,
  `./internal/storage`, and `./internal/cli` on Linux, macOS, and Windows so
  native permission and ACL semantics flow through database, backup, restore,
  certificate, and key operations.
- `Python SDK (pytest & mypy)` — the supported Python-version matrix.
- `TypeScript SDK` — the complete package gate on supported Node.js majors 22,
  24, and 26.
- `Frontend (quality, tests & build)` — locked install, generated types,
  TypeScript, linting, formatting, component/browser tests, and static export.
- Go lint and `govulncheck` remain independent required checks.

The integration job deliberately provisions no external services. If a new
integration scenario needs a database or server, construct it inside the test
using temporary files and an ephemeral loopback listener so local and CI runs
stay identical.
