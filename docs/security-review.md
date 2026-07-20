# Security Review — Automated Scan Candidates

> **Status: not validated.** These are **unvalidated candidates**, not confirmed
> vulnerabilities. The scan **did not run to completion** — it stopped before
> reconciliation, dedupe, and sealing.
> **Scope:** the whole repository at `f902af4ba4b4314a86bd295986e94f6234b15214`,
> which **is** the current `HEAD`, so every `file:line` below resolves.
> **18 candidate ledgers → 14 distinct issues** (duplicates grouped here by hand).
> This is *not* [`security.md`](security.md) — that is the hand-authored reference
> describing the system **as designed**; this file is a machine's **unconfirmed
> reading of the implementation**. Where they disagree, neither is automatically
> right: `security.md` states intent, this file states an untriaged observation.

## How to read this

Every row carries an **evidence** word, and they mean exactly this:

- **Demonstrated** — an executed test actually reproduced the behavior. Strongest evidence here.
- **Traced** — static reading with `file:line` evidence from source to sink. Nothing was run.
- **Lead** — discovery-only, never validated. *(No item in this repo is a bare Lead.)*

**Severity is quoted verbatim from the scan.** Ten of fourteen items say *not
assigned* because the scan stopped before the stage that rates them. *Not
assigned* means **unrated — it does not mean low.** No severity here was
re-derived or guessed.

This repository is unusual: **six of the fourteen items were actually executed**
— rows 3–4 over real HTTP, rows 5 and 9–11 against a real filesystem — rather
than only read. Those are the ones to trust first.

## Triage table

Ranked by what a maintainer should look at first, not by candidate ID.

