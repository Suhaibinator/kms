import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("manage a secret version in place", async ({ page }) => {
  const state = incidentState();
  await mockConsole(page, state);

  await page.goto("/secrets?env=prod&app=gradethis");
  const secretLink = page.getByRole("link", { name: "db_password" });
  await expect(secretLink).toHaveAttribute(
    "href",
    "/secrets/detail?env=prod&app=gradethis&key=db_password",
  );
  const backgroundUrl = page.url();

  // Ordinary activation opens the contextual workspace and keeps the list URL.
  await secretLink.click();
  const workspace = page.getByRole("dialog", { name: /prod\/gradethis\/db_password/ });
  await expect(workspace).toBeVisible();
  expect(page.url()).toBe(backgroundUrl);

  // Escape returns focus to the real link; keyboard activation opens the same workspace.
  await page.keyboard.press("Escape");
  await expect(workspace).toBeHidden();
  await expect(secretLink).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(workspace).toBeVisible();

  // The workspace itself fits the viewport; wide version data scrolls inside its table wrapper.
  const hasPageOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(hasPageOverflow).toBe(false);

  await workspace.getByRole("tab", { name: "Versions" }).click();
  await workspace.getByRole("button", { name: "New version" }).click();
  const versionDialog = page.getByRole("dialog", { name: "New secret version" });
  await versionDialog.getByPlaceholder("secret value…").fill("rotated-value");
  await versionDialog.getByText("Advanced options", { exact: true }).click();
  await versionDialog.getByLabel("Expires at").fill("2030-01-02T03:04");
  await versionDialog.getByRole("button", { name: "Save new version" }).click();
  await expect(versionDialog).not.toBeVisible();

  const createCall = state.log.find(
    (entry) => entry.method === "POST" && entry.path === "/secrets",
  );
  expect(createCall).toBeDefined();
  const createBody = createCall?.body as { expires_at_unix_ms: number };
  expect(createBody).toMatchObject({
    env: "prod",
    app: "gradethis",
    key: "db_password",
    value_base64: "cm90YXRlZC12YWx1ZQ==",
  });
  expect(createBody.expires_at_unix_ms).toBeGreaterThan(0);

  const versionTwo = workspace.getByRole("row").filter({ hasText: "v2" });
  await expect(versionTwo).toContainText("current");

  // A lifecycle mutation refreshes the workspace in place.
  await versionTwo.getByRole("button", { name: "Disable" }).click();
  const disableDialog = page.getByRole("dialog", { name: "Disable version?" });
  await disableDialog.getByRole("button", { name: "Disable" }).click();
  await expect(versionTwo).toContainText("disabled");
  expect(page.url()).toBe(backgroundUrl);
});

/**
 * The two workspace toolbars share one definition, and the parts of it that
 * are easy to break silently are the ones a screenshot would not catch: the
 * sticky inset has to bleed with the negative margin (or a strip of page ground
 * opens between the dialog's header rule and the toolbar), and the bleed has to
 * reach the dialog's own edges on both sides.
 */
