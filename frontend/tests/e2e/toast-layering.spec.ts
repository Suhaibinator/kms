// The console raises a toast on nearly every mutation, and sonner's viewport is
// a page-level layer at z-index 999999999 pinned to the top-right corner — the
// same corner that holds every dialog's ✕ and, on a phone, the drawer trigger.
// These are hit tests rather than screenshots because the defect was precisely
// that: elementFromPoint at the ✕ returned the toast, for the full eight
// seconds an error toast lasts.
import { expect, type Page, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

/**
 * Removes the clipboard so CopyButton takes its error path. That raises a real
 * toast through the app's own ToastProvider, with the error duration, which is
 * the case the collision was reported on.
 */
async function blockClipboard(page: Page) {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    document.execCommand = () => false;
  });
}

/** What the user's finger or pointer would actually reach at `selector`'s centre. */
function hitAtCentreOf(page: Page, selector: string) {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) return "missing";
    const box = el.getBoundingClientRect();
    const hit = document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2);
    if (!hit) return "nothing";
    if (hit === el || hit.closest(sel) === el) return "self";
    return hit.closest("[data-sonner-toaster]") ? "toast" : hit.tagName;
  }, selector);
}

/** The toast box and the boxes it must stay clear of, in one read. */
function layerBoxes(page: Page) {
  return page.evaluate(() => {
    const rect = (selector: string) => {
      const el = document.querySelector(selector);
      if (!el) return null;
      const box = el.getBoundingClientRect();
      return { top: box.top, bottom: box.bottom, left: box.left, right: box.right };
    };
    return {
      toast: rect("[data-sonner-toast]"),
      close: rect('[data-slot="dialog-close"]'),
      title: rect('[data-slot="dialog-title"]'),
      bar: rect(".mobile-topbar"),
      trigger: rect('[data-slot="sheet-trigger"]'),
    };
  });
}

/** Opens the release workspace and makes its Copy digest button fail. */
async function releaseWorkspaceWithToast(page: Page) {
  await mockConsole(page, incidentState());
  await page.goto("/releases?app=gradethis&env=prod");
  await page.getByRole("button", { name: "View" }).first().click();
  const dialog = page.getByRole("dialog").first();
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: /Copy digest/ }).click();
  const toast = page.locator("[data-sonner-toast]").first();
  await expect(toast).toBeVisible();
  // sonner slides the toast in; measuring before that settles reads a box the
  // user never sees.
  await toast.evaluate((el) =>
    Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished)),
  );
  return dialog;
}

test("a toast never covers an open dialog's title or close button", async ({ page }) => {
  await blockClipboard(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await releaseWorkspaceWithToast(page);

  expect(await hitAtCentreOf(page, '[data-slot="dialog-close"]')).toBe("self");

  const { toast, close, title } = await layerBoxes(page);
  if (!toast || !close || !title) throw new Error("toast or dialog chrome missing");
  // The whole header band, not only the two boxes: the toast starts below the
  // lower of the ✕ and the title, so neither is even partly obscured.
  expect(toast.top).toBeGreaterThanOrEqual(Math.max(close.bottom, title.bottom));
});

test("a toast never covers the mobile drawer trigger", async ({ page }) => {
  await blockClipboard(page);
  await page.setViewportSize({ width: 375, height: 700 });
  // A page with no dialog, so the sticky top bar is the thing under the toast:
  // below 768 the drawer trigger is the only way to reach navigation at all.
  await mockConsole(page, incidentState());
  await page.goto("/parameters/detail?env=prod&app=gradethis&key=database");
  await page
    .getByRole("button", { name: /Copy value/ })
    .first()
    .click();
  const toast = page.locator("[data-sonner-toast]").first();
  await expect(toast).toBeVisible();
  await toast.evaluate((el) =>
    Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished)),
  );

  expect(await hitAtCentreOf(page, '[data-slot="sheet-trigger"]')).toBe("self");

  const { toast: toastBox, bar } = await layerBoxes(page);
  if (!toastBox || !bar) throw new Error("toast or top bar missing");
  expect(toastBox.top).toBeGreaterThanOrEqual(bar.bottom);
});
