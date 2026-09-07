// Geometry guards for the application surfaces (pipeline, definition card,
// configuration matrix, applications list toolbar). The incident fixture has
// two short-named environments in 482px columns, which is the one shape where
// none of these defects reproduce, so every test here builds the state that
// does: six environments and a contract alias longer than a column.
import { expect, type Locator, type Page, test } from "@playwright/test";
import { type ConsoleState, incidentState, mockConsole } from "./fakes/console-api";

/** 63 characters, no break opportunities: underscores are not one. */
const LONG_ALIAS = "extremely_long_configuration_alias_for_overflow_testing_purposes";
/** The physical key behind it. Different from the alias, which is the only
 *  state in which the matrix renders the alias chip inside the frozen column. */
const PHYSICAL_KEY = "svc_db";
const EXTRA_ENVIRONMENTS = ["stg", "qa", "uat", "perf"];

const box = async (locator: Locator) => {
  const rect = await locator.boundingBox();
  if (!rect) throw new Error("element has no box");
  return rect;
};

/** Six environments and one alias far wider than a 300px column. */
function wideState(): ConsoleState {
  const state = incidentState();
  const dev = state.namespaces.dev;
  for (const env of EXTRA_ENVIRONMENTS) {
    const clone = structuredClone(dev);
    clone.namespace = { ...dev.namespace, env };
    clone.releases = clone.releases.map((release) => ({
      ...release,
      namespace: { ...release.namespace, env },
    }));
    clone.subscribers = clone.subscribers.map((row, index) => ({
      ...row,
      namespace: { ...row.namespace, env },
      instance_id: `${env}-${index + 1}`,
    }));
    state.namespaces[env] = clone;
  }
  state.application = {
    ...state.application,
    contract: state.application.contract.map((field) =>
      field.alias === "rate_limits" ? { ...field, alias: LONG_ALIAS } : field,
    ),
  };
  for (const ns of Object.values(state.namespaces)) {
    const parameter = ns.parameters.rate_limits;
    if (parameter) {
      ns.parameters[LONG_ALIAS] = { ...parameter, key: LONG_ALIAS };
      delete ns.parameters.rate_limits;
    }
    ns.releases = ns.releases.map((release) => ({
      ...release,
      entries: release.entries.map((entry) =>
        entry.alias === LONG_ALIAS || entry.alias === "rate_limits"
          ? { ...entry, alias: LONG_ALIAS }
          : entry,
      ),
    }));
  }
  return state;
}

/**
 * The fake resolves every alias to a physical key of the same name, so the
 * matrix's `.matrix-alias` chip — the thing that pins the frozen column — never
 * renders against it. Rewriting the overview response on the way in is the
 * smallest way to produce that state without teaching the fake a second
 * key-space that no other test needs.
 */
async function withPhysicalKey(page: Page, alias: string, key: string) {
  await page.addInitScript(
    ([aliasName, keyName]) => {
      const original = window.fetch;
      window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
        const response = await original(input, init);
        const url =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        if (!url.includes("/applications/overview")) return response;
        const body = await response.clone().json();
        for (const environment of body.environments ?? [])
          for (const value of environment.values ?? [])
            if (value.alias === aliasName && value.key) value.key = keyName;
        for (const row of body.rows ?? []) if (row.key === aliasName) row.key = keyName;
        return new Response(JSON.stringify(body), { status: response.status });
      };
    },
    [alias, key] as const,
  );
}

async function openWideApplication(page: Page, state: ConsoleState = wideState()) {
  await withPhysicalKey(page, LONG_ALIAS, PHYSICAL_KEY);
  await mockConsole(page, state);
  await page.goto("/applications?app=gradethis");
  await expect(page.locator(".definition-card")).toBeVisible();
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
}

async function desktop(page: Page) {
  test.skip((page.viewportSize()?.width ?? 1280) < 768, "desktop layout guard");
  await page.setViewportSize({ width: 1280, height: 900 });
}

// `.pipeline-column` is a grid whose sections, rows and action groups all kept
// min-width: auto, so one long alias stretched every row to its own max-content
// width — 519px inside a 266px content box — and painted over the neighbour.
test("pipeline columns contain a long alias at six environments", async ({ page }) => {
  await desktop(page);
  await openWideApplication(page);
  const columns = page.locator(".pipeline-column");
  await expect(columns).toHaveCount(2 + EXTRA_ENVIRONMENTS.length);
  const overflowing = await columns.evaluateAll((nodes) =>
    nodes
      .filter((node) => node.scrollWidth > node.clientWidth + 1)
      .map((node) => `${node.getAttribute("data-env")}: ${node.scrollWidth}/${node.clientWidth}`),
  );
  expect(overflowing).toEqual([]);
  // Every descendant stays inside the column it belongs to, which is what makes
  // the column's own scrollWidth a sufficient check for the rows above.
  const escaped = await columns.evaluateAll((nodes) =>
    nodes.flatMap((node) => {
      const edge = node.getBoundingClientRect().right;
      return [...node.querySelectorAll<HTMLElement>("*")]
        .filter((child) => child.getBoundingClientRect().right > edge + 0.5)
        .map((child) => `${node.getAttribute("data-env")}: ${child.className}`);
    }),
  );
  expect(escaped).toEqual([]);
});

