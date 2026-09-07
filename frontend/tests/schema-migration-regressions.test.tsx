import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SchemaMigrationModal } from "@/components/applications/SchemaMigrationModal";
import { ApiError } from "@/lib/api";
import type {
  ApplicationOverview,
  ConfigurationSchema,
  SchemaMigrationRequest,
  SchemaMigrationResponse,
} from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";

const mocks = vi.hoisted(() => ({
  listSchemas: vi.fn(),
  getParameter: vi.fn(),
  getActiveRelease: vi.fn(),
  migrateApplicationSchema: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      listSchemas: mocks.listSchemas,
      getParameter: mocks.getParameter,
      getActiveRelease: mocks.getActiveRelease,
      migrateApplicationSchema: mocks.migrateApplicationSchema,
    },
  };
});

const overview = incidentJson as unknown as ApplicationOverview;
const dev = overview.environments.find((item) => item.namespace.env === "dev")!;
const prod = overview.environments.find((item) => item.namespace.env === "prod")!;

function registeredSchema(
  version: number,
  properties: Record<string, { type: string }> = { new_setting: { type: "string" } },
): ConfigurationSchema {
  return {
    application: overview.application.name,
    release_name: overview.application.release_name,
    version,
    schema_json: JSON.stringify({ type: "object", properties }),
    digest: `sha256:schema-${version}`,
    metadata_json: "{}",
    created_by: "admin",
    created_at_unix_ms: 1,
  };
}

function migrationResult(
  overrides: Partial<SchemaMigrationResponse> = {},
): SchemaMigrationResponse {
  return {
    plan_digest: "sha256:plan",
    valid: true,
    executed: false,
    release_name: overview.application.release_name,
    source_version: dev.release.active!.version,
    source_activation_revision: dev.release.active!.activation_revision,
    schema_version: overview.application.schema_version + 1,
    entries: [],
    validation: [],
    affected_environments: [],
    definition_changed: true,
    ...overrides,
  };
}

function modalProps(overrides: Partial<React.ComponentProps<typeof SchemaMigrationModal>> = {}) {
  return {
    application: overview.application,
    environments: overview.environments,
    initialEnvironment: dev.namespace.env,
    initialSchemaVersion: overview.application.schema_version + 1,
    open: true,
    onClose: vi.fn(),
    onApplied: vi.fn(),
    ...overrides,
  };
}

async function reachContract(dialog: HTMLElement) {
  await waitFor(() =>
    expect(within(dialog).getByLabelText("Target registered schema")).toBeEnabled(),
  );
  fireEvent.click(within(dialog).getByRole("button", { name: /Review contract/ }));
}

async function reachValues(dialog: HTMLElement) {
  await reachContract(dialog);
  fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
}

async function reachPreview(dialog: HTMLElement) {
  await reachValues(dialog);
  const button = within(dialog).getByRole("button", { name: /Preview migration/ });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
  await waitFor(() => expect(within(dialog).getByText("Backend validation passed.")).toBeVisible());
}

