import { expect, test } from "@playwright/test";

test("login exposes a labelled identity-token form with the intended font", async ({ page }) => {
  await page.goto("/login");

  await expect(page.getByRole("heading", { name: "KMS Console", level: 1 })).toBeVisible();
  await expect(page.getByLabel("Identity token")).toHaveAttribute("type", "password");
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();

  const fontFamily = await page
    .getByRole("heading", { name: "KMS Console", level: 1 })
    .evaluate((heading) => getComputedStyle(heading).fontFamily);
  expect(fontFamily.toLowerCase()).not.toContain("times");
});

test("mobile navigation is isolated, focus-managed, and capability-aware", async ({
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "mobile-chromium", "mobile-only drawer behavior");

  await page.addInitScript(() => {
    sessionStorage.setItem("kms_token", "kms_e2e_token");
  });
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const payload = path.endsWith("/whoami")
      ? { name: "e2e-client", kind: "client", namespace: { env: "prod", app: "billing" } }
      : path.endsWith("/health")
        ? { healthy: true, ready: true, version: "e2e", current_revision: 0 }
        : path.endsWith("/namespaces")
          ? { namespaces: [], next_page_token: "" }
          : path.endsWith("/subscribers")
            ? { subscribers: [], current_revision: 0 }
            : path.endsWith("/audit")
              ? { events: [], next_page_token: "" }
              : {};
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });

  await page.goto("/");
  const toggle = page.getByRole("button", { name: "Open navigation" });
  await expect(toggle).toBeVisible();

  await toggle.click();
  const sidebar = page.locator("#app-sidebar");
  await expect(sidebar).not.toHaveAttribute("aria-hidden", "true");
  await expect(page.locator("main")).toHaveAttribute("inert", "");
  await expect(page.getByRole("link", { name: "Policies" })).toHaveCount(0);

  await page.keyboard.press("Escape");
  await expect(sidebar).toHaveAttribute("aria-hidden", "true");
  await expect(toggle).toBeFocused();
  await expect(page.locator("main")).not.toHaveAttribute("inert");
});
