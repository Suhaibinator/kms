import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SchemaMigrationModal } from "@/components/applications/SchemaMigrationModal";
import type { ApplicationOverview, SchemaMigrationRequest } from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";

const mocks = vi.hoisted(() => ({
  listSchemas: vi.fn(),
  getParameter: vi.fn(),
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
      migrateApplicationSchema: mocks.migrateApplicationSchema,
    },
  };
});

const overview = incidentJson as unknown as ApplicationOverview;
const environment = overview.environments.find((item) => item.release.active);
if (!environment?.release.active) throw new Error("fixture needs an active environment");
const active = environment.release.active;
const schema = {
  application: overview.application.name,
  release_name: overview.application.release_name,
  version: overview.application.schema_version + 1,
  schema_json: JSON.stringify({
    type: "object",
    properties: { renamed_rate_limits: { type: "object" } },
  }),
  digest: "sha256:target",
  metadata_json: "{}",
  created_by: "admin",
  created_at_unix_ms: 1,
};
const result = {
  plan_digest: "sha256:plan",
  valid: true,
  executed: false,
  release_name: overview.application.release_name,
  source_version: active.version,
  source_activation_revision: active.activation_revision,
  schema_version: schema.version,
  entries: [],
  validation: [],
  affected_environments: [],
  definition_changed: true,
};

describe("SchemaMigrationModal", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) {
      if (typeof mock === "function" && "mockReset" in mock) mock.mockReset();
    }
    mocks.listSchemas.mockResolvedValue({ schemas: [schema], next_page_token: "" });
    mocks.getParameter.mockResolvedValue({
      parameter: { value: "{}", content_type: "json" },
    });
    mocks.migrateApplicationSchema.mockResolvedValue(result);
  });

  it("preselects a registry schema, suggests its typed field, and previews exact pins", async () => {
    render(
      <SchemaMigrationModal
        application={overview.application}
        environments={overview.environments}
        initialEnvironment={environment.namespace.env}
        initialSchemaVersion={schema.version}
        open
        onClose={vi.fn()}
        onApplied={vi.fn()}
      />,
    );

    const dialog = screen.getByRole("dialog");
    await waitFor(() =>
      expect(within(dialog).getByLabelText("Target registered schema")).toHaveValue(
        String(schema.version),
      ),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: /Review contract/ }));
    expect(within(dialog).getByDisplayValue("renamed_rate_limits")).toBeVisible();

    const source = active.entries.find((entry) => entry.alias === "rate_limits");
    if (!source) throw new Error("fixture needs rate_limits");
    const renamedRow = within(dialog)
      .getByDisplayValue("renamed_rate_limits")
      .closest(".migration-contract-row");
    if (!(renamedRow instanceof HTMLElement)) throw new Error("contract row missing");
    fireEvent.change(within(renamedRow).getByLabelText("Source alias"), {
      target: { value: source.alias },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Edit values/ }));
    await waitFor(() =>
      expect(mocks.getParameter).toHaveBeenCalledWith(
        { env: environment.namespace.env, app: overview.application.name, key: source.ref.key },
        source.version,
      ),
    );
    await waitFor(() =>
      expect(within(dialog).getByRole("button", { name: /Preview migration/ })).toBeEnabled(),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: /Preview migration/ }));
    await waitFor(() => expect(mocks.migrateApplicationSchema).toHaveBeenCalled());

    const request = mocks.migrateApplicationSchema.mock.calls[0][1] as SchemaMigrationRequest;
    const renamed = request.changes.find((change) => change.alias === "renamed_rate_limits");
    if (!renamed) throw new Error("renamed change missing");
    expect(renamed.from_alias).toBe(source.alias);
    expect(renamed.version).toBeUndefined();
    expect(renamed.value).toBeUndefined();
    expect(request.expected_source_version).toBe(active.version);
  });
});
