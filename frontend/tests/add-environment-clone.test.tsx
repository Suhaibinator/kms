import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AddEnvironmentModal } from "@/components/applications/AddEnvironmentModal";
import CloneEnvironmentModal from "@/components/applications/CloneEnvironmentModal";
import type { ApplicationOverview, CloneEnvironmentResponse } from "@/lib/types";
import readyJson from "./fixtures/backend/overview-ready.json";
import { chooseSelectOption } from "./select-test-utils";

const mocks = vi.hoisted(() => ({
  cloneEnvironment: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return { ...actual, api: { ...actual.api, cloneEnvironment: mocks.cloneEnvironment } };
});

const ready = readyJson as unknown as ApplicationOverview;
// Only dev exists, so prod-eu and staging are new targets.
const environments = ready.environments.filter((environment) => !environment.production);

const cloneResult: CloneEnvironmentResponse = {
  namespace: { ...environments[0].namespace, env: "prod-eu" },
  namespace_created: true,
  items: [
    {
      alias: "database",
      key: "database",
      kind: "parameter",
      action: "copied",
      source_version: 3,
      target_version: 1,
    },
    {
      alias: "rate_limits",
      key: "rate_limits",
      kind: "parameter",
      action: "exists",
      target_version: 2,
    },
    { alias: "db_password", key: "db_password", kind: "secret", action: "needs_value" },
  ],
  needs_value: ["db_password"],
};

describe("AddEnvironmentModal with Start from", () => {
  it("hands over to the clone flow instead of creating an empty namespace", async () => {
    const onSave = vi.fn(async () => undefined);
    const onClone = vi.fn();
    render(
      <AddEnvironmentModal
        app={ready.application.name}
        environments={["dev"]}
        open
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
        onClone={onClone}
      />,
    );
    const modal = screen.getByRole("dialog");
    fireEvent.change(within(modal).getByLabelText("Environment"), { target: { value: "staging" } });
    await chooseSelectOption(within(modal).getByLabelText("Start from"), "Copy values from dev");
    fireEvent.click(within(modal).getByRole("button", { name: "Continue" }));
    expect(onClone).toHaveBeenCalledWith({
      source: "dev",
      target: "staging",
      description: "",
      methods: ["mtls"],
    });
    expect(onSave).not.toHaveBeenCalled();
  });

  it("still creates an empty environment by default", () => {
    const onSave = vi.fn(async () => undefined);
    render(
      <AddEnvironmentModal
        app={ready.application.name}
        environments={["dev"]}
        open
        saving={false}
        onClose={() => undefined}
        onSave={onSave}
        onClone={vi.fn()}
      />,
    );
    const modal = screen.getByRole("dialog");
    fireEvent.change(within(modal).getByLabelText("Environment"), { target: { value: "staging" } });
    fireEvent.click(within(modal).getByRole("button", { name: "Add environment" }));
    expect(onSave).toHaveBeenCalledWith("staging", "", ["mtls"]);
  });
});

describe("CloneEnvironmentModal", () => {
  beforeEach(() => {
    mocks.cloneEnvironment.mockReset().mockResolvedValue(cloneResult);
    mocks.toast.error.mockClear();
    mocks.toast.success.mockClear();
  });

  it("asks for the production name before cloning, then lists what still needs a value", async () => {
    const onCreated = vi.fn();
    const onAddSecret = vi.fn();
    render(
      <CloneEnvironmentModal
        application={ready.application}
        environments={environments}
        seed={{ source: "dev", target: "prod-eu", description: "EU", methods: ["mtls", "token"] }}
        open
        onClose={() => undefined}
        onCreated={onCreated}
        onAddSecret={onAddSecret}
      />,
    );
    const modal = screen.getByRole("dialog", { name: "Copy an environment" });
    expect(within(modal).getByLabelText("New environment")).toHaveValue("prod-eu");
    expect(within(modal).getByLabelText("Description")).toHaveValue("EU");
    expect(within(modal).getByRole("checkbox", { name: /bearer tokens/ })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    fireEvent.click(within(modal).getByRole("button", { name: "Create production environment…" }));
    const confirm = await screen.findByRole("dialog", { name: "Create prod-eu" });
    expect(mocks.cloneEnvironment).not.toHaveBeenCalled();
    const confirmButton = within(confirm).getByRole("button", { name: "Create environment" });
    expect(confirmButton).toBeDisabled();
    fireEvent.change(within(confirm).getByLabelText(/Type/), { target: { value: "prod-eu" } });
    expect(confirmButton).toBeEnabled();
    fireEvent.click(confirmButton);

    await waitFor(() => expect(mocks.cloneEnvironment).toHaveBeenCalledTimes(1));
    expect(mocks.cloneEnvironment).toHaveBeenCalledWith({
      application: ready.application.name,
      source_env: "dev",
      target_env: "prod-eu",
      copy_values: true,
      auth_methods: ["mtls", "token"],
      description: "EU",
    });
    const result = await screen.findByRole("dialog", { name: "prod-eu created from dev" });
    expect(within(result).getByText("v3 → v1")).toBeVisible();
    expect(within(result).getByText("kept v2")).toBeVisible();
    expect(within(result).getByText("Needs a value")).toBeVisible();
    expect(within(result).getByText(/Secret values are never copied/)).toBeVisible();
    fireEvent.click(within(result).getByRole("button", { name: "Add secret" }));
    expect(onAddSecret).toHaveBeenCalledWith("prod-eu", "db_password");
    fireEvent.click(within(result).getByRole("button", { name: "Done" }));
    expect(onCreated).toHaveBeenCalledWith(cloneResult);
  });

  it("clones a non-production target directly and refuses an existing name", async () => {
    render(
      <CloneEnvironmentModal
        application={ready.application}
        environments={environments}
        open
        onClose={() => undefined}
        onCreated={vi.fn()}
      />,
    );
    const modal = screen.getByRole("dialog");
    const target = within(modal).getByLabelText("New environment");
    fireEvent.change(target, { target: { value: "dev" } });
    fireEvent.blur(target);
    expect(within(modal).getByRole("alert")).toHaveTextContent("dev already exists");
    expect(within(modal).getByRole("button", { name: "Create environment" })).toBeDisabled();
    fireEvent.change(target, { target: { value: "staging" } });
    fireEvent.click(within(modal).getByRole("button", { name: "Create environment" }));
    await waitFor(() => expect(mocks.cloneEnvironment).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("dialog", { name: "Create staging" })).toBeNull();
    expect(mocks.cloneEnvironment.mock.calls[0][0]).toMatchObject({
      source_env: "dev",
      target_env: "staging",
      auth_methods: ["mtls"],
    });
  });
});
