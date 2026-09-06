import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

for (const entry of ["list", "application"] as const) {
  for (const token of [false, true]) {
    test(`secret creation from ${entry} keeps its width${token ? " through token reveal" : ""}`, async ({
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
      await expect.poll(async () => (await create.boundingBox())?.width).toBeCloseTo(width, 0);
      if (token) {
        await create.getByText("Advanced options", { exact: true }).click();
        await create.getByRole("checkbox", { name: /Generate a per-secret access token/ }).check();
      }
      await create.getByRole("button", { name: "Create secret", exact: true }).click();
      if (token) {
        const reveal = page.getByRole("dialog", { name: "Save this access token now" });
        await expect(reveal).toBeVisible();
        await expect.poll(async () => (await reveal.boundingBox())?.width).toBeCloseTo(width, 0);
        await reveal.getByRole("button", { name: "I've saved it — manage secret" }).click();
      }
      const manager = page.getByRole("dialog", { name: "/prod/gradethis/modal-size-test" });
      await expect(manager.getByRole("tab", { name: "Overview" })).toBeVisible();
      await expect.poll(async () => (await manager.boundingBox())?.width).toBeCloseTo(width, 0);
      if (!isMobile) {
        // Simple resource details should not occupy the full desktop height.
        await expect
          .poll(async () => (await manager.boundingBox())?.height ?? 1000)
          .toBeLessThan(900);
      }
      expect(page.url()).toBe(url);
      await manager.getByRole("tab", { name: "Versions" }).click();
      await expect(manager.getByRole("row").filter({ hasText: "v1" })).toBeVisible();
      await expect.poll(async () => (await manager.boundingBox())?.width).toBeCloseTo(width, 0);
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
  if (!isMobile)
    await expect.poll(async () => (await manager.boundingBox())?.width).toBeCloseTo(720, 0);
  const width = isMobile ? page.viewportSize()?.width : 720;
  if (!width) throw new Error("Missing viewport width");
  const size = await manager.boundingBox();
  if (!size) throw new Error("Parameter dialog has no bounds");
  if (!isMobile) {
    expect(size.width).toBeCloseTo(720, 0);
    expect(size.height).toBeLessThan(900);
  }
  await manager.getByRole("button", { name: "New version", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "New parameter version" });
  await expect.poll(async () => (await editor.boundingBox())?.width).toBeCloseTo(width, 0);
});
