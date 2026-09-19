// The SRE journey the release comparison exists for: from the environment
// page's "What changed" to the compare page, read the verdict, find the row,
// read a JSON parameter's field plan without a click, switch the whole-value
// view, filter, swap sides, reveal the unchanged rows without a secret value
// ever reaching the DOM, and copy the comparison as text for the incident
// channel.
import { expect, type Page, test } from "@playwright/test";
import { FEATURES_FIELD_TOTAL, FEATURES_TLS_LINES } from "../fixtures/release-diff-json";
import { incidentState, mockConsole, withFeaturesJson } from "./fakes/console-api";

const SECRET_PLAINTEXT = "super-secret-value-that-must-never-render";
const SECRET_BASE64 = Buffer.from(SECRET_PLAINTEXT).toString("base64");

/**
 * The incident state with readable rate limits (v1 pins @2 = 7, v2 pins @3 =
 * 12), the `features` JSON parameter (v1 → v2, every field-change kind) and a
 * known secret byte string.
 */
function journeyState() {
  const state = withFeaturesJson(incidentState());
  const prod = state.namespaces.prod;
  prod.parameters.rate_limits.versions = ["5", "7", "12", "20"];
  prod.secrets.db_password.versions = [
    {
      version: 1,
      state: "enabled",
      bound: false,
      valueBase64: SECRET_BASE64,
      metadataJson: "{}",
      expiresAtUnixMs: 0,
      createdAtUnixMs: 1,
    },
  ];
  return state;
}

const isMobile = (page: Page) => (page.viewportSize()?.width ?? 1280) <= 768;

const noSidewaysScroll = (page: Page) =>
  page.evaluate(
    () => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1,
  );

