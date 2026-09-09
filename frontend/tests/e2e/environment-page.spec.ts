// The dedicated environment page: reached from a pipeline column, it ships,
// switches environments and edits the namespace's own settings. The fake
// serves the whole application overview and ignores `?env=`, so what the page
// shows is entirely its own selection.
import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("opens one environment from the pipeline and works inside it", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.goto("/applications?app=gradethis&env=prod");

  // The column header's chip is the way in.
  const column = page.locator('[data-env="prod"]');
  await column.getByRole("link", { name: "prod" }).first().click();
  await expect(page).toHaveURL(/\/applications\/environment\?app=gradethis&env=prod/);

  const heading = page.getByRole("heading", { level: 1 });
  await expect(heading).toContainText("prod");
  // Only this environment's values are listed, as a real table.
  const rows = page.locator("table.data tbody tr");
  await expect(rows).toHaveCount(3);
  await expect(page.getByTestId("table-summary")).toContainText("Showing 3 of 3 values");
  await expect(page.locator('[data-alias="rate_limits"]')).toBeVisible();

  // Ship is promoted into the header and targets this environment.
  await page.getByRole("button", { name: /Ship to prod/ }).click();
  const ship = page.getByRole("dialog", { name: /Ship/ });
  await expect(ship).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(ship).toBeHidden();

  // The switcher moves to the sibling environment without leaving the page.
  await page.getByRole("combobox", { name: "Environment" }).click();
  await page.getByRole("option", { name: "dev" }).click();
  await expect(page).toHaveURL(/\/applications\/environment\?app=gradethis&env=dev/);
  await expect(page.getByRole("button", { name: /Ship to dev/ })).toBeVisible();

  // "All environments" goes back to the side-by-side pipeline.
  await page.getByRole("link", { name: "All environments" }).click();
  await expect(page).toHaveURL(/\/applications\?app=gradethis/);
  await expect(page.locator('[data-env="dev"]')).toBeVisible();
});

test("edits the environment's description from its settings card", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.goto("/applications/environment?app=gradethis&env=dev");

  await expect(page.getByRole("button", { name: /Ship to dev/ })).toBeVisible();
  await page.getByRole("button", { name: "Edit environment settings" }).click();
  const dialog = page.getByRole("dialog", { name: "Edit dev/gradethis" });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Description").fill("the shared dev box");
  await dialog.getByRole("button", { name: "Save changes" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.locator(".page-subtitle")).toHaveText("the shared dev box");
});

test("renders the values as labelled cards on a phone", async ({ page, isMobile }) => {
  test.skip(!isMobile, "the card layout only applies to the phone breakpoint");
  await mockConsole(page, incidentState());
  await page.goto("/applications/environment?app=gradethis&env=prod");

  await expect(page.getByRole("button", { name: /Ship to prod/ })).toBeVisible();
  // The phone layout keeps the sort controls beside the table and labels every
  // cell; the document itself must not scroll sideways.
  await expect(page.locator(".mobile-list-toolbar")).toBeVisible();
  const cell = page.locator('table.data tbody td[data-label="Alias"]').first();
  await expect(cell).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});
