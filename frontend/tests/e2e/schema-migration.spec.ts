import { expect, type Page, test } from "@playwright/test";
import type { SchemaMigrationRequest, SchemaMigrationResponse } from "../../lib/types";
import { incidentState, mockConsole } from "./fakes/console-api";

const targetSchema = {
  application: "gradethis",
  release_name: "runtime",
  version: 2,
  schema_json: JSON.stringify({
    type: "object",
    properties: { renamed_rate_limits: { type: "integer" } },
    required: ["renamed_rate_limits"],
  }),
  digest: `sha256:${"2".repeat(64)}`,
  metadata_json: "{}",
  created_by: "admin",
  created_at_unix_ms: 2,
};

async function installMigrationAPI(page: Page, requests: SchemaMigrationRequest[]) {
  await page.route("**/api/v1/configuration-schemas?**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ schemas: [targetSchema], next_page_token: "" }),
    });
  });
  await page.route("**/api/v1/applications/gradethis/schema-migration", async (route) => {
    const request = route.request().postDataJSON() as SchemaMigrationRequest;
    requests.push(request);
    const executed = request.execute === true;
    const response: SchemaMigrationResponse = {
      plan_digest: `sha256:${"a".repeat(64)}`,
      valid: true,
      executed,
      release_name: "runtime",
      source_version: 2,
      source_activation_revision: 12,
      schema_version: 2,
      entries: [
        {
          alias: "renamed_rate_limits",
          kind: "parameter",
          key: "rate_limits",
          from_version: 3,
          to_version: 4,
          source: "edited",
        },
      ],
      validation: [],
      ...(executed
        ? {
            release: {
              namespace: { env: "prod", app: "gradethis" },
              name: "runtime",
              version: 3,
              schema_version: 2,
              entries: [],
              digest: `sha256:${"b".repeat(64)}`,
              metadata_json: "{}",
              created_by: "admin",
              created_at_unix_ms: 3,
            },
            activation: { activation_revision: 13, previous_version: 2, changed: true },
          }
        : {}),
      affected_environments: [{ environment: "dev", active_version: 1, schema_version: 1 }],
      definition_changed: true,
    };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(response),
    });
  });
}

async function expectNoPageOverflow(page: Page) {
  const geometry = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    page: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
  }));
  expect(geometry.page).toBeLessThanOrEqual(geometry.viewport);
  expect(geometry.body).toBeLessThanOrEqual(geometry.viewport);
  const modalBody = page.locator("[data-modal-body]");
  if (await modalBody.isVisible()) {
    const modalGeometry = await modalBody.evaluate((element) => ({
      client: element.clientWidth,
      scroll: element.scrollWidth,
    }));
    expect(modalGeometry.scroll).toBeLessThanOrEqual(modalGeometry.client);
  }
}

for (const width of [390, 1280]) {
  test(`guided production schema migration at ${width}px`, async ({ page }, testInfo) => {
    test.skip(testInfo.project.name !== "chromium", "fixed Chromium viewport coverage");
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 });
    const requests: SchemaMigrationRequest[] = [];
    await mockConsole(page, incidentState());
    await installMigrationAPI(page, requests);
    await page.goto("/applications?app=gradethis&env=prod");

    await page.getByRole("button", { name: "More for prod" }).click();
    await page.getByRole("menuitem", { name: "Upgrade schema in prod…" }).click();
    const dialog = page.getByRole("dialog", { name: "Upgrade application schema" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByLabel("Source environment")).toHaveValue("prod");
    await expect(dialog.getByLabel("Target registered schema")).toHaveValue("2");
    await expectNoPageOverflow(page);

    await dialog.getByRole("button", { name: /Review contract/ }).click();
    const renamed = dialog.locator('input[value="renamed_rate_limits"]');
    const contractRow = renamed.locator(
      "xpath=ancestor::*[contains(@class, 'migration-contract-row')]",
    );
    await contractRow.getByLabel("Source alias").selectOption("rate_limits");
    await expectNoPageOverflow(page);

    await dialog.getByRole("button", { name: /Edit values/ }).click();
    await dialog.locator("summary").filter({ hasText: "renamed_rate_limits" }).click();
    const value = dialog.getByRole("textbox", { name: "renamed_rate_limits value" });
    await expect(value).toHaveValue("300");
    await value.fill("500");
    await page.screenshot({ path: testInfo.outputPath(`schema-migration-editor-${width}.png`) });
    await expectNoPageOverflow(page);

    await dialog.getByRole("button", { name: /Preview migration/ }).click();
    await expect(dialog).toContainText("Backend validation passed.");
    await expect(dialog).toContainText("renamed_rate_limits");
    const version = dialog.locator('td[data-label="Version"]');
    await expect(version).toHaveText("v3 → v4");
    await expect(version).toBeInViewport();
    const previewGeometry = await dialog.locator(".table-wrap").evaluate((wrapper) => {
      const versionCell = wrapper.querySelector<HTMLElement>('td[data-label="Version"]');
      if (!versionCell) throw new Error("Migration preview version cell is missing");
      const frame = wrapper.getBoundingClientRect();
      const cell = versionCell.getBoundingClientRect();
      return {
        scroll: wrapper.scrollWidth,
        client: wrapper.clientWidth,
        versionInsideFrame: cell.left >= frame.left && cell.right <= frame.right,
        versionInsideViewport: cell.left >= 0 && cell.right <= window.innerWidth,
      };
    });
    expect(previewGeometry.scroll).toBeLessThanOrEqual(previewGeometry.client);
    expect(previewGeometry.versionInsideFrame).toBe(true);
    expect(previewGeometry.versionInsideViewport).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`schema-migration-preview-${width}.png`) });
    await expectNoPageOverflow(page);

    const activate = dialog.getByRole("button", { name: "Upgrade schema & ship to prod" });
    await expect(activate).toBeDisabled();
    await dialog.getByLabel("Production confirmation").fill("prod");
    await expect(activate).toBeEnabled();
    await activate.click();
    await expect(dialog).toContainText("Schema migration activated");
    await expect(dialog).toContainText("runtime@3");
    await page.screenshot({ path: testInfo.outputPath(`schema-migration-success-${width}.png`) });
    await expectNoPageOverflow(page);

    expect(requests).toHaveLength(2);
    expect(requests[0]).toEqual({
      environment: "prod",
      schema_version: 2,
      contract: [
        { alias: "renamed_rate_limits", kind: "parameter", content_type: "integer" },
        { alias: "db_password", kind: "secret" },
      ],
      changes: [
        {
          alias: "renamed_rate_limits",
          from_alias: "rate_limits",
          key: "rate_limits",
          value: "500",
          content_type: "integer",
        },
        { alias: "db_password" },
      ],
      execute: false,
      expected_source_version: 2,
      expected_source_activation_revision: 12,
    });
    expect(requests[1]).toEqual({
      ...requests[0],
      execute: true,
      plan_digest: `sha256:${"a".repeat(64)}`,
    });
  });
}
