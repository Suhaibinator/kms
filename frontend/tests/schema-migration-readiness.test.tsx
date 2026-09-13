import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SchemaMigrationModal } from "@/components/applications/SchemaMigrationModal";
import type {
  ApplicationOverview,
  ConfigurationSchema,
  ReleaseValidationError,
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
const sourceVersion = dev.release.active!.schema_version;
const targetVersion = sourceVersion + 1;

function registeredSchema(version: number, schema: unknown): ConfigurationSchema {
  return {
    application: overview.application.name,
    release_name: overview.application.release_name,
    version,
    schema_json: JSON.stringify(schema),
    digest: `sha256:schema-${version}`,
    metadata_json: "{}",
    created_by: "admin",
    created_at_unix_ms: 1,
  };
}

/** Source: `database` is an open object with a `legacy` key; `rate_limits` an integer. */
const currentSchema = registeredSchema(sourceVersion, {
  type: "object",
  properties: {
    database: { type: "object", properties: { legacy: { type: "string" } } },
    rate_limits: { type: "integer" },
  },
});

/** Target: `database` closes, drops `legacy`, and newly requires boolean `tls`. */
const targetSchema = registeredSchema(targetVersion, {
  type: "object",
  properties: {
    database: {
      type: "object",
      additionalProperties: false,
      required: ["tls"],
      properties: { tls: { type: "boolean" }, port: { type: "integer" } },
    },
    rate_limits: { type: "integer" },
  },
});

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
    schema_version: targetVersion,
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
    initialSchemaVersion: targetVersion,
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

/** Waits until the local checks have run over the loaded `database` pin. */
async function readinessSettled(dialog: HTMLElement) {
  const readiness = within(dialog).getByRole("region", { name: "Release readiness" });
  await waitFor(() =>
    expect(readiness).toHaveTextContent("database.tls · is required · new required field"),
  );
  return readiness;
}

function databaseRow(dialog: HTMLElement): HTMLDetailsElement {
  return within(dialog)
    .getByRole("group", { name: "database value", hidden: true })
    .closest("details") as HTMLDetailsElement;
}

async function reachInvalidPreview(dialog: HTMLElement, problem: ReleaseValidationError) {
  mocks.migrateApplicationSchema.mockResolvedValue(
    migrationResult({ valid: false, validation: [problem] }),
  );
  await reachValues(dialog);
  await readinessSettled(dialog);
  const button = within(dialog).getByRole("button", { name: /Preview migration/ });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
  return within(dialog).findByRole("button", { name: "database · Fix field" });
}

describe("SchemaMigrationModal release readiness", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listSchemas.mockResolvedValue({
      schemas: [targetSchema, currentSchema],
      next_page_token: "",
    });
    mocks.getParameter.mockImplementation((ref: { key: string }) =>
      Promise.resolve({
        parameter: {
          value: ref.key === "database" ? "{}" : "300",
          content_type: ref.key === "database" ? "json" : "integer",
        },
      }),
    );
    mocks.getActiveRelease.mockResolvedValue({
      release: dev.release.active,
      activation_revision: dev.release.active!.activation_revision,
    });
    mocks.migrateApplicationSchema.mockResolvedValue(migrationResult());
  });

  it("lists the nested field to provide at Review contract, before any preview", async () => {
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    const readiness = await readinessSettled(dialog);
    expect(mocks.migrateApplicationSchema).not.toHaveBeenCalled();
    expect(mocks.getParameter).toHaveBeenCalledWith(
      { env: "dev", app: overview.application.name, key: "database" },
      expect.any(Number),
    );
    expect(readiness).toHaveTextContent(`Update to satisfy schema v${targetVersion}`);
    expect(
      within(readiness).getByRole("button", {
        name: /^database · 1 field fails the target schema/,
      }),
    ).toBeInTheDocument();
    expect(within(readiness).getByRole("button", { name: "Go to database.tls" })).toBeVisible();
    const counts = within(readiness).getByText("Values to update").parentElement!;
    expect(counts).toHaveTextContent("1");
    const navigator = within(dialog).getByRole("region", { name: "Field changes" });
    expect(within(navigator).getByText("Needs attention").parentElement).toHaveTextContent(
      "Needs attention1",
    );
    // The contract row carries the effect of each difference and the local issue.
    const contractRow = within(dialog)
      .getAllByLabelText("Alias")
      .find((element) => (element as HTMLInputElement).value === "database")!
      .closest(".migration-contract-row") as HTMLElement;
    expect(contractRow).toHaveTextContent("database.tls");
    expect(contractRow).toHaveTextContent("Provide a value");
    expect(contractRow).toHaveTextContent("Fails target schema");
  });

  it("keeps Preview enabled, explains why in the footer, and flips the row chip once fixed", async () => {
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    await readinessSettled(dialog);
    const preview = within(dialog).getByRole("button", { name: /Preview migration/ });
    await waitFor(() => expect(preview).toBeEnabled());
    expect(within(dialog).getByTestId("migration-blocked-reason")).toHaveTextContent(
      "1 value fails local schema checks; the preview will report them.",
    );
    const row = databaseRow(dialog);
    expect(row).toHaveAttribute("open");
    expect(row.querySelector("summary")).toHaveTextContent("Fails target schema");
    fireEvent.click(within(row).getByRole("checkbox", { name: "tls" }));
    await waitFor(() =>
      expect(row.querySelector("summary")).toHaveTextContent("Passes local checks"),
    );
    expect(row.querySelector("summary")).not.toHaveTextContent("Fails target schema");
    expect(within(dialog).getByTestId("migration-blocked-reason")).not.toHaveTextContent(
      "local schema checks",
    );
    const readiness = within(dialog).getByRole("region", { name: "Release readiness" });
    expect(readiness).toHaveTextContent("All 3 values pass local checks.");
    expect(preview).toBeEnabled();
  });

  it("jumps from the checklist to the nested control on Edit values", async () => {
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    const readiness = await readinessSettled(dialog);
    fireEvent.click(within(readiness).getByRole("button", { name: "Go to database.tls" }));
    await waitFor(() => {
      const active = document.activeElement;
      expect(active?.closest('[data-path="tls"]')).not.toBeNull();
    });
    expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeInTheDocument();
    expect(databaseRow(dialog)).toHaveAttribute("open");
  });

  it("links a keyword-only server problem to the nested field the local checks flagged", async () => {
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    const fix = await reachInvalidPreview(dialog, {
      alias: "database",
      code: "schema_violation",
      schema_pointer: "/required",
      message: 'Add the missing required field "tls".',
    });
    expect(within(dialog).getByTestId("migration-problem-path")).toHaveTextContent("database.tls");
    fireEvent.click(fix);
    await waitFor(() =>
      expect(document.activeElement?.closest('[data-path="tls"]')).not.toBeNull(),
    );
  });

  it("uses the server's instance pointer to focus the nested field", async () => {
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    const fix = await reachInvalidPreview(dialog, {
      alias: "database",
      code: "schema_violation",
      schema_pointer: "/properties/database/required",
      instance_pointer: "/database/tls",
      message: "Required field is missing.",
    });
    expect(within(dialog).getByTestId("migration-problem-path")).toHaveTextContent("database.tls");
    fireEvent.click(fix);
    await waitFor(() =>
      expect(document.activeElement?.closest('[data-path="tls"]')).not.toBeNull(),
    );
  });

  it("states the effect of each schema difference on values", async () => {
    render(<SchemaMigrationModal {...modalProps()} />);
    const dialog = screen.getByRole("dialog");
    await reachContract(dialog);
    const comparison = within(dialog)
      .getByText(`Compare current v${sourceVersion} → target v${targetVersion}`)
      .closest("details")!;
    const cell = (path: string) =>
      within(comparison).getByText(path, { selector: "td" }).parentElement!;
    expect(cell("database.tls")).toHaveTextContent("added");
    expect(cell("database.tls")).toHaveTextContent("Provide a value");
    expect(cell("database.legacy")).toHaveTextContent("removed");
    expect(cell("database.legacy")).toHaveTextContent("Remove it from the value");
    expect(within(comparison).getByText("Effect on values")).toBeInTheDocument();
  });

  it("styles the validation banner and release-wide problems with existing panels", async () => {
    mocks.getParameter.mockImplementation((ref: { key: string }) =>
      Promise.resolve({
        parameter: {
          value: ref.key === "database" ? '{"tls":true}' : "300",
          content_type: ref.key === "database" ? "json" : "integer",
        },
      }),
    );
    const { unmount } = render(<SchemaMigrationModal {...modalProps()} />);
    let dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    let preview = within(dialog).getByRole("button", { name: /Preview migration/ });
    await waitFor(() => expect(preview).toBeEnabled());
    fireEvent.click(preview);
    const banner = await within(dialog).findByText("Backend validation passed.");
    expect(banner).toHaveClass("success-panel");
    expect(within(dialog).queryByRole("region", { name: "Release readiness" })).toBeNull();
    unmount();

    mocks.migrateApplicationSchema.mockResolvedValue(
      migrationResult({
        valid: false,
        validation: [
          { alias: "", code: "release", schema_pointer: "", message: "Release-level failure." },
        ],
      }),
    );
    render(<SchemaMigrationModal {...modalProps()} />);
    dialog = screen.getByRole("dialog");
    await reachValues(dialog);
    preview = within(dialog).getByRole("button", { name: /Preview migration/ });
    await waitFor(() => expect(preview).toBeEnabled());
    fireEvent.click(preview);
    const releaseWide = await within(dialog).findByRole("region", {
      name: "Release-wide problems",
    });
    expect(releaseWide).toHaveClass("warn-panel");
    expect(releaseWide).toHaveTextContent("Release-level failure.");
  });
});