// The neighbouring column's header sat on top of the button, so Playwright
// could not click the per-column menu at all ("subtree intercepts pointer
// events"). elementFromPoint is the assertion, since a hidden overlap is
// exactly what a screenshot would not show.
test("every pipeline column's More menu is hit-testable", async ({ page }) => {
  await desktop(page);
  await openWideApplication(page);
  const misses: string[] = [];
  for (const column of await page.locator(".pipeline-column").all()) {
    // Columns past the fold are outside the viewport, where elementFromPoint
    // reports nothing at all; the overlap is between neighbours, so bringing
    // each one into view in turn reproduces it.
    await column.scrollIntoViewIfNeeded();
    const miss = await column.evaluate((node) => {
      const button = node.querySelector<HTMLElement>('[data-slot="button"]');
      if (!button) return `${node.getAttribute("data-env")}: no More button`;
      const rect = button.getBoundingClientRect();
      const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
      return hit && button.contains(hit)
        ? null
        : `${node.getAttribute("data-env")}: ${hit?.tagName}.${hit?.className}`;
    });
    if (miss) misses.push(miss);
  }
  expect(misses).toEqual([]);
  // And the first column's menu really opens, which is what the overlap broke.
  await page
    .locator(".pipeline-column")
    .first()
    .getByRole("button", { name: /More for/ })
    .click();
  await expect(page.getByRole("menuitem", { name: "Parameters" })).toBeVisible();
});

// The Contract cell's alias chips are nowrap, and `justify-items: start` sizes
// each row of the cell to its min-content — which min-width: 0 does not lower.
// The chips ran 136px past the card and were the sole reason the page scrolled
// sideways on both tabs.
for (const width of [375, 768, 1280] as const) {
  test(`the definition card never widens the document at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await openWideApplication(page);
    const grid = page.locator(".definition-grid");
    expect(await grid.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1);
    const card = await box(page.locator(".definition-card"));
    const escaped = await page
      .locator(".definition-grid > div > *")
      .evaluateAll(
        (nodes, right) =>
          nodes
            .filter((node) => node.getBoundingClientRect().right > right + 0.5)
            .map((node) => node.className),
        card.x + card.width,
      );
    expect(escaped).toEqual([]);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth),
    ).toBeLessThanOrEqual(0);
  });
}

// The frozen Key column exists to keep the comparison readable; a nowrap alias
// chip inside it defeated `overflow-wrap: anywhere` and took 551px of a 978px
// wrapper, hiding three of six environment columns.
test("the matrix key column stays capped and the page never scrolls sideways", async ({ page }) => {
  await desktop(page);
  await openWideApplication(page);
  await page.getByRole("tab", { name: "Matrix" }).click();
  const matrix = page.locator(".application-matrix");
  await expect(matrix).toBeVisible();
  const keys = await matrix
    .locator("td.matrix-key")
    .evaluateAll((nodes) => nodes.map((node) => node.getBoundingClientRect().width));
  expect(keys.length).toBeGreaterThan(0);
  for (const width of keys) expect(width).toBeLessThanOrEqual(340.5);
  // The alias chip clamps itself rather than the column, and it is still there.
  await expect(matrix.locator(".matrix-alias .ident").first()).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth),
  ).toBeLessThanOrEqual(0);
  // The matrix scrolls inside .table-wrap, which is where the sideways motion
  // belongs; the guard above only forbids it on the document.
  expect(await matrix.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true);
});

// Field's cva base carries `w-full`, which no rule in the component layer can
// beat, so the Lifecycle select took a full-width line of its own and the
// toolbar was 109px tall instead of one 38px row.
test("the applications list toolbar is one row at 1280", async ({ page }) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/applications");
  const toolbar = page.locator(".application-list-toolbar");
  await expect(toolbar).toBeVisible();
  const input = await box(toolbar.locator(".application-list-filter"));
  const field = await box(toolbar.locator('[data-slot="field"]'));
  expect(field.width).toBeLessThan(400);
  // Same line: the field is a label over a select, so its box is taller than
  // the input's — the centres are what has to agree.
  expect(Math.abs(field.y + field.height / 2 - (input.y + input.height / 2))).toBeLessThan(16);
  expect((await box(toolbar)).height).toBeLessThan(70);
});

// The skeleton used to be a three-column table where the result is a
// breadcrumb trail, a definition card and a pipeline of cards (layout rule 8).
test("the application skeleton mirrors the loaded page", async ({ page }) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/applications/overview**", async (route) => {
    await gate;
    await route.fallback();
  });
  const navigation = page.goto("/applications?app=gradethis");
  await expect(page.locator('[aria-busy="true"]')).toBeVisible();
  const before = await page.evaluate(() => ({
    header: document.querySelector(".page-header")?.getBoundingClientRect().height ?? 0,
    crumbs: Boolean(document.querySelector("nav.crumbs-nav")),
    definition: Boolean(document.querySelector(".definition-card")),
    tabs: Boolean(document.querySelector('[data-slot="tabs-list"]')),
    pipeline: Boolean(document.querySelector(".pipeline-scroll")),
  }));
  expect(before).toMatchObject({ crumbs: true, definition: true, tabs: true, pipeline: true });
  release();
  await navigation;
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  const after = await page.evaluate(
    () => document.querySelector(".page-header")?.getBoundingClientRect().height ?? 0,
  );
  // The header is fully determined by the URL, so it must not move at all.
  expect(Math.abs(after - before.header)).toBeLessThan(1);
});

// .pipeline-cta-reason interpolates an alias into a sentence, and underscores
// are not break opportunities, so the whole token set the column's minimum.
test("a blocked column's reason wraps inside its column", async ({ page }) => {
  await desktop(page);
  const state = wideState();
  for (const ns of Object.values(state.namespaces)) delete ns.parameters[LONG_ALIAS];
  await openWideApplication(page, state);
  const reason = page.locator(".pipeline-cta-reason").first();
  await expect(reason).toBeVisible();
  const column = await box(page.locator(".pipeline-column").first());
  const rect = await box(reason);
  expect(rect.width).toBeLessThanOrEqual(column.width);
  expect(rect.x + rect.width).toBeLessThanOrEqual(column.x + column.width + 0.5);
});
