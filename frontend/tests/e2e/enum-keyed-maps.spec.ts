import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("enum map keys can be added and repaired without losing values or drafts", async ({
  page,
}, info) => {
  const state = incidentState();
  const original = JSON.stringify({ weights: { user: 4 }, plans: { USER: { count: 3 } } });
  const parameter = state.namespaces.dev.parameters.database;
  parameter.versions = parameter.versions.map(() => original);
  const propertyNames = {
    type: "string",
    enum: ["USER", "ADMIN"],
    "x-kms-enum-numbers": { USER: 1, ADMIN: 2 },
  };
  await mockConsole(page, state);
  await page.route("**/api/v1/configuration-schemas?**", (route) =>
    route.fulfill({
      json: {
        schemas: [
          {
            application: "gradethis",
            release_name: "runtime",
            version: state.application.schema_version + 1,
            digest: "sha256:enum-map-target",
            metadata_json: "{}",
            created_by: "admin",
            created_at_unix_ms: 1,
            schema_json: JSON.stringify({
              type: "object",
              properties: {
                database: {
                  type: "object",
                  properties: {
                    weights: {
                      type: "object",
                      propertyNames,
                      additionalProperties: { type: "integer" },
                    },
                    plans: {
                      anyOf: [
                        {
                          type: "object",
                          propertyNames,
                          additionalProperties: {
                            type: "object",
                            properties: { count: { type: "integer" } },
                          },
                        },
                        { type: "null" },
                      ],
                    },
                  },
                },
                rate_limits: { type: "integer" },
              },
            }),
          },
        ],
        next_page_token: "",
      },
    }),
  );
  await page.goto("/applications?app=gradethis&env=dev");
  await page.getByRole("button", { name: "Upgrade schema…" }).click();
  const dialog = page.getByRole("dialog", { name: "Upgrade application schema" });
  await dialog.getByRole("button", { name: "Review contract", exact: true }).click();
  await dialog.getByRole("button", { name: "Edit values", exact: true }).click();
  const card = dialog.locator(".upgrade-value-card").filter({
    has: page.locator("summary").filter({ hasText: "database · parameter" }),
  });
  if (!(await card.evaluate((element) => (element as HTMLDetailsElement).open))) {
    await card.locator("summary").first().click();
  }
  const invalidKey = card.getByRole("combobox", { name: 'weights key "user"', exact: true });
  await expect(invalidKey).toHaveText('"user" (invalid)');
  await expect(card).toContainText('key must be one of "USER", "ADMIN"');
  await invalidKey.click();
  await page.getByRole("option", { name: "USER", exact: true }).click();
  await expect(card.getByRole("textbox", { name: "weights entry USER", exact: true })).toHaveValue(
    "4",
  );

  await card.getByRole("button", { name: "Add weights entry", exact: true }).click();
  await card.getByRole("combobox", { name: "New weights key", exact: true }).click();
  await expect(page.getByRole("option", { name: "USER", exact: true })).toHaveCount(0);
  await page.getByRole("option", { name: "ADMIN", exact: true }).click();
  await card.getByRole("button", { name: "Add new weights entry", exact: true }).click();
  await expect(card.getByRole("button", { name: "Add weights entry", exact: true })).toBeDisabled();
  await expect(card.getByRole("textbox", { name: "weights entry ADMIN", exact: true })).toHaveValue(
    "0",
  );

  const count = card.getByRole("textbox", { name: "count", exact: true });
  await count.fill("3e");
  await card.getByRole("combobox", { name: 'plans key "USER"', exact: true }).click();
  await page.getByRole("option", { name: "ADMIN", exact: true }).click();
  await expect(count).toHaveValue("3e");
  await count.fill("9");
  expect(
    await dialog.locator("[data-modal-body]").evaluate((el) => el.scrollWidth <= el.clientWidth),
  ).toBe(true);
  await page.screenshot({ path: info.outputPath("enum-keyed-maps.png"), animations: "disabled" });
  await card.getByRole("button", { name: "JSON", exact: true }).click();
  const json = card.getByRole("textbox", { name: "database value", exact: true });
  const value = JSON.parse(await json.inputValue());
  expect(value).toEqual({ weights: { USER: 4, ADMIN: 0 }, plans: { ADMIN: { count: 9 } } });
});
