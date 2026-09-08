import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("manage parameter versions without leaving the filtered list", async ({ page }) => {
  const state = incidentState();
  state.namespaces.prod.parameters.retries = {
    key: "retries",
    content_type: "integer",
    versions: ["2", "3"],
    metadataJson: '{"owner":"billing"}',
  };
  await mockConsole(page, state);
  await page.goto("/parameters?env=prod&app=gradethis&prefix=retries");
  const link = page.getByRole("link", { name: "retries", exact: true });
  await expect(link).toHaveAttribute(
    "href",
    "/parameters/detail?env=prod&app=gradethis&key=retries",
  );
  const url = page.url();
  await link.scrollIntoViewIfNeeded();
  await expect(link).toBeInViewport();
  await link.click();
  const workspace = page.getByRole("dialog", { name: "/prod/gradethis/retries" });
  await expect(workspace.getByRole("button", { name: "New version" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(workspace).toBeHidden();
  await expect(link).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(workspace).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
  await workspace.getByRole("button", { name: "New version" }).click();
  const editor = page.getByRole("dialog", { name: "New parameter version" });
  await editor.getByRole("textbox", { name: "Value", exact: true }).fill("4");
  await page.keyboard.press("Escape");
  const discard = page.getByRole("dialog", { name: "Discard changes?" });
  await expect(discard).toBeVisible();
  await discard.getByRole("button", { name: "Discard", exact: true }).click();
  await expect(editor).toBeHidden();
  await expect(workspace).toBeVisible();
  await workspace.getByRole("button", { name: "New version" }).click();
  await editor.getByRole("textbox", { name: "Value", exact: true }).fill("4");
  await editor.getByRole("button", { name: "Save new version" }).click();
  await expect(editor).toBeHidden();
  expect(state.namespaces.prod.parameters.retries.versions).toEqual(["2", "3", "4"]);
  expect(JSON.parse(state.namespaces.prod.parameters.retries.metadataJson ?? "{}")).toEqual({
    owner: "billing",
  });
  await workspace.getByRole("tab", { name: "Versions" }).click();
  await workspace
    .getByRole("row")
    .filter({ hasText: "v1" })
    .getByRole("button", { name: "View value" })
    .click();
  await workspace.getByRole("button", { name: "Compare with current" }).click();
  await expect(workspace.getByRole("button", { name: "Close compare" })).toBeVisible();
  await workspace.getByRole("button", { name: "Restore v1", exact: true }).first().click();
  await expect(editor.getByRole("textbox", { name: "Value", exact: true })).toHaveValue("2");
  await editor.getByRole("button", { name: "Save new version" }).click();
  await expect(editor).toBeHidden();
  expect(state.namespaces.prod.parameters.retries.versions).toEqual(["2", "3", "4", "2"]);
  expect(page.url()).toBe(url);
  await workspace.getByRole("button", { name: "Delete", exact: true }).click();
  await page
    .getByRole("dialog", { name: "Delete parameter?" })
    .getByRole("button", { name: "Delete parameter" })
    .click();
  await expect(workspace).toBeHidden();
  await expect(link).toHaveCount(0);
  expect(page.url()).toBe(url);
});

test("application parameter cells open a workspace and retain real detail links", async ({
  page,
}) => {
  const state = incidentState();
  await mockConsole(page, state);
  await page.goto("/applications?app=gradethis&tab=matrix");
  await expect(page).toHaveURL(/schema_version=1/);
  const link = page.getByRole("link", { name: "Open rate_limits in prod" }).first();
  await expect(link).toBeVisible();
  const href = await link.getAttribute("href");
  expect(href).toContain("/parameters/detail?");
  const url = page.url();
  await link.scrollIntoViewIfNeeded();
  await expect(link).toBeInViewport();
  await link.click();
  const workspace = page
    .getByRole("dialog")
    .filter({ has: page.getByRole("tab", { name: "Overview" }) });
  await expect(workspace.getByRole("button", { name: "New version" })).toBeVisible();
  expect(page.url()).toBe(url);
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false);
  await workspace.getByRole("button", { name: "New version" }).click();
  const editor = page.getByRole("dialog", { name: "New parameter version" });
  await editor.getByRole("textbox", { name: "Value", exact: true }).fill("987");
  await editor.getByRole("button", { name: "Save new version" }).click();
  await expect(editor).toBeHidden();
  await expect(workspace).toBeVisible();
  expect(page.url()).toBe(url);
  await page.keyboard.press("Escape");
  await expect(workspace).toBeHidden();
  await expect(link).toBeFocused();
  await expect(link).toHaveText("987");
  if (!href) throw new Error("Missing parameter detail link");
  await page.goto(href);
  await expect(page.getByRole("button", { name: "New version" })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
