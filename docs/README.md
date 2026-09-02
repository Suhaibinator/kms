# Documentation

The repository root [`README`](../README.md) explains what KMS is and provides
a local quick start. Use this index for deeper reference material.

## Operate and integrate

- [`operations.md`](operations.md) — configuration,
  [development mode (`dev`)](operations.md#development-mode-dev),
  production mTLS onboarding,
  [admin credentials and browser setup](operations.md#admin-credentials-and-browser-setup),
  CLI commands including
  [JSON output, exit codes, token files, and confirmations](operations.md#global-flags-output-formats-and-exit-codes),
  [audit list/export/prune with retention and archive](operations.md#audit-retention-and-archive),
  and [running any process with store values](operations.md#run-any-process-with-store-values);
  startup, [systemd](operations.md#running-under-systemd) and
  [hot reload over SIGHUP](operations.md#hot-reload-sighup), backup/restore,
  [Prometheus metrics](operations.md#prometheus-metrics), and retention.
- [Deployment assets](../deploy/README.md) — the systemd unit and the
  Prometheus alerting rules that go with the sections above.
- [`security.md`](security.md) — current encryption, authentication,
  authorization, audit, and threat-boundary guarantees.
- [`http-api.md`](http-api.md) — browser/admin HTTP API and console aggregates.
- [`configuration-releases.md`](configuration-releases.md) — immutable release,
  schema, activation, watch, and acknowledgement semantics.
- [`migration.md`](migration.md) — migration from SuhaibParameterStore.

## Client SDKs

- [`sdk-go.md`](sdk-go.md) — Go client, declarative values, watches, and release
  loading.
- [`sdk-python.md`](sdk-python.md) — synchronous and asyncio Python clients,
  declarative values, watches, and release loading.
- [TypeScript SDK guide](../sdk/typescript/README.md) — Node.js client,
  framework adapters, generated configuration, and package boundaries.
- [`sdk-typescript-api.md`](sdk-typescript-api.md) — TypeScript public API and
  compatibility policy.
- [`sdk-typescript-parity.md`](sdk-typescript-parity.md) — living behavioral
  parity and qualification matrix.

## Managed configuration

- [`managed-go-configuration.md`](managed-go-configuration.md) — generated,
  typed Go configuration and runtime lifecycle.
- [Python managed configuration](../sdk/python/MANAGED_CONFIG.md) — Pydantic
  generation, defaults, runtime use, and verification.
- [`managed-defaults.md`](managed-defaults.md) — export, verify, preview, and
  apply application-owned defaults.
- [`consumer-adoption.md`](consumer-adoption.md) — adoption checklist for a
  consuming Go service.
- [Runnable managed Go example](../examples/managed-config/README.md).

## Develop and release

- [`testing.md`](testing.md) — local suites, generated artifacts, frontend test
  boundaries, and CI jobs.
- [Frontend development](../frontend/README.md) — local UI development and
  static export workflow.
- [`releasing.md`](releasing.md) — published artifacts, verification, and the
  maintainer release process.

## Historical and audit records

These files preserve decisions or evidence from completed work. They are not
current API or operational contracts.

- [`../plan.md`](../plan.md) — original requirements before the namespace-native
  rewrite.
- [`../plan-namespaces.md`](../plan-namespaces.md) — completed namespace-native
  implementation plan.
- [`sdk-typescript-plan.md`](sdk-typescript-plan.md) and
  [`sdk-typescript-review-ledger.md`](sdk-typescript-review-ledger.md) — completed
  TypeScript delivery plan and review evidence.
