import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("empty release resources stay closed and validation does not shift the row", async ({
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium", "desktop release-builder layout check");
  const state = incidentState();
  state.application.contract.unshift({ alias: "anthropic_api_key", kind: "secret" });
  state.namespaces.prod.secrets = {};
  // Exercise the production-sized case from the report: filtering must stay
  // responsive even when the resource list contains dozens of client-side items.
  for (let index = 1; index <= 60; index += 1) {
    const alias = `resource_${String(index).padStart(2, "0")}`;
    state.application.contract.push({ alias, kind: "parameter", content_type: "json" });
    for (const namespace of Object.values(state.namespaces)) {
      namespace.parameters[alias] = { key: alias, content_type: "json", versions: ["{}"] };
    }
  }
  await mockConsole(page, state);

  await page.goto("/releases?app=gradethis&env=prod");
  await page.getByRole("button", { name: "New release" }).first().click();
  const dialog = page.getByRole("dialog", { name: "New release · prod/gradethis" });
  await expect(dialog).toBeVisible();

  const resources = dialog.getByRole("combobox", { name: "Resource" });
  const emptyResource = resources.first();
  await expect(emptyResource).toBeDisabled();
  await expect(emptyResource).toHaveText("No matching secrets");

  const emptyBox = await emptyResource.boundingBox();
  expect(emptyBox).not.toBeNull();
  if (emptyBox) {
    await page.mouse.click(emptyBox.x + emptyBox.width / 2, emptyBox.y + emptyBox.height / 2);
  }
  await expect(page.getByRole("listbox")).toHaveCount(0);

  const populatedResource = resources.nth(1);
  await expect(populatedResource).toBeEnabled();
  await populatedResource.click();
  const resourceFilter = page.getByRole("combobox", { name: "Filter parameters…" });
  await expect(resourceFilter).toBeFocused();
  await expect(page.getByRole("option")).toHaveCount(61);
  await expect(page.getByRole("option", { name: "database" })).toBeVisible();
  const requestCountBeforeFilter = state.log.length;
  await resourceFilter.fill("base");
  await expect(page.getByRole("option", { name: "database" })).toBeVisible();
  await expect(page.getByRole("option")).toHaveCount(1);
  expect(state.log).toHaveLength(requestCountBeforeFilter);
  if (process.env.CAPTURE_QA) {
    await page.screenshot({ path: testInfo.outputPath("release-resource-filter.png") });
  }
  await resourceFilter.press("ArrowDown");
  await resourceFilter.press("Enter");
  await expect(populatedResource).toHaveText("database");
  await expect(page.locator('[data-slot="combobox-content"]')).toBeHidden();

  // The client-side query is transient; reopening starts with the complete list.
  await populatedResource.click();
  await expect(page.getByRole("combobox", { name: "Filter parameters…" })).toHaveValue("");
  await page.keyboard.press("Escape");
  await expect(page.locator('[data-slot="combobox-content"]')).toBeHidden();

  const topBefore = await emptyResource.evaluate((element) => element.getBoundingClientRect().top);
  const kindTop = await dialog
    .getByRole("combobox", { name: "Kind" })
    .first()
    .evaluate((element) => element.getBoundingClientRect().top);
  expect(topBefore).toBeCloseTo(kindTop, 0);

  await dialog.getByRole("button", { name: "Create release" }).click();
  await expect(dialog.getByText("Choose a resource.").first()).toBeVisible();
  const topAfter = await emptyResource.evaluate((element) => element.getBoundingClientRect().top);
  const kindTopAfter = await dialog
    .getByRole("combobox", { name: "Kind" })
    .first()
    .evaluate((element) => element.getBoundingClientRect().top);
  expect(topAfter - kindTopAfter).toBeCloseTo(topBefore - kindTop, 0);

  if (process.env.CAPTURE_QA) {
    await page.screenshot({ path: testInfo.outputPath("release-builder-validation.png") });
  }

  await page.setViewportSize({ width: 1024, height: 768 });
  const fit = await dialog.locator(".release-builder-entries").evaluate((element) => {
    const bounds = element.getBoundingClientRect();
    return {
      left: bounds.left,
      right: bounds.right,
      viewportWidth: window.innerWidth,
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
    };
  });
  expect(fit.left).toBeGreaterThanOrEqual(0);
  expect(fit.right).toBeLessThanOrEqual(fit.viewportWidth);
  expect(fit.scrollWidth).toBeLessThanOrEqual(fit.clientWidth);
  if (process.env.CAPTURE_QA) {
    await page.screenshot({ path: testInfo.outputPath("release-builder-validation-narrow.png") });
    await populatedResource.click();
    await expect(page.getByRole("combobox", { name: "Filter parameters…" })).toBeFocused();
    await page.screenshot({ path: testInfo.outputPath("release-resource-filter-narrow.png") });
    await page.keyboard.press("Escape");
  }
});

test("a free-form entry keeps its empty resource picker closed across kind changes", async ({
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium", "desktop release-builder interaction check");
  const state = incidentState();
  state.application.contract = [];
  await mockConsole(page, state);

  await page.goto("/releases?app=gradethis&env=prod");
  await page.getByRole("button", { name: "New release" }).first().click();
  const dialog = page.getByRole("dialog", { name: "New release · prod/gradethis" });
  const resource = dialog.getByRole("combobox", { name: "Resource" });
  await expect(resource).toBeDisabled();
  await expect(resource).toHaveText("No matching parameters");

  await dialog.getByRole("combobox", { name: "Kind" }).click();
  await page.getByRole("option", { name: "Secret" }).click();
  await expect(resource).toBeDisabled();
  await expect(resource).toHaveText("No matching secrets");
  await expect(page.getByRole("listbox")).toHaveCount(0);
});