| # | Issue | Location | Severity | Evidence | Detail |
|---|---|---|---|---|---|
| 1 | Go SDK picks cleartext gRPC when `Config.TLS` is left unset — tokens and secret plaintext ride the wire | `sdk/go/paramstore/client.go:170` | **high** | Traced | [→](security-scan/candidate-details.md#1-go-sdk-selects-insecure-transport-by-default) |
| 2 | Python SDK does the same when `tls=None` | `sdk/python/kms_paramstore/client.py:129` | **high** | Traced | [→](security-scan/candidate-details.md#2-python-sdk-selects-insecure-transport-by-default) |
| 3 | `ListNamespaces` never runs the per-namespace auth-method gate, so a token client sees an mTLS-only namespace | `internal/core/admin.go:128` | medium | **Demonstrated** | [→](security-scan/candidate-details.md#3-listnamespaces-skips-the-namespace-authentication-method-gate) |
| 4 | Audit query with `app` omitted skips the method gate and returns rows from mTLS-only namespaces | `internal/core/admin.go:37` | medium | **Demonstrated** | [→](security-scan/candidate-details.md#4-partial-audit-filter-skips-the-method-gate) |
| 5 | Private-key file write follows symlinks and reuses an existing file's permissions (no `O_EXCL`) | `internal/cli/admincmds.go:525` | *not assigned* | **Demonstrated** | [→](security-scan/candidate-details.md#5-private-key-output-follows-symlinks-and-does-not-use-o_excl) |
| 6 | Unbatched reads pair a stale access-token hash with a post-rotation version, disclosing plaintext | `internal/storage/secrets.go:214` | medium | Traced | [→](security-scan/candidate-details.md#6-non-transactional-read-pairs-a-stale-token-hash-with-a-new-version) |
| 7 | Two concurrent client-bound creates both see "not found"; the loser's write is reclassified as an update | `internal/core/secrets.go:197` | medium | Traced | [→](security-scan/candidate-details.md#7-toctou-on-concurrent-client-bound-secret-creation) |
| 8 | Old token validated outside the transaction, so a racing rotation leaves the new version keyed to the old token | `internal/core/secrets.go:218` | medium | Traced | [→](security-scan/candidate-details.md#8-toctou-defeats-client-bound-token-rotation) |
| 9 | Database file is created under the process umask — observed at `0644` | `internal/storage/store.go:152` | *not assigned* | **Demonstrated** | [→](security-scan/candidate-details.md#9-database-file-created-at-mode-0644) |
| 10 | `VACUUM INTO` backup is created at `0644` even when the source database is `0600` | `internal/storage/store.go:267` | *not assigned* | **Demonstrated** | [→](security-scan/candidate-details.md#10-vacuum-into-backup-created-at-mode-0644) |
| 11 | `restore` checks the destination, then renames over it — a file created in between is destroyed without `--force` | `internal/cli/restore.go:99` | *not assigned* | **Demonstrated** | [→](security-scan/candidate-details.md#11-restore-replaces-a-concurrently-created-destination-without---force) |
| 12 | Release connection key is `(namespace, name, client, instance)` with no authenticated identity, so principals collide | `internal/storage/releases.go:599` | *not assigned* | Traced | [→](security-scan/candidate-details.md#12-release-connection-key-omits-authenticated-identity) |
| 13 | Release acknowledgements can be overwritten across principals, with an attacker-chosen future timestamp winning | `internal/storage/releases.go:477` | *not assigned* | Traced | [→](security-scan/candidate-details.md#13-release-acknowledgement-overwrite-across-principals) |
| 14 | mTLS enrollment matches on serial + SAN only — an additional trusted CA could mint a leaf that authenticates as another identity | `internal/core/service.go:301` | *not assigned* | Traced | [→](security-scan/candidate-details.md#14-mtls-certificate-enrollment-is-not-bound-to-the-stored-fingerprint-or-issuer) |

Rows 3 and 4 each cover **three** candidate IDs that the scan never deduped
(`CAND-W02-001`/`W03-001`/`W05-001` and `CAND-W02-002`/`W03-002`/`W05-002`) —
that is the entire 18 → 14 difference. Full ID mapping is in
[`candidate-details.md`](security-scan/candidate-details.md).

## Start here

1. **Read the two SDK constructors (rows 1–2).** `sdk/go/paramstore/client.go:167-170`
   and `sdk/python/kms_paramstore/client.py:125-130` are a few minutes of reading
   and settle both **high** items outright: decide whether an unset TLS config
   should select cleartext, or whether cleartext should require an explicit
   opt-in. Safe explicit TLS builders already exist in both SDKs.
2. **Re-run the two demonstrated method-gate bypasses (rows 3–4).** These
   reproduced over real HTTP *and* contradict a written guarantee in
   `security.md` that the gate runs "on every namespaced operation." Confirm,
   then decide which document is wrong.
3. **Decide the file-mode posture (rows 5, 9, 10, 11).** All four were observed
   directly under a forced `umask 022`. They share one question: should the CLI
   and storage layer set explicit restrictive modes rather than inheriting the
   umask? Row 5 leaks a private key and deserves priority within this group.
4. **Assess the three concurrency traces (rows 6–8).** None were executed — no
   scheduling harness was built. Either build one or reason the interleavings
   out at the source. Row 6 is the only plaintext-disclosure claim in the scan
   and also carries its lowest confidence (0.82).
5. **Check whether any deployment configures an additional client CA (row 14).**
   If none does, this is inert. If one does, it moves to the top of this list.

## Scope & limits

What this review can and cannot support:

- **The scan is unsealed.** No `findings.json`, `coverage.json`,
  `scan-manifest.json`, or `report.md` exists at the workspace root — none of the
  artifacts a completed scan produces. Nothing here was confirmed against a
  running deployment.
- **Reconciliation never ran.** `artifacts/04_reconciliation/` was empty, so no
  dedupe report exists. The 18 → 14 grouping above was done by hand, and no
  cross-candidate severity normalisation happened.
- **Negative assurance is weak.** All **26** rows of
  [`repository-coverage-ledger.md`](security-scan/repository-coverage-ledger.md)
  are still `open_frontier` with blank candidate-ID columns — including rows
  where the scan looked and found nothing. The ledger states its own
  precondition: those rows *"must be reconciled to `reportable`, `suppressed`,
  `not_applicable`, or `deferred` before final reporting."* That never happened.
  So "the scan did not flag X" is **not** evidence that X is safe.
- **Line numbers drift.** Citations were spot-checked against `HEAD` and land
  correctly today; they will rot as soon as these files are edited.
- **To regenerate:** re-run the Codex security scan against
  `f902af4ba4b4314a86bd295986e94f6234b15214` and let it proceed through
  reconciliation and sealing. The original workspace was a temporary directory
  and should be assumed gone.

## Artifact index

| File | Contents |
|---|---|
| [`security-scan/candidate-details.md`](security-scan/candidate-details.md) | Full per-candidate detail for all 14 issues — claims, locations, rubrics, counterevidence, and each static candidate's `proof_gap`. Plus the scan's severity calibration and its list of reviewed-but-unflagged files. |
| [`security-scan/threat-model.md`](security-scan/threat-model.md) | Scan-generated threat model: trust boundaries, assumptions, attack surface, attacker stories, and the full severity calibration. |
| [`security-scan/repository-coverage-ledger.md`](security-scan/repository-coverage-ledger.md) | 26 coverage rows (COV-001 … COV-026) with root controls, boundaries, and closure requirements — **all still `open_frontier`**. |
| [`security.md`](security.md) | **Not a scan artifact.** Hand-authored security reference describing the system as designed. Untouched by this review. |

Not copied from the workspace: `01_context/security_guidance.md` was zero bytes;
the `02_discovery/*.jsonl` ranking and work ledgers and the 18
`05_findings/*/candidate_ledger.jsonl` files are machine formats whose content is
summarised in `candidate-details.md`; `04_reconciliation/` was empty.

### Scan metadata

| Field | Value |
|---|---|
| Target revision | `f902af4ba4b4314a86bd295986e94f6234b15214` (== current `HEAD`) |
| Scan ID | `f902af4ba4b4314a86bd295986e94f6234b15214_20260720T032103Z_hko3mvpu` |
| Repository digest | `sha256:aff589429acb0ca0b8e3d908a882e4eb55c22d46574df91a33b6196c272bdbf4` |
| Mode | `standard_repository_scan` |
| Completed | **No** — stopped before reconciliation and sealing |
| Executed validation | `go test -overlay=... ./internal/server/httpserver -run '^TestW02'` (rows 3–4); `TestW04SecurityProbe` under forced `umask 022` (rows 5, 9–11) |
