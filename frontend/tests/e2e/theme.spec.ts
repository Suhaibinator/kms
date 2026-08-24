import { expect, type Page, test } from "@playwright/test";

const DARK_BG = "rgb(11, 14, 20)"; // --bg on .dark
const LIGHT_BG = "rgb(244, 246, 249)"; // --bg on :root

// The stylesheet is a separate chunk on the dev server, so under a loaded
// suite the class can be on <html> a beat before the tokens apply. Polling the
// computed colour keeps the assertion about what renders without a timing bet;
// the "before first paint" claim is carried by the class check, which runs
// against the boot script's synchronous output.
const bodyBackground = (page: Page) =>
  expect.poll(() => page.evaluate(() => getComputedStyle(document.body).backgroundColor));

// Playwright's default colour scheme is light, so "system" resolves to light.
test("a stored preference is applied before the first paint", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("kms-theme", "dark"));
  await page.goto("/login");
  await expect(page.locator("html")).toHaveClass(/\bdark\b/);
  await bodyBackground(page).toBe(DARK_BG);
});

test("the switch persists across reloads and can return to the OS setting", async ({ page }) => {
  await page.goto("/login");
  await expect(page.locator("html")).not.toHaveClass(/\bdark\b/);
  await bodyBackground(page).toBe(LIGHT_BG);

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

// The typed-chip tokens are defined on both :root and .dark (the unit guard
// checks parity); this checks the dark set actually wins once `.dark` is set,
// i.e. the chips do not keep their light ink on the dark ground.
test("ident chip tokens re-point in dark mode", async ({ page }) => {
  await page.goto("/login");
  const read = () =>
    page.evaluate(() => {
      const style = getComputedStyle(document.documentElement);
      return ["app", "env", "alias", "release", "identity"].map((kind) => [
        style.getPropertyValue(`--ident-${kind}`).trim(),
        style.getPropertyValue(`--ident-${kind}-soft`).trim(),
      ]);
    });
  const light = await read();
  for (const [fg, soft] of light) {
    expect(fg).not.toBe("");
    expect(soft).not.toBe("");
  }
  await page.getByTitle("Dark").click();
  await expect(page.locator("html")).toHaveClass(/\bdark\b/);
  const dark = await read();
  for (let i = 0; i < light.length; i += 1) {
    expect(dark[i]?.[0]).not.toBe(light[i]?.[0]);
    expect(dark[i]?.[1]).not.toBe(light[i]?.[1]);
  }
});
