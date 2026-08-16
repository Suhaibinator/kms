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

  const drawer = page.getByRole("dialog", { name: "Primary navigation" });
  const shell = page.locator(".app-shell");
  await expect(drawer).toHaveCount(0);

  await toggle.click();
  await expect(drawer).toBeVisible();

  // The drawer is a modal portalled outside the shell, so the whole shell —
  // topbar, desktop sidebar, and main — leaves the accessibility tree while it
  // is open. Asserting the shell is hidden rather than that `main` is inert
  // keeps this tied to the behaviour instead of the drawer's implementation.
  await expect(shell).toHaveAttribute("aria-hidden", "true");
  // The desktop sidebar renders the same links, so a single reachable
  // "Overview" link proves the background really is isolated.
  await expect(page.getByRole("link", { name: "Overview" })).toHaveCount(1);
  await expect(drawer.getByRole("link", { name: "Overview" })).toBeVisible();

  // Focus is pulled into the drawer rather than left behind on the trigger.
  await expect(drawer.locator(":focus")).toHaveCount(1);

  // A non-admin client never sees admin-only destinations.
  await expect(page.getByRole("link", { name: "Policies" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Identities" })).toHaveCount(0);

  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);
  await expect(toggle).toBeFocused();
  await expect(shell).not.toHaveAttribute("aria-hidden", "true");
});
