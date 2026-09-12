// Geometry guards for the layout defects found after the Sep 2026 console
// pass: modals resizing between tabs, tables forcing horizontal scroll inside
// a dialog, toolbar controls touching their rules, ident chips stretched by a
// parent selector, and pipeline row actions overflowing their column. Each
// assertion is a measurement, not a screenshot, so it fails on the cause.
import { expect, type Locator, type Page, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

const box = async (locator: Locator) => {
  const rect = await locator.boundingBox();
  if (!rect) throw new Error("element has no box");
  return rect;
};

/** True when the element scrolls sideways: the one thing a dialog body must never do. */
const scrollsSideways = (locator: Locator) =>
  locator.evaluate((element) => element.scrollWidth > element.clientWidth + 1);

/** The popup zooms in on open; measuring before that settles reads a scaled box. */
const settled = (locator: Locator) =>
  locator.evaluate((element) =>
    Promise.all(element.getAnimations({ subtree: true }).map((animation) => animation.finished)),
  );

async function desktop(page: Page) {
  test.skip((page.viewportSize()?.width ?? 1280) <= 768, "desktop layout guard");
  await page.setViewportSize({ width: 1280, height: 900 });
}

/** The centre of `locator` on the block axis, for alignment comparisons. */
const centreY = async (locator: Locator) => {
  const rect = await box(locator);
  return rect.y + rect.height / 2;
};

test("the secret workspace keeps one width across tabs and never scrolls sideways", async ({
  page,
}) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "db_password" }).click();
  const dialog = page.getByRole("dialog", { name: /prod\/gradethis\/db_password/ });
  await expect(dialog).toBeVisible();
  const body = dialog.locator("[data-modal-body]");
  await expect(dialog.getByRole("tab", { name: "Overview" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await settled(dialog);
  const overview = await box(dialog);
  const overviewContent = await box(body.locator("> *").first());
  expect(await scrollsSideways(body)).toBe(false);

  await dialog.getByRole("tab", { name: "Versions" }).click();
  await expect(dialog.getByRole("columnheader", { name: /version/i })).toBeVisible();
  const versions = await box(dialog);
  const versionsContent = await box(body.locator("> *").first());
  expect(await scrollsSideways(body)).toBe(false);
  // The dialog and its content column are the same width on both tabs; a
  // scrollbar appearing on one tab must not shift the other.
  expect(Math.abs(versions.width - overview.width)).toBeLessThan(1);
  expect(Math.abs(versions.x - overview.x)).toBeLessThan(1);
  expect(Math.abs(versionsContent.width - overviewContent.width)).toBeLessThan(1);

  // Toolbar controls keep clearance from the header rule above and the tab
  // rule below instead of touching them.
  const toolbar = dialog.locator(".secret-workspace-toolbar");
  const toolbarBox = await box(toolbar);
  for (const button of await toolbar.getByRole("button").all()) {
    const rect = await box(button);
    expect(rect.y - toolbarBox.y).toBeGreaterThanOrEqual(4);
    expect(toolbarBox.y + toolbarBox.height - (rect.y + rect.height)).toBeGreaterThanOrEqual(4);
    expect(rect.x + rect.width).toBeLessThanOrEqual(toolbarBox.x + toolbarBox.width + 0.5);
  }
});

test("release chips in the status strip stay one line and content-width", async ({ page }) => {
  await desktop(page);
  const state = incidentState();
  await mockConsole(page, state);
  await page.goto(`/releases?app=gradethis&env=prod&name=${state.application.release_name}`);
  const strip = page.locator(".release-status-strip");
  await expect(strip).toBeVisible();
  const chips = strip.locator(".ident");
  expect(await chips.count()).toBeGreaterThan(0);
  for (const chip of await chips.all()) {
    const rect = await box(chip);
    const column = await box(chip.locator("xpath=ancestor::div[1]"));
    // One line of 12px mono inside a 22px chip; a second line would be ~34px.
    expect(rect.height).toBeLessThanOrEqual(26);
    // Content-width, not stretched to the grid column.
    expect(rect.width).toBeLessThan(column.width - 8);
    const kind = await box(chip.locator(".ident-kind"));
    const value = await box(chip.locator(".ident-value"));
    // Prefix and value share a line.
    expect(Math.abs(kind.y + kind.height / 2 - (value.y + value.height / 2))).toBeLessThan(2);
    expect(value.y + value.height).toBeLessThanOrEqual(rect.y + rect.height + 0.5);
  }
});

test("pipeline row actions stay inside their column and the matrix inside its wrapper", async ({
  page,
}) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/applications?app=gradethis&env=prod");
  const columns = page.locator(".pipeline-column");
  await expect(columns.first()).toBeVisible();
  for (const column of await columns.all()) {
    expect(await scrollsSideways(column)).toBe(false);
    const columnBox = await box(column);
    for (const actions of await column.locator(".pipeline-row-actions").all()) {
      const rect = await box(actions);
      expect(rect.x + rect.width).toBeLessThanOrEqual(columnBox.x + columnBox.width + 0.5);
      expect(rect.x).toBeGreaterThanOrEqual(columnBox.x - 0.5);
    }
  }
  await page.getByRole("tab", { name: "Matrix" }).click();
  const matrix = page.locator(".application-matrix");
  await expect(matrix).toBeVisible();
  // The page itself never scrolls sideways because of the matrix.
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(
    false,
  );
  // With the fixture's two environments the table fits its wrapper outright.
  expect(await scrollsSideways(matrix)).toBe(false);
});

