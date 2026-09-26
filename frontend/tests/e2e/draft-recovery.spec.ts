import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("browser Back keeps an unsaved modal and restores the URL when cancelled", async ({
  page,
  isMobile,
}) => {
  test.skip(isMobile, "Desktop navigation journey; touch dismissal is covered separately.");
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page
    .getByRole("navigation", { name: "Primary navigation" })
    .getByRole("link", { name: "Parameters", exact: true })
    .click();
  await expect(page).toHaveURL(/\/parameters/);
  const originalUrl = page.url();
  await page.getByRole("button", { name: "New parameter", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "New parameter", exact: true });
  await editor.getByLabel("Key", { exact: true }).fill("keep-my-draft");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(originalUrl);
  await expect(editor.getByLabel("Key", { exact: true })).toHaveValue("keep-my-draft");
  page.once("dialog", (dialog) => dialog.accept());
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(/\/secrets/);
});

test("session expiry keeps the actual editor draft through in-place sign-in", async ({ page }) => {
  const state = incidentState();
  await mockConsole(page, state);
  await page.route("**/api/v1/auth/login", (route) =>
    route.fulfill({ json: { identity: state.identity, auth_method: "token" } }),
  );
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "New secret", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "New secret", exact: true });
  await editor.getByLabel("Secret key", { exact: true }).fill("recover-me");
  await editor.getByPlaceholder("secret value…").fill("memory-only-test-value");
  await page.evaluate(() => {
    sessionStorage.removeItem("kms_token");
    window.dispatchEvent(new Event("kms:unauthorized"));
  });
  const recovery = page.getByRole("dialog", { name: "Sign in to resume your draft" });
  await expect(recovery).toBeVisible();
  await recovery.getByLabel(`Token for ${state.identity.name}`).fill("renewed-test-token");
  await recovery.getByRole("button", { name: "Resume editing" }).click();
  await expect(recovery).toBeHidden();
  await expect(editor.getByLabel("Secret key", { exact: true })).toHaveValue("recover-me");
  await expect(editor.getByPlaceholder("secret value…")).toHaveValue("memory-only-test-value");
  await expect(page).toHaveURL(/\/secrets\?/);
});

test("full-page secret Cancel protects a draft", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.goto("/secrets/new?env=prod&app=gradethis");
  await page.getByLabel("Key", { exact: true }).fill("full-page-draft");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page.getByRole("link", { name: "Cancel", exact: true }).click();
  await expect(page).toHaveURL(/\/secrets\/new/);
  await expect(page.getByLabel("Key", { exact: true })).toHaveValue("full-page-draft");
});

test("a create request rejected with 401 retains the pending secret draft", async ({ page }) => {
  const state = incidentState();
  await mockConsole(page, state);
  await page.route("**/api/v1/auth/login", (route) =>
    route.fulfill({ json: { identity: state.identity } }),
  );
  await page.route("**/api/v1/secrets", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    await route.fulfill({
      status: 401,
      json: { error: { code: "unauthenticated", message: "Session expired" } },
    });
  });
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "New secret", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "New secret", exact: true });
  await editor.getByLabel("Secret key", { exact: true }).fill("retry-after-auth");
  await editor.getByPlaceholder("secret value…").fill("memory-only-pending-value");
  await editor.getByRole("button", { name: "Create secret", exact: true }).click();
  const recovery = page.getByRole("dialog", { name: "Sign in to resume your draft" });
  await expect(recovery).toBeVisible();
  await recovery.getByLabel(`Token for ${state.identity.name}`).fill("renewed-test-token");
  await recovery.getByRole("button", { name: "Resume editing" }).click();
  await expect(recovery).toBeHidden();
  await expect(editor.getByLabel("Secret key", { exact: true })).toHaveValue("retry-after-auth");
  await expect(editor.getByPlaceholder("secret value…")).toHaveValue("memory-only-pending-value");
});

test("search can be dismissed using its visible Close button", async ({ page, isMobile }) => {
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  if (isMobile) await page.getByRole("button", { name: "Open navigation" }).click();
  await page
    .getByRole("button", { name: /Search/ })
    .first()
    .click();
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  await palette.getByRole("button", { name: /Close/ }).click();
  await expect(palette).toBeHidden();
});
