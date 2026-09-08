import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("release-policy users discover inactive newest tracks and retain explicit history choices", async ({
  page,
}) => {
  const state = incidentState();
  state.identity = { name: "release-operator", kind: "client", auth_method: "token" };
  await mockConsole(page, state);
  let adminSchemaReads = 0;
  await page.route("**/api/v1/configuration-schemas?**", async (route) => {
    adminSchemaReads += 1;
    await route.fulfill({
      status: 403,
      json: { error: { code: "permission_denied", message: "admin only" } },
    });
  });
  await page.route("**/api/v1/releases/schema-versions?**", async (route) => {
    await route.fulfill({ json: { schema_versions: [5, 1], next_page_token: "" } });
  });
  const selected: number[] = [];
  await page.route("**/api/v1/releases?**", async (route) => {
    const schema = Number(new URL(route.request().url()).searchParams.get("schema_version"));
    selected.push(schema);
    await route.fulfill({
      json: {
        releases:
          schema === 1
            ? state.namespaces.prod.releases.map((release) => ({
                release: { ...release, schema_version: 1 },
                current: false,
                previous: false,
                activation_revision: 0,
              }))
            : [],
        next_page_token: "",
      },
    });
  });
  await page.goto("/releases?app=gradethis&env=prod&name=runtime");
  await expect(page).toHaveURL(/schema_version=5/);
  const selector = page.getByRole("combobox", { name: "Schema version" });
  await expect(selector).toHaveValue("5");
  await expect(page.getByText("No releases found", { exact: true })).toBeVisible();
  await selector.selectOption("1");
  await expect(page).toHaveURL(/schema_version=1/);
  await expect(page.getByRole("table").getByRole("button", { name: "View" }).first()).toBeVisible();
  await page.goto("/releases?app=gradethis&env=prod&name=runtime&schema_version=0");
  await expect(selector).toHaveValue("0");
  await expect.poll(() => selected.at(-1)).toBe(0);
  await page.goBack();
  await expect(selector).toHaveValue("1");
  expect(adminSchemaReads).toBe(0);
  expect(selected).toContain(5);
  expect(selected).toContain(1);
  expect(selected).toContain(0);
});

test("denied discovery permits a deliberate numeric schema choice without falling back", async ({
  page,
}) => {
  const state = incidentState();
  state.identity = { name: "release-reader", kind: "client", auth_method: "token" };
  await mockConsole(page, state);
  const discoveredNames: string[] = [];
  await page.route("**/api/v1/releases/schema-versions?**", async (route) => {
    discoveredNames.push(new URL(route.request().url()).searchParams.get("name") ?? "");
    await route.fulfill({
      status: 403,
      json: { error: { code: "permission_denied", message: "denied" } },
    });
  });
  const selections: string[] = [];
  await page.route("**/api/v1/releases?**", async (route) => {
    selections.push(new URL(route.request().url()).searchParams.get("schema_version") ?? "missing");
    await route.fulfill({ json: { releases: [], next_page_token: "" } });
  });
  await page.goto("/releases?app=gradethis&env=prod&name=runtime");
  const input = page.getByRole("textbox", { name: "Schema version" });
  await expect(input).toBeVisible();
  expect(selections).toEqual([]);
  const nameFilter = page.getByRole("textbox", { name: "Release name" });
  await expect(nameFilter).toBeEditable();
  await nameFilter.fill("other");
  await page.getByRole("button", { name: "Apply filter" }).click();
  await expect.poll(() => discoveredNames.at(-1)).toBe("other");
  await expect(input).toBeVisible();
  await input.fill("9007199254740992");
  await expect(page.getByRole("button", { name: "Select schema" })).toBeDisabled();
  for (const version of ["0", "99"]) {
    await input.fill(version);
    await page.getByRole("button", { name: "Select schema" }).click();
    await expect(page).toHaveURL(new RegExp(`schema_version=${version}$`));
    await expect.poll(() => selections.at(-1)).toBe(version);
    await expect(input).toHaveValue(version);
  }
});
