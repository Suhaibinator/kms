import { expect, test } from "@playwright/test";
import type { ApplicationOverview } from "@/lib/types";
import readyJson from "../fixtures/backend/overview-ready.json";
import { incidentState, mockConsole } from "./fakes/console-api";

for (const schemaVersion of [0, 1]) {
  test(`v${schemaVersion} survives matrix, resource context, and release setup navigation`, async ({
    page,
  }) => {
    const state = incidentState();
    state.application.schema_version = 2;
    await mockConsole(page, state);
    const overview = structuredClone(readyJson) as unknown as ApplicationOverview;
    await page.route("**/api/v1/applications/overview?**", async (route) => {
      const selected = Number(
        new URL(route.request().url()).searchParams.get("schema_version") ?? 2,
      );
      await route.fulfill({
        json: { ...overview, application: { ...overview.application, schema_version: selected } },
      });
    });
    await page.goto(
      `/applications?app=gradethis&schema_version=${schemaVersion}&env=prod&tab=matrix`,
    );
    await expect(page.getByRole("heading", { name: "Configuration matrix" })).toBeVisible();
    await expect(
      page.getByRole("table").getByRole("link", { name: "prod", exact: true }),
    ).toHaveAttribute(
      "href",
      `/applications?app=gradethis&schema_version=${schemaVersion}&env=prod`,
    );
    await page.getByRole("link", { name: "Open db_password in prod" }).click();
    const workspace = page.getByRole("dialog");
    await expect(workspace).toBeVisible();
    const returnLink = workspace.getByRole("link", { name: "gradethis", exact: true });
    await expect(returnLink).toHaveAttribute(
      "href",
      `/applications?app=gradethis&schema_version=${schemaVersion}&env=prod&tab=matrix`,
    );
    await returnLink.click();
    await expect(page).toHaveURL(
      new RegExp(`schema_version=${schemaVersion}.*env=prod.*tab=matrix`),
    );
    // Close the workspace if the same-route return retains it.
    if (await workspace.isVisible())
      await workspace.getByRole("button", { name: "Dismiss dialog" }).click();
    const definition = page.getByRole("region", { name: "Definition" });
    await definition.getByRole("button", { name: "Fix", exact: true }).click();
    await page.getByRole("menuitem", { name: "Manage releases", exact: true }).click();
    await expect(page).toHaveURL(
      `/releases?app=gradethis&env=prod&name=runtime&schema_version=${schemaVersion}`,
    );
    const breadcrumb = page.getByRole("navigation", { name: "Breadcrumb" });
    await expect(breadcrumb.getByRole("link", { name: "gradethis", exact: true })).toHaveAttribute(
      "href",
      `/applications?app=gradethis&schema_version=${schemaVersion}`,
    );
    await page.getByRole("button", { name: "New release", exact: true }).first().click();
    await expect(
      page.getByRole("dialog").getByRole("textbox", { name: "Schema", exact: true }),
    ).toHaveValue(`gradethis/runtime@${schemaVersion}`);
  });
}

test("URL track changes discard description drafts and unadopted setup reaches first release creation", async ({
  page,
}) => {
  const state = incidentState();
  state.application.schema_version = 2;
  await mockConsole(page, state);
  const overview = structuredClone(readyJson) as unknown as ApplicationOverview;
  await page.route("**/api/v1/applications/overview?**", async (route) => {
    const selected = Number(new URL(route.request().url()).searchParams.get("schema_version") ?? 2);
    await route.fulfill({
      json: {
        ...overview,
        status: "setup",
        findings: [],
        application: { ...overview.application, schema_version: selected, contract: [] },
      },
    });
  });
  await page.goto("/applications?app=gradethis&schema_version=1&env=prod");
  await expect(page.getByRole("region", { name: "Definition" })).toBeVisible();
  await page.getByRole("combobox", { name: "Schema version" }).selectOption("2");
  await expect(page).toHaveURL(/schema_version=2/);
  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("menuitem", { name: "Edit definition", exact: true }).click();
  const edit = page.getByRole("dialog");
  await expect(edit.getByRole("button", { name: "Add alias" })).toHaveCount(0);
  await edit.getByRole("textbox", { name: "Description" }).fill("unsaved track two draft");
  // Open the older track directly; its definition starts from persisted metadata.
  await page.goto("/applications?app=gradethis&schema_version=1&env=prod");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("button", { name: "More actions" }).click();
  await page.getByRole("menuitem", { name: "Edit definition", exact: true }).click();
  await expect(page.getByRole("dialog").getByRole("textbox", { name: "Description" })).toHaveValue(
    overview.application.description,
  );
  await page.getByRole("dialog").getByRole("button", { name: "Dismiss dialog" }).click();
  await page
    .getByText("Define the contract", { exact: true })
    .locator("xpath=ancestor::li[1]")
    .getByRole("button", { name: "Manage releases" })
    .click();
  await expect(page).toHaveURL("/releases?app=gradethis&env=prod&name=runtime&schema_version=1");
  await page.getByRole("button", { name: "New release", exact: true }).first().click();
  const builder = page.getByRole("dialog");
  await expect(builder.getByRole("textbox", { name: "Alias", exact: true })).toBeEditable();
  await expect(builder.getByRole("textbox", { name: "Schema", exact: true })).toHaveValue(
    "gradethis/runtime@1",
  );
  const request = {
    namespace: { app: "gradethis", env: "prod" },
    name: "runtime",
    schema_version: 1,
    entries: [
      {
        alias: "database_password",
        kind: "secret",
        ref: { namespace: { app: "gradethis", env: "prod" }, key: "db_password" },
        version: 1,
      },
    ],
    metadata_json: "{}",
  };
  let created: unknown;
  await page.route("**/api/v1/releases", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    created = route.request().postDataJSON();
    await route.fulfill({
      json: {
        release: {
          ...request,
          version: 1,
          entries: [
            { alias: "database_password", kind: "secret", ref: request.entries[0].ref, version: 1 },
          ],
        },
      },
    });
  });
  await builder.getByRole("tab", { name: "JSON", exact: true }).click();
  await builder
    .getByRole("textbox", { name: "Release definition", exact: true })
    .fill(JSON.stringify(request));
  await builder.getByRole("button", { name: "Create release", exact: true }).click();
  await expect.poll(() => created).toEqual(request);
  expect(state.application.schema_version).toBe(2);
});
