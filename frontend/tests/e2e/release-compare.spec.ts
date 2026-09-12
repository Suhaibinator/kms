// The SRE journey the release comparison exists for: from the environment
// page's "What changed" to the compare page, read the counts, find the row,
// filter, swap sides, reveal the unchanged rows without a secret value ever
// reaching the DOM, and copy the comparison as text for the incident channel.
import { expect, type Page, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

const SECRET_PLAINTEXT = "super-secret-value-that-must-never-render";
const SECRET_BASE64 = Buffer.from(SECRET_PLAINTEXT).toString("base64");

/** The incident state with readable rate limits (v1 pins @2 = 7, v2 pins @3 = 12) and a known secret byte string. */
function journeyState() {
  const state = incidentState();
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

test("what changed: environment page → compare page → filter, swap, unchanged, copy", async ({
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

  // The counts are what an operator reads first.
  const strip = page.getByTestId("release-diff-strip");
  await expect(strip).toBeVisible();
  await expect(page.getByTestId("release-diff-count-changed")).toContainText("1");
  await expect(page.getByTestId("release-diff-count-added")).toContainText("0");
  await expect(page.getByTestId("release-diff-count-secrets")).toContainText("0");
  // The `to` side is current, so the rollout cell is present on the full page.
  await expect(page.getByTestId("release-diff-rollout")).toBeVisible();

  // The changed row with its old → new values inline.
  const rows = page.getByTestId("release-diff-row");
  await expect(rows).toHaveCount(1);
  const rate = page.locator('[data-testid="release-diff-row"][data-alias="rate_limits"]');
  await expect(rate).toHaveAttribute("data-change", "changed");
  await expect(rate.locator(".release-diff-old")).toHaveText("7");
  await expect(rate.locator(".release-diff-new")).toHaveText("12");

  // Filter narrows to the alias; an empty match says so.
  const filter = page.getByRole("searchbox");
  await filter.fill("rat");
  await expect(rows).toHaveCount(1);
  await expect(rate.locator("mark")).toHaveText("rat");
  await filter.fill("zzz");
  await expect(rows).toHaveCount(0);
  await expect(page.getByRole("status").filter({ hasText: "No entries match" })).toBeVisible();
  await filter.fill("");
  await expect(rows).toHaveCount(1);

  // Swap flips the URL and the direction of the values.
  await page.getByTestId("release-diff-swap").click();
  await expect(page).toHaveURL(/from=2&to=1/);
  await expect(rate.locator(".release-diff-old")).toHaveText("12");
  await expect(rate.locator(".release-diff-new")).toHaveText("7");
  await page.getByTestId("release-diff-swap").click();
  await expect(page).toHaveURL(/from=1&to=2/);
  await expect(rate.locator(".release-diff-old")).toHaveText("7");

  // Unchanged rows are behind a toggle; the secret row shows a version, never a value.
  await page.getByRole("checkbox", { name: /Show unchanged/ }).click();
  await expect(page).toHaveURL(/view=all/);
  await expect(rows).toHaveCount(3);
  const secret = page.locator('[data-testid="release-diff-row"][data-alias="db_password"]');
  await expect(secret).toHaveAttribute("data-kind", "secret");
  await expect(secret).toHaveAttribute("data-change", "unchanged");
  await expect(secret.locator(".release-diff-old")).toHaveCount(0);
  await expect(secret.locator(".release-diff-new")).toHaveCount(0);
  await expect(secret.getByRole("button", { name: "Load value" })).toHaveCount(0);
  const html = await page.content();
  expect(html).not.toContain(SECRET_PLAINTEXT);
  expect(html).not.toContain(SECRET_BASE64);
  // No request for a secret value or metadata went out for the comparison.
  expect(state.log.some((entry) => entry.path.startsWith("/secrets/"))).toBe(false);

  // Copy as text: one line per change, secrets by version only.
  test.skip(browserName !== "chromium", "clipboard permissions are Chromium-only in Playwright");
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.getByRole("button", { name: "Copy as text" }).click();
  const text = await page.evaluate(() => navigator.clipboard.readText());
  const lines = text.split("\n");
  expect(lines[0]).toMatch(/^runtime@1:1 → runtime@1:2 in prod\/gradethis/);
  expect(lines.find((line) => line.startsWith("changed"))).toMatch(/rate_limits\s+7 → 12/);
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
  expect(await noSidewaysScroll(page)).toBe(true);
  // Reveal every row and expand the changed one: still one column, no overflow.
  await page.getByRole("checkbox", { name: /Show unchanged/ }).click();
  await expect(page.getByTestId("release-diff-row")).toHaveCount(3);
  expect(await noSidewaysScroll(page)).toBe(true);
});
