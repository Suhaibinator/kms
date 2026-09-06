import { expect, type Page, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

async function fits(page: Page) {
  await expect
    .poll(() =>
      page.evaluate(() => ({
        page: document.documentElement.scrollWidth,
        viewport: window.innerWidth,
      })),
    )
    .toEqual(expect.objectContaining({ page: page.viewportSize()?.width }));
}

for (const width of [320, 390, 400, 640, 641, 768, 769, 1280, 1440]) {
  test(`filter layout and list controls at ${width}px`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 900 });
    await mockConsole(page, incidentState());
    await page.goto("/secrets?env=prod&app=gradethis");
    await expect(page.getByRole("link", { name: "db_password" })).toBeVisible();
    await fits(page);
    const fields = page.locator(".filters > .field, .filters > .filter-grow");
    const bounds = await fields.evaluateAll((elements) =>
      elements.map((el) => {
        const r = el.getBoundingClientRect();
        return { height: r.height, top: r.top, bottom: r.bottom };
      }),
    );
    if (width <= 768) {
      for (const field of bounds) expect(field.height).toBeLessThan(110);
      for (let i = 1; i < bounds.length; i++) {
        expect(bounds[i].top - bounds[i - 1].bottom).toBeGreaterThanOrEqual(0);
        expect(bounds[i].top - bounds[i - 1].bottom).toBeLessThanOrEqual(20);
      }
      expect(
        await page
          .getByRole("textbox", { name: "Key prefix" })
          .evaluate((el) => Number.parseFloat(getComputedStyle(el).fontSize)),
      ).toBeGreaterThanOrEqual(16);
    }
    if (process.env.CAPTURE_QA && width === 400) {
      await page.screenshot({ path: info.outputPath("mobile-secrets.png"), fullPage: true });
    }
    const toolbar = page.getByRole("group", { name: "List controls" });
    if (width <= 640) {
      await expect(toolbar).toBeVisible();
      for (const select of await toolbar.getByRole("combobox").all()) {
        expect((await select.boundingBox())?.height).toBeGreaterThanOrEqual(44);
      }
      await expect(page.getByRole("columnheader")).toHaveCount(0);
      await toolbar.getByRole("combobox", { name: "Sort by", exact: true }).selectOption("key");
      await toolbar.getByRole("combobox", { name: "Direction" }).selectOption("desc");
      await expect(page).toHaveURL(/sort=key.*dir=desc/);
      await toolbar.getByRole("checkbox", { name: /Select all secrets/ }).check();
      await expect(page.getByRole("region", { name: "Bulk actions" })).toContainText("selected");
      await page.setViewportSize({ width: 1280, height: 900 });
      await expect(toolbar).toBeHidden();
      await expect(
        page.getByRole("checkbox", { name: "Select all secrets on this page" }),
      ).toBeChecked();
      await expect(page.getByRole("columnheader", { name: "Key" })).toHaveAttribute(
        "aria-sort",
        "descending",
      );
    } else {
      await expect(toolbar).toBeHidden();
      await expect(page.getByRole("columnheader", { name: "Key" })).toBeVisible();
    }
  });
}

test("phone workspace fits and preserves unsaved edits across resizing", async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 740 });
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "db_password" }).click();
  const workspace = page.getByRole("dialog", { name: /prod\/gradethis\/db_password/ });
  await expect(workspace).toBeVisible();
  await expect(workspace).toHaveAttribute("data-mobile-fullscreen", "true");
  const rect = await workspace.boundingBox();
  expect(rect).toMatchObject({ x: 0, y: 0, width: 400, height: 740 });
  await workspace.getByRole("tab", { name: "Versions" }).click();
  await workspace.getByRole("button", { name: "New version" }).click();
  const editor = page.getByRole("dialog", { name: "New secret version" });
  const value = editor.getByPlaceholder("secret value…");
  await value.fill("test-only-unsaved-value");
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect(value).toHaveValue("test-only-unsaved-value");
  await page.setViewportSize({ width: 320, height: 568 });
  await expect(value).toHaveValue("test-only-unsaved-value");
  await fits(page);
  const footer = editor.locator('[data-slot="dialog-footer"]');
  // Visual viewport resize events settle after setViewportSize returns.
  await expect
    .poll(async () => {
      const rect = await footer.boundingBox();
      return rect ? rect.y + rect.height : Number.POSITIVE_INFINITY;
    })
    .toBeLessThanOrEqual(568);
  await editor.getByRole("button", { name: "Dismiss dialog" }).click();
  const discard = page.getByRole("dialog", { name: "Discard changes?" });
  await expect(discard).toBeVisible();
  await discard.getByRole("button", { name: "Keep editing" }).click();
  await expect(value).toHaveValue("test-only-unsaved-value");
});

