# Frontend development

The KMS console is a Next.js application that ships as a static export in
`frontend/out`. The Go build embeds that directory into the `parameter-store`
binary; there is no Next.js server in production.

## Local development

Install the locked dependencies:

```bash
cd frontend
npm ci
```

Start the Go HTTP server on `localhost:8080`, then run:

```bash
npm run dev
```

The Next.js development server proxies `/api/*` to
`http://localhost:8080/api/*`. Open the URL printed by Next.js and sign in with
an identity token. See the root [quickstart](../README.md#initialize-and-run-locally)
for a local server setup.

## List tables

Every list page composes the same three pieces, so a new one does not invent
its own:

- **Sorting** — declare the columns once as a module-level `SortColumn[]`
  (`lib/sort.ts`), then `useSort(pathname, COLUMNS)` and `<SortHeaderRow>` from
  `components/SortableTable.tsx`. The state lives in `?sort=&dir=`, so a sorted
  list survives a reload and can be shared. Server-paginated lists sort the
  loaded page only and say so in the header tooltip and the footer.
- **Totals** — `<TableSummary>` (`components/ui.tsx`) is the table's `<caption>`,
  flipped to the bottom: "Showing N of M" plus the count of active filters.
- **Bulk actions** — `useBulkSelection`, `SelectAllCell`, `SelectRowCell`,
  `BulkActionBar` and `BulkDeleteDialog` (`components/BulkSelection.tsx`) run the
  existing per-item API once per selection with `runBulk` (`lib/bulk.ts`); there
  are no bulk endpoints.
- **Search** — one `<SearchField>` (`components/SearchField.tsx`), focused with
  `/` and cleared with Esc. A non-empty box switches the page out of server
  pagination: `useNamespaceIndex` (`lib/useNamespaceIndex.ts`) walks the whole
  namespace once, up to 5,000 rows, and `lib/key-search.ts` ranks it — every
  whitespace token has to match the key or the row's text, key hits outrank
  value hits, and the best 200 are shown with the matched characters marked
  (`components/Highlight.tsx`). The query lives in `?q=`, so a search is a
  shareable link; the old `?key_prefix=` still opens as one.

Keyboard shortcuts are declared in `lib/shortcuts.ts` and rendered by the `?`
sheet. A new `keydown` handler belongs in that list.

## Checks and tests

Run the source, component, and production-build gates with:

```bash
npm run check
```

This runs generated route types, TypeScript, Biome lint/format checks, Vitest,
and the static export. Browser tests are separate because they require
Chromium:

```bash
npx playwright install chromium # first run only
npm run test:e2e
```

Playwright starts its own Next.js development server and intercepts the JSON
API with in-memory fakes. It does not require a running Go server. See
[`docs/testing.md`](../docs/testing.md#frontend) for fixture ownership and CI
boundaries.

## Production export and preview

Build the files embedded by Go with:

```bash
npm run build
test -f out/index.html
```

From the repository root, `make frontend` performs the locked install and the
same build. `next start` is not a valid preview command when Next.js uses
`output: "export"`; serve `out/` with a static file server for a frontend-only
preview, or run `make build` and start the resulting binary as described in the
root quickstart to exercise the deployed routing behavior.

All runtime data comes from the JSON API documented in
[`docs/http-api.md`](../docs/http-api.md).

## Responsive console

The desktop layout remains the baseline. At 768px and below, navigation uses the
existing drawer and forms stack with natural field heights. At 640px and below,
ordinary lists render labeled cards. Sortable lists must also render
`MobileListToolbar` beside the table, using the same sort controller and bulk
selection as the desktop header. Every card cell needs a meaningful `data-label`,
including cells rendered by child row components. Configuration matrices keep
local horizontal scrolling for comparing environments.

Creation and editing dialogs opt into `Modal`'s `mobileFullScreen` prop. It keeps
one mounted editor across breakpoints, uses safe-area padding, and follows the
visual viewport when an on-screen keyboard reduces the available height.
Confirmation dialogs retain the compact inset layout. Mobile checkbox chrome
remains compact; its reserved hit area is 44px.

Browser tests now include desktop Chromium, Android Chromium, and iPhone WebKit:

```bash
npx playwright install chromium webkit
npm run test:e2e
```

`mobile-console.spec.ts` covers populated/empty routes with long identifiers;
`mobile-layout.spec.ts` covers breakpoint geometry, sorting and bulk selection,
draft preservation, drawer cleanup, request failures, and simulated visual
viewport changes. The desktop pixel baselines were captured on macOS in both
themes at 1280px and 1440px; their snapshot test runs on macOS, while behavior and
geometry tests run on every platform. Set `CAPTURE_QA=1` to save additional mobile
screenshots in the Playwright output directory.

For concurrent local test runs, isolate the server and output:

```bash
KMS_E2E_PORT=32190 KMS_E2E_DIST_DIR=.next/mobile-e2e \
KMS_E2E_OUTPUT_DIR=.next/mobile-results npm run test:e2e
```

Real-device release checks should exercise iOS Safari and Android Chrome with
the software keyboard open, portrait/landscape rotation, browser chrome changes,
safe areas, pinch zoom, and nested discard confirmations. Playwright's WebKit
project and simulated visual viewport changes do not replace these device checks.
