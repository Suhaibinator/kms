// Quick Change needs room for every release column; long identifiers wrap
// within the preview instead of forcing horizontal navigation.
import { expect, type Locator, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

/**
 * The dialog's box once it has stopped moving: two identical readings a frame
 * apart. It is sized by its content, so it re-lays-out as the preview arrives
 * and a single reading can catch it mid-flight.
 */
async function settledBox(target: Locator) {
  let previous = "";
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const box = await target.boundingBox();
    const reading = JSON.stringify(box);
    if (box && reading === previous) return box;
    previous = reading;
    await target.page().waitForTimeout(50);
  }
  throw new Error("The dialog's geometry never settled");
}

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
    const dialog = page.getByRole("dialog", { name: /^Ship · / });
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
    // The card-table block is `width < 640px`, so 640 is still a table.
    if (width >= 640) {
      expect(geometry.columns.every((column) => column.visible)).toBe(true);
    } else {
      // Phone tables become cards; each field retains its visible data label.
      await expect(table.locator("tbody tr").first().locator("td[data-label]")).toHaveCount(5);
      await expect(
        table.locator("tbody tr").first().locator('td[data-label="Change"]'),
      ).toBeVisible();
    }
    const bounds = await settledBox(dialog);
    // The mobile-fullscreen block is `width < 768px`, matching Tailwind's `md`,
    // so 768 is the first width that gets the centred desktop dialog. That
    // dialog is `wide="xl"` — 960px, or the viewport less its 32px gutter —
    // rather than the old full-viewport workspace whose children were then
    // capped at 900px, leaving ~300px of empty dialog beside them.
    if (width >= 768) {
      expect(bounds.width).toBeCloseTo(Math.min(960, width - 32), 0);
      // The desktop dialog hugs its content between the wizard floor and the
      // viewport ceiling; it no longer claims the full height whatever it
      // holds. Below 768 it is deliberately full-screen.
      expect(bounds.height).toBeLessThanOrEqual(1000 - 32);
    } else {
      expect(bounds.width).toBeCloseTo(width, 0);
    }
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
  // ship.css's mobile block is `width < 768px`, matching the mobile-fullscreen
  // chrome and Tailwind's `md`: 768 is the first desktop width.
  expect(layout.alignItems).toBe(
    (page.viewportSize()?.width ?? 1280) < 768 ? "stretch" : "flex-start",
  );
  expect(layout.field.left).toBeGreaterThanOrEqual(0);
  expect(layout.field.right).toBeLessThanOrEqual(layout.viewportWidth);
  expect(layout.summary.left).toBeGreaterThanOrEqual(0);
  expect(layout.summary.right).toBeLessThanOrEqual(layout.viewportWidth);

  if ((page.viewportSize()?.width ?? 1280) < 768) {
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

// The stepper, the mode row and the form it heads share one measure, so moving
// between steps never re-flows the content column.
test("every ship step shares one measure", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "chromium", "desktop measure");
  await page.setViewportSize({ width: 1280, height: 900 });
  await mockConsole(page, incidentState());
  await page.goto("/applications?app=gradethis&env=prod");
  await page.getByRole("button", { name: "Edit & ship rate_limits in prod" }).click();
  const modal = page.getByTestId("ship-modal");
  await expect(modal).toBeVisible();
  const showSteps = modal.getByRole("checkbox", { name: "Show steps" });
  if ((await showSteps.getAttribute("data-state")) !== "checked") await showSteps.click();
  await expect(modal.locator(".wizard-steps")).toBeVisible();
  const children = await modal.evaluate((element) =>
    Array.from(element.children).map((node) => {
      const rect = node.getBoundingClientRect();
      return { cls: node.className, left: Math.round(rect.left), width: Math.round(rect.width) };
    }),
  );
  expect(children.length).toBeGreaterThan(2);
  const widths = new Set(children.map((child) => child.width));
  const lefts = new Set(children.map((child) => child.left));
  expect([...widths]).toHaveLength(1);
  expect([...lefts]).toHaveLength(1);
  // That measure is the dialog's own content box: the modal is sized to hug
  // its content, so nothing inside it is capped narrower than the dialog.
  const measure = await page.locator("[data-modal-body]").evaluate((element) => {
    const style = getComputedStyle(element);
    return (
      element.clientWidth -
      Number.parseFloat(style.paddingLeft) -
      Number.parseFloat(style.paddingRight)
    );
  });
  expect(Math.abs(children[0].width - measure)).toBeLessThanOrEqual(1);
});

