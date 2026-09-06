import { expect, test } from "@playwright/test";
import { mockMobileConsole } from "./fakes/mobile-console";

const routes = [
  "/",
  "/applications",
  "/applications?app=gradethis&env=prod",
  "/namespaces",
  "/secrets?app=gradethis&env=prod",
  "/secrets/new?app=gradethis&env=prod",
  "/secrets/detail?app=gradethis&env=prod&key=db_password",
  "/parameters?app=gradethis&env=prod",
  "/parameters/detail?app=gradethis&env=prod&key=rate_limits",
  "/releases?app=gradethis&env=prod",
  "/policies",
  "/identities",
  "/subscribers",
  "/audit",
  "/posture",
  "/health",
];

for (const width of [320, 400, 768, 1280]) {
  for (const empty of [false, true]) {
    test(`console route coverage at ${width}px, ${empty ? "empty" : "populated"}`, async ({
      page,
    }) => {
      test.setTimeout(90000);
      await page.setViewportSize({ width, height: 900 });
      await mockMobileConsole(page, empty);
      for (const route of routes) {
        await test.step(route, async () => {
          await page.goto(route);
          await expect(page.locator("h1").first()).toBeVisible();
          await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
          await expect(page.getByText("Something went wrong", { exact: true })).toHaveCount(0);
          const overflow = await page.evaluate(() => {
            const viewport = document.documentElement.clientWidth;
            if (document.documentElement.scrollWidth <= viewport) return [];
            return [...document.querySelectorAll("main *")]
              .filter((el) => el.getBoundingClientRect().right > viewport + 1)
              .slice(0, 12)
              .map((el) => `${el.tagName}.${el.className}`);
          });
          expect(overflow, `${route} at ${width}px`).toEqual([]);
          if (width <= 640) {
            await expect(page.locator(".card-table thead").first()).toBeHidden();
          }
        });
      }
    });
  }
}

test("failed and loading secret requests keep compact filters and recover", async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 800 });
  await mockMobileConsole(page);
  let releaseRequest: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    releaseRequest = resolve;
  });
  await page.route("**/api/v1/secrets?*", async (route) => {
    await gate;
    await route.fulfill({ status: 500, json: { error: "Test failure" } });
  });
  await page.goto("/secrets?app=gradethis&env=prod");
  await expect(page.locator('[aria-busy="true"]')).toBeVisible();
  const filters = await page.locator(".filters").boundingBox();
  expect(filters?.height).toBeLessThan(400);
  releaseRequest();
  await expect(page.getByText("Failed to load secrets", { exact: true })).toBeVisible();
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  await page.unroute("**/api/v1/secrets?*");
  await page.reload();
  await expect(page.getByRole("link", { name: "db_password" })).toBeVisible();
});
