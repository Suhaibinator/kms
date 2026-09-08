import { expect, type Page, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

/** Build same-page history entries without a full document reload. */
async function navigate(page: Page, path: string) {
  await page.evaluate(async (destination) => {
    const router = (
      window as unknown as { next: { router: { push: (path: string) => Promise<boolean> } } }
    ).next.router;
    await router.push(destination);
  }, path);
}

test("history jumps close parameter drafts before the selected key changes", async ({ page }) => {
  const state = incidentState();
  state.namespaces.prod.parameters.alpha = {
    key: "alpha",
    content_type: "string",
    versions: ["original alpha"],
  };
  state.namespaces.prod.parameters.beta = {
    key: "beta",
    content_type: "string",
    versions: ["original beta"],
  };
  await mockConsole(page, state);
  await page.goto("/parameters/detail?env=prod&app=gradethis&key=alpha");
  await expect(page.getByRole("button", { name: "New version", exact: true })).toBeVisible();
  await navigate(page, "/parameters?env=prod&app=gradethis");
  await expect(page.getByRole("heading", { name: "Parameters", exact: true })).toBeVisible();
  await navigate(page, "/parameters/detail?env=prod&app=gradethis&key=beta");
  await page.getByRole("button", { name: "New version", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "New parameter version" });
  await dialog.getByLabel("Value", { exact: true }).fill("draft for beta");

  // Equivalent to selecting alpha directly from the browser's history menu.
  await page.evaluate(() => history.go(-2));
  await expect(page).toHaveURL(/key=alpha/);
  await expect(dialog).toBeHidden();
  await page.getByRole("button", { name: "New version", exact: true }).click();
  await expect(dialog.getByLabel("Value", { exact: true })).not.toHaveValue("draft for beta");
  expect(state.namespaces.prod.parameters.alpha.versions).toEqual(["original alpha"]);
  expect(state.namespaces.prod.parameters.beta.versions).toEqual(["original beta"]);
});

test("history jumps close secret confirmations before they can act on another key", async ({
  page,
}) => {
  const state = incidentState();
  state.namespaces.prod.secrets.alpha = { key: "alpha", versionCount: 1, bound: false };
  state.namespaces.prod.secrets.beta = { key: "beta", versionCount: 1, bound: false };
  await mockConsole(page, state);
  await page.goto("/secrets/detail?env=prod&app=gradethis&key=alpha");
  await expect(page.getByRole("button", { name: "New version", exact: true })).toBeVisible();
  await navigate(page, "/secrets?env=prod&app=gradethis");
  await expect(page.getByRole("heading", { name: "Secrets", exact: true })).toBeVisible();
  await navigate(page, "/secrets/detail?env=prod&app=gradethis&key=beta");
  await page.getByRole("button", { name: "Disable", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Disable version?" });
  await expect(dialog).toBeVisible();
  await page.evaluate(() => history.go(-2));
  await expect(page).toHaveURL(/key=alpha/);
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Disable", exact: true })).toBeVisible();
  expect(
    state.log.filter((entry) => entry.method === "POST" && entry.path === "/secrets/disable"),
  ).toEqual([]);
});
