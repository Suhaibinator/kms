# Release session protocol and rollout

This is the acknowledgement contract shared by the Go, TypeScript, synchronous
Python, and asynchronous Python release loaders. It also governs generated Go
loaders, which use the Go runtime. It replaces timestamp/lifecycle-rank reduction
of legacy acknowledgement rows. Historical legacy rows remain inspection data;
they are not evidence of current health.

## Causality and replay

A loader execution registers a unique session scoped to its authenticated
identity, namespace, release name, schema track, client, and instance. A network
reconnect retains the session; each new run of a loader creates a new session even
if it reuses the loader object or instance name. Do not configure, copy, or persist a session ID
across process restarts. A live connection generation fences acknowledgements
and disconnects from superseded streams.

Each newly generated acknowledgement receives a strictly increasing, nonzero
sequence in its session. Replay preserves that sequence, original timestamp,
and complete payload. Replaying an outcome, or reconciling an unchanged applied
target, must not generate a new lifecycle event. A genuine retry may generate
new received, prepared, and applied events after rejection of the same target.

The assigned target revision orders configuration targets. Release version is
not an ordering key: rollback and pinning can assign an older version at a newer
target revision. Within a target, sequence orders attempts. Client timestamps
are diagnostics and server receipt timestamps measure contact/freshness only.
Neither timestamp decides which outcome wins.

The transactional persistence reducer validates scope, connection generation,
target assignment, sequence, and payload before selecting state. An exact
duplicate is idempotent. Reusing a retained session/sequence with another payload
is a protocol conflict, even if its lifecycle rank is higher. A bounded identity
and fingerprint ledger retains replay checks; pruned old events are stale and
cannot regain authority. Invalid/unavailable targets do not become successful
application evidence. Downstream live registries consume the persisted snapshot,
never independently apply the incoming message.

Latest attempt and last confirmed application are separate. A pending or
rejected new target does not erase the target last confirmed as serving. A
delayed valid applied event can advance historical applied evidence only when
its target/sequence is newer than that evidence; it cannot overwrite a newer
effective attempt. Rejection and divergence metadata are selected atomically
with their event. Diagnostics remain bounded and redacted under the existing
server policy; never put credentials or configuration plaintext in them.

## Authoritative status

Application/environment views, Subscribers, rollout polling/streaming, and CLI
consume the same server projection and bounded reason codes. Raw history is
separate. Complete counts cover the projected population before pagination;
clients use summary completeness and projection revision rather than treating a
display page as the population or mixing revisions from different snapshots.
The bounded `/api/v1/release-subscribers/history` endpoint exposes historical
rows separately; effective status endpoints do not repeat history on every page.
Pagination cursors are scoped to the projection and must be restarted if it
changes between requests.

| Effective instance | Meaning for current readiness |
| --- | --- |
| Disconnected/stale | Historical evidence only, not current health |
| Connected, desired target unfinished | Pending / Rolling out |
| Desired target rejected | Rejected / Degraded |
| Desired fleet target applied | Applied |
| Explicit pin applied | Pinned separately; a differing pin prevents fleet convergence |
| Missing/invalid projection or lookup failure | Unknown/error; never fabricated revision zero or Ready |

Blocking configuration errors keep precedence. Otherwise, any connected
rejection degrades the environment; connected pending instances mean Rolling
out. No connected release sessions means Unknown. Ready requires every
connected unpinned session to confirm the desired fleet target, without a
differing applied pin, and remains subject to configuration-drift checks.
Last-applied information explains what a pending/rejected process last confirmed
it served; it is not a claim that it serves the new target.

## Compatibility

Use protocol capability and tested build identifiers, not an assumed version
number, when inventorying deployments. The release containing this change is a
coordinated compatibility boundary.

| Client | Session-consistent KMS | Pre-session server |
| --- | --- | --- |
| Updated Go/generated Go release loader | Required session protocol | Actionable compatibility error; no fallback |
| Updated TypeScript release loader | Required session protocol | Actionable compatibility error; no fallback |
| Updated Python sync or async release loader | Required session protocol | Actionable compatibility error; no fallback |
| Legacy release-watch registration | Upgrade-required error; cannot affect health | Historical behavior only, not supported by this contract |
| Ordinary non-release configuration subscription | Unchanged | Unchanged |

An older server that exposes session RPCs is not automatically conformance
compatible: validate the complete replay/retry contract in staging. Session
negotiation alone is insufficient proof of correct event ordering.

