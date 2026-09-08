import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

  it("offers the current application schema for a lagging environment", async () => {
    const target = registeredSchema(2);
    mocks.listSchemas.mockResolvedValue({
      schemas: [target, registeredSchema(1)],
      next_page_token: "",
    });
    render(
      <SchemaMigrationModal
        {...modalProps({
          application: { ...overview.application, schema_version: 2 },
          environments: [
            {
              ...dev,
              release: { ...dev.release, active: { ...dev.release.active!, schema_version: 1 } },
            },
          ],
          initialSchemaVersion: 2,
        })}
      />,
    );
    const dialog = screen.getByRole("dialog");
    await waitFor(() =>
      expect(within(dialog).getByLabelText("Target registered schema")).toHaveValue("2"),
    );
    await reachPreview(dialog);
    expect(mocks.migrateApplicationSchema.mock.calls[0][1]).toMatchObject({ schema_version: 2 });
  });

  it("submits an untouched empty string after detaching a parameter source", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { database: { type: "string" } })],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    const row = within(dialog)
      .getByDisplayValue("database")
      .closest(".migration-contract-row") as HTMLElement;
    fireEvent.change(within(row).getByLabelText("Source alias"), { target: { value: "" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    expect(within(dialog).getByLabelText("database value")).toHaveValue("");
    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalled());
    expect(mocks.migrateApplicationSchema.mock.calls[0][1].changes).toEqual(
      expect.arrayContaining([
        { alias: "database", key: "database", value: "", content_type: "string" },
      ]),
    );
  });

  it("retains incomplete form drafts while filtered and blocks preview until repaired", async () => {
    const target = registeredSchema(2);
    target.schema_json = JSON.stringify({
      type: "object",
      properties: {
        database: { type: "object", properties: { count: { type: "number" } } },
        new_setting: { type: "string" },
      },
    });
    mocks.listSchemas.mockResolvedValue({ schemas: [target], next_page_token: "" });
    mocks.getParameter.mockResolvedValue({
      parameter: { value: '{"count":3}', content_type: "json" },
    });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    const count = await within(dialog).findByRole("textbox", { name: "count" });
    await waitFor(() => expect(count).toHaveValue("3"));
    fireEvent.change(count, { target: { value: "4" } });
    fireEvent.change(count, { target: { value: "4e" } });
    let previewButton = within(dialog).getByRole("button", { name: /Preview migration/ });
    expect(previewButton).toBeDisabled();
    const search = within(dialog).getByLabelText("Search fields or schema paths");
    fireEvent.change(search, { target: { value: "new_setting" } });
    expect(count).not.toBeVisible();
    expect(previewButton).toBeDisabled();
    fireEvent.click(previewButton);
    expect(mocks.migrateApplicationSchema).not.toHaveBeenCalled();
    fireEvent.change(search, { target: { value: "" } });
    expect(count).toHaveValue("4e");
    fireEvent.click(within(dialog).getByRole("button", { name: "Back" }));
    expect(count).not.toBeVisible();
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    expect(count).toHaveValue("4e");
    previewButton = within(dialog).getByRole("button", { name: /Preview migration/ });
    expect(previewButton).toBeDisabled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Back" }));
    const mappingRow = within(dialog)
      .getAllByDisplayValue("database")
      .find((node) => node.closest(".migration-contract-row"))
      ?.closest(".migration-contract-row") as HTMLElement;
    fireEvent.change(within(mappingRow).getByLabelText("Source alias"), { target: { value: "" } });
    fireEvent.change(within(mappingRow).getByLabelText("Source alias"), {
      target: { value: "database" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    await waitFor(() => expect(count).toHaveValue("3"));
    previewButton = within(dialog).getByRole("button", { name: /Preview migration/ });
    fireEvent.change(count, { target: { value: "4e2" } });
    await waitFor(() => expect(previewButton).toBeEnabled());
    fireEvent.click(previewButton);
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalled());
    expect(
      mocks.migrateApplicationSchema.mock.calls[0][1].changes.find(
        (change: { alias: string }) => change.alias === "database",
      ).value,
    ).toBe(JSON.stringify({ count: 400 }, null, 2));
  });

  it("allows preview after replacing an invalid parameter with a valid secret", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { new_setting: { type: "integer" } })],
      next_page_token: "",
    });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeDisabled();

    fireEvent.click(within(dialog).getByRole("button", { name: "Back" }));
    const row = within(dialog)
      .getAllByLabelText("Alias")
      .find((element) => (element as HTMLInputElement).value === "new_setting")
      ?.closest(".migration-contract-row") as HTMLElement;
    fireEvent.change(within(row).getByLabelText("Kind"), { target: { value: "secret" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    fireEvent.change(within(dialog).getByLabelText("new_setting exact version"), {
      target: { value: "1" },
    });

    expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeEnabled();
  });

  it("uses target fields, prepares obsolete properties explicitly, and restores the original draft", async () => {
    const target = registeredSchema(overview.application.schema_version + 1);
    target.schema_json = JSON.stringify({
      type: "object",
      properties: {
        database: {
          type: "object",
          additionalProperties: false,
          required: ["urls"],
          properties: {
            urls: { type: "array", items: { type: "string" } },
            count: { type: "integer" },
            host: { type: "string" },
          },
        },
        rate_limits: { type: "integer" },
      },
    });
    mocks.listSchemas.mockResolvedValue({ schemas: [target], next_page_token: "" });
    const original = '{"host":"retained","old":"obsolete","count":10}';
    mocks.getParameter.mockImplementation((ref: { key: string }) =>
      Promise.resolve({
        parameter: {
          value: ref.key === "database" ? original : "300",
          content_type: ref.key === "database" ? "json" : "integer",
        },
      }),
    );
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    await waitFor(() =>
      expect(within(dialog).getByRole("textbox", { name: "host" })).toHaveValue("retained"),
    );
    const editor = within(dialog)
      .getByRole("group", { name: "database value", hidden: true })
      .closest("details")!;
    fireEvent.click(editor.querySelector("summary")!);
    expect(within(dialog).queryByRole("textbox", { name: "old" })).toBeNull();
    expect(within(dialog).getByText("Target schema v2")).toBeVisible();
    const prepare = within(dialog).getByRole("region", { name: "Prepare database" });
    expect(prepare).toHaveTextContent("Initialize 1 empty list: urls");
    expect(prepare).toHaveTextContent("Remove: old");
    fireEvent.click(within(prepare).getByRole("button", { name: "Prepare draft" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Preview migration" }));
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalled());
    const request = mocks.migrateApplicationSchema.mock.calls[0][1] as SchemaMigrationRequest;
    expect(request.changes.find((change) => change.alias === "database")?.value).toBe(
      '{"host":"retained","count":10,"urls":[]}',
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Back" }));
    fireEvent.change(within(dialog).getByRole("textbox", { name: "count" }), {
      target: { value: "20e" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore pre-preparation value" }));
    expect(within(dialog).getByRole("textbox", { name: "count" })).toHaveValue("10");
    expect(within(dialog).getByRole("region", { name: "Prepare database" })).toHaveTextContent(
      "Remove: old",
    );
    expect(within(dialog).getByRole("textbox", { name: "host" })).toHaveValue("retained");
    fireEvent.click(within(dialog).getByRole("button", { name: "Prepare draft" }));
    let resolvePin!: (result: unknown) => void;
    mocks.getParameter.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePin = resolve;
        }),
    );
    fireEvent.change(within(dialog).getByLabelText("database exact version"), {
      target: { value: "2" },
    });
    expect(
      within(dialog).queryByRole("button", { name: "Restore pre-preparation value" }),
    ).toBeNull();
    await waitFor(() => expect(resolvePin).toBeDefined());
    resolvePin({ parameter: { value: '{"host":"replacement","urls":[]}', content_type: "json" } });
    await waitFor(() =>
      expect(within(dialog).getByRole("textbox", { name: "host" })).toHaveValue("replacement"),
    );
    await waitFor(() =>
      expect(within(dialog).getByRole("button", { name: "Preview migration" })).toBeEnabled(),
    );
  });

  it("keeps authoritative removal details visible in a filtered preview", async () => {
    mocks.migrateApplicationSchema.mockResolvedValue(
      migrationResult({
        entries: [
          {
            alias: "removed_pin",
            key: "original_key",
            kind: "parameter",
            source: "removed",
            from_version: 7,
            to_version: 0,
          },
        ],
      }),
    );
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    fireEvent.change(within(dialog).getByLabelText("Search fields or schema paths"), {
      target: { value: "database" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Preview migration" }));
    await waitFor(() =>
      expect(within(dialog).getByText("Backend validation passed.")).toBeVisible(),
    );
    expect(within(dialog).getByRole("region", { name: "Removed aliases" })).toHaveTextContent(
      "removed_pin · removed from release · key original_key · v7",
    );
    expect(within(dialog).getByText(/Search and filters are active/)).toBeVisible();
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
        await waitFor(() =>
          expect(screen.getAllByText(/Too large/).some((node) => !node.closest("[hidden]"))).toBe(
            true,
          ),
        );
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
      .getAllByDisplayValue("db_password")
      .find((node) => node.closest(".migration-contract-row"))
      ?.closest(".migration-contract-row");
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
  it("puts validation reasons above the preview table and jumps to the value without losing edits", async () => {
    mocks.listSchemas.mockResolvedValue({
      schemas: [registeredSchema(2, { rate_limits: { type: "integer" } })],
      next_page_token: "",
    });
    mocks.getParameter.mockResolvedValue({ parameter: { value: "300", content_type: "integer" } });
    mocks.migrateApplicationSchema.mockResolvedValue(
      migrationResult({
        valid: false,
        validation: [
          {
            alias: "rate_limits",
            code: "schema",
            schema_pointer: "/minimum",
            message: "Increase to the required minimum.",
          },
        ],
      }),
    );
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    await waitFor(() =>
      expect(within(dialog).getByLabelText("rate_limits value")).toHaveValue("300"),
    );
    fireEvent.change(within(dialog).getByLabelText("rate_limits value"), {
      target: { value: "20" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    const fix = await within(dialog).findByRole("button", { name: "rate_limits · Fix field" });
    const reasons = within(dialog).getByRole("list", { name: "Validation problems" });
    expect(
      reasons.compareDocumentPosition(within(dialog).getByRole("table")) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    fireEvent.click(fix);
    const input = within(dialog).getByLabelText("rate_limits value");
    await waitFor(() => expect(input).toHaveFocus());
    expect(input).toHaveValue("20");
    expect(input.closest("details")).toHaveAttribute("open");
    fireEvent.change(input, { target: { value: "21" } });
    expect(input.closest("details")).toHaveAttribute("open");
    expect(input).toHaveFocus();
  });

  it("keeps rows stable during edits, filters by nested path, and preserves collapse choices across steps", async () => {
    const old = registeredSchema(1, {
      database: { type: "object" },
      rate_limits: { type: "integer" },
    });
    const target = {
      ...registeredSchema(2),
      schema_json: JSON.stringify({
        type: "object",
        properties: {
          database: {
            type: "object",
            properties: {
              connection: { type: "object", properties: { timeout: { type: "integer" } } },
            },
          },
          rate_limits: { type: "integer" },
        },
      }),
    };
    mocks.listSchemas.mockResolvedValue({ schemas: [target, old], next_page_token: "" });
    mocks.getParameter.mockImplementation((ref: { key: string }) =>
      Promise.resolve({
        parameter: {
          value: ref.key === "database" ? "{}" : "300",
          content_type: ref.key === "database" ? "json" : "integer",
        },
      }),
    );
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    const rows = () =>
      Array.from(dialog.querySelectorAll("details[id^=upgrade-field]:not([hidden])")).map(
        (node) => node.id,
      );
    const before = rows();
    await waitFor(() =>
      expect(within(dialog).getByLabelText("rate_limits value")).toHaveValue("300"),
    );
    fireEvent.change(within(dialog).getByLabelText("rate_limits value"), {
      target: { value: "301" },
    });
    expect(rows()).toEqual(before);
    fireEvent.change(within(dialog).getByLabelText("Search fields or schema paths"), {
      target: { value: "database.connection.timeout" },
    });
    expect(rows()).toHaveLength(1);
    fireEvent.click(within(dialog).getByRole("button", { name: "Next change" }));
    const database = within(dialog)
      .getByRole("group", { name: "database value", hidden: true })
      .closest("details")!;
    expect(database).toHaveAttribute("open");
    expect(database.querySelector("summary")).toHaveFocus();
    database.open = false;
    fireEvent(database, new Event("toggle"));
    fireEvent.click(within(dialog).getByRole("button", { name: "Back" }));
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    expect(
      within(dialog)
        .getByRole("group", { name: "database value", hidden: true })
        .closest("details"),
    ).not.toHaveAttribute("open");
    expect(within(dialog).getByLabelText("rate_limits value")).toHaveValue("301");
  });
  it("uses an adopted destination contract and carries a renamed secret pin separately", async () => {
    const target = registeredSchema(overview.application.schema_version + 1);
    target.contract = [{ alias: "new_token", kind: "secret", content_type: "" }];
    mocks.listSchemas.mockResolvedValue({ schemas: [target], next_page_token: "" });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    const aliases = within(dialog).getAllByLabelText("Alias");
    expect(aliases).toHaveLength(1);
    expect(aliases[0]).toHaveValue("new_token");
    expect(aliases[0]).toBeDisabled();
    const secret = dev.release.active!.entries.find((entry) => entry.kind === "secret")!;
    fireEvent.change(within(dialog).getByLabelText("Source alias"), {
      target: { value: secret.alias },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalled());
    expect(mocks.migrateApplicationSchema.mock.calls[0][1]).toMatchObject({
      contract: [{ alias: "new_token", kind: "secret" }],
      changes: [{ alias: "new_token", from_alias: secret.alias }],
      source_schema_version: dev.release.active!.schema_version,
      schema_version: target.version,
    });
  });

  it("does not populate an established empty destination with source fields", async () => {
    const target = { ...registeredSchema(overview.application.schema_version + 1), contract: [] };
    mocks.listSchemas.mockResolvedValue({ schemas: [target], next_page_token: "" });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    expect(within(dialog).queryAllByLabelText("Alias")).toHaveLength(0);
    expect(within(dialog).getByText(/established empty contract/)).toBeVisible();
    expect(within(dialog).getByRole("button", { name: /Edit values/ })).toBeDisabled();
    expect(mocks.migrateApplicationSchema).not.toHaveBeenCalled();
  });

  it("keeps the adopted parameter content type even when its schema suggests another type", async () => {
    const target = registeredSchema(overview.application.schema_version + 1, {
      count: { type: "integer" },
    });
    target.contract = [{ alias: "count", kind: "parameter", content_type: "json" }];
    mocks.listSchemas.mockResolvedValue({ schemas: [target], next_page_token: "" });
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    expect(within(dialog).getByLabelText("Content type")).toHaveValue("json");
    expect(within(dialog).getByLabelText("Content type")).toBeDisabled();
    expect(within(dialog).getAllByLabelText("Alias")).toHaveLength(1);
  });
  it("discards an in-flight preview when the explicit source track changes", async () => {
    let resolvePreview!: (value: SchemaMigrationResponse) => void;
    mocks.migrateApplicationSchema.mockReturnValue(
      new Promise((resolve) => {
        resolvePreview = resolve;
      }),
    );
    const props = modalProps();
    const view = render(<SchemaMigrationModal {...props} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    const previewButton = within(dialog).getByRole("button", { name: /Preview migration/ });
    await waitFor(() => expect(previewButton).toBeEnabled());
    fireEvent.click(previewButton);
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalled());
    view.rerender(
      <SchemaMigrationModal
        {...props}
        application={{ ...props.application, schema_version: 0 }}
        environments={props.environments.map((item) => ({
          ...item,
          release: {
            ...item.release,
            active: item.release.active ? { ...item.release.active, schema_version: 0 } : undefined,
          },
        }))}
      />,
    );
    await act(async () => resolvePreview(migrationResult()));
    expect(within(dialog).queryByText("Backend validation passed.")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Review contract/ })).toBeVisible();
  });
});
