import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("the secrets list offers the contract secrets an environment is missing", async ({
  page,
}) => {
  const state = incidentState();
  const app = state.application.name;
  const missing = state.application.contract
    .filter((field) => field.kind === "secret")
    .map((field) => field.alias);
  expect(missing.length).toBeGreaterThan(0);
  // Nothing behind any contract secret in prod: what a deleted or never-set
  // secret looks like to a release that needs it.
  for (const alias of missing) delete state.namespaces.prod.secrets[alias];
  await mockConsole(page, state);

  await page.goto(`/secrets?env=prod&app=${app}`);
  const panel = page.getByTestId("missing-contract-secrets");
  await expect(panel).toBeVisible();
  for (const alias of missing) await expect(panel.getByText(alias, { exact: true })).toBeVisible();

  const [first] = missing;
  await panel.getByRole("button", { name: `Add secret ${first}` }).click();
  const dialog = page.getByRole("dialog", { name: "New secret" });
  await expect(dialog.getByLabel("Secret key")).toHaveValue(first);
  await dialog.getByPlaceholder("secret value…").fill("a-new-value");
  await dialog.getByRole("button", { name: "Create secret" }).click();
  await expect(dialog).toBeHidden();

  // The list stays, the new secret is in it, and the panel no longer asks for it.
  await expect(page.getByRole("link", { name: first })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  if (missing.length === 1) await expect(panel).toBeHidden();
  else await expect(panel.getByText(first, { exact: true })).toBeHidden();
});

test("a missing secret's detail page offers to create it", async ({ page }) => {
  const state = incidentState();
  const app = state.application.name;
  await mockConsole(page, state);

  await page.goto(`/secrets/detail?env=prod&app=${app}&key=not-yet-set`);
  await page.getByRole("link", { name: "Create this secret" }).click();
  await expect(page).toHaveURL(/\/secrets\/new\?env=prod&app=.+&key=not-yet-set/);
  await expect(page.getByRole("textbox", { name: "Key" })).toHaveValue("not-yet-set");
});