## Coordinated upgrade

1. Release the updated SDKs and rebuild every release-consuming service. Record
   SDK/runtime and application build identifiers, including generated clients.
2. Exercise those builds against the candidate KMS in staging. Verify sessions,
   same-target rejection/retry, immutable replay, transport reconnect, process
   restart, rollback, pin/unpin, and agreement across status surfaces.
3. Back up the production database and matching KMS build/configuration as below.
   Upgrade KMS in the approved window. Legacy release subscribers now receive
   the documented upgrade-required error, not ambiguous historical health.
4. Roll the rebuilt consumers where necessary. Verify new session IDs and
   confirmed desired-target application. Do not infer success from connection
   count alone. No automatic production restart or deployment is part of this PR.

For prod-linkie, first verify the actual deployed KMS build and consumer SDK
protocol. In the approved rollout window, force a transport reconnect while
retaining the loader session. Applications and live Subscribers must continue
to agree on its confirmed applied target throughout replay (the incident's
fleet target was schema v4, revision 153; verify the current target rather than
assuming that historical value is still current). A process restart deliberately
creates a new session and may correctly show pending until it applies.

## Backup and rollback

Before migration, stop KMS writes and take a verified SQLite backup with the
SQLite backup API/CLI `.backup`, or a consistent stopped-server copy including
any required WAL state. Do not copy only the main database file while writes
are active. Record the backup checksum, previous binary, configuration, and
encryption-key availability, and test restoration in an isolated environment.
Treat the backup as sensitive operational data with restricted access.

The migration adds retained event identity/fingerprint and session projection
metadata while preserving legacy history. Do not infer a trustworthy sequence
from old receipt timestamps. Inspect migration completion and session health
before reopening the rollout gate.

Prefer fixing forward. If rollback is approved, stop KMS and preserve the failed
upgrade database for diagnosis. Restore the verified pre-migration backup and
its matching binary/configuration together in an explicitly planned recovery;
account for loss of writes since that backup. Do not automatically downgrade the
schema, drop projection tables, or run an old binary against the upgraded DB.
Do not silently resume legacy health reporting: coordinate compatible consumers
and clearly declare readiness unavailable until trustworthy session evidence
has been re-established.

## Operational signals and acceptance

`kms_release_acknowledgements_total{outcome="..."}` exposes the bounded outcomes
`accepted`, `duplicate`, `stale`, `conflict`, `unavailable`, and
`legacy_rejected`. Unknown internal labels collapse to `other`. No identity,
session, namespace, client, target, diagnostic, or secret is a metric label.
Duplicate and stale traffic can be normal during reconnect. Investigate
conflicts as protocol violations, unavailable outcomes as target/retention or
session problems, and legacy refusals as incomplete consumer upgrades.

The release gate is behavioral, not merely green acknowledgement-count tests:
all four SDK variants must preserve event identities under replay, and storage,
live Subscribers, HTTP overview, rollout polling/streaming, and CLI must report
the same state/reason. Include real gRPC/SQLite reconnect, delivery permutations
and duplicates, fenced old streams, session expiry, metadata coherence, scope
isolation, retained last-applied evidence, disconnected-only Unknown, and
pagination beyond 1,000 records. Record exact tested builds and any skipped
checks in the PR; a skipped deployment acceptance check is not a production
verification.

### Reproducing the cross-language release gate

Build TypeScript with `npm ci --prefix sdk/typescript` followed by
`npm run build --prefix sdk/typescript`, and install the Python SDK with its
development dependencies in a virtual environment. Then run:

```sh
KMS_SDK_CONFORMANCE=1 KMS_CONFORMANCE_PYTHON=/path/to/venv/bin/python \
  go test ./internal/integration -race -count=1 -timeout=10m \
  -run 'TestExternalSDKSessionConformance|TestReleaseSessionReplayConsistencyOverRealKMS'
```

The external-client test uses real TLS gRPC, SQLite, and a TCP proxy that cuts
connections without restarting the loader. It checks persisted session/sequence,
live classification/reason, and HTTP readiness after replay. The ordinary Go
integration and SDK suites cover Go loaders; the event-set reference model checks
192 delivery permutations against persistence. Database reopen/reset tests cover
server-restart persistence separately from transport reconnects.

CI's **Release sessions (real gRPC, SQLite & all SDKs)** job installs these
runtimes and enables the external-client test; it is opt-in only for ordinary
local Go-only test runs.
