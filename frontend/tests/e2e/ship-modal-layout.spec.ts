// The ship modal must never scroll sideways: wide preview content (the entries
// table with long mono aliases and keys) scrolls inside its own .table-wrap,
// not by widening the modal body's grid column.

import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("ship modal body does not scroll horizontally with a wide preview", async ({ page }) => {
  test.skip(test.info().project.name !== "chromium", "desktop-only layout check");
  const state = incidentState();
  // Long aliases and keys, like a real app with many provider credentials,
  // make the preview table's min-content wider than the modal.
  for (const alias of [
    "attachment_presign_secret_key_rotation_window_seconds",
    "discord_oauth_client_secret_fallback_credential",
  ]) {
    state.application.contract.push({ alias, kind: "parameter", content_type: "string" });
    for (const ns of Object.values(state.namespaces)) {
      ns.parameters[alias] = { key: alias, content_type: "string", versions: ["value-1"] };
    }
  }
  await mockConsole(page, state);

  await page.goto("/applications?app=gradethis&env=prod");
  await page.getByRole("button", { name: "Edit & ship rate_limits in prod" }).click();
  const modal = page.getByTestId("ship-modal");
  await expect(modal).toBeVisible();
  // The dry run only runs after an edit; the table renders once it lands.
  await modal.getByRole("textbox", { name: "rate_limits value" }).fill("250");
  await expect(modal.getByTestId("ship-activation")).toBeVisible();
  await expect(modal.locator("table.ship-entries")).toBeVisible();

  const body = page.locator("[data-modal-body]");
  const widths = await body.evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
    table: el.querySelector("table.ship-entries")?.scrollWidth ?? 0,
  }));
  console.log("modal body widths", JSON.stringify(widths));
  expect(widths.table).toBeGreaterThan(widths.client); // the table really is wider
  expect(widths.scroll).toBeLessThanOrEqual(widths.client); // …but the body does not scroll
});

test("ship environment summary is anchored independently of field content", async ({
  page,
}, testInfo) => {
  const state = incidentState();
  await mockConsole(page, state);

  await page.goto("/applications?app=gradethis&env=prod");
  await page.getByRole("button", { name: "Edit & ship rate_limits in prod" }).click();
  const modal = page.getByTestId("ship-modal");
  const row = modal.locator(".ship-env-row");
  const field = row.locator(".ship-env-field");
  const environment = row.getByRole("combobox", { name: "Environment" });
  const summary = row.locator(".ship-env-summary");
  await expect(summary).toBeVisible();

  const layout = await row.evaluate((element) => {
    const fieldElement = element.querySelector(".ship-env-field");
    const controlElement = element.querySelector('[role="combobox"]');
    const summaryElement = element.querySelector(".ship-env-summary");
    if (!fieldElement || !controlElement || !summaryElement) {
      throw new Error("Ship environment row is missing a required element");
    }
    const field = fieldElement.getBoundingClientRect();
    const control = controlElement.getBoundingClientRect();
    const summary = summaryElement.getBoundingClientRect();
    const style = getComputedStyle(element);
    return {
      alignItems: style.alignItems,
      field: { left: field.left, right: field.right, top: field.top, bottom: field.bottom },
      control: { top: control.top },
      summary: { left: summary.left, right: summary.right, top: summary.top },
      viewportWidth: window.innerWidth,
    };
  });
  expect(layout.alignItems).toBe(
    testInfo.project.name === "mobile-chromium" ? "stretch" : "flex-start",
  );
  expect(layout.field.left).toBeGreaterThanOrEqual(0);
  expect(layout.field.right).toBeLessThanOrEqual(layout.viewportWidth);
  expect(layout.summary.left).toBeGreaterThanOrEqual(0);
  expect(layout.summary.right).toBeLessThanOrEqual(layout.viewportWidth);

  if (testInfo.project.name === "mobile-chromium") {
    expect(layout.summary.top).toBeGreaterThanOrEqual(layout.field.bottom);
    expect(layout.summary.top - layout.field.bottom).toBeLessThanOrEqual(32);
  } else {
    expect(layout.summary.top).toBeCloseTo(layout.control.top, 0);
  }

  // Keep the locators live through the assertions so failures report the
  // user-facing controls rather than only anonymous geometry.
  await expect(field).toBeVisible();
  await expect(environment).toBeEnabled();
  if (process.env.CAPTURE_QA) {
    await page.screenshot({ path: testInfo.outputPath("ship-environment-row.png") });
  }
});