// A fleet card's environment row let only the name shrink: the release cell
// was flex-shrink: 0, the prod pill and dot flex: 0 0 auto, so in a 300px
// track "prod" rendered as "p…" beside a pill that said PROD. The name is the
// fixed part now and the release cell yields, and the track floor is what the
// row actually needs.
test("fleet card environment names and release chips are never ellipsised at 1280", async ({
  page,
}) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/");
  const rows = page.locator(".fleet-env");
  await expect(rows.first()).toBeVisible();
  await expect(rows.first().locator(".fleet-env-release .ident")).toBeVisible();
  for (const row of await rows.all()) {
    const name = row.locator(".fleet-env-name");
    expect(await name.evaluate((el) => el.scrollWidth <= el.clientWidth + 0.5)).toBe(true);
    const value = row.locator(".fleet-env-release .ident-value");
    expect(await value.evaluate((el) => el.scrollWidth <= el.clientWidth + 0.5)).toBe(true);
    // The chip and its cell end together: nothing spills past the row.
    const rowRect = await box(row);
    const chip = await box(row.locator(".fleet-env-release .ident"));
    expect(chip.x + chip.width).toBeLessThanOrEqual(rowRect.x + rowRect.width + 0.5);
  }
});

// The release comparison's rows expand to nested content (a structural leaf
// list or the side-by-side JSON diff). Each head is a touch target, and a
// 60-line JSON value must scroll inside its own pane, never widen the row,
// the page or, at phone width, the document.
test("release comparison rows keep a 44px head and never scroll sideways with JSON expanded", async ({
  page,
}) => {
  await desktop(page);
  const state = incidentState();
  const prod = state.namespaces.prod;
  const json = (max: number) =>
    JSON.stringify(
      {
        pool: { max, idle: 10, timeout: "30s" },
        hosts: Array.from({ length: 24 }, (_, i) => ({
          name: `db-${i}.internal.example.com`,
          port: 5432 + i,
          weight: i % 3,
        })),
        features: { read_replicas: max > 10, sharding: false },
      },
      null,
      2,
    );
  prod.parameters.features = {
    key: "features",
    content_type: "json",
    versions: [json(50), json(5)],
  };
  for (const release of prod.releases) {
    release.entries.push({
      alias: "features",
      kind: "parameter",
      ref: { namespace: { env: "prod", app: "gradethis" }, key: "features" },
      version: release.version >= 2 ? 2 : 1,
      content_type: "json",
      metadata_json: "{}",
      parameter_digest: "",
    });
  }
  await mockConsole(page, state);
  await page.goto(
    "/releases/compare?app=gradethis&env=prod&name=runtime&schema_version=1&from=1&to=2",
  );
  const features = page.locator('[data-testid="release-diff-row"][data-alias="features"]');
  await expect(features).toBeVisible();
  const main = page.locator("main");

  const check = async (label: string) => {
    for (const head of await page.locator(".release-diff-row-head").all()) {
      const rect = await box(head);
      expect(rect.height, `${label}: row head height`).toBeGreaterThanOrEqual(44);
    }
    expect(await scrollsSideways(main), `${label}: main scrolls sideways`).toBe(false);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
      ),
      `${label}: document scrolls sideways`,
    ).toBe(false);
    const body = features.locator(".release-diff-row-body");
    if ((await body.count()) > 0) {
      expect(await scrollsSideways(body), `${label}: row body scrolls sideways`).toBe(false);
      const rowRect = await box(features);
      const bodyRect = await box(body);
      expect(bodyRect.x + bodyRect.width, `${label}: body inside row`).toBeLessThanOrEqual(
        rowRect.x + rowRect.width + 0.5,
      );
    }
  };

  for (const width of [1280, 400]) {
    await page.setViewportSize({ width, height: 900 });
    await check(`${width} collapsed`);
    if (!(await features.getByRole("button", { name: "Collapse features" }).isVisible())) {
      await features.getByRole("button", { name: "Expand features" }).click();
    }
    await expect(features.getByTestId("release-diff-structural")).toBeVisible();
    await check(`${width} structural`);
    await features.getByRole("tab", { name: "Side-by-side" }).click();
    await expect(features.getByTestId("json-diff")).toBeVisible();
    await check(`${width} side-by-side`);
    await features.getByRole("tab", { name: "Structural" }).click();
  }
});

