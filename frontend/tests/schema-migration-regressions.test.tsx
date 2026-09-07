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

    const activate = within(dialog).getByRole("button", { name: "Activate migration" });
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
    fireEvent.click(within(dialog).getByRole("button", { name: "Activate migration" }));
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
    fireEvent.click(within(dialog).getByRole("button", { name: "Activate migration" }));
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

  it("includes the application's current schema in the target options", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(1), registeredSchema(2)],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps({ initialSchemaVersion: undefined })} />);
    const select = await screen.findByLabelText("Target registered schema");
    expect(within(select).getByRole("option", { name: /v1/ })).toHaveValue("1");
  });

  it.each(["abc", "1.5", "1e3", "-1", "9007199254740992"])(
    "rejects invalid exact version text %s before previewing",
    async (versionText) => {
      render(<SchemaMigrationModal {...modalProps()} />);
      const dialog = screen.getByRole("dialog");
      await reachValues(dialog);
      fireEvent.change(within(dialog).getByLabelText("new_setting exact version"), {
        target: { value: versionText },
      });

      expect(within(dialog).getByText(/positive whole number|too large/)).toBeVisible();
      expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeDisabled();
      expect(mocks.migrateApplicationSchema).not.toHaveBeenCalled();
    },
  );

  it("allows a blank version for parameter writes and requires one for secrets", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { database: { type: "object" } })],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    fireEvent.change(within(dialog).getByLabelText("database exact version"), {
      target: { value: "" },
    });
    expect(within(dialog).queryByText(/exact version for a secret/)).not.toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Back" }));
    const secretRow = within(dialog)
      .getByDisplayValue("db_password")
      .closest(".migration-contract-row");
    if (!(secretRow instanceof HTMLElement)) throw new Error("secret row missing");
    fireEvent.change(within(secretRow).getByLabelText("Source alias"), { target: { value: "" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    expect(within(dialog).getByText(/exact version for a secret/)).toBeVisible();
    expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeDisabled();
  });

  it("shows schema-derived removals and type changes during contract review", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { rate_limits: { type: "string" } })],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);

    expect(within(dialog).getByRole("note")).toHaveTextContent(
      "`rate_limits` changed from integer to string",
    );
    expect(within(dialog).getByRole("note")).toHaveTextContent(
      "`database` is not a schema property and was dropped",
    );
  });

  it("clears all source and loaded-value state when kind changes in either direction", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { database: { type: "object" } })],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    const row = within(dialog).getByDisplayValue("database").closest(".migration-contract-row");
    if (!(row instanceof HTMLElement)) throw new Error("database row missing");

    fireEvent.change(within(row).getByLabelText("Kind"), { target: { value: "secret" } });
    expect(within(row).getByLabelText("Source alias")).toHaveValue("");
    fireEvent.change(within(row).getByLabelText("Kind"), { target: { value: "parameter" } });
    expect(within(row).getByLabelText("Source alias")).toHaveValue("");
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    expect(within(dialog).getByLabelText("database resource key")).toHaveValue("database");
    expect(within(dialog).getByLabelText("database exact version")).toHaveValue("");
    expect(within(dialog).getByLabelText("database value")).toHaveValue("");
    expect(mocks.getParameter).not.toHaveBeenCalled();
  });

  it("warns only for different-schema environments using definite activation language", async () => {
    mocks.migrateApplicationSchema.mockResolvedValue(
      migrationResult({
        affected_environments: [
          { environment: "same", active_version: 3, schema_version: 2 },
          { environment: "old", active_version: 4, schema_version: 1 },
          { environment: "inactive", active_version: 0, schema_version: 1 },
        ],
      }),
    );
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachPreview(dialog);
    const warning = within(dialog).getByText(
      "Global schema pin affects other environments",
    ).parentElement;
    expect(warning).toHaveTextContent("old (release v4, schema v1)");
    expect(warning).not.toHaveTextContent("same (release v3, schema v2)");
    expect(warning).not.toHaveTextContent("inactive");
    expect(warning).toHaveTextContent("will not activate or roll back until they are migrated");
    expect(warning).toHaveTextContent("The contract change applies globally");
  });
});
