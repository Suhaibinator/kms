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
Linux-only run or cross-compilation.

## Python SDK

The Python SDK tests start only an in-process fake gRPC server:

```bash
cd sdk/python
python -m venv .venv
.venv/bin/python -m pip install -e '.[dev]'
.venv/bin/python -m mypy
.venv/bin/python -m pytest -q
```

CI runs mypy and pytest on the oldest and newest supported Python versions.

## Frontend

The frontend has compile-time regression checks and a production export:

```bash
cd frontend
npm ci
npm run typecheck
npm run build
test -f out/index.html
```

`npm ci` consumes the committed lockfile, and CI rejects a build that does not
produce the embedded entry point.

## CI jobs

The workflow in `.github/workflows/ci.yml` runs these independent checks:

- `Go build & unit tests (race)` — module tidiness, vet, build, unit/component
  tests, the race detector, and coverage.
- `Go integration tests (race)` — only `./internal/integration`, with fresh
  execution, the race detector, a test timeout, and coverage.
- `Native filesystem security` — `./internal/fileutil`, `./internal/crypto`,
  `./internal/storage`, and `./internal/cli` on Linux, macOS, and Windows so
  native permission and ACL semantics flow through database, backup, restore,
  certificate, and key operations.
- `Python SDK (pytest & mypy)` — the supported Python-version matrix.
- `Frontend (typecheck & build)` — locked install, generated types, TypeScript,
  and static export.
- Go lint and `govulncheck` remain independent required checks.

The integration job deliberately provisions no external services. If a new
integration scenario needs a database or server, construct it inside the test
using temporary files and an ephemeral loopback listener so local and CI runs
stay identical.