// The blocked reason keeps its line whether or not it has anything to say: on a
// phone the footer is a fifth of the screen and the body must not grow under
// the finger at the moment the production name is finished.
test("the footer keeps its height when the blocked reason clears", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 667 });
  await mockConsole(page, incidentState());
  await page.goto("/applications?app=gradethis&env=prod");
  await page.getByRole("button", { name: "Edit & ship rate_limits in prod" }).click();
  const modal = page.getByTestId("ship-modal");
  await expect(modal).toBeVisible();
  await modal.getByRole("textbox", { name: "rate_limits value" }).fill("250");
  await expect(modal.getByTestId("ship-validation")).toContainText("valid");
  const heights = () =>
    page.evaluate(() => {
      const footer = document.querySelector('[data-slot="dialog-footer"]');
      const body = document.querySelector("[data-modal-body]");
      return {
        footer: footer?.getBoundingClientRect().height ?? 0,
        body: body?.getBoundingClientRect().height ?? 0,
      };
    });
  // The footer, and so the note, sits outside [data-modal-body].
  await expect(page.getByTestId("ship-blocked-reason")).toContainText("Type prod");
  const blocked = await heights();
  await modal.getByTestId("ship-confirm-env").fill("prod");
  await expect(page.getByTestId("ship-submit")).toBeEnabled();
  const ready = await heights();
  expect(ready.footer).toBeCloseTo(blocked.footer, 1);
  expect(ready.body).toBeCloseTo(blocked.body, 1);
});

// A five-column violation table cannot break below its badges and buttons, so
// the dialog that holds it has to be wide enough for its min-content.
test("the rollback dialog fits its violation table", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "chromium", "desktop table layout");
  test.slow();
  await page.setViewportSize({ width: 1280, height: 900 });
  const state = incidentState();
  await mockConsole(page, state);
  await page.goto("/applications?app=gradethis&env=prod");
  await page.getByRole("button", { name: "Edit & ship rate_limits in prod" }).click();
  const modal = page.getByTestId("ship-modal");
  await modal.getByRole("textbox", { name: "rate_limits value" }).fill("250");
  await expect(modal.getByTestId("ship-validation")).toContainText("valid");
  await modal.getByTestId("ship-confirm-env").fill("prod");
  await page.getByTestId("ship-submit").click();
  await expect(page.getByTestId("ship-rollback")).toBeVisible();
  // The previous release only fails validation now, after the ship succeeded.
  state.validate = () => ({
    valid: false,
    errors: [
      {
        alias: "rate_limits",
        code: "schema_violation",
        schema_pointer: "/properties/rate_limits/per_minute",
        message: "value 250 is above the maximum of 200 for this environment",
      },
    ],
  });
  await page.getByTestId("ship-rollback").click();
  const dialog = page.getByRole("dialog", { name: /Roll back|Re-activate/ });
  await expect(dialog.getByTestId("rollback-check")).toContainText("invalid");
  const table = await dialog.evaluate((element) => {
    const wrap = element.querySelector(".table-wrap");
    if (!wrap) throw new Error("Missing violation table");
    const rect = wrap.getBoundingClientRect();
    return {
      client: wrap.clientWidth,
      scroll: wrap.scrollWidth,
      headers: Array.from(wrap.querySelectorAll("th")).map((header) => {
        const cell = header.getBoundingClientRect();
        return { text: header.textContent, visible: cell.right <= rect.right + 1 };
      }),
    };
  });
  expect(table.scroll).toBeLessThanOrEqual(table.client);
  expect(table.headers).toHaveLength(5);
  expect(table.headers.every((header) => header.visible)).toBe(true);
});
