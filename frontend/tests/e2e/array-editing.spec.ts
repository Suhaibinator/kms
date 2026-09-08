import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("array intent, migration review, and required checkbox labels", async ({ page }, info) => {
  const state = incidentState();
  const original = JSON.stringify({
    url: "https://example.com/callback",
    use_discord_id_as_email: false,
  });
  const parameter = state.namespaces.dev.parameters.database;
  parameter.versions = parameter.versions.map(() => original);
  await mockConsole(page, state);
  await page.route("**/api/v1/configuration-schemas?**", (route) =>
    route.fulfill({
      json: {
        schemas: [
          {
            application: "gradethis",
            release_name: "runtime",
            version: state.application.schema_version + 1,
            digest: "sha256:target",
            metadata_json: "{}",
            created_by: "admin",
            created_at_unix_ms: 1,
            schema_json: JSON.stringify({
              type: "object",
              properties: {
                database: {
                  type: "object",
                  additionalProperties: false,
                  required: ["urls", "empty_urls", "use_discord_id_as_email"],
                  properties: {
                    urls: { type: "array", items: { type: "string", minLength: 1 } },
                    empty_urls: { type: "array", items: { type: "string" } },
                    optional_urls: {
                      anyOf: [
                        { type: "array", items: { type: "string", minLength: 1 } },
                        { type: "null" },
                      ],
                    },
                    use_discord_id_as_email: { type: "boolean" },
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
  const databaseCard = dialog
    .locator(".upgrade-value-card")
    .filter({ has: page.locator("summary").filter({ hasText: "database · parameter" }) });
  if (!(await databaseCard.evaluate((element) => (element as HTMLDetailsElement).open))) {
    await databaseCard.locator("summary").first().click();
  }
  const preparation = dialog.getByRole("region", { name: "Prepare database" });
  const conversion = preparation.getByRole("checkbox", { name: /url → urls/ });
  await expect(conversion).not.toBeChecked();
  await expect(preparation).toContainText("Initialize 1 empty list: empty_urls");
  await preparation.getByRole("button", { name: "Prepare draft" }).click();
  // Unaccepted suggestions remain available after applying other changes.
  await expect(conversion).not.toBeChecked();
  await conversion.check();
  await preparation.getByRole("button", { name: "Prepare draft" }).click();
  const urls = dialog.getByRole("group", { name: "urls", exact: true });
  await expect(urls.getByRole("textbox", { name: "urls item 1" })).toHaveValue(
    "https://example.com/callback",
  );
  await preparation.getByRole("button", { name: "Restore pre-preparation value" }).click();
  await expect(urls).toContainText("Missing · Required field");
  await expect(dialog.getByRole("group", { name: "empty_urls", exact: true })).toContainText(
    "Missing · Required field",
  );
  await conversion.check();
  await preparation.getByRole("button", { name: "Prepare draft" }).click();

  const optional = dialog.getByRole("group", { name: "optional_urls", exact: true });
  await optional.getByRole("button", { name: "Add optional_urls item", exact: true }).click();
  await expect(optional).toContainText("Not configured · Field omitted");
  await expect(dialog.getByRole("button", { name: "Preview migration" })).toBeDisabled();
  await expect(optional.getByRole("button", { name: "Add new optional_urls item" })).toBeDisabled();
  await optional
    .getByRole("textbox", { name: "New optional_urls item" })
    .fill("https://example.com/second");
  await optional.getByRole("button", { name: "Cancel new optional_urls item" }).click();
  await expect(optional).toContainText("Not configured · Field omitted");
  await optional.getByRole("button", { name: "Add optional_urls item", exact: true }).click();
  await optional
    .getByRole("textbox", { name: "New optional_urls item" })
    .fill("https://example.com/second");
  await optional.getByRole("button", { name: "Add new optional_urls item" }).click();
  await expect(optional).toContainText("1 item");
  await optional.getByRole("button", { name: "Remove optional_urls item 1" }).click();
  await expect(optional).toContainText("No items · Empty list []");
  await optional.getByText("More options").click();
  await optional.getByRole("button", { name: "Set optional_urls to null" }).click();
  await expect(optional).toContainText("Explicit null · null");
  await optional.getByRole("button", { name: "Omit optional_urls field" }).click();
  await expect(optional).toContainText("Not configured · Field omitted");
  await expect(dialog.getByRole("button", { name: "Preview migration" })).toBeEnabled();

  const checkbox = dialog.getByRole("checkbox", { name: "use_discord_id_as_email" });
  await checkbox.scrollIntoViewIfNeeded();
  const label = dialog
    .locator(".schema-form-boolean label")
    .filter({ hasText: "use_discord_id_as_email" });
  const geometry = await label.evaluate((element) => {
    const range = document.createRange();
    range.selectNodeContents(element.firstChild!);
    const text = range.getBoundingClientRect();
    const star = element.querySelector("span")!.getBoundingClientRect();
    return {
      textTop: text.top,
      textBottom: text.bottom,
      textRight: text.right,
      starTop: star.top,
      starBottom: star.bottom,
      starLeft: star.left,
    };
  });
  expect(geometry.starTop).toBeLessThan(geometry.textBottom);
  expect(geometry.starBottom).toBeGreaterThan(geometry.textTop);
  expect(geometry.starLeft).toBeGreaterThanOrEqual(geometry.textRight);
  await label.click();
  await expect(checkbox).toBeChecked();
  await expect(dialog.locator("[data-modal-body]")).toBeVisible();
  expect(
    await dialog.locator("[data-modal-body]").evaluate((el) => el.scrollWidth <= el.clientWidth),
  ).toBe(true);
  await page.screenshot({
    path: info.outputPath("array-intent-checkbox.png"),
    animations: "disabled",
  });
});
