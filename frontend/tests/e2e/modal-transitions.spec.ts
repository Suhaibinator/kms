import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

for (const entry of ["list", "application"] as const) {
  for (const bound of [false, true]) {
    test(`secret creation from ${entry} keeps its width (${bound ? "bound" : "unbound"})`, async ({
      page,
      isMobile,
    }) => {
      if (!isMobile) await page.setViewportSize({ width: 1440, height: 1000 });
      await mockConsole(page, incidentState());
      await page.goto(
        entry === "list"
          ? "/secrets?env=prod&app=gradethis"
          : "/applications?app=gradethis&tab=matrix",
      );
      if (entry === "application") {
        await expect(page).toHaveURL(/schema_version=1/);
      }
      const url = page.url();
      await page
        .getByRole(entry === "list" ? "link" : "button", { name: "New secret", exact: true })
        .click();
      const create = page.getByRole("dialog", { name: "New secret", exact: true });
      await expect(create).toBeVisible();
      if (entry === "application") {
        await create.getByLabel("Environment", { exact: true }).click();
        await page.getByRole("option", { name: "prod", exact: true }).click();
      }
      await create.getByLabel("Secret key", { exact: true }).fill("modal-size-test");
      await create.getByPlaceholder("secret value…").fill("test-value");
      const width = isMobile ? page.viewportSize()?.width : 720;
      if (!width) throw new Error("Missing viewport width");
      // The manager is a workspace: it fills the viewport up to 1200px so its
      // versions table never scrolls sideways; the create/edit dialogs stay wide.
      const workspaceWidth = isMobile ? width : Math.min(1200, 1440 - 32);
      await expect.poll(async () => (await create.boundingBox())?.width).toBeCloseTo(width, 0);
      if (bound) {
        await create.getByText("Advanced options", { exact: true }).click();
        await create
          .getByRole("checkbox", { name: /Bind this version to an application key/ })
          .check();
        await create
          .getByLabel("Binding key", { exact: true })
          .fill("modal-binding-key-01234567890123456789");
        await expect.poll(async () => (await create.boundingBox())?.width).toBeCloseTo(width, 0);
      }
      await create.getByRole("button", { name: "Create secret", exact: true }).click();
      const manager = page.getByRole("dialog", { name: "/prod/gradethis/modal-size-test" });
      await expect(manager.getByRole("tab", { name: "Overview" })).toBeVisible();
      await expect
        .poll(async () => (await manager.boundingBox())?.width)
        .toBeCloseTo(workspaceWidth, 0);
      expect(page.url()).toBe(url);
      await manager.getByRole("tab", { name: "Versions" }).click();
      await expect(manager.getByRole("row").filter({ hasText: "v1" })).toBeVisible();
      // Same footprint on both tabs, and no sideways scroll inside the body.
      await expect
        .poll(async () => (await manager.boundingBox())?.width)
        .toBeCloseTo(workspaceWidth, 0);
      expect(
        await manager
          .locator("[data-modal-body]")
          .evaluate((element) => element.scrollWidth > element.clientWidth + 1),
      ).toBe(false);
      await manager.getByRole("button", { name: "New version", exact: true }).click();
      const editor = page.getByRole("dialog", { name: "New secret version" });
      await expect(editor).toBeVisible();
      await expect.poll(async () => (await editor.boundingBox())?.width).toBeCloseTo(width, 0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(
        false,
      );
    });
  }
}

test("parameter management and its version editor use the same width", async ({
  page,
  isMobile,
}) => {
  if (!isMobile) await page.setViewportSize({ width: 1440, height: 1000 });
  await mockConsole(page, incidentState());
  await page.goto("/parameters?env=prod&app=gradethis");
  await page.getByRole("link", { name: "rate_limits", exact: true }).click();
  const manager = page.getByRole("dialog", { name: "/prod/gradethis/rate_limits" });
  await expect(manager.getByRole("tab", { name: "Overview" })).toBeVisible();
  // The manager is a workspace (up to 1200px); its version editor stays wide (720px).
  if (!isMobile)
    await expect.poll(async () => (await manager.boundingBox())?.width).toBeCloseTo(1200, 0);
  const width = isMobile ? page.viewportSize()?.width : 720;
  if (!width) throw new Error("Missing viewport width");
  await manager.getByRole("button", { name: "New version", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "New parameter version" });
  await expect.poll(async () => (await editor.boundingBox())?.width).toBeCloseTo(width, 0);
});
