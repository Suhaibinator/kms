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
