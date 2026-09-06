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
