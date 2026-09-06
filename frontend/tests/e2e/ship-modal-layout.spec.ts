// Quick Change needs room for every release column; long identifiers wrap
// within the preview instead of forcing horizontal navigation.
import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

for (const width of [320, 390, 640, 768, 820, 1024, 1280, 1440]) {
  test(`Quick Change keeps all preview columns visible at ${width}px`, async ({
    page,
  }, testInfo) => {
    test.skip(width > 768 && testInfo.project.name !== "chromium", "desktop table layout");
    await page.setViewportSize({ width, height: 1000 });
    const state = incidentState();
    await mockConsole(page, state);
    await page.goto("/applications?app=gradethis&env=prod");
    await page.getByRole("button", { name: "Edit & ship rate_limits in prod" }).click();
    const modal = page.getByTestId("ship-modal");
    const dialog = page.getByRole("dialog", { name: /Quick change/ });
    await expect(modal).toBeVisible();
    await expect(modal.getByRole("textbox", { name: "rate_limits value" })).toBeVisible();
    // Expand the server preview after opening so this test isolates the dialog
    // from the application page's separate long-contract layout.
    for (const alias of [
      "attachment_presign_secret_key_rotation_window_seconds",
      "discord_oauth_client_secret_fallback_credential",
      `integration_${"long_identifier_".repeat(12)}`,
    ]) {
      state.application.contract.push({ alias, kind: "parameter", content_type: "string" });
      for (const ns of Object.values(state.namespaces)) {
        ns.parameters[alias] = { key: alias, content_type: "string", versions: ["value-1"] };
      }
    }
    await modal.getByRole("textbox", { name: "rate_limits value" }).fill("250");
    const table = modal.locator("table.ship-entries");
    await expect(table).toBeVisible();
    await table.scrollIntoViewIfNeeded();
    if (process.env.CAPTURE_QA)
      await page.screenshot({ path: testInfo.outputPath(`quick-change-${width}.png`) });
    const geometry = await table.evaluate((element) => {
      const wrapper = element.parentElement;
      const body = element.closest("[data-modal-body]");
      if (!wrapper || !body) throw new Error("Missing preview container");
      const rect = wrapper.getBoundingClientRect();
      return {
        wrapper: { scroll: wrapper.scrollWidth, client: wrapper.clientWidth },
        body: { scroll: body.scrollWidth, client: body.clientWidth },
        columns: Array.from(element.querySelectorAll("th")).map((header) => {
          const cell = header.getBoundingClientRect();
          return {
            text: header.textContent,
            visible: cell.left >= rect.left && cell.right <= rect.right,
          };
        }),
      };
    });
    expect(geometry.wrapper.scroll).toBeLessThanOrEqual(geometry.wrapper.client);
    expect(geometry.body.scroll).toBeLessThanOrEqual(geometry.body.client);
    expect(geometry.columns.map((column) => column.text)).toEqual([
      "Alias",
      "Kind",
      "Key",
      "Version",
      "Change",
    ]);
    if (width > 640) {
      expect(geometry.columns.every((column) => column.visible)).toBe(true);
    } else {
      // Phone tables become cards; each field retains its visible data label.
      await expect(table.locator("tbody tr").first().locator("td[data-label]")).toHaveCount(5);
      await expect(
        table.locator("tbody tr").first().locator('td[data-label="Change"]'),
      ).toBeVisible();
    }
    const bounds = await dialog.boundingBox();
    if (!bounds) throw new Error("Missing dialog bounds");
    if (width > 768) expect(bounds.width).toBeGreaterThan(720);
    else expect(bounds.width).toBeCloseTo(width, 0);
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
  });
}

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
    (page.viewportSize()?.width ?? 1280) <= 768 ? "stretch" : "flex-start",
  );
  expect(layout.field.left).toBeGreaterThanOrEqual(0);
  expect(layout.field.right).toBeLessThanOrEqual(layout.viewportWidth);
  expect(layout.summary.left).toBeGreaterThanOrEqual(0);
  expect(layout.summary.right).toBeLessThanOrEqual(layout.viewportWidth);

  if ((page.viewportSize()?.width ?? 1280) <= 768) {
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
