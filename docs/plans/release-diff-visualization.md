<!-- Planned 2026-09-12 from main @ 8a9b52c. Section 0 lists what already exists; §E0 holds the frozen contracts. -->

# Release diff visualization — plan

Branch: `release-diff` off `main` (8a9b52c). Delivered as a PR with intermediate commits per lane (per the user's stated working style). Check `git status` for concurrent-session edits before Lane B starts.

## 0. What the codebase already has (do not rebuild)

| Need | Exists at | Verdict |
|---|---|---|
| Release-vs-release compare UI | `frontend/components/releases/ReleaseWorkspace.tsx:283-335` — "Compare" tab, a 3-column table of `kind ref@version digest12` strings computed at `:102-118` | Replace the tab body with the new `ReleaseDiffView` (compact mode). Its deep link `links.releases({ release, section: "compare", compare })` (`lib/links.ts:146-169`) stays valid and is consumed at `pages/releases.tsx:244-282, 655-732`. |
| Side-by-side line diff with folding | `frontend/components/JsonDiff.tsx` (`JsonDiffProps` `:8-21`) on `lib/line-diff.ts`; CSS `styles/globals.css:2006-2131` (`data-op="add|del|empty"`, `--success-soft`/`--danger-soft`, `inset 2px` rail) | Reuse verbatim for the "side-by-side" mode of a JSON/string value. |
| Raw-token JSON tree parser (preserves `1.0`, bignums) | `frontend/components/JsonTree.tsx` exports `parseJsonTree(text)`, `buildJsonTreeRows` (`:47`, `:167`), `JsonTreeNode` (`:34`) | Base the new structural value diff on `parseJsonTree`, never `JSON.parse`. |
| Alias → resource links | `entryHrefResolver(entries, fallbackNamespace, links)` in `components/releases/ViolationTable.tsx:130-152` | Reuse for the row's "Open parameter/secret" action. |
| Row-level "changed" idiom | `.ship-entries tr.ship-entry-changed td` (`styles/ship.css:349-366`): `--accent-soft` fill + `inset 3px 0 0 var(--primary)` rail | Reuse the idiom (not the class) for changed rows. |
| Counts strip / search / next-change navigator | `components/applications/UpgradeChangeNavigator.tsx` + `lib/upgrade-field-changes.ts` (`UpgradeFieldChange`), CSS `globals.css:4487-4628` | Do not reuse: it is bound to schema-upgrade drafts. Copy its layout idea only. |
| "What changes" link in Rollback | `components/ship/RollbackDialog.tsx:183-195, 255-274` builds `links.releases(..., section: "compare")` | Repoint to `links.releaseCompare`, add an inline summary. |
| Old→new value diff in Ship | `components/ship/ShipEditor.tsx:252-267` per edited row only; `ShipPreview.tsx` shows versions only (`versionArrow` `:44-49`) | Leave Ship's preview alone; add a post-ship link. |
| Release CLI diff | `internal/cli/releasecmds.go:596-760` `computeReleaseDiff` — entries only (`kind`, `path`, `version`, `parameter_digest`), JSON contract in `docs/operations.md:696` | The new server endpoint mirrors its `from`/`to`/`added`/`removed`/`changed` vocabulary and adds values. |
| Historical parameter value | `GET /api/v1/parameters/get?version=` (`internal/server/httpserver/handlers.go:44,461`); every version is retained, never pruned (`internal/storage/parameters.go:184-196`); `created_by`/`created_at` per version (`storage/models.go:74-88`) | The diff endpoint reads both pinned versions server-side. |
| "Previous" release | `configuration_release_labels` (`storage/models.go:299`) `current`/`previous` per track; moved in `storage/releases.go:497-567`; surfaced as `previous_version` on `/releases/active`, the overview's `release.active.previous_version`, and `RollbackResponse` | Labels give only one step back. Older history is audit-only (`configuration_release.activate` events carry `previous_version` in `metadata_json`, `internal/core/releases.go:635`, `release_audit.go:14-24`). |
| Restart-bound fields | Only in generated Go bindings (`RestartRequiredFields`, `internal/configgen/binding.go:372-408`, from `reload=restart` struct tags, `model.go:239`). Not in the schema JSON, not on the server. | The diff row model carries a `flags` slot; populating it needs a schema annotation (follow-up, §E.8). |

Backend gaps confirmed: no diff endpoint, no queryable activation timeline (`configuration_release_activations` has no actor/from-version and only an existence probe, `storage/releases.go:476-495`), secret metadata is only readable as a whole version list (`GET /secrets/metadata`), and parameter reads are not audited (`internal/core/parameters.go:62-71`), so a server-side diff reading 2×N values emits no audit noise.

---

## A. SRE jobs-to-be-done

Each scenario lists what must be visible first, second, third. The design in §C is derived from these orderings.

**A1. Paged at 03:00, error rate up since 02:41.** Entry: fleet card or environment page for `prod/gradethis`.
1. First: *what is running now vs what was running before*, as two release chips with "shipped 19 min ago by alice", and the count of changes: "3 changed · 1 added · 0 removed · 1 secret repinned". If the count is 0, say "identical" loudly and send them elsewhere.
2. Second: the changed rows, highest-risk first: secrets repinned, content-type/kind changes, then values, with the old→new scalar visible inline without clicking (`rate_limits: 100 → 20`).
3. Third: for JSON values, the leaf paths that changed (`database.pool.max: 50 → 5`), unchanged subtrees collapsed. Roll back button one click away, on the same page.

**A2. Rollout partially rejected (rollout_state `degraded`).**
1. First: the summary strip shows "1 of 3 instances rejected (config_validation_failed)" beside the diff header, so the operator knows the *new* release is not fully serving and which instance still runs the old one.
2. Second: the changed rows, with the validation-relevant ones (content-type change, schema version change) flagged "needs attention".
3. Third: link to the rollout panel (release workspace "Rollout status") and the rejection guidance (`lib/glossary` `rejectionGuidance`).

**A3. "Did anyone touch the rate limit?"** Entry: audit log filtered to `env=prod app=gradethis`.
1. First: the activation row shows "What changed (v7 → v9)"; the compare page opened from it lets them type `rate` in the filter and see only that alias.
2. Second: the row shows *who wrote the value* (parameter version `created_by`, distinct from who activated) and when.
3. Third: "Copy as text" so the answer can be pasted into the incident channel.

**A4. Comparing prod to staging before shipping.** Entry: environment page More menu "Compare with another environment…".
1. First: a banner making the semantics explicit ("matched by alias; keys and secrets are per environment").
2. Second: rows grouped by change kind; secrets shown as "different versions (expected)" without alarm tone.
3. Third: filter to a prefix (`db_`), toggle "Show unchanged" to confirm parity.

**A5. Auditing who changed what across several activations.**
1. First: from the audit log, every `configuration_release.activate`, `.rollback` and `application.ship` row links to its own from→to diff.
2. Second: on the compare page, swapping from/to and stepping to adjacent versions (v3 → v4, then v4 → v5) without leaving the page.
3. Third: a permalink to attach to the postmortem.

**A6. A rollback happened; is prod now on the older release?** The `to` side is current and `to.version < from.version` → the header says "Rolled back: v9 → v7 is active" with `Badge kind="warning"`, and the "Re-activate v9" action is offered (mirrors `ReleaseSection.tsx:136-148`).

---

## B. Entry points and navigation

### B1. URL shape and link helper

New page route `frontend/pages/releases/compare.tsx` (Next pages router allows `pages/releases.tsx` + `pages/releases/compare.tsx`; `AppShell.isActive` at `components/AppShell.tsx:92-95` already highlights "Releases" for `/releases/…`).

```
/releases/compare?app=gradethis&env=prod&name=runtime&schema_version=1&from=7&to=9
                  [&to_env=staging][&to_schema_version=2][&view=all|changed][&q=rate]
```

Add to `frontend/lib/links.ts` (param order is load-bearing, tests assert strings verbatim, `tests/links.test.ts`):

```ts
export interface ReleaseCompareLinkOptions {
  app: string;
  env: string;
  name: string;
  schemaVersion: number;
  /** A version number, or the track labels `current` / `previous`. */
  from: number | "current" | "previous";
  to: number | "current" | "previous";
  /** Cross-environment comparison: the `to` side lives in this environment (same app). */
  toEnv?: string;
  toSchemaVersion?: number;
  view?: "all";          // default is changed-only, so only "all" is emitted
  q?: string;
}
releaseCompare: (opts: ReleaseCompareLinkOptions): string
// emits: app, env, name, schema_version, from, to, to_env, to_schema_version, view, q — in that order
```

`from`/`to` accept the literal labels so entry points that only know "active" can link without a version (`from=previous&to=current`). The page resolves labels through the endpoint (§D), then rewrites the URL to numeric versions with `router.replace({shallow:true})` inside an event-handler-safe path (the page effect pattern is the one sanctioned exception at `pages/applications/environment.tsx:40-57`; copy that comment).

Breadcrumbs: add `crumbs.releaseCompare(ns, name, from, to, schemaVersion)` in `lib/crumbs.ts` → Applications › app › env › Releases › `Compare v7 → v9`; add a case to `tests/breadcrumbs.test.tsx`.

Not in `NAV` (drill-down page, like `/applications/environment`), so not in `PALETTE_PAGES`.

### B2. Swap, step, and back navigation

- **Swap** button (icon `ArrowLeftRight` from lucide, `Button variant="outline" size="sm"`) between the two chips: `router.replace` with `from`/`to` exchanged; filter and view preserved.
- **Step** controls: two `AppSelect` pickers (from, to) listing the track's versions from `api.listReleases(ns, name, 100, undefined, {signal}, schemaVersion)` labelled `runtime@1:7 · current` / `· previous`; plus "◀ older / newer ▶" icon buttons that move `to` by one existing version.
- **Back**: `router.back()` is not used; the breadcrumb trail and a "Releases" `ButtonLink` cover return. Opening the page from a modal uses a plain `Link` (new tab safe); opening from the environment page uses `router.push`.
- **Keyboard**: `/` focuses the filter (`useSearchShortcut`), `Esc` clears it (SearchField already does this). Add one group to `SHORTCUT_GROUPS` in `lib/shortcuts.ts`: "Release comparison" with `n` / `p` = next / previous changed row (guarded by `isTypingTarget`), `x` = expand/collapse the focused row. These are the only new letter shortcuts; they are page-scoped and listed in the `?` sheet. Cut line: drop `n`/`p`/`x` if time is short, keep `/`.

### B3. Where the diff is reachable from

| Surface | File and anchor | Link |
|---|---|---|
| Environment page & pipeline column Release section | `components/applications/ReleaseSection.tsx:117-134` next to `previous` chip; `:103-116` next to "latest vN not active"; `:136-141` inside the rolled-back panel | "What changed" → `from: previous_version, to: active.version`; "Compare with latest" → `from: active.version, to: latest`; rolled-back → `from: active.version, to: previous_version` (the newer one) |
| Environment page More menu | `components/applications/EnvironmentHome.tsx:166-236` (after the `releases` item at `:206`) | "Compare releases…" (`from: "previous", to: "current"`), "Compare with another environment…" opens a small `Modal` with an env `AppSelect` then navigates with `toEnv` |
| Releases list row actions | `pages/releases.tsx:1011-1037` (`div.row-actions`) | "Compare" → against the current release (or the previous version when the row *is* current). No multi-select today; add a two-step "compare mode": first click sets the base (`compareBase` state, toast "Pick the second release"), the buttons then read "Compare with runtime@1:3". Cut line: single-target only. |
| Releases page status strip | `pages/releases.tsx:900-915` next to "Roll back to previous" | "What changed" for current vs previous |
| Release workspace Compare tab | `ReleaseWorkspace.tsx:283-335` | Body replaced by `<ReleaseDiffView compact …/>`; header gets "Open full comparison" `ButtonLink` |
| Ship modal, success step | `components/ship/ShipModal.tsx:886-923` inside `div.ship-activation-line` after the version arrow | "See what changed" → `from: activation.previousVersion, to: activation.version` (only when `previousVersion > 0`) |
| Rollback dialog | `components/ship/RollbackDialog.tsx:243-282` after the danger panel | Inline `<ReleaseDiffSummary>` (counts + first 5 aliases) plus the existing link repointed to `links.releaseCompare` |
| Audit log | `pages/audit.tsx:594-612` resource cell; expanded metadata row `:633-644` | For `event_type` in {`configuration_release.activate`, `configuration_release.rollback`} with `metadata_json.previous_version` > 0: "What changed" → `from: previous_version, to: resource_version` (schema from `auditReleaseSchemaVersion`). For `application.ship` with `activated:"true"`: `from: previous_version, to: release_version`. The expanded row renders `<ReleaseDiffSummary>` under the `JsonView`. |
| Fleet card | `components/overview/ApplicationCard.tsx:85-98` footer | The "activated 3h ago" text becomes a `Link` to the compare page for the environment with the latest activation (`from: previous_version`). Optional; `tests/e2e/layout-guards.spec.ts:135-152` guards `.fleet-env` geometry, so only the footer text changes. |
| Command palette | `lib/palette.ts:176-187` (the rollback action loop) | Add `action:compare:{env}/{app}` — "What changed in env/app", subtitle "Compare the active release with the previous one", keywords `["diff","compare","changed","release", env, app]` |

### B4. Cross-environment comparison

Same page, `to_env` (and optionally `to_schema_version`) set. Both sides are fetched by the same endpoint; the header shows two `NamespaceIdent` chips instead of one. Banner (`.info-panel`): "Comparing prod against staging. Entries are matched by alias; keys, versions and secrets are per environment, so version numbers are expected to differ — look at values." In cross-env mode the "pin only" change reason is hidden for parameters (a version change with equal digest is not a change), and secrets show as "different version (expected)" in neutral tone.

---

## C. The diff view

### C1. Page skeleton (desktop, 1200px `.page`)

```
PageHeader
  breadcrumbs: Applications › gradethis › prod › Releases › Compare v7 → v9
  title:   [release runtime@7]  →  [release runtime@9]         (ReleaseIdent × 2, schema v1 once, faint, after the pair)
           [Badge warning: previous]   [Badge success: current · rev 53]
  subtitle: "9 shipped 19 min ago by alice · 7 shipped 2 d ago by bob" (formatRelative + useNow, title=formatUnixMs)
  actions: [Swap] [◀] [from ▾] [to ▾] [▶] [Copy link] [Copy as text] [Roll back / Re-activate v9]  (Button size="sm")
Banners (conditional): rolled back · cross-environment · different schema tracks · one side truncated
Summary strip (.stat-strip, 6 cells at ≥1080px container width, 3 at ≥534px, 2 below)
  Changed 3 | Added 1 | Removed 0 | Secrets repinned 1 | Schema v1 → v1 (same) | Rollout 2/3 applied · 1 rejected
Toolbar (.between): [SearchField "Filter aliases, keys, paths, values" /] [Kind: All · Parameters · Secrets] [☐ Show unchanged] [☐ Group by prefix] [Expand all · Collapse all]
Groups (each a .card with .card-title "Needs attention (1)", "Secrets (1)", "Changed (3)", "Added (1)", "Removed (0)", "Unchanged (12, hidden)")
  Row … (see C3)
```

Sizes come from tokens (`styles/globals.css`): `.page` padding 24/32/64px; `.stat-strip .stat` padding 12px 16px, `--stat-value` 22px, `--stat-label` 11px; `.card` padding 16px 20px; `.card + .card` 16px gap; toolbar gap `--space-3` (12px); control heights `--control-h-sm` (30px) for every toolbar control and row action.

Compact mode (inside `ReleaseWorkspace` and the Rollback dialog): no PageHeader, no strip cells for rollout, `maxHeight` on value panes (`40vh`, the same cap `ShipEditor.tsx:265` uses), groups rendered as plain `<section>` with `.pipeline-section-title` sized headings instead of cards.

### C2. Summary strip cells

| Cell | Source | Rendering |
|---|---|---|
| Changed / Added / Removed | `counts` | `.stat-value` tabular number; numbers stay `--text`; the label gets a tone dot (`.status-dot`-style 10px circle, `--warning`/`--success`/`--danger`) |
| Secrets repinned | `counts.secrets_changed` | number + `.stat-sub` "values never shown" |
| Schema | `from.schema_version` vs `to.schema_version` | `Ident kind="schema"` chips; "same" in `.stat-sub` when equal, `Badge kind="warning"` "different tracks" when not |
| Rollout | overview `rollout` of the env whose active release is `to` (page fetches `api.applicationOverview(app, [env], {signal}, schemaVersion)` only when `to.current`) | `2/3 applied` + `Badge kind="danger"` `1 rejected` (same badges as `RolloutPanel.tsx:140-151`); absent in compact mode and when `to` is not current |

### C3. Row anatomy

One row per alias, rendered as a `<article className="release-diff-row" data-alias data-change data-kind data-flags>` inside a group; not a `<table>` (rows expand to nested content and must stack on phones). Row head is a CSS grid `grid-template-columns: minmax(0, 220px) minmax(0, 1fr) auto` at ≥640px, single column below; min-height 44px (`--control-h-touch`) so the whole head is the expand target.

```
┃ [alias rate_limits]  key rate_limits         100  →  20   (−80, −80 %)      [Badge changed] [integer]  [Open ▸] [⌄]
```

- Left rail: `box-shadow: inset 3px 0 0 var(--rail)` where `--rail` = `--primary` (changed), `--success` (added), `--danger` (removed), `--warning` (needs attention), none (unchanged). Fill: `--accent-soft` / `--success-soft` / `--danger-soft` / `--warning-soft` at the head only.
- Column 1: `Ident kind="alias"` (chip, 22px, mono `--text-sm`), then `Ident kind="key"` only when key ≠ alias (faint), plus the secret kind glyph (`.kind-glyph`, `KeyRound size={13}`, same as `ValuesSection.tsx:86`).
- Column 2: the **inline change**, type-aware (§C4). For anything that does not fit one line: a one-line summary ("4 fields changed, 1 added · 2.1 KiB → 2.3 KiB") and the expanded body below.
- Column 3: badges — change kind (`Badge kind` accent/success/danger/neutral: `changed`/`added`/`removed`/`unchanged`), content type (neutral), reasons other than value as extra badges (`pin only`, `key changed`, `kind changed`, `type changed`, `restart` when flagged); actions: "Open" (`Link` via `entryHrefResolver`, opens the parameter/secret detail; inside modals pass `onOpen`, per the QoL-pass `ResourceLink` rule), expand chevron (`aria-expanded`).
- Meta line under the head (`--text-xs`, faint, mono for versions): `v3 by bob 2 d ago  →  v4 by alice 19 min ago` — the version authors, distinct from the release author, sourced from each side's `created_by`/`created_at_unix_ms`.

### C4. Type-aware value rendering (`lib/value-diff.ts` → `describeChange`)

Content types are the KMS tokens `string | integer | float | boolean | json | binary` (`lib/validation.ts` `PARAMETER_CONTENT_TYPES`).

| Type | Inline | Expanded |
|---|---|---|
| boolean | `true → false` as two `.tok-boolean` tokens with an arrow | none |
| integer / float | `100 → 20` plus delta chip `(−80, −80 %)` computed on the raw decimal text via `BigInt` for integers and `Number` for floats; percent omitted when from = 0; tone neutral (direction is a judgement) | none |
| string, single-line, both ≤ 80 chars | `"a" → "b"`; if the strings share a prefix/suffix, dim the common part (`--text-faint`) and highlight the differing span with `--warning-soft` background only (the `.cell-path mark` rule at `globals.css:2436-2451`: background and weight only, never padding) | `JsonDiff` plain mode when longer |
| string matching Go duration (`^\d+(\.\d+)?(ns|us|µs|ms|s|m|h)+$`) or schema `format: go-duration` | `3s → 30s (×10)`, normalised to ms for the ratio; the raw strings stay visible | none |
| json (object/array) | summary: "`database.pool.max` 50 → 5, `database.pool.idle` 10 → 2, +1 field" (first two leaf changes, then a count) | **Structural mode** (default): a list of leaf changes, one per line at 19.4px (mono `--text-sm`, line-height 1.55, the `.json-diff-table` metrics), path in `.tok-key`, old value `data-op="del"` tint, arrow, new value `data-op="add"` tint; unchanged subtrees collapsed with "N unchanged fields" folds. **Side-by-side mode**: the existing `JsonDiff` with `contentType="json"`, `fold`, `maxHeight="60vh"`. A `Tabs variant="line"` pair in the row body toggles Structural / Side-by-side; the choice persists per session in `localStorage` (`kms-release-diff-mode`). |
| json where either side fails to tokenize | side-by-side only, with an `.info-panel` "Stored value is not valid JSON" | |
| binary | `1.2 KiB → 1.3 KiB · digest 3fa9… → 8c01…` (`formatBytes` from `lib/validation.ts:296`) | none; "Open" leads to the detail page |
| value omitted (`value_state: "omitted_size"`) | `Too large to compare inline (1.4 MiB)` + button "Load value" → `api.getParameter(ref, version)` for both sides, then renders as above | |
| **secret** | `v2 → v3` as `Ident kind="version"` chips, then state badges from `SecretStateBadge` (enabled/disabled/destroyed) and `BindingModeBadge` (`binding key` / `master key only`) for each side; expires shown when set | Never a value. Fixed microcopy on the Secrets group header: "Secrets are compared by pinned version and metadata. Values are never shown or fetched." |

Added and removed rows show one side only; the missing side reads `—` (faint em dash, matching `ReleaseWorkspace.tsx:111-116`).

Structural diff algorithm (`lib/value-diff.ts`): parse both sides with `parseJsonTree`; walk objects by key (union, sorted), arrays by index (index alignment is what SREs expect for config lists, and `JsonDiff` side-by-side covers reordering); leaves compared by token text after `minifyJson`-style normalisation of the scalar (so `1.0` vs `1.0` is equal, `1.0` vs `1` is a change and is shown as such). Output:

```ts
export type ValueChangeKind = "added" | "removed" | "changed";
export interface ValueChange { path: string[]; kind: ValueChangeKind; before?: string; after?: string; }
export interface StructuralDiff { changes: ValueChange[]; unchangedLeaves: number; truncated: boolean; }
export function structuralDiff(before: string, after: string, maxLeaves = 5_000): StructuralDiff | null; // null when either side is not JSON
export function describeScalarChange(before: string | undefined, after: string | undefined, contentType: string, format?: string): ScalarChange;
```

Caps: either side over `HIGHLIGHT_MAX_BYTES` (200 KiB) → structural mode disabled, side-by-side shows `JsonDiff`'s existing "too large" replacement.

### C5. Grouping and ordering

Default grouping by change kind with a leading "Needs attention" group. Order within a group: alphabetical by alias. "Needs attention" membership: `reasons` includes `kind` or `content_type`; a secret whose `to` side is disabled/destroyed/expired; `value_state !== "present"` on a changed parameter; schema version differs and the alias is only on one side; `flags` includes `restart`. "Group by prefix" replaces kind groups with one group per alias prefix (text before the first `_` or `-`; aliases are `[A-Za-z][A-Za-z0-9_-]*`), rows keep their rail colour.

Unchanged rows are hidden by default (`view=changed`); "Show unchanged" (`view=all`) reveals them collapsed, with values fetched lazily on expand (`api.getParameter(ref, version)`, cached per `${env}/${app}/${key}@${version}` in a `Map` ref).

### C6. Filtering

`SearchField` (`/` shortcut, mono input) matches, case-insensitive: alias, key, change reasons, structural leaf paths, scalar before/after text, and (in side-by-side mode) nothing else. Matches highlighted with `Highlight` (`components/Highlight.tsx`). Group headers show "(2 of 5)" when filtered. Kind chips use `Tabs variant="line"` (All / Parameters / Secrets). Filter state lives in the URL (`q`, `view`) through `useQueryReplace("/releases/compare")` from event handlers only.

### C7. Empty and error states

| State | Detection | Rendering |
|---|---|---|
| Identical | `identical: true` | `.info-panel` in place of groups: "No differences. runtime@7 and runtime@9 pin the same versions and values (digest 3fa9… on both). Compare with another version ▾" + the from/to pickers. If `from.digest === to.digest` but versions differ, add "created separately by … / …". |
| From/to is the same version | client guard before the request | "Pick two different versions." with the pickers focused |
| One side missing | `ApiError.code === "not_found"` with the endpoint's `error.message` naming the side | `.warn-panel`: "runtime@2 is not retained any more (release history keeps the newest 100 versions and 90 days; see `watch.release_retain_versions`). The audit log still records its activation." + `Link` to `links.audit()` prefilled `?app&env&key_prefix=runtime&event_type=configuration_release.activate` |
| No previous | `from=previous` and the track has no previous label → 412 | "runtime@9 is the first activation in prod; there is nothing to compare it with." + "Compare with any version ▾" |
| Different schema tracks | `schema_changed: true` | `.warn-panel` banner (stays visible): "Different schema tracks (v1 → v2). Aliases are matched by name; an alias that exists only on one track appears as added or removed." Schema chip in the strip turns warning. |
| Permission denied on a value | per-row `value_state: "unavailable"` | row body: "Value not readable with your permissions" (admin console: unlikely, still handled) |
| Loading | | skeleton with the strip's 6 cells and 5 row heads (rule 8: skeleton mirrors structure), `aria-busy` |
| Network error | `isUnreachableError` | standard `.danger-panel` with Retry, same as `pages/releases.tsx` handling |

### C8. Copy and permalink

- "Copy link": `CopyButton` (`value={() => window.location.href}` with `q` and `view` retained, labels resolved to numbers).
- "Copy as text": `CopyButton` whose value is `releaseDiffAsText(diff)` from `lib/release-diff.ts`, e.g.:

```
runtime@1:7 → runtime@1:9 in prod/gradethis (shipped by alice, 2026-09-12 02:41 UTC, rev 53)
changed  rate_limits        100 → 20
changed  database           database.pool.max 50 → 5; database.pool.idle 10 → 2
added    feature_flags      {"beta":true}
secret   db_password        v2 → v3 (binding key → binding key)
```

Secrets appear with versions only. JSON scalars over 120 chars are elided with `…`.

### C9. Theme

Everything reads tokens; the new sheet `frontend/styles/release-diff.css` contains no hex/rgb/color-mix (guard: `tests/visual-tokens.test.ts:207-215`). Rails and tints: `--primary`/`--accent-soft`, `--success`/`--success-soft`, `--danger`/`--danger-soft`, `--warning`/`--warning-soft`; code on `--bg-elev`; leaf values use `.tok-*` classes (`--syntax-*` tokens were tuned for "code read on `--bg-elev` with a diff row's tint over it", `globals.css:229-234`). Dark mode is the `.dark` class on `<html>` (`lib/theme.tsx:48`); nothing theme-specific is needed. Add an assertion in `tests/e2e/theme.spec.ts` style: a row with `data-change="changed"` has a different computed background in the two themes.

### C10. Responsive

At `< 640px`: row head stacks (alias line, change line, badge line), rail moves to the row border (`box-shadow` on the article like `.ship-entry-changed` at `ship.css:359-362`), strip 2 columns, toolbar wraps (`.between` + `grow basis-80` on the SearchField per the `.between` trap). Side-by-side `JsonDiff` inherits its own mobile handling. Route added to `tests/e2e/mobile-console.spec.ts:4-21` so the overflow sweep covers it.

---

## D. Backend / API

### D1. Recommendation: one server endpoint, values included, secrets structurally impossible to leak

`GET /api/v1/releases/diff`. Justification against client-side assembly:

1. **Round trips.** Client-side needs 2 release reads + 2 parameter reads per changed alias + 1 metadata read per changed alias (author) + secret metadata per secret: 17 requests for a 5-alias change, ~40 for a broad one; one request server-side. The SRE scenario is latency-bound.
2. **Redaction in one place.** The DTO has no field for a secret value; `secret_state`/`bound`/`expires` come from `GetSecretInfo` metadata only. The endpoint reads parameter values through `GetParameter`, which never touches secrets.
3. **Attribution.** Version `created_by`/`created_at` are already on `parameter_versions`; the server attaches them per side without extra calls.
4. **Size discipline.** The server elides values above a per-side cap and reports digests, so the response is bounded (see D3); a client cannot apply that rule before fetching.
5. **"Previous" sugar.** `from=previous`/`to=current` resolve through the labels in the same read as the release itself.
6. **Contract fixture.** A Go-generated `release-diff.json` pins the wire shape the same way the other console aggregates are pinned (`fixtures_test.go`), which is how this codebase keeps Go and TS types honest.

The CLI's `computeReleaseDiff` (`internal/cli/releasecmds.go:653`) stays as is (gRPC, no values). The new core function lives beside the console aggregates; the CLI can adopt it later (follow-up).

### D2. Request

```
GET /api/v1/releases/diff?env=&app=&name=&schema_version=&from=&to=[&to_env=][&to_schema_version=][&values=1]
```

- `env`, `app`, `name`, `schema_version` (required, explicit `0` allowed — same rule as `/releases/get`, `params.go:66`) address the **from** track; `to_env` and `to_schema_version` default to the from side.
- `from`, `to`: a positive integer, or `current` / `previous` (resolved through `GetActiveConfigurationRelease` of the respective track; `previous` with no previous label → `failed_precondition` 412 with message `no previous release`; `current` with nothing active → 404 `not_found`).
- `values=1` (default on): include parameter values on changed/added/removed rows. `values=0` returns the entry-only diff (what the Rollback dialog summary and audit row need; cheap).
- Errors: 400 `invalid_argument` (missing selectors, `from == to` after resolution), 404 for a missing release (message names the side: `from release runtime@1:2 not found`), 403 per authorization below.

Authorization (`internal/core/release_diff.go`): `configuration-release:read` on each track via the existing `authorize` (`core/service.go:491`), exactly like `GetConfigurationRelease` (`core/releases.go:329-347`); then for every changed/added/removed **parameter** entry, `parameter:read` on the entry's own `Ref` (entries carry their namespace; cross-namespace refs are rejected at create time today but authorize per ref anyway); a denied ref does not fail the request, it yields `value_state: "unavailable"`. Secrets: `secret:read` on the ref for `GetSecretInfo` (`internal/core/secrets.go:695`), same degrade rule. No audit event for reads (matches `GetParameter`).

### D3. Response (`ReleaseDiffResponse`, mirrored field-for-field in `frontend/lib/types.ts`)

```ts
export type ReleaseDiffChange = "added" | "removed" | "changed" | "unchanged";
export type ReleaseDiffReason = "value" | "pin" | "key" | "kind" | "content_type";
export type ReleaseDiffValueState = "present" | "omitted_size" | "omitted_unchanged" | "secret" | "unavailable";

export interface ReleaseDiffSide {
  namespace: NamespaceRef;
  name: string;
  version: number;
  schema_version: number;
  digest: string;
  created_by: string;
  created_at_unix_ms: number;
  current: boolean;
  previous: boolean;
  activation_revision: number;   // 0 unless current
  previous_version: number;      // the track's previous label at read time; 0 when none
}

export interface ReleaseDiffPin {
  ref: ResourceReference;         // { namespace, key } as on ConfigurationReleaseEntry
  version: number;
  content_type: string;
  parameter_digest: string;       // "" for secrets
  metadata_json: string;          // the entry's captured metadata
  created_by: string;             // author of that resource version ("" when unavailable)
  created_at_unix_ms: number;
  value_state: ReleaseDiffValueState;
  value?: string;                 // parameters only, only when value_state === "present"
  value_bytes: number;            // size of the stored value, even when omitted
  // secrets only
  secret_state?: SecretVersionState;
  bound?: boolean;
  expires_at_unix_ms?: number;
}

export interface ReleaseDiffRow {
  alias: string;
  kind: ReleaseEntryKind;         // the `to` side's kind, or `from` when removed
  change: ReleaseDiffChange;
  reasons: ReleaseDiffReason[];   // empty for unchanged/added/removed
  from?: ReleaseDiffPin;
  to?: ReleaseDiffPin;
}

export interface ReleaseDiffResponse {
  from: ReleaseDiffSide;
  to: ReleaseDiffSide;
  identical: boolean;             // no row has change !== "unchanged"
  schema_changed: boolean;        // from.schema_version !== to.schema_version
  cross_environment: boolean;     // namespaces differ
  counts: { added: number; removed: number; changed: number; unchanged: number; secrets_changed: number; attention: number };
  rows: ReleaseDiffRow[];         // sorted by alias
  value_cap_bytes: number;        // 262144
  values_included: boolean;       // false when values=0
}
```

Rules the Go side implements (`internal/core/release_diff.go`, pure function `computeReleaseDiff(from, to domain.ConfigurationRelease) []releaseDiffRow` + a loader that fills pins):

- Row `change`: absent on one side → added/removed; else `unchanged` when kind, ref, version, content type and digest all match; else `changed` with `reasons`: `kind` (kinds differ), `key` (ref differs), `content_type`, `value` (parameter digests differ), `pin` (versions differ but digest equal, or a secret whose version differs).
- `counts.attention` = rows with reason `kind` or `content_type`, or a `to` secret not `enabled`, or a changed parameter whose value is unavailable.
- Values are loaded only for parameters on changed/added/removed rows, only when `values=1`; a value over `value_cap_bytes` (256 KiB; above the 200 KiB highlighter cap so structural mode is always possible under it) is omitted with `omitted_size`; an unchanged row is `omitted_unchanged`; a secret is `secret`. A total budget of 4 MiB of values per response: once exceeded, remaining rows get `omitted_size` (rows are processed alias-sorted, so the client's "Load value" fallback is deterministic).
- Version authorship: `GetParameter` returns `CreatedBy`/`CreatedAt` (`storage/parameters.go:206-210`); for secrets pick the matching version out of `GetSecretInfo(...).Versions`.
- `counts.secrets_changed` = secret rows with `change !== "unchanged"`.

### D4. "Previous release" determination

- Single step back: the `previous` label (`current`/`previous` moved together in `storage/releases.go:553-562`; a rollback makes `previous` the version rolled back *from*, `core/releases.go:667-676`). Exposed already as `previous_version` on `/releases/active`, overview `release.active.previous_version`, and now `ReleaseDiffSide.previous_version`.
- Any older step: the audit log. `configuration_release.activate` / `.rollback` rows carry `previous_version`, `schema_version`, `activation_revision` in `metadata_json` (all decimal strings, `core/release_audit.go:14-24`, `core/releases.go:635`); `resource_key` is the release name and `resource_version` the activated version. `application.ship` rows carry `previous_version` and `release_version`. That is all the audit lane needs to link an activation to a diff; nothing new is written.
- Not built now (follow-up F1): a `GET /api/v1/releases/activations` timeline. `configuration_release_activations` (`storage/models.go:311-328`) has revision/version/time but no actor or from-version, so a real timeline needs a schema addition; out of scope.

### D5. Fixture

`TestConsoleFixtures` (`internal/server/httpserver/fixtures_test.go:121`) gets one more capture in the incident block (prod already has v1 with `rate_limits=7` and v2 with `rate_limits=12`, `:150-159`): `capture("release-diff", e.admin(GET, "/api/v1/releases/diff?env=prod&app=gradethis&name=runtime&schema_version=1&from=previous&to=current"))`. Timestamps are already normalised to `fixtureTime`. `frontend/tests/fixtures.test.ts` gains `assertReleaseDiff`.

---

## E. Implementation lanes

Exclusive file ownership; the lead commits per lane. `styles/globals.css` is touched by exactly one lane (F-shared) and only to add one `@import` line.

### E0. Contracts frozen before any lane starts

1. TS types in D3 (appended to `frontend/lib/types.ts` under a `// --- Release diff ---` header, names exactly as above).
2. `api.releaseDiff(req: ReleaseDiffQuery, request?: ApiRequestOptions): Promise<ReleaseDiffResponse>` where
   ```ts
   export interface ReleaseDiffQuery { env: string; app: string; name: string; schemaVersion: number;
     from: number | "current" | "previous"; to: number | "current" | "previous";
     toEnv?: string; toSchemaVersion?: number; values?: boolean }
   ```
   hitting `/releases/diff` with `qs({ env, app, name, schema_version, from, to, to_env, to_schema_version, values: values === false ? 0 : undefined })`.
3. `links.releaseCompare(opts: ReleaseCompareLinkOptions)` (B1) and `crumbs.releaseCompare(ns, name, from, to, schemaVersion)`.
4. Components (`frontend/components/releases/diff/`):
   ```ts
   export interface ReleaseDiffViewProps {
     query: ReleaseDiffQuery;                 // what to fetch; the view owns the request (useLatestRequest)
     compact?: boolean;                       // modal/dialog embedding
     view?: "changed" | "all"; q?: string;    // controlled filter state (page passes URL state)
     onViewChange?(view: "changed" | "all"): void; onQueryChange?(q: string): void;
     rollout?: OverviewRollout | null;        // page-supplied; strip cell hidden when undefined
     resolveHref?: (alias: string) => string | null;  // from entryHrefResolver
     onOpenResource?: (ref: ResourceRef, kind: ReleaseEntryKind) => void; // modal contexts (dirty guard)
     onRollback?: () => void;
     onLoaded?(diff: ReleaseDiffResponse): void;  // page uses it to rewrite label URLs to numbers and set the title
   }
   export interface ReleaseDiffSummaryProps { query: ReleaseDiffQuery; maxAliases?: number /* 5 */; href?: string }
   ```
   `ReleaseDiffSummary` requests `values: false` and renders one line of counts plus the first aliases and a "See all" link.
5. `lib/release-diff.ts` (pure): `buildRows(diff, opts): DiffRowModel[]` (adds `attention`, `flags`, `prefix`, `searchText`), `groupRows(rows, mode: "kind" | "prefix")`, `filterRows(rows, q, kind, view)`, `releaseDiffAsText(diff)`, `resolveDurationFormat(schemaJson, alias)` (reads `format: "go-duration"` from the pinned schema when the page has it).
6. `lib/value-diff.ts` exports in C4.
7. CSS family `.release-diff-*` only, in `frontend/styles/release-diff.css`, opened with `@layer features {` and imported from globals.css after `./palette.css`; `data-` attributes: `data-change`, `data-kind`, `data-alias`, `data-mode` (`structural|side`), `data-testid`: `release-diff`, `release-diff-strip`, `release-diff-row`, `release-diff-summary`, `release-diff-filter`, `release-diff-swap`.
8. Test ids and roles above are the e2e contract.

### E1. Lane B — backend (Go)

Files: **create** `internal/core/release_diff.go`, `internal/core/release_diff_test.go`, `internal/server/httpserver/releases_diff_test.go`; **modify** `internal/domain/releases.go` (append `ReleaseDiff*` domain types), `internal/server/httpserver/releases.go` (`handleDiffReleases`), `internal/server/httpserver/handlers.go` (one route line after `:91`), `internal/server/httpserver/dto_console.go` (DTOs + `toReleaseDiffDTO`; header comment says field names mirror `types.ts` — keep that true), `internal/server/httpserver/fixtures_test.go` (capture), `docs/http-api.md` (new bullet under "Configuration releases and schemas" at `:1387` and a "Release diff" subsection under Console aggregates after Rollback `:724`).

Steps: domain types → pure `computeReleaseDiff` with table tests (added/removed/changed reasons, unchanged, secrets, cross-env pin rule) → loader (`DiffConfigurationReleases(ctx, pr, req)`: resolve labels, authorize both tracks, load releases, per-row values with cap and budget, secrets via `GetSecretInfo`) → handler + route → HTTP tests on `newReleaseTestEnv` (pattern `handlers_test.go:1599`): 200 shape, `from=previous` 412 before any activation, 404 side naming, `to_env` cross-env, `values=0`, over-cap omission (write a 300 KiB `string` parameter), unauthenticated 401 → fixture capture → `go test ./internal/core ./internal/server/httpserver -run TestConsoleFixtures -update` → commit fixture JSON with the DTO change.

Gate: `go test ./...`, `go vet ./...` (Makefile `vet`), fixture test green without `-update`.

### E2. Lane F-shared — types, client, libs, view components, CSS

Files: **modify** `frontend/lib/types.ts`, `frontend/lib/api.ts`, `frontend/lib/links.ts`, `frontend/lib/crumbs.ts`, `frontend/styles/globals.css` (import line only), `frontend/tests/visual-tokens.test.ts` (import list `:192-205`); **create** `frontend/lib/value-diff.ts`, `frontend/lib/release-diff.ts`, `frontend/styles/release-diff.css`, `frontend/components/releases/diff/ReleaseDiffView.tsx`, `ReleaseDiffStrip.tsx`, `ReleaseDiffRow.tsx`, `ValueChange.tsx` (scalar/duration/binary/secret renderers), `StructuralDiff.tsx` (leaf list with folds), `ReleaseDiffSummary.tsx`, `useReleaseDiff.ts` (fetch hook on `useLatestRequest`; never abort in an effect cleanup keyed on state — the JSON-editor pass gotcha).

Must reuse: `JsonDiff`, `JsonLine`/`.tok-*`, `parseJsonTree`, `Ident`/`ReleaseIdent`/`NamespaceIdent`, `Badge` (from `components/ui`, `kind` prop), `SecretStateBadge`, `BindingModeBadge`, `SearchField`, `CopyButton`, `Highlight`, `Tabs variant="line"`, `Checkbox`, `AppSelect`, `formatRelative`/`useNow`/`formatUnixMs`, `formatBytes`, `countNoun`, `entryHrefResolver`.

CSS rules to honour (globals.css header): scrollers get `position: relative; min-width: 0; contain: inline-size`; grid containers holding mono text declare `> * { min-width: 0 }`; `min-height` not `height`; `--space-*` only; no literal colours; feature sheet in `@layer features` only.

Gate: `npm run typecheck`, `npx biome check --write` scoped to the touched files only (the broad `--write` reorders ~20 unrelated imports), vitest for `value-diff`, `release-diff`, `ReleaseDiffView` (fixture-driven).

### E3. Lane F-page — the page, releases surfaces, palette, shortcuts

Files: **create** `frontend/pages/releases/compare.tsx`; **modify** `frontend/components/releases/ReleaseWorkspace.tsx` (Compare tab body → `ReleaseDiffView compact`, keep `initialCompareKey` semantics, add "Open full comparison"), `frontend/pages/releases.tsx` (row "Compare" action, status-strip "What changed", compare-mode base state), `frontend/lib/palette.ts` (action), `frontend/lib/shortcuts.ts` (group), `frontend/components/ShortcutsDialog.tsx` only if the group needs a scope label.

Page responsibilities: read params with `useQueryParams(["app","env","name","schema_version","from","to","to_env","to_schema_version","view","q"])`; guard invalid schema via `parseSchemaVersion`; fetch the version list for the pickers; fetch the overview for the rollout cell only when `to` resolves to current; `PageHeader` with `crumbs.releaseCompare`; `documentTitle` "Compare runtime v7 → v9"; on `onLoaded` rewrite label params to numbers (`router.replace`, shallow, with the sanctioned-exception comment); handle the not-found/412 states from C7; `RollbackDialog` mounted with `active` synthesised the way `pages/releases.tsx:85-102` does (`activeFromSummaries`).

Gate: `npm run typecheck`, page vitest, `tests/releases.test.tsx` updated (the three compare tests at `:945-1060` assert the old 3-column headers and must be rewritten against the new view's test ids).

### E4. Lane F-entry — links from other surfaces

Files: **modify** `frontend/components/applications/ReleaseSection.tsx`, `frontend/components/applications/EnvironmentHome.tsx`, `frontend/components/ship/ShipModal.tsx` (`:886-923`), `frontend/components/ship/RollbackDialog.tsx` (`:183-195`, `:243-282`), `frontend/pages/audit.tsx` (`:571-574`, `:594-612`, `:633-644`), `frontend/lib/audit-release.ts` (add `auditReleasePreviousVersion(event)` and `auditShipReleaseVersions(event)` readers of the decimal-string metadata), `frontend/components/overview/ApplicationCard.tsx` (optional footer link).

Rules: inside modals use `Link` for navigation (new page) and `onOpenResource` for in-modal resource opening; `RollbackDialog` summary uses `values: false`; the audit expanded row summary also `values: false`; a `configuration_release.rollback` row links with `from: previous_version` (the newer one) → `to: resource_version`.

Gate: `tests/rollback-dialog.test.tsx` (`:103-123` href assertion updated), `tests/ship-modal.test.tsx` (new success-link assertion near `:572`), `tests/audit.test.tsx` (new link cases next to `:567-592`), `tests/environment-page.test.tsx`, `tests/environment-pipeline.test.tsx`.

### E5. Lane T — tests, fixtures, e2e

Files: **create** `frontend/tests/value-diff.test.ts`, `frontend/tests/release-diff.test.ts`, `frontend/tests/release-diff-view.test.tsx`, `frontend/tests/release-compare-page.test.tsx`, `frontend/tests/e2e/release-compare.spec.ts`; **modify** `frontend/tests/fixtures.test.ts` (load `release-diff.json`, assert `change`/`reasons`/`value_state` unions and that no secret row carries `value`), `frontend/tests/links.test.ts`, `frontend/tests/breadcrumbs.test.tsx`, `frontend/tests/command-palette.test.tsx`, `frontend/tests/shortcuts.test.tsx`, `frontend/tests/e2e/fakes/console-api.ts` (new `case "GET /releases/diff"` computing rows from `FakeNamespace.releases` and `parameters[key].versions[v-1]`, secrets from `secrets[key]`; `incidentState()` already synthesises v1..latest at `:178-197`), `frontend/tests/e2e/fakes/console-api.test.ts`, `frontend/tests/e2e/mobile-console.spec.ts` (add `/releases/compare?app=gradethis&env=prod&name=runtime&schema_version=1&from=1&to=2`), `frontend/tests/e2e/layout-guards.spec.ts` (one guard: row head ≥ 44px, no sideways scroll at 1280 and 400 with a 60-line JSON value expanded in structural and side-by-side modes).

e2e journey (`release-compare.spec.ts`): from `/applications/environment?app=gradethis&env=prod` click "What changed" → URL matches `/releases/compare?…from=1&to=2` → strip shows "Changed 1" → row `[data-alias="rate_limits"]` shows `7 → 12` → filter `rat` keeps one row → Swap flips the chips and the URL → Show unchanged reveals `database` and `db_password` (secret row has no value text; assert the stored fake secret bytes never appear in the DOM) → "Copy as text" writes clipboard (assert via `navigator.clipboard.readText` with permissions). Mobile project: same page, no document overflow.

Gate: `npm run test`, `npx playwright test release-compare layout-guards application-layout mobile-console` on a single shared `next dev` (`KMS_E2E_REUSE_SERVER=1`; never point `KMS_E2E_DIST_DIR` at a sibling dir — Tailwind scans any non-ignored `frontend/` dir).

### E6. Lane D — docs

Files: `docs/configuration-releases.md` (Management surfaces `:401-409` "diff" wording; Application-centred console list gets a "**Compare releases**" bullet after Roll back `:460-464`), `docs/http-api.md` (Lane B writes the endpoint; Lane D adds the console-aggregate prose and the link from the Rollback section), `docs/testing.md:213-231` (mention `release-diff.json`), `docs/operations.md:696` (note that the console diff includes values while the CLI diff stays entry-only).

### E7. Integration order and final gate

1. B (backend + fixture) → commit.
2. F-shared → commit.
3. F-page and F-entry concurrently (disjoint files) → commit each.
4. T → commit.
5. D → commit.
6. Lead: `cd frontend && npm run check` (typecheck + lint + format:check + vitest + `next build`), `go test ./...`, `make build` (embeds the export), full Playwright (3 projects), regenerate `desktop-mobile-baseline` snapshots only if page chrome changed, push, PR.

### E8. Risks, cut lines, follow-ups

Risks:
- **Response size.** Bounded by the 256 KiB per-value cap and 4 MiB budget; the client's "Load value" fallback covers the rest. Test with a 300 KiB value in Lane B.
- **Retention.** Pruned releases 404; the page's not-found state explains retention and links to audit.
- **Existing tests.** `tests/releases.test.tsx:945-1060` and `rollback-dialog.test.tsx:103-123` assert the old compare shapes and must change in the same commit as the components.
- **Fleet card geometry** is guarded (`layout-guards.spec.ts:135-152`); the optional footer link must not touch `.fleet-env`.
- **URL writes in effects** are forbidden except the documented pattern; the label→number rewrite must copy `environment.tsx:40-57`.
- **Concurrent sessions** editing the repo: commit-first, branch, no direct commits to main.

Cut lines (in order): (1) `n`/`p`/`x` shortcuts; (2) fleet-card link; (3) two-step compare mode in the releases list; (4) "Group by prefix"; (5) rollout strip cell; (6) cross-environment mode (keep the `to_env` param parsing so the URL contract does not change later); (7) inline summaries in Rollback dialog and audit rows (keep the links).

Follow-ups (not in this pass):
- F1: activation timeline endpoint (needs actor + from-version columns on `configuration_release_activations`).
- F2: `x-kms-reload: "restart"` schema annotation emitted by `kms-config-gen` from the `reload=restart` tag, consumed by `flags` in `lib/release-diff.ts` (the `flags` slot and the "restart" badge rendering ship now, dormant).
- F3: CLI `release diff --values` reusing the core function.
- F4: side-by-side JSON diff with array LCS alignment (today index-aligned in structural mode, line-LCS in side-by-side).
