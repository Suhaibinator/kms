import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

async function openPolicyRule(page: Parameters<typeof mockConsole>[0]) {
  const state = incidentState();
  await mockConsole(page, state);
  // The shared console fake intentionally focuses on application/release flows.
  // Add the two policy-page list endpoints here and let all other requests fall
  // through to it.
  await page.route("**/api/v1/policies*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ policies: [], next_page_token: "" }),
    });
  });
  await page.route("**/api/v1/identities*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ identities: [], next_page_token: "" }),
    });
  });

  await page.goto("/policies");
  await page.getByRole("button", { name: "New policy" }).first().click();
  const dialog = page.getByRole("dialog", { name: "New policy" });
  await dialog.getByPlaceholder("gradethis-read").fill("read-all");
  await dialog.getByPlaceholder("gradethis-be").fill("*");
  await dialog.getByRole("button", { name: "Add rule" }).first().click();
  return dialog;
}

test("policy validation stays below its control without shifting the rule row", async ({
  page,
}, testInfo) => {
  test.skip((page.viewportSize()?.width ?? 1280) <= 768, "desktop policy layout check");
  const dialog = await openPolicyRule(page);
  const row = dialog.locator(".rule-row").first();
  const operation = row.getByRole("combobox", { name: "Operation" });
  const app = row.getByPlaceholder("gradethis");
  const env = row.getByPlaceholder("prod");
  const duplicate = row.getByRole("button", { name: /Duplicate allow rule/ });
  const remove = row.getByRole("button", { name: /Remove allow rule/ });
  const controls = [operation, app, env, duplicate, remove];

  const before = await Promise.all(
    controls.map((control) => control.evaluate((element) => element.getBoundingClientRect().top)),
  );
  for (const top of before.slice(1)) expect(top).toBeCloseTo(before[0], 0);

  await dialog.getByRole("button", { name: "Create policy" }).click();
  await expect(dialog.getByText("Operation is required.")).toBeVisible();
  const after = await Promise.all(
    controls.map((control) => control.evaluate((element) => element.getBoundingClientRect().top)),
  );
  // The centred modal can recalculate its own top as content grows; compare
  // controls to their siblings so the assertion targets the row-level jump.
  for (const top of after.slice(1)) expect(top).toBeCloseTo(after[0], 0);

  if (process.env.CAPTURE_QA) {
    await page.screenshot({ path: testInfo.outputPath("policy-rule-validation.png") });
  }
});

test("a long operation neither widens its column nor wraps the row", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 1280) <= 768, "desktop policy layout check");
  const dialog = await openPolicyRule(page);
  const row = dialog.locator(".rule-row").first();
  await row.getByRole("combobox", { name: "Operation" }).click();
  await page.getByRole("option", { name: "configuration-release:validate" }).click();

  // .rule-op has a definite basis, so a 30-character option cannot set the
  // column's width from its content and push "Remove" onto a second line.
  const controls = [
    row.getByRole("combobox", { name: "Operation" }),
    row.getByPlaceholder("gradethis"),
    row.getByPlaceholder("prod"),
    row.getByRole("button", { name: /Duplicate allow rule/ }),
    row.getByRole("button", { name: /Remove allow rule/ }),
  ];
  const tops = await Promise.all(
    controls.map((control) => control.evaluate((el) => el.getBoundingClientRect().top)),
  );
  for (const top of tops.slice(1)) expect(top).toBeCloseTo(tops[0], 0);
  const op = await row.evaluate(
    (element) => element.querySelector(".rule-op")?.getBoundingClientRect().width ?? 0,
  );
  expect(op).toBeCloseTo(180, 0);

  // The unknown-namespace sentence is a row-level note across the full row,
  // not a hint wrapping to five lines inside a 130px column.
  await row.getByPlaceholder("gradethis").fill("nope");
  await row.getByPlaceholder("prod").fill("alsonope");
  const note = row.locator(".rule-note");
  await expect(note).toBeVisible();
  const widths = await row.evaluate((element) => ({
    note: element.querySelector(".rule-note")?.getBoundingClientRect().width ?? 0,
    row: element.getBoundingClientRect().width,
  }));
  expect(widths.note).toBeCloseTo(widths.row, 0);
});

test("policy rule validation remains coherent when controls stack", async ({ page }, testInfo) => {
  test.skip((page.viewportSize()?.width ?? 1280) > 768, "mobile policy layout check");
  const dialog = await openPolicyRule(page);
  const row = dialog.locator(".rule-row").first();
  await dialog.getByRole("button", { name: "Create policy" }).click();
  await expect(dialog.getByText("Operation is required.")).toBeVisible();

  const layout = await row.evaluate((element) => {
    const rowBounds = element.getBoundingClientRect();
    const children = [...element.children].map((child) => {
      const bounds = child.getBoundingClientRect();
      return { left: bounds.left, right: bounds.right, top: bounds.top, bottom: bounds.bottom };
    });
    return {
      row: { left: rowBounds.left, right: rowBounds.right },
      children,
      viewportWidth: window.innerWidth,
    };
  });
  expect(layout.row.left).toBeGreaterThanOrEqual(0);
  expect(layout.row.right).toBeLessThanOrEqual(layout.viewportWidth);
  for (const child of layout.children) {
    expect(child.left).toBeGreaterThanOrEqual(layout.row.left);
    expect(child.right).toBeLessThanOrEqual(layout.row.right);
    expect(child.bottom - child.top).toBeLessThan(180);
  }
  for (let index = 1; index < layout.children.length; index += 1) {
    expect(layout.children[index].top).toBeGreaterThanOrEqual(layout.children[index - 1].bottom);
    expect(layout.children[index].top - layout.children[index - 1].bottom).toBeLessThanOrEqual(32);
  }

  if (process.env.CAPTURE_QA) {
    await page.screenshot({ path: testInfo.outputPath("policy-rule-validation-mobile.png") });
  }
});