test("what changed: environment page → compare page → fields, views, filter, swap, unchanged, copy", async ({
  page,
  browserName,
  context,
}) => {
  test.skip(isMobile(page), "desktop journey; the mobile variant is below");
  await page.setViewportSize({ width: 1280, height: 900 });
  const state = await mockConsole(page, journeyState());

  await page.goto("/applications/environment?app=gradethis&env=prod");
  await page.getByRole("link", { name: "What changed" }).click();
  await expect(page).toHaveURL(
    /\/releases\/compare\?app=gradethis&env=prod&name=runtime&schema_version=1&from=1&to=2$/,
  );

  // The verdict band is what an operator reads first: one sentence of facts.
  const strip = page.getByTestId("release-diff-strip");
  await expect(strip).toBeVisible();
  await expect(page.getByTestId("release-diff-count-changed")).toHaveText("2 parameters changed");
  await expect(page.getByTestId("release-diff-count-added")).toHaveText("0 added");
  await expect(page.getByTestId("release-diff-count-removed")).toHaveText("0 removed");
  await expect(page.getByTestId("release-diff-fields-total")).toHaveText(
    `${FEATURES_FIELD_TOTAL} fields (+1 ~14 ↷1)`,
  );
  await expect(page.getByTestId("release-diff-count-secrets")).toContainText("no secrets repinned");
  await expect(page.getByTestId("release-diff-schema")).toHaveText("schema v1 unchanged");
  // The `to` side is current, so the rollout fact is present on the full page
  // (the incident fixture: 3 instances, 2 applied, 1 rejected).
  await expect(page.getByTestId("release-diff-rollout")).toBeVisible();
  await expect(page.getByTestId("release-diff-rollout")).toHaveText(
    "applied on 2 of 3 instances, 1 rejected",
  );

  // The changed rows: the scalar one with its old → new values inline.
  const rows = page.getByTestId("release-diff-row");
  await expect(rows).toHaveCount(2);
  const rate = page.locator('[data-testid="release-diff-row"][data-alias="rate_limits"]');
  await expect(rate).toHaveAttribute("data-change", "changed");
  await expect(rate.locator(".release-diff-old")).toHaveText("7");
  await expect(rate.locator(".release-diff-new")).toHaveText("12");

  // The JSON row is open to its field plan without a click: twelve lines, the
  // move among them, and the counts as chips in the head.
  const features = page.locator('[data-testid="release-diff-row"][data-alias="features"]');
  await expect(features).toHaveAttribute("data-change", "changed");
  await expect(features.getByRole("button", { name: "Collapse features" })).toBeVisible();
  const fields = features.getByTestId("release-diff-fields");
  await expect(fields).toBeVisible();
  const fieldLines = fields.locator(".release-diff-field");
  await expect(fieldLines).toHaveCount(12);
  await expect(fields.locator('.release-diff-field[data-change="moved"]')).toHaveCount(1);
  await expect(fields.locator('.release-diff-field[data-change="moved"]')).toContainText(
    "legacy_endpoint → endpoints.legacy",
  );
  await expect(features.locator('.release-diff-chip[data-tone="added"]')).toHaveText("+1");
  await expect(features.locator('.release-diff-chip[data-tone="changed"]')).toHaveText("~14");
  await expect(features.locator('.release-diff-chip[data-tone="moved"]')).toHaveText("↷1");
  await expect(features.locator('.release-diff-chip[data-tone="removed"]')).toHaveCount(0);
  // The cap folds the rest; Show all reveals the added subtree printed in full.
  await fields.getByRole("button", { name: `Show all ${FEATURES_FIELD_TOTAL} fields` }).click();
  await expect(fieldLines).toHaveCount(FEATURES_FIELD_TOTAL);
  const added = fields.locator('.release-diff-field[data-change="added"][data-subtree="true"]');
  await expect(added).toHaveCount(1);
  await expect(added.locator(".release-diff-field-code")).toHaveCount(FEATURES_TLS_LINES);
  await expect(added.locator(".release-diff-field-code").first()).toHaveAttribute("data-op");

  // The whole-value view is chosen once in the toolbar: Unified, Split, Fields.
  const views = page.getByRole("tablist", { name: "Value view" });
  await views.getByRole("tab", { name: "Unified" }).click();
  const jsonDiff = features.getByTestId("json-diff");
  await expect(jsonDiff).toBeVisible();
  await expect(jsonDiff).toHaveAttribute("data-layout", "unified");
  expect(await jsonDiff.locator('td.json-diff-sign[data-op="del"]').count()).toBeGreaterThan(0);
  await expect(features.getByTestId("release-diff-fields")).toHaveCount(0);
  await views.getByRole("tab", { name: "Split" }).click();
  await expect(features.getByTestId("json-diff")).toHaveAttribute("data-layout", "split");
  await views.getByRole("tab", { name: "Fields" }).click();
  await expect(fields).toBeVisible();
  await expect(features.getByTestId("json-diff")).toHaveCount(0);
  // The chevron collapses to head and meta only, and reopens.
  await features.getByRole("button", { name: "Collapse features" }).click();
  await expect(features.getByTestId("release-diff-fields")).toHaveCount(0);
  await features.getByRole("button", { name: "Expand features" }).click();
  await expect(fields).toBeVisible();

  // Filter narrows to the alias; an empty match says so.
  const filter = page.getByRole("searchbox");
  await filter.fill("rat");
  await expect(rows).toHaveCount(1);
  await expect(rate.locator("mark")).toHaveText("rat");
  await filter.fill("zzz");
  await expect(rows).toHaveCount(0);
  await expect(page.getByRole("status").filter({ hasText: "No entries match" })).toBeVisible();
  await filter.fill("");
  await expect(rows).toHaveCount(2);

  // Swap flips the URL and the direction of the values.
  await page.getByTestId("release-diff-swap").click();
  await expect(page).toHaveURL(/from=2&to=1/);
  await expect(rate.locator(".release-diff-old")).toHaveText("12");
  await expect(rate.locator(".release-diff-new")).toHaveText("7");
  await page.getByTestId("release-diff-swap").click();
  await expect(page).toHaveURL(/from=1&to=2/);
  await expect(rate.locator(".release-diff-old")).toHaveText("7");

  // Unchanged rows are behind a toggle; the secret row shows a version, never
  // a value, and never a field list.
  const showUnchanged = page.getByRole("checkbox", { name: /Show unchanged/ });
  await showUnchanged.click();
  await expect(showUnchanged).toBeChecked();
  await expect(page).toHaveURL(/view=all/);
  await expect(rows).toHaveCount(4);
  const secret = page.locator('[data-testid="release-diff-row"][data-alias="db_password"]');
  await expect(secret).toHaveAttribute("data-kind", "secret");
  await expect(secret).toHaveAttribute("data-change", "unchanged");
  await expect(secret.locator(".release-diff-old")).toHaveCount(0);
  await expect(secret.locator(".release-diff-new")).toHaveCount(0);
  await expect(secret.locator(".release-diff-fields")).toHaveCount(0);
  await expect(secret.getByTestId("release-diff-fields")).toHaveCount(0);
  await expect(secret.getByRole("button", { name: "Load value" })).toHaveCount(0);
  const html = await page.content();
  expect(html).not.toContain(SECRET_PLAINTEXT);
  expect(html).not.toContain(SECRET_BASE64);
  // No request for a secret value or metadata went out for the comparison.
  expect(state.log.some((entry) => entry.path.startsWith("/secrets/"))).toBe(false);

  // Copy as text: one line per change, the field plan under the JSON alias,
  // secrets by version only.
  test.skip(browserName !== "chromium", "clipboard permissions are Chromium-only in Playwright");
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.getByRole("button", { name: "Copy as text" }).click();
  const text = await page.evaluate(() => navigator.clipboard.readText());
  const lines = text.split("\n");
  expect(lines[0]).toMatch(/^runtime@1:1 → runtime@1:2 in prod\/gradethis/);
  expect(lines.find((line) => /^changed\s+rate_limits/.test(line))).toMatch(/rate_limits\s+7 → 12/);
  expect(lines.find((line) => /^changed\s+features/.test(line))).toMatch(
    /features\s+16 fields \(\+1 ~14 ↷1\)\s+v1 → v2/,
  );
  expect(text).toMatch(/^\s+↷ legacy_endpoint → endpoints\.legacy/m);
  expect(text).toMatch(/^\s+~ pool\.max\s+50 → 5 \(−45, −90 %\)/m);
  expect(text).not.toContain(SECRET_PLAINTEXT);
  expect(text).not.toContain(SECRET_BASE64);
});

test("the compare page stacks on a phone without sideways scroll", async ({ page }) => {
  test.skip(!isMobile(page), "mobile variant");
  await mockConsole(page, journeyState());
  await page.goto(
    "/releases/compare?app=gradethis&env=prod&name=runtime&schema_version=1&from=1&to=2",
  );
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  const rate = page.locator('[data-testid="release-diff-row"][data-alias="rate_limits"]');
  await expect(rate).toBeVisible();
  await expect(rate.locator(".release-diff-new")).toHaveText("12");
  // The JSON row is open to its field list here too.
  const features = page.locator('[data-testid="release-diff-row"][data-alias="features"]');
  await expect(features.getByTestId("release-diff-fields")).toBeVisible();
  expect(await noSidewaysScroll(page)).toBe(true);
  // Reveal every row: still one column, no overflow.
  await page.getByRole("checkbox", { name: /Show unchanged/ }).click();
  await expect(page.getByTestId("release-diff-row")).toHaveCount(4);
  expect(await noSidewaysScroll(page)).toBe(true);
});
