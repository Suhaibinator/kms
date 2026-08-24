import { expect, test } from "@playwright/test";

// Playwright's default colour scheme is light, so "system" resolves to light.
test("a stored preference is applied before the first paint", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("kms-theme", "dark"));
  await page.goto("/login");
  await expect(page.locator("html")).toHaveClass(/\bdark\b/);
  expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe(
    "rgb(11, 14, 20)",
  );
});

test("the switch persists across reloads and can return to the OS setting", async ({ page }) => {
  await page.goto("/login");
  await expect(page.locator("html")).not.toHaveClass(/\bdark\b/);
  expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe(
    "rgb(244, 246, 249)",
  );

  await page.getByTitle("Dark").click();
  await expect(page.locator("html")).toHaveClass(/\bdark\b/);
  await page.reload();
  await expect(page.locator("html")).toHaveClass(/\bdark\b/);
  await expect(page.getByRole("radio", { name: "Dark" })).toBeChecked();

  await page.getByTitle("Match system").click();
  await expect(page.locator("html")).not.toHaveClass(/\bdark\b/);
  expect(await page.evaluate(() => localStorage.getItem("kms-theme"))).toBeNull();
});

test("the OS preference is honoured when nothing is stored", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto("/login");
  await expect(page.locator("html")).toHaveClass(/\bdark\b/);
  await context.close();
});
