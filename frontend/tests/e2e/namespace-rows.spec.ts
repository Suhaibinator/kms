import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("namespace rows navigate while their explicit controls remain independent", async ({
  page,
}) => {
  await mockConsole(page, incidentState());
  await page.goto("/namespaces");

  const manage = page.getByRole("link", { name: "Manage gradethis/prod" });
  await expect(manage).toHaveAttribute("href", "/applications?app=gradethis&env=prod");
  const row = page.locator("tr", { has: manage });
  const tableWrap = page.locator(".ns-group", { has: row }).locator(".table-wrap");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect(await tableWrap.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(
    true,
  );

  // Explicit resource links sit above the stretched row link.
  await row.locator('td[data-label="Parameters"] a').click();
  await expect(page).toHaveURL("/parameters?env=prod&app=gradethis");

  await page.goto("/namespaces");
  const currentRow = page.locator("tr", {
    has: page.getByRole("link", { name: "Manage gradethis/prod" }),
  });
  await currentRow.getByRole("button", { name: "Edit" }).click();
  const editDialog = page.getByRole("dialog", { name: "Edit prod/gradethis" });
  await expect(editDialog).toBeVisible();
  await expect(page).toHaveURL("/namespaces");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(editDialog).toBeHidden();
  await expect(page.locator('[data-slot="dialog-overlay"]')).toHaveCount(0);

  // Clicking a non-control cell exercises the link's full-row hit target.
  const description = currentRow.locator('td[data-label="Description"]');
  const box = await description.boundingBox();
  expect(box).not.toBeNull();
  await page.mouse.click(
    (box?.x ?? 0) + (box?.width ?? 0) / 2,
    (box?.y ?? 0) + (box?.height ?? 0) / 2,
  );
  await expect(page).toHaveURL("/applications?app=gradethis&env=prod");
  await expect(page.locator('[data-env="prod"]')).toBeVisible();
});
