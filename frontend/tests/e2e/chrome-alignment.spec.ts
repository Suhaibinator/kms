// Geometry guards for the three rows of page chrome (layout rule 9). Every
// assertion here failed before the PageHeader / ContextBar / SectionHeader
// system landed:
//
//   a. `.page-header` is `align-items: flex-start`, so a ~24px badge title sat
//      7px above the 38px buttons beside it.
//   b. AppSelect's own `w-full` filled the switcher's label row and pushed the
//      caption onto a line of its own, so "All environments" was centred
//      against a two-line block.
//   c. SearchField always drew a caption above the input, so the "Values"
//      heading was bottom-aligned to the input's underside instead of sharing
//      its centreline.
//   d. The freshness pill was a 22px status chip loose among 38px buttons.
import { expect, type Locator, type Page, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

const box = async (locator: Locator) => {
  const rect = await locator.boundingBox();
  if (!rect) throw new Error("element has no box");
  return rect;
};

const centreY = (rect: { y: number; height: number }) => rect.y + rect.height / 2;

async function desktop(page: Page) {
  test.skip((page.viewportSize()?.width ?? 1280) < 768, "chrome alignment is a desktop guard");
  await page.setViewportSize({ width: 1280, height: 900 });
}

/** The header's title line and its action buttons share one centreline. */
async function titleSharesCentreWithActions(page: Page, action: Locator) {
  const title = await box(page.locator(".page-title"));
  const button = await box(action);
  // Only meaningful while the two are side by side; a header whose actions
  // have wrapped to their own line is a different assertion (see the compare
  // page below).
  expect(button.x).toBeGreaterThan(title.x + title.width - 1);
  expect(Math.abs(centreY(title) - centreY(button))).toBeLessThan(1);
}

/** The two segments of a RefreshControl are one control, not a pill beside a button. */
async function refreshSegmentsShareEdges(page: Page) {
  const control = page.locator(".refresh-control").first();
  await expect(control).toBeVisible();
  const badge = await box(control.getByRole("status"));
  const button = await box(control.getByRole("button", { name: /Refresh/ }));
  expect(Math.abs(badge.y - button.y)).toBeLessThan(1);
  expect(Math.abs(badge.y + badge.height - (button.y + button.height))).toBeLessThan(1);
  // Segments of one control: the seam is a shared border, not a gap.
  expect(Math.abs(badge.x + badge.width - button.x)).toBeLessThanOrEqual(1);
}

test("the environment page's header, context bar and section head each hold one line", async ({
  page,
}) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/applications/environment?app=gradethis&env=prod");
  await expect(page.getByRole("button", { name: /Ship to prod/ })).toBeVisible();

  // (a) the badge title and the primary action.
  await titleSharesCentreWithActions(page, page.getByRole("button", { name: /Ship to prod/ }));

  // (b) the switcher and the link beside it.
  const select = await box(page.getByRole("combobox", { name: "Environment" }));
  const allEnvironments = await box(page.getByRole("link", { name: "All environments" }));
  expect(Math.abs(centreY(select) - centreY(allEnvironments))).toBeLessThan(1);
  // One row: the caption sits beside the select, not above it.
  const contextBar = await box(page.locator(".context-bar"));
  expect(contextBar.height).toBeLessThan(48);

  // (c) the Values heading and its filter box.
  const heading = await box(page.getByRole("heading", { level: 2, name: "Values" }));
  const filter = await box(page.getByLabel("Filter values"));
  expect(Math.abs(centreY(heading) - centreY(filter))).toBeLessThan(1);

  // (d) freshness and refresh are one control.
  await refreshSegmentsShareEdges(page);
});

test("the application page's header holds one line", async ({ page }) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/applications?app=gradethis");
  const ship = page.getByRole("button", { name: /^Ship/ });
  await expect(ship).toBeVisible();

  await titleSharesCentreWithActions(page, ship);
  await refreshSegmentsShareEdges(page);

  // The schema track left the action row for the context bar.
  const picker = page.getByRole("combobox", { name: "Schema version" });
  await expect(picker).toBeVisible();
  const pickerBox = await box(picker);
  const header = await box(page.locator(".page-header"));
  expect(pickerBox.y).toBeGreaterThanOrEqual(header.y + header.height - 1);
});

test("the overview's header holds one line", async ({ page }) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/");
  const create = page.getByRole("button", { name: "New application" });
  await expect(create).toBeVisible();
  await titleSharesCentreWithActions(page, create);
  await refreshSegmentsShareEdges(page);
});

test("the subscribers page's header holds one line", async ({ page }) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto("/subscribers");
  await expect(page.getByRole("heading", { level: 1, name: "Subscribers" })).toBeVisible();
  await refreshSegmentsShareEdges(page);
  await titleSharesCentreWithActions(
    page,
    page.locator(".refresh-control").getByRole("button", { name: /Refresh/ }),
  );
});

test("the compare page's pickers and buttons hold one line", async ({ page }) => {
  await desktop(page);
  await mockConsole(page, incidentState());
  await page.goto(
    "/releases/compare?app=gradethis&env=prod&name=runtime&schema_version=1&from=1&to=2",
  );
  const swap = page.getByTestId("release-diff-swap");
  await expect(swap).toBeVisible();

  // No title-centre check here: this header's title is a pair of release
  // idents ~700px wide and its action row is ~880px, so the actions take a
  // line of their own at 1280 (they did at `size="sm"` too). What has to hold
  // is that the row is one line of equal-height controls.

  // Both pickers and both arrows sit on the swap button's centreline: the
  // action row used to mix sm buttons with a label-above-select grid.
  const swapBox = await box(swap);
  for (const name of ["From", "To"]) {
    const picker = await box(page.getByRole("combobox", { name }));
    expect(Math.abs(centreY(picker) - centreY(swapBox))).toBeLessThan(1);
  }
  for (const name of ["Compare with the older version", "Compare with the newer version"]) {
    const arrow = await box(page.getByRole("button", { name }));
    expect(Math.abs(centreY(arrow) - centreY(swapBox))).toBeLessThan(1);
    expect(Math.abs(arrow.height - swapBox.height)).toBeLessThan(1);
  }
});
