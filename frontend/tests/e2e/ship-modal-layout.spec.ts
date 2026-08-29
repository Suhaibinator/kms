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