// The hand-written mobile block used to be inclusive (`max-width: 768px`) while
// Tailwind's `max-md:`, which gates the drawer trigger, compiles to
// `width < 48rem` against the initial 16px root — exclusive. At exactly 768.0
// the sidebar was hidden and the trigger was not shown, so an iPad in portrait
// had no way into the navigation at all. Both are exclusive now, so 768 is the
// first desktop width.
test("navigation is reachable on both sides of the 768px breakpoint", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.setViewportSize({ width: 767, height: 1024 });
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();

  for (const width of [768, 769]) {
    await page.setViewportSize({ width, height: 1024 });
    await expect(page.locator(".desktop-sidebar")).toBeVisible();
    await expect(page.getByRole("button", { name: "Open navigation" })).toBeHidden();
  }
});

// `[data-slot="checkbox"] { margin: 14px }` bought a touch target with layout:
// under .checkbox-row's flex-start it dropped every box 14px below its own
// label. The hit area is the box's absolutely positioned ::after instead.
test("every checkbox sits beside the first line of its own label at 375", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.setViewportSize({ width: 375, height: 667 });
  await page.goto("/secrets/new");
  const rows = page.locator(".checkbox-row");
  await expect(rows.first()).toBeVisible();
  expect(await rows.count()).toBeGreaterThan(0);
  for (const row of await rows.all()) {
    const boxEl = row.locator('[data-slot="checkbox"]');
    const label = row.locator("label");
    // A label that stacks a title over a hint is measured on its first line.
    const first = label.locator("> *").first();
    const line = (await first.count()) > 0 ? first : label;
    expect(Math.abs((await centreY(boxEl)) - (await centreY(line)))).toBeLessThan(2);
    const boxRect = await box(boxEl);
    const rowRect = await box(row);
    // And in the row's own content column, not 14px into it.
    expect(boxRect.x - rowRect.x).toBeLessThan(1);
  }
});

// Automatic table layout hands the actions column whatever the text columns
// leave; `flex-wrap: wrap` then turned a few pixels of shortfall into a second
// line on every row (identities measured 130.5px rows at 1280).
for (const [name, route] of [
  ["identities", "/identities"],
  ["releases", "/releases?app=gradethis&env=prod"],
  ["namespaces", "/namespaces"],
] as const) {
  test(`${name} keeps its rows to one line of actions at 1280`, async ({ page }) => {
    await desktop(page);
    await mockConsole(page, incidentState());
    await page.goto(route);
    // Skeleton rows carry their own reserved height; measure the loaded table.
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    const rows = page.locator("table.data tbody tr");
    await expect(rows.first()).toBeVisible();
    // In one evaluate: a per-row round trip re-resolves the locator and races
    // any re-render between them.
    const brokenActions = await rows.evaluateAll((nodes) =>
      nodes.flatMap((node) => {
        const actions = node.querySelector<HTMLElement>(".row-actions");
        if (!actions) return [];
        const cell = actions.closest("td");
        if (!cell) return [{ text: (node.textContent ?? "").slice(0, 40), reason: "no cell" }];
        const cellBox = cell.getBoundingClientRect();
        const buttons = [...actions.children].map((button) => button.getBoundingClientRect());
        const lines = new Set(buttons.map((button) => Math.round(button.top)));
        const overflows = buttons.some(
          (button) => button.left < cellBox.left - 0.5 || button.right > cellBox.right + 0.5,
        );
        return lines.size > 1 || overflows
          ? [{ text: (node.textContent ?? "").slice(0, 40), reason: "wrapped or overflowed" }]
          : [];
      }),
    );
    expect(brokenActions).toEqual([]);
  });
}

// The ≤768px `[data-slot="button"] { height: auto }` reset had no replacement
// height for the two smallest sizes, so `icon-xs` was the one control in the
// app whose two dimensions disagreed (24×30 with a 10px radius).
test("icon-xs buttons stay square at 375", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.setViewportSize({ width: 375, height: 667 });
  await page.goto("/parameters");
  await page.getByRole("button", { name: "New parameter" }).click();
  const buttons = page.locator('[data-slot="button"][data-size="icon-xs"]');
  await expect(page.getByRole("dialog")).toBeVisible();
  for (const button of await buttons.all()) {
    if (!(await button.isVisible())) continue;
    const rect = await box(button);
    expect(Math.abs(rect.width - rect.height)).toBeLessThan(1);
  }
});
