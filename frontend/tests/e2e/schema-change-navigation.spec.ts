import { expect, test } from "@playwright/test";
import ready from "../fixtures/backend/overview-ready.json";
import { incidentState, mockConsole } from "./fakes/console-api";

test("finds changed fields and fixes a validation problem in a large upgrade", async ({
  page,
}, info) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await mockConsole(page, incidentState());
  const overview = structuredClone(ready);
  overview.environments = overview.environments.filter((e) => e.namespace.env === "dev");
  const active = overview.environments[0].release.active!;
  const secret = active.entries.find((e) => e.kind === "secret")!;
  for (let i = 0; i < 64; i++) {
    const alias = `secret_${i}`;
    overview.application.contract.push({ alias, kind: "secret", content_type: "" });
    active.entries.push({ ...secret, alias, ref: { ...secret.ref, key: alias } });
  }
  await page.route("**/api/v1/applications/overview?**", (r) => r.fulfill({ json: overview }));
  const schema = {
    application: "gradethis",
    release_name: "runtime",
    digest: "sha256:target",
    metadata_json: "{}",
    created_by: "admin",
    created_at_unix_ms: 1,
  };
  const properties = { database: { type: "object" }, rate_limits: { type: "integer" } };
  await page.route("**/api/v1/configuration-schemas?**", (r) =>
    r.fulfill({
      json: {
        schemas: [
          {
            ...schema,
            version: 2,
            schema_json: JSON.stringify({
              type: "object",
              properties: {
                ...properties,
                database: {
                  type: "object",
                  additionalProperties: false,
                  properties: {
                    host: { type: "string" },
                    client_id: { type: "string" },
                    go_auth_config: {
                      type: "object",
                      properties: Object.fromEntries(
                        [
                          "apple",
                          "discord",
                          "facebook",
                          "github",
                          "google",
                          "linkedin",
                          "okta",
                        ].map((provider) => [
                          `${provider}_oauth_additional_redirect_urls`,
                          { type: "array", items: { type: "string" } },
                        ]),
                      ),
                    },
                  },
                  required: ["client_id", "go_auth_config"],
                },
              },
            }),
          },
          { ...schema, version: 1, schema_json: JSON.stringify({ type: "object", properties }) },
        ],
        next_page_token: "",
      },
    }),
  );
  await page.route("**/api/v1/applications/gradethis/schema-migration", (r) =>
    r.fulfill({
      json: {
        valid: false,
        executed: false,
        plan_digest: "plan",
        release_name: "runtime",
        source_version: active.version,
        source_activation_revision: active.activation_revision,
        schema_version: 2,
        entries: active.entries.map((e) => ({
          alias: e.alias,
          key: e.ref.key,
          kind: e.kind,
          from_version: e.version,
          to_version: e.version,
          source: "preserved",
        })),
        validation: [
          {
            alias: "database",
            code: "schema",
            schema_pointer: "/required",
            message: 'Add the missing required field(s): "client_id".',
          },
        ],
        affected_environments: [],
        definition_changed: true,
      },
    }),
  );
  await page.goto("/applications?app=gradethis");
  await page.getByRole("button", { name: "Upgrade schema…" }).click();
  const dialog = page.getByRole("dialog", { name: "Upgrade application schema" });
  await dialog.getByRole("button", { name: "Review contract", exact: true }).click();
  await expect(dialog.locator(".migration-contract-row").first()).toContainText("database");
  await dialog.getByRole("region", { name: "Field changes" }).scrollIntoViewIfNeeded();
  await page.screenshot({
    path: info.outputPath("change-toolbar-dark.png"),
    animations: "disabled",
  });
  const contractCard = dialog.locator(".migration-contract-row").first();
  await contractCard.scrollIntoViewIfNeeded();
  const cardGeometry = await contractCard.evaluate((card) => {
    const bounds = card.getBoundingClientRect();
    const remove = card.querySelector("button")!.getBoundingClientRect();
    return { deleteOffset: remove.top - bounds.top, overflow: card.scrollWidth - card.clientWidth };
  });
  expect(cardGeometry.deleteOffset).toBeLessThan(32);
  expect(cardGeometry.overflow).toBeLessThanOrEqual(0);
  await page.screenshot({
    path: info.outputPath("contract-layout-dark.png"),
    animations: "disabled",
  });

  await dialog.getByRole("button", { name: "Edit values", exact: true }).click();
  const search = dialog.getByLabel("Search fields or schema paths");
  await search.fill("database.client_id");
  // Filtered editors stay mounted to preserve incomplete drafts.
  await expect(dialog.locator("details[id^=upgrade-field]:visible")).toHaveCount(1);
  await dialog.getByRole("button", { name: "Next change", exact: true }).click();
  const row = dialog
    .getByRole("group", { name: "database value", exact: true })
    .locator("xpath=ancestor::details");
  await expect(row.locator("summary")).toBeFocused();
  await expect(row).toHaveAttribute("open");
  await expect(row.getByText("Target schema v2")).toBeVisible();
  await row.getByRole("button", { name: "Prepare draft", exact: true }).click();
  await expect(row.getByRole("button", { name: "Restore pre-preparation value" })).toBeVisible();
  await row
    .getByRole("button", { name: "Add apple_oauth_additional_redirect_urls item", exact: true })
    .scrollIntoViewIfNeeded();
  const arrayState = row.getByRole("combobox", {
    name: "apple_oauth_additional_redirect_urls state",
  });
  await expect(arrayState).toHaveValue("unset");
  await arrayState.selectOption("set");
  await expect(row.getByText("Empty array · 0 items", { exact: true })).toBeVisible();
  await arrayState.selectOption("unset");
  await expect(arrayState).toHaveValue("unset");
  await arrayState.selectOption("set");
  await page.screenshot({
    path: info.outputPath("target-schema-fields.png"),
    animations: "disabled",
  });
  await dialog.getByRole("button", { name: "Preview migration", exact: true }).click();
  const reasons = dialog.getByRole("list", { name: "Validation problems" });
  await expect(reasons).toContainText('"client_id"');
  await expect(reasons).toBeInViewport();
  await page.screenshot({
    path: info.outputPath("validation-reasons.png"),
    animations: "disabled",
  });
  const beforeTable = await reasons.evaluate((el) =>
    Boolean(
      el.compareDocumentPosition(
        el.closest("[data-modal-body]")!.querySelector(".card-table table")!,
      ) & Node.DOCUMENT_POSITION_FOLLOWING,
    ),
  );
  expect(beforeTable).toBe(true);
  await dialog.getByRole("button", { name: "database · Fix field" }).click();
  const value = dialog.getByRole("textbox", { name: "client_id", exact: true });
  await expect(
    row.getByRole("combobox", { name: "apple_oauth_additional_redirect_urls state" }),
  ).toHaveValue("set");
  await expect(value).toBeFocused();
  await expect(value).toBeInViewport();
  await expect(row).toHaveAttribute("open");
  const order = await dialog
    .locator("details[id^=upgrade-field]")
    .evaluateAll((elements) => elements.map((el) => el.id));
  await value.fill("client-123");
  await expect(
    row.getByRole("combobox", { name: "apple_oauth_additional_redirect_urls state" }),
  ).toHaveValue("set");
  await expect(value).toBeFocused();
  await expect(value).toBeInViewport();
  await expect(row).toHaveAttribute("open");
  expect(
    await dialog
      .locator("details[id^=upgrade-field]")
      .evaluateAll((elements) => elements.map((el) => el.id)),
  ).toEqual(order);
  await page.screenshot({
    path: info.outputPath("changed-field-editor.png"),
    fullPage: true,
    animations: "disabled",
  });
  const geometry = await dialog
    .locator("[data-modal-body]")
    .evaluate((el) => ({ client: el.clientWidth, scroll: el.scrollWidth }));
  expect(geometry.scroll).toBeLessThanOrEqual(geometry.client);
});
