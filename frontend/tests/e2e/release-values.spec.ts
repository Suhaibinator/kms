import { expect, test } from "@playwright/test";
import { incidentState, mockConsole, withFeaturesJson } from "./fakes/console-api";

test("inspect a release's pinned JSON directly on desktop and mobile", async ({
  page,
}, testInfo) => {
  const state = withFeaturesJson(incidentState());
  // The release pins an older parameter version than the latest stored value.
  state.namespaces.prod.parameters.rate_limits.versions = ["5", "7", "12", "20"];
  await mockConsole(page, state);
  const parameterReads: URL[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/parameters/get") parameterReads.push(url);
  });

  await page.goto(
    "/releases?app=gradethis&env=prod&name=runtime&schema_version=1&release=runtime%401%3A1&section=values",
  );
  const dialog = page.getByRole("dialog", { name: "Release runtime@1 · schema v1" });
  await expect(dialog.getByRole("tab", { name: "Values", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await dialog.getByRole("button", { name: "View rate_limits value" }).click();
  const rate = dialog.getByRole("region", { name: "rate_limits pinned value" });
  await expect(rate.locator("pre")).toHaveText("7");
  expect(parameterReads.at(-1)?.searchParams.get("version")).toBe("2");

  await dialog.getByRole("searchbox", { name: "Filter parameters" }).fill("features");
  await dialog.getByRole("button", { name: "View features value" }).click();
  const features = dialog.getByRole("region", { name: "features pinned value" });
  await expect(features.getByRole("button", { name: "Copy features value" })).toBeVisible();
  await expect(features.locator("pre")).toContainText("pool");
  await features.getByRole("button", { name: "Collapse all", exact: true }).click();
  await expect(features.locator("pre")).not.toContainText("pool");
  await features.getByRole("button", { name: "Expand all", exact: true }).click();
  await expect(features.locator("pre")).toContainText("pool");

  const fits = await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth + 1);
  expect(fits).toBe(true);
  if (process.env.CAPTURE_QA) {
    await page.screenshot({ path: testInfo.outputPath("release-values.png") });
  }
});
