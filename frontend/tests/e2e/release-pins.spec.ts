import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

test("operator assigns and removes a process pin with production confirmation", async ({
  page,
}) => {
  const state = incidentState();
  const space = state.namespaces.prod;
  const schema = state.application.schema_version || 0;
  const source = space.subscribers[0];
  const subscriber = {
    ...source,
    state: "applied" as const,
    connected: true,
    schema_version: schema,
    release_version: space.active,
    activation_revision: space.activationRevision,
    session_id: "client-process-a",
    pin_version: 0,
    pin_revision: 0,
    target_revision: space.activationRevision,
    desired_revision: space.activationRevision,
    desired_version: space.active,
    last_applied_version: space.active,
  };
  space.subscribers = [subscriber];
  await mockConsole(page, state);
  await page.route("**/api/v1/releases/validate", async (route) =>
    route.fulfill({ json: { valid: true, errors: [] } }),
  );
  const assignments: Record<string, unknown>[] = [];
  await page.route("**/api/v1/release-subscribers/pin", async (route) => {
    const request = route.request().postDataJSON();
    assignments.push(request);
    subscriber.pin_version = request.version;
    subscriber.pin_revision += 100;
    subscriber.desired_revision = subscriber.pin_revision;
    subscriber.desired_version = request.version || space.active;
    await route.fulfill({
      json: { pinned: request.version > 0, pin_revision: subscriber.pin_revision },
    });
  });
  await page.goto(`/releases?app=gradethis&env=prod&name=runtime&schema_version=${schema}`);
  await page.getByRole("button", { name: "View", exact: true }).first().click();
  await page.getByRole("tab", { name: "Rollout status" }).click();
  await page.getByRole("button", { name: "Pin to release", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Pin to release", exact: true });
  await expect(dialog).toContainText("KMS restarts preserve the pin");
  await dialog.getByRole("spinbutton", { name: "Release version" }).fill("1");
  await dialog.getByRole("button", { name: "Validate release" }).click();
  await expect(dialog.getByRole("button", { name: "Assign release" })).toBeDisabled();
  await dialog.getByRole("textbox", { name: "Confirm environment" }).fill("prod");
  await dialog.getByRole("button", { name: "Assign release" }).click();
  await expect(dialog).not.toBeVisible();
  expect(assignments[0]).toMatchObject({
    session_id: "client-process-a",
    version: 1,
    expected_pin_revision: 0,
    schema_version: schema,
  });
  await expect(page.getByTestId("rollout-instance")).toContainText(`v${space.active}`);
  await page.getByRole("button", { name: "Unpin", exact: true }).click();
  const unpin = page.getByRole("dialog", { name: "Unpin instance" });
  await unpin.getByRole("textbox", { name: "Confirm environment" }).fill("prod");
  await unpin.getByRole("button", { name: "Unpin", exact: true }).click();
  await expect(unpin).not.toBeVisible();
  expect(assignments[1]).toMatchObject({
    session_id: "client-process-a",
    version: 0,
    expected_pin_revision: 100,
  });
});
