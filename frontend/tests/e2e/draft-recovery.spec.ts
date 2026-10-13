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
  const prompt = page.getByRole("dialog", { name: "Discard unsaved changes?" });
  await page.evaluate(() => history.back());
  await expect(prompt).toBeVisible();
  // Nested in the editor, so focus moves into the prompt rather than staying
  // trapped in the editor beneath it.
  await expect(prompt.getByRole("button", { name: "Keep editing" })).toBeFocused();
  // The URL is restored while the prompt is open, not only once it is answered.
  await expect(page).toHaveURL(originalUrl);
  // Clicking inside the prompt must not count as an outside press on the
  // editor beneath it, which would open the editor's own discard prompt.
  await prompt.getByRole("button", { name: "Keep editing" }).click();
  await expect(prompt).toBeHidden();
  await expect(page.getByRole("dialog", { name: "Discard changes?" })).toHaveCount(0);
  await expect(page).toHaveURL(originalUrl);
  await expect(editor.getByLabel("Key", { exact: true })).toHaveValue("keep-my-draft");

  // Escape answers only the prompt, never the editor under it.
  await page.evaluate(() => history.back());
  await expect(prompt.getByRole("button", { name: "Keep editing" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(prompt).toBeHidden();
  await expect(editor.getByLabel("Key", { exact: true })).toHaveValue("keep-my-draft");
  await expect(page).toHaveURL(originalUrl);

  await page.evaluate(() => history.back());
  await prompt.getByRole("button", { name: "Discard and leave" }).click();
  await expect(page).toHaveURL(/\/secrets/);
  await expect(editor).toBeHidden();
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

test("signing out from session recovery asks above the recovery dialog", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.goto("/secrets?env=prod&app=gradethis");
  await page.getByRole("link", { name: "New secret", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "New secret", exact: true });
  await editor.getByLabel("Secret key", { exact: true }).fill("recover-me");
  await page.evaluate(() => {
    sessionStorage.removeItem("kms_token");
    window.dispatchEvent(new Event("kms:unauthorized"));
  });
  const recovery = page.getByRole("dialog", { name: "Sign in to resume your draft" });
  await recovery.getByRole("button", { name: "Discard draft and sign out" }).click();
  const prompt = page.getByRole("dialog", { name: "Discard unsaved changes?" });
  await expect(prompt.getByRole("button", { name: "Keep editing" })).toBeFocused();
  await prompt.getByRole("button", { name: "Keep editing" }).click();
  await expect(prompt).toBeHidden();
  await expect(recovery).toBeVisible();
  await expect(editor.getByLabel("Secret key", { exact: true })).toHaveValue("recover-me");

  await recovery.getByRole("button", { name: "Discard draft and sign out" }).click();
  await prompt.getByRole("button", { name: "Discard and leave" }).click();
  await expect(page).toHaveURL(/\/login$/);
});

test("full-page secret Cancel protects a draft", async ({ page }) => {
  await mockConsole(page, incidentState());
  await page.goto("/secrets/new?env=prod&app=gradethis");
  await page.getByLabel("Key", { exact: true }).fill("full-page-draft");
  await page.getByRole("link", { name: "Cancel", exact: true }).click();
  const prompt = page.getByRole("dialog", { name: "Discard unsaved changes?" });
  await prompt.getByRole("button", { name: "Keep editing" }).click();
  await expect(prompt).toBeHidden();
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

test("creating a release goes straight to it without a discard prompt", async ({ page }) => {
  const state = incidentState();
  await mockConsole(page, state);
  const created = { ...state.namespaces.prod.releases[0], version: 99 };
  await page.route("**/api/v1/releases", (route) =>
    route.request().method() === "POST"
      ? route.fulfill({ json: { release: created } })
      : route.fallback(),
  );
  const nativeDialogs: string[] = [];
  page.on("dialog", (dialog) => {
    nativeDialogs.push(dialog.message());
    void dialog.dismiss();
  });
  await page.goto("/releases?app=gradethis&env=prod");
  await page.getByRole("button", { name: "New release" }).first().click();
  const builder = page.getByRole("dialog", { name: "New release · prod/gradethis" });
  await expect(builder.getByRole("textbox", { name: "Release name" })).toHaveValue("runtime");
  await builder.getByRole("button", { name: "Create release" }).click();
  await expect(builder).toBeHidden();
  await expect(page).toHaveURL(new RegExp(`name=${created.name}`));
  await expect(page.getByRole("dialog", { name: "Discard unsaved changes?" })).toHaveCount(0);
  expect(nativeDialogs).toEqual([]);
});