for (const surface of [
  {
    name: "secret",
    open: async (page: import("@playwright/test").Page) => {
      await page.goto("/secrets?env=prod&app=gradethis");
      await page.getByRole("link", { name: "db_password" }).click();
      return page.getByRole("dialog", { name: /prod\/gradethis\/db_password/ });
    },
    toolbar: ".secret-workspace-toolbar",
  },
  {
    name: "release",
    open: async (page: import("@playwright/test").Page) => {
      await page.goto("/releases?env=prod&app=gradethis");
      await page.getByRole("button", { name: "View" }).first().click();
      return page.getByRole("dialog", { name: /^Release / });
    },
    toolbar: ".release-workspace-toolbar",
  },
]) {
  test(`the ${surface.name} workspace toolbar meets the dialog header and both edges`, async ({
    page,
  }) => {
    test.skip((page.viewportSize()?.width ?? 1280) < 640, "sticky, full-bleed toolbar is ≥640px");
    await page.setViewportSize({ width: 1280, height: 900 });
    await mockConsole(page, incidentState());
    const dialog = await surface.open(page);
    await expect(dialog).toBeVisible();
    await dialog.evaluate((element) =>
      Promise.all(element.getAnimations({ subtree: true }).map((animation) => animation.finished)),
    );

    const geometry = await dialog.evaluate((popup, selector) => {
      const toolbar = popup.querySelector(selector) as HTMLElement;
      const header = popup.querySelector('[data-slot="dialog-header"]') as HTMLElement;
      const body = popup.querySelector("[data-modal-body]") as HTMLElement;
      const style = getComputedStyle(toolbar);
      const box = toolbar.getBoundingClientRect();
      const bodyBox = body.getBoundingClientRect();
      // The body reserves a scrollbar gutter (`scrollbar-gutter: stable`). On
      // platforms with classic scrollbars (Linux CI, Windows) that gutter is
      // 15px of the border box that no content can occupy, so the bleed is
      // measured against the padding box, which `clientWidth` gives without it.
      const paddingRight = bodyBox.left + body.clientLeft + body.clientWidth;
      return {
        band: box.top - header.getBoundingClientRect().bottom,
        left: box.left - bodyBox.left,
        right: paddingRight - box.right,
        position: style.position,
        opaque: style.backgroundColor,
        rule: style.borderBottomWidth,
      };
    }, surface.toolbar);

    // No strip of the page's own ground between the header rule and the toolbar.
    expect(Math.abs(geometry.band)).toBeLessThanOrEqual(1);
    // Bled to the body's padding edges on both sides.
    expect(Math.abs(geometry.left)).toBeLessThanOrEqual(1);
    expect(Math.abs(geometry.right)).toBeLessThanOrEqual(1);
    expect(geometry.position).toBe("sticky");
    expect(geometry.rule).toBe("1px");
    // An opaque ground: a sticky toolbar over transparent paint shows the rows
    // scrolling under it.
    expect(geometry.opaque).not.toContain("rgba(0, 0, 0, 0)");

    // It stays put while the body scrolls, which is the whole point of pinning it.
    const body = dialog.locator("[data-modal-body]");
    const before = await dialog.locator(surface.toolbar).boundingBox();
    await body.evaluate((element) => {
      element.scrollTop = 300;
    });
    const after = await dialog.locator(surface.toolbar).boundingBox();
    expect(after?.y).toBeCloseTo(before?.y ?? 0, 0);
  });
}

test("below 640px the workspace toolbar scrolls with the body but keeps its bleed", async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 667 });
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "db_password" }).click();
  const dialog = page.getByRole("dialog", { name: /prod\/gradethis\/db_password/ });
  await expect(dialog).toBeVisible();

  const geometry = await dialog.evaluate((popup) => {
    const toolbar = popup.querySelector(".secret-workspace-toolbar") as HTMLElement;
    const body = popup.querySelector("[data-modal-body]") as HTMLElement;
    const box = toolbar.getBoundingClientRect();
    const bodyBox = body.getBoundingClientRect();
    // Padding box, not border box: see the desktop test for the scrollbar gutter.
    const paddingRight = bodyBox.left + body.clientLeft + body.clientWidth;
    const rows = Array.from(toolbar.children, (child) => child.getBoundingClientRect().height);
    return {
      position: getComputedStyle(toolbar).position,
      left: box.left - bodyBox.left,
      right: paddingRight - box.right,
      height: box.height,
      content: rows.reduce((sum, height) => sum + height, 0),
    };
  });
  // Pinning 130px of a 667px dialog costs more than it buys on a phone.
  expect(geometry.position).toBe("static");
  expect(Math.abs(geometry.left)).toBeLessThanOrEqual(1);
  expect(Math.abs(geometry.right)).toBeLessThanOrEqual(1);
  // The toolbar is exactly its rows plus 12px + 8px of padding and one 16px
  // gap: no reserved band. An absolute cap would encode the font, and CI's
  // Linux fallback face wraps the three action buttons onto a second row.
  expect(geometry.height).toBeLessThanOrEqual(geometry.content + 36 + 1);
});
