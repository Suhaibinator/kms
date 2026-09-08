import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("release audits link duplicate version numbers to their exact schema tracks", async ({
  page,
}) => {
  await mockConsole(page, incidentState());
  await page.route("**/api/v1/audit**", (route) =>
    route.fulfill({
      json: {
        events: [0, 1, 2, 999].map((schemaVersion) => ({
          id: schemaVersion + 1,
          event_type: "configuration_release.activate",
          actor_identity: "admin",
          actor_type: "admin",
          resource_type: "configuration_release",
          resource_env: "prod",
          resource_app: "gradethis",
          resource_key: "runtime",
          resource_version: 1,
          resource_namespace_id: 1,
          decision: "allow",
          created_at_unix_ms: 1700000000000,
          source_ip: "",
          user_agent: "",
          request_id: "",
          metadata_json: JSON.stringify({
            schema_version: String(schemaVersion),
            activation_revision: "100",
          }),
        })),
        next_page_token: "",
      },
    }),
  );
  await page.goto("/audit");
  for (const schemaVersion of [0, 1, 2, 999]) {
    const link = page.getByRole("link", {
      name: `/prod/gradethis/runtime · schema v${schemaVersion} · v1`,
    });
    await expect(link).toBeVisible();
    const url = new URL((await link.getAttribute("href"))!, "http://localhost");
    expect(url.searchParams.get("schema_version")).toBe(String(schemaVersion));
    expect(url.searchParams.get("release")).toBe(`runtime@${schemaVersion}:1`);
  }
});

test("schema-free JSON releases require an explicit numeric zero before sending", async ({
  page,
}) => {
  const state = incidentState();
  state.application.schema_version = 0;
  await mockConsole(page, state);
  const submissions: Record<string, unknown>[] = [];
  await page.route("**/api/v1/releases", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    submissions.push(route.request().postDataJSON());
    return route.fulfill({
      status: 400,
      json: { error: { code: "invalid_argument", message: "Submission recorded by browser test" } },
    });
  });
  await page.goto("/releases?app=gradethis&env=prod&schema_version=0");
  await page.getByRole("button", { name: "New release" }).first().click();
  const dialog = page.getByRole("dialog", { name: "New release · prod/gradethis" });
  await expect(dialog.getByRole("textbox", { name: "Release name" })).toHaveValue("runtime");
  await dialog.getByRole("tab", { name: "JSON", exact: true }).click();
  const editor = dialog.getByRole("textbox", { name: "Release definition" });
  const definition = JSON.parse(await editor.inputValue()) as Record<string, unknown>;
  delete definition.schema_version;
  await editor.fill(JSON.stringify(definition));
  await expect(dialog.getByText(/schema_version is required/)).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Create release" })).toBeDisabled();
  expect(submissions).toEqual([]);
  await editor.fill(JSON.stringify({ ...definition, schema_version: 0 }));
  await dialog.getByRole("button", { name: "Create release" }).click();
  await expect.poll(() => submissions.length).toBe(1);
  expect(submissions[0].schema_version).toBe(0);
});
