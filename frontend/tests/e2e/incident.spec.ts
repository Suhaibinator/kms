// The incident journey (plan §2.9): from the focused prod column, Edit & ship
// a value, watch the rollout report one rejected instance, roll back from the
// same modal, and see the column serve the previous release again. Five
// clicks plus typing, all against the in-memory fake.

import { expect, test } from "@playwright/test";
import { allApplied, incidentState, mockConsole, oneRejected } from "./fakes/console-api";

test("incident: edit & ship to prod, rejected instance, roll back to the previous release", async ({
  page,
}) => {
  const state = incidentState();
  const prod = state.namespaces.prod;
  const releaseName = state.application.release_name;
  const activeBefore = prod.active;
  const nextVersion = prod.releases.length + 1;
  const rateLimits = prod.parameters.rate_limits;
  const currentValue = rateLimits.versions[rateLimits.versions.length - 1];
  const rejectedId = prod.subscribers.find((row) => row.state === "rejected")?.instance_id ?? "";
  const instanceCount = prod.subscribers.length;

  // The ship activation leaves one instance rejected; the rollback heals it.
  state.onActivate = (ctx) =>
    ctx.kind === "ship"
      ? oneRejected(
          rejectedId,
          "config_validation_failed",
          "rate_limits must be greater than zero",
        )(ctx)
      : allApplied(ctx);
  await mockConsole(page, state);

  await page.goto("/applications?app=gradethis&env=prod");
  const column = page.locator('[data-env="prod"]');
  await expect(column).toBeVisible();
  await expect(column).toContainText(`${releaseName}@${activeBefore}`);

  // Click 1: the row's Edit & ship.
  await page.getByRole("button", { name: "Edit & ship rate_limits in prod" }).click();
  const modal = page.getByTestId("ship-modal");
  await expect(modal).toBeVisible();
  await expect(modal).toHaveAttribute("data-phase", "compose");

  // The editor is prefilled with the current value; edit it.
  const editor = modal.getByRole("textbox", { name: "rate_limits value" });
  await expect(editor).toHaveValue(currentValue);
  await editor.fill(rateLimits.content_type === "json" ? '{"per_minute": 250}' : "250");

  // The dry run arrives on its own and shows what would ship.
  const preview = modal.getByTestId("ship-preview");
  await expect(preview).toHaveAttribute("data-stale", "false");
  await expect(modal.getByTestId("ship-activation")).toHaveText(
    `${releaseName}@${activeBefore} → @${nextVersion}`,
  );
  await expect(preview.locator('tr[data-alias="rate_limits"]')).toHaveAttribute(
    "data-changed",
    "true",
  );
  await expect(modal.getByTestId("ship-validation")).toContainText("valid");

  // Production: Ship stays disabled until the environment name is typed.
  // Footer buttons sit outside the modal body element.
  const ship = page.getByTestId("ship-submit");
  await expect(ship).toBeDisabled();
  await modal.getByTestId("ship-confirm-env").fill("prod");
  await expect(ship).toBeEnabled();

  // Click 2: Ship.
  await ship.click();
  await expect(modal).toHaveAttribute("data-phase", "rollout");
  await expect(page.getByRole("dialog", { name: /Shipped/ })).toContainText(
    `${releaseName}@${nextVersion}`,
  );
  const shipCalls = state.log.filter(
    (entry) =>
      entry.path === "/applications/ship" && !(entry.body as { dry_run?: boolean }).dry_run,
  );
  expect(shipCalls).toHaveLength(1);
  expect((shipCalls[0].body as { expected_active_version: number }).expected_active_version).toBe(
    activeBefore,
  );

  // The rollout (polling — the fake has no stream) reports the rejected instance first.
  const rollout = modal.getByTestId("ship-rollout");
  await expect(rollout.getByTestId("rollout-progress")).toContainText(
    `${instanceCount - 1}/${instanceCount} applied`,
  );
  const rejected = rollout.locator('[data-testid="rollout-instance"][data-state="rejected"]');
  await expect(rejected).toHaveCount(1);
  await expect(rollout.getByTestId("rollout-instance").first()).toHaveAttribute(
    "data-state",
    "rejected",
  );
  await expect(rejected).toContainText("config_validation_failed");
  await expect(rejected).toContainText(`still serving v${activeBefore}`);
  await expect(rollout.getByRole("status")).toContainText(/Polling|Live/);

  // Click 3: Roll back, from the rollout itself.
  await page.getByTestId("ship-rollback").click();
  const dialog = page.getByRole("dialog", { name: "Roll back release?" });
  await expect(dialog.getByTestId("rollback-check")).toContainText(
    `${releaseName}@${activeBefore}`,
  );
  await expect(dialog.getByTestId("rollback-check")).toContainText("is valid");
  const confirm = dialog.getByTestId("rollback-confirm");
  await expect(confirm).toBeDisabled();
  await dialog.getByTestId("rollback-confirm-env").fill("prod");
  await expect(confirm).toBeEnabled();

  // Click 4: Confirm.
  await confirm.click();
  await expect(dialog).toBeHidden();
  await expect(modal.getByTestId("ship-rolled-back")).toContainText(
    `${releaseName}@${activeBefore}`,
  );
  const rollbackCall = state.log.find((entry) => entry.path === "/releases/rollback");
  expect(rollbackCall?.body).toMatchObject({
    env: "prod",
    app: "gradethis",
    name: releaseName,
    expected_current_version: nextVersion,
  });
  expect(prod.active).toBe(activeBefore);
  await expect(rollout.getByTestId("rollout-progress")).toContainText(
    `${instanceCount}/${instanceCount} applied`,
  );
  await expect(rejected).toHaveCount(0);

  // Click 5: Done — the column serves the previous release again.
  await page.getByTestId("ship-done").click();
  await expect(page.getByTestId("ship-modal")).toHaveCount(0);
  await expect(column).toContainText(`${releaseName}@${activeBefore}`);
});
