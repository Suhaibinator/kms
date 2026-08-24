import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RollbackDialogProps } from "@/components/applications/contracts";
import RollbackDialog from "@/components/ship/RollbackDialog";
import { ApiError } from "@/lib/api";
import type { ApplicationOverview, OverviewActiveRelease } from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";

const mocks = vi.hoisted(() => ({
  validateRelease: vi.fn(),
  rollbackRelease: vi.fn(),
  getActiveRelease: vi.fn(),
}));

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      validateRelease: mocks.validateRelease,
      rollbackRelease: mocks.rollbackRelease,
      getActiveRelease: mocks.getActiveRelease,
    },
  };
});

const incident = incidentJson as unknown as ApplicationOverview;
const prod = incident.environments.find((env) => env.namespace.env === "prod");
const active = prod?.release.active as OverviewActiveRelease;
const name = incident.application.release_name;
const prodNs = { env: "prod", app: incident.application.name };
const devNs = { env: "dev", app: incident.application.name };

const rolledBack = {
  release: { ...prod?.release.active, namespace: prodNs, version: active.previous_version },
  activation_revision: active.activation_revision + 1,
  previous_version: active.version,
  rolled_back_from: active.version,
  changed: true,
};

function renderDialog(overrides: Partial<RollbackDialogProps> = {}) {
  const props: RollbackDialogProps = {
    namespace: prodNs,
    name,
    active,
    open: true,
    onClose: vi.fn(),
    onDone: vi.fn(),
    ...overrides,
  };
  render(<RollbackDialog {...props} />);
  return props;
}

function dialog(): HTMLElement {
  return screen.getByRole("dialog", { name: "Roll back release?" });
}

function confirmButton(): HTMLElement {
  return within(dialog()).getByTestId("rollback-confirm");
}

describe("RollbackDialog", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.validateRelease.mockResolvedValue({ valid: true, errors: [] });
    mocks.rollbackRelease.mockResolvedValue(rolledBack);
  });

  it("validates the previous release on open and rolls back with the CAS guard", async () => {
    const props = renderDialog({ namespace: devNs });
    await waitFor(() =>
      expect(mocks.validateRelease).toHaveBeenCalledWith(devNs, name, active.previous_version),
    );
    const check = await within(dialog()).findByTestId("rollback-check");
    expect(check).toHaveTextContent(`${name}@${active.previous_version}`);
    expect(check).toHaveTextContent("is valid");
    // Not production: no typed confirmation.
    expect(within(dialog()).queryByTestId("rollback-confirm-env")).toBeNull();
    expect(confirmButton()).toBeEnabled();

    fireEvent.click(confirmButton());
    await waitFor(() =>
      expect(mocks.rollbackRelease).toHaveBeenCalledWith({
        env: "dev",
        app: incident.application.name,
        name,
        expected_current_version: active.version,
      }),
    );
    expect(props.onDone).toHaveBeenCalledWith(rolledBack);
  });

  it("keeps Confirm disabled on production until the environment name is typed", async () => {
    renderDialog();
    await within(dialog()).findByText("is valid and can be activated.");
    expect(confirmButton()).toBeDisabled();
    const field = within(dialog()).getByTestId("rollback-confirm-env");
    fireEvent.change(field, { target: { value: "pro" } });
    expect(confirmButton()).toBeDisabled();
    fireEvent.change(field, { target: { value: "prod" } });
    expect(confirmButton()).toBeEnabled();
  });

  it("shows violations and disables Confirm when the previous release no longer validates", async () => {
    mocks.validateRelease.mockResolvedValue({
      valid: false,
      errors: [
        {
          alias: "db_password",
          code: "secret_disabled",
          schema_pointer: "",
          message: "secret version 1 is disabled",
        },
      ],
    });
    renderDialog();
    const check = await within(dialog()).findByTestId("rollback-check");
    await within(check).findByText("secret version 1 is disabled");
    expect(check).toHaveTextContent("can no longer be activated");
    expect(
      within(check).getByRole("link", { name: "Activate a different version…" }),
    ).toHaveAttribute("href", `/releases?app=${incident.application.name}&env=prod&name=${name}`);
    expect(confirmButton()).toBeDisabled();
    expect(within(dialog()).queryByTestId("rollback-confirm-env")).toBeNull();
  });

  it("explains a 412 without violations", async () => {
    mocks.validateRelease.mockRejectedValue(
      new ApiError("failed_precondition", "contract changed since the release was created", 412),
    );
    renderDialog();
    const check = await within(dialog()).findByTestId("rollback-check");
    await within(check).findByText("contract changed since the release was created");
    expect(check).toHaveTextContent("blocked");
    expect(confirmButton()).toBeDisabled();
  });

  it("asks to refresh on a 409 and re-validates against the new active version", async () => {
    mocks.rollbackRelease.mockRejectedValueOnce(
      new ApiError("already_exists", "active version moved", 409),
    );
    mocks.getActiveRelease.mockResolvedValue({
      release: { ...prod?.release.active, namespace: prodNs, version: active.version + 1 },
      activation_revision: active.activation_revision + 1,
      previous_version: active.version,
    });
    const props = renderDialog({ namespace: devNs });
    await within(dialog()).findByText("is valid and can be activated.");
    fireEvent.click(confirmButton());

    const notice = await within(dialog()).findByRole("alert");
    expect(notice).toHaveTextContent("changed meanwhile");
    expect(props.onDone).not.toHaveBeenCalled();

    fireEvent.click(within(dialog()).getByRole("button", { name: "Refresh" }));
    await waitFor(() =>
      expect(mocks.validateRelease).toHaveBeenLastCalledWith(devNs, name, active.version),
    );
    expect(within(dialog()).getByTestId("rollback-check")).toHaveTextContent(
      `${name}@${active.version}`,
    );
    fireEvent.click(confirmButton());
    await waitFor(() =>
      expect(mocks.rollbackRelease).toHaveBeenLastCalledWith({
        env: "dev",
        app: incident.application.name,
        name,
        expected_current_version: active.version + 1,
      }),
    );
  });

  it("reports an already-active previous release without treating it as a failure", async () => {
    mocks.rollbackRelease.mockResolvedValue({ ...rolledBack, changed: false });
    const props = renderDialog({ namespace: devNs });
    await within(dialog()).findByText("is valid and can be activated.");
    fireEvent.click(confirmButton());
    expect(await within(dialog()).findByRole("status")).toHaveTextContent("is already active");
    expect(props.onDone).toHaveBeenCalledWith({ ...rolledBack, changed: false });
    expect(confirmButton()).toBeDisabled();
    expect(within(dialog()).getByRole("button", { name: "Close" })).toBeVisible();
  });

  it("has nothing to roll back to when there is no previous version", async () => {
    renderDialog({ active: { ...active, previous_version: 0 } });
    expect(within(dialog()).getByText(/no previous release/)).toBeVisible();
    expect(mocks.validateRelease).not.toHaveBeenCalled();
    expect(confirmButton()).toBeDisabled();
  });
});