test("desktop resize releases an open navigation drawer", async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 800 });
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("button", { name: "Open navigation" }).click();
  await expect(page.getByRole("dialog", { name: "Primary navigation" })).toBeVisible();
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect(page.getByRole("dialog", { name: "Primary navigation" })).toBeHidden();
  await expect(page.locator('[data-slot="sheet-overlay"]')).toHaveCount(0);
  await page.getByRole("link", { name: "db_password" }).click();
  await expect(page.getByRole("dialog", { name: /prod\/gradethis\/db_password/ })).toBeVisible();
});

test("full-screen editor follows visual viewport changes without losing its draft", async ({
  page,
}) => {
  await page.setViewportSize({ width: 400, height: 800 });
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "New secret", exact: true }).first().click();
  const editor = page
    .getByRole("dialog")
    .filter({ has: page.locator("[data-modal-body]") })
    .first();
  await expect(editor).toBeVisible();
  await editor.getByPlaceholder("stripe-api-key").fill("mobile-keyboard-draft");
  // Simulate the geometry event delivered by a keyboard; this does not emulate an OS keyboard.
  await page.evaluate(() => {
    const viewport = window.visualViewport;
    if (!viewport) throw new Error("Visual viewport unavailable");
    Object.defineProperty(viewport, "height", { configurable: true, get: () => 460 });
    viewport.dispatchEvent(new Event("resize"));
  });
  await expect.poll(async () => (await editor.boundingBox())?.height).toBe(460);
  await expect(editor.getByPlaceholder("stripe-api-key")).toHaveValue("mobile-keyboard-draft");
  const footer = editor.locator('[data-slot="dialog-footer"]');
  await expect(footer).toBeVisible();
  const rect = await footer.boundingBox();
  expect((rect?.y ?? 0) + (rect?.height ?? 0)).toBeLessThanOrEqual(460);
});

test("mobile editor stays within the viewport when visual viewport measurements are stale", async ({
  page,
}) => {
  await page.setViewportSize({ width: 400, height: 800 });
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "New secret", exact: true }).first().click();
  const editor = page.getByRole("dialog", { name: "New secret", exact: true });
  await editor.getByPlaceholder("stripe-api-key").fill("resize-draft");
  // Reproduce WebKit returning the old height while the layout viewport has shrunk.
  await page.evaluate(() => {
    const viewport = window.visualViewport;
    if (!viewport) throw new Error("Visual viewport unavailable");
    Object.defineProperty(viewport, "height", { configurable: true, get: () => 800 });
  });
  await page.setViewportSize({ width: 320, height: 568 });
  await page.evaluate(() => window.visualViewport?.dispatchEvent(new Event("resize")));
  await expect.poll(async () => (await editor.boundingBox())?.height).toBe(568);
  const footer = editor.locator('[data-slot="dialog-footer"]');
  await expect
    .poll(async () => {
      const rect = await footer.boundingBox();
      return rect ? rect.y + rect.height : Number.POSITIVE_INFINITY;
    })
    .toBeLessThanOrEqual(568);
  await expect(editor.getByPlaceholder("stripe-api-key")).toHaveValue("resize-draft");
});