describe("SchemaMigrationModal regressions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(overview.application.schema_version + 1)],
      next_page_token: "",
    });
    mocks.getParameter.mockResolvedValue({ parameter: { value: "old", content_type: "string" } });
    mocks.getActiveRelease.mockResolvedValue({
      release: dev.release.active,
      activation_revision: dev.release.active!.activation_revision,
    });
    mocks.migrateApplicationSchema.mockResolvedValue(migrationResult());
  });

  it("requires the production name and does not show success when execution is declined", async () => {
    const onApplied = vi.fn();
    render(
      <SchemaMigrationModal
        {...modalProps({ initialEnvironment: prod.namespace.env, onApplied })}
      />,
    );
    const dialog = screen.getByRole("dialog");
    await reachPreview(dialog);

    const activate = within(dialog).getByRole("button", { name: /Upgrade schema & ship to/ });
    expect(activate).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("Production confirmation"), {
      target: { value: prod.namespace.env },
    });
    expect(activate).toBeEnabled();
    fireEvent.click(activate);

    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalledTimes(2));
    expect(within(dialog).queryByText("Schema migration activated")).not.toBeInTheDocument();
    expect(onApplied).not.toHaveBeenCalled();
    expect(mocks.toast.error).toHaveBeenCalledWith(
      expect.objectContaining({ message: "The migration was not executed." }),
      "Could not apply migration",
    );
  });

  it("keeps edits after a conflict and uses refreshed source identity for the next preview", async () => {
    mocks.migrateApplicationSchema
      .mockResolvedValueOnce(migrationResult())
      .mockRejectedValueOnce(new ApiError("already_exists", "source changed", 409))
      .mockResolvedValueOnce(migrationResult());
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { db_password: { type: "string" } })],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    fireEvent.change(within(dialog).getByLabelText("Alias"), {
      target: { value: "renamed_password" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    await waitFor(() =>
      expect(within(dialog).getByText("Backend validation passed.")).toBeVisible(),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: /Upgrade schema & ship to/ }));
    await waitFor(() =>
      expect(within(dialog).getByLabelText("renamed_password resource key")).toHaveValue(
        "db_password",
      ),
    );

    expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeDisabled();
    const refreshedVersion = dev.release.active!.version + 1;
    const refreshedRevision = dev.release.active!.activation_revision + 1;
    mocks.getActiveRelease.mockResolvedValueOnce({
      release: {
        ...dev.release.active!,
        version: refreshedVersion,
        entries: dev.release.active!.entries.map((entry) =>
          entry.alias === "db_password" ? { ...entry, version: 2 } : entry,
        ),
      },
      activation_revision: refreshedRevision,
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Reload source" }));
    await waitFor(() => expect(mocks.getActiveRelease).toHaveBeenCalled());
    await waitFor(() =>
      expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeEnabled(),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalledTimes(3));

    const retried = mocks.migrateApplicationSchema.mock.calls[2][1] as SchemaMigrationRequest;
    expect(retried.changes[0]).toMatchObject({
      alias: "renamed_password",
      from_alias: "db_password",
      key: "db_password",
      version: 1,
    });
    expect(retried.expected_source_version).toBe(refreshedVersion);
    expect(retried.expected_source_activation_revision).toBe(refreshedRevision);
  });

  it("submits a new parameter's editable key and empty value without a version", async () => {
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    fireEvent.change(within(dialog).getByLabelText("new_setting resource key"), {
      target: { value: "blank-setting" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalled());

    const request = mocks.migrateApplicationSchema.mock.calls[0][1] as SchemaMigrationRequest;
    expect(request.changes[0]).toMatchObject({
      alias: "new_setting",
      key: "blank-setting",
      value: "",
      content_type: "string",
    });
    expect(request.changes[0].version).toBeUndefined();
  });

  it("does not infinitely retry a failed parameter read", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { database: { type: "object" } })],
      next_page_token: "",
    });
    mocks.getParameter.mockRejectedValue(new Error("read failed"));
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);

    await waitFor(() => expect(within(dialog).getByRole("alert")).toHaveTextContent("read failed"));
    await waitFor(() => expect(mocks.getParameter).toHaveBeenCalledTimes(1));
    await new Promise((resolve) => setTimeout(resolve, 25));
    expect(mocks.getParameter).toHaveBeenCalledTimes(1);
  });

  it("does not reset a draft or completed result when overview props rerender", async () => {
    const view = render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    fireEvent.change(within(dialog).getByLabelText("new_setting value"), {
      target: { value: "draft survives" },
    });
    view.rerender(
      <SchemaMigrationModal
        {...modalProps({ environments: structuredClone(overview.environments) })}
      />,
    );
    expect(within(dialog).getByLabelText("new_setting value")).toHaveValue("draft survives");

    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    await waitFor(() =>
      expect(within(dialog).getByText("Backend validation passed.")).toBeVisible(),
    );
    mocks.migrateApplicationSchema.mockResolvedValueOnce(
      migrationResult({
        executed: true,
        release: {
          namespace: dev.namespace,
          name: overview.application.release_name,
          version: 2,
          schema_version: 2,
          entries: [],
          digest: "sha256:release",
          metadata_json: "{}",
          created_by: "admin",
          created_at_unix_ms: 1,
        },
      }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: /Upgrade schema & ship to/ }));
    await waitFor(() =>
      expect(within(dialog).getByText("Schema migration activated")).toBeVisible(),
    );
    view.rerender(
      <SchemaMigrationModal
        {...modalProps({ environments: structuredClone(overview.environments) })}
      />,
    );
    expect(within(dialog).getByText("Schema migration activated")).toBeVisible();
  });

  it("offers only newer schemas as upgrade targets", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(1), registeredSchema(2)],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps({ initialSchemaVersion: undefined })} />);
    const select = await screen.findByLabelText("Target registered schema");
    expect(within(select).queryByRole("option", { name: /v1/ })).not.toBeInTheDocument();
    expect(select).toHaveValue("2");
  });
  it("blocks an upgrade when there is no newer registered schema", async () => {
    mocks.listSchemas.mockResolvedValue({ schemas: [registeredSchema(1)], next_page_token: "" });
    render(<SchemaMigrationModal {...modalProps()} />);
    await screen.findByText("No newer registered schema");
    expect(screen.getByRole("button", { name: /Review contract/ })).toBeDisabled();
  });

  it.each(["validation", "load failure"])(
    "keeps a retained row open while editing after %s",
    async (reason) => {
      mocks.listSchemas.mockResolvedValue({
        schemas: [registeredSchema(2, { rate_limits: { type: "integer" } })],
        next_page_token: "",
      });
      mocks.getParameter.mockResolvedValue({
        parameter: { value: "300", content_type: "integer" },
      });
      if (reason === "load failure")
        mocks.getParameter.mockRejectedValue(new Error("Pin unavailable"));
      else
        mocks.migrateApplicationSchema.mockResolvedValue(
          migrationResult({
            valid: false,
            validation: [
              {
                alias: "rate_limits",
                code: "invalid",
                schema_pointer: "/properties/rate_limits",
                message: "Too large",
              },
            ],
          }),
        );
      render(<SchemaMigrationModal {...modalProps()} />);
      const dialog = screen.getByRole("dialog");
      await reachValues(dialog);
      if (reason === "validation") {
        const previewButton = within(dialog).getByRole("button", { name: /Preview migration/ });
        await waitFor(() => expect(previewButton).toBeEnabled());
        fireEvent.click(previewButton);
        await screen.findByText(/Too large/);
        fireEvent.click(within(dialog).getByRole("button", { name: "Back" }));
      } else await screen.findByRole("alert");
      const input = within(dialog).getByLabelText(
        reason === "validation" ? "rate_limits value" : "rate_limits resource key",
      );
      const row = input.closest("details")!;
      await waitFor(() => expect(row).toHaveAttribute("open"));
      input.focus();
      fireEvent.change(input, {
        target: { value: reason === "validation" ? "20" : "another_key" },
      });
      expect(row).toHaveAttribute("open");
      expect(input).toHaveFocus();
      row.open = false;
      fireEvent(row, new Event("toggle"));
      expect(row).not.toHaveAttribute("open");
    },
  );
});
