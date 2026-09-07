import { fireEvent, render, screen } from "@testing-library/react";
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

describe("Schema upgrade defaults source", () => {
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

  it("imports exact encoded parameter values while keeping dev explicit and preserving secret references", async () => {
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
    await screen.findByLabelText("Starting values");
    fireEvent.change(screen.getByLabelText("Starting values"), { target: { value: "artifact" } });
    const value = '{"id":90071992547409931234}';
    const artifact = {
      format: "kms-config-defaults/v1",
      profile: "source-profile",
      schema_sha256: schema.digest,
      contract: [
        { alias: "renamed_rate_limits", kind: "parameter", content_type: "json" },
        { alias: "db_password", kind: "secret" },
      ],
      parameters: [{ alias: "renamed_rate_limits", content_type: "json", value }],
    };
    const file = new File([JSON.stringify(artifact)], "defaults.json", {
      type: "application/json",
    });
    fireEvent.change(screen.getByLabelText("Defaults artifact"), { target: { files: [file] } });
    await screen.findByText(/Source profile: source-profile/);
    expect(screen.getByText(/Destination:/)).toHaveTextContent(
      `${environment.namespace.env}/${overview.application.name}`,
    );
    fireEvent.click(screen.getByRole("button", { name: /Review contract/ }));
    fireEvent.click(screen.getByRole("button", { name: /Edit values/ }));
    fireEvent.click(screen.getByRole("button", { name: /Preview migration/ }));
    await screen.findByText("Backend validation passed.");
    const request = mocks.migrateApplicationSchema.mock.calls[0][1] as SchemaMigrationRequest;
    expect(request.environment).toBe(environment.namespace.env);
    expect(request.changes[0].value).toBe(value);
    expect(request.changes.find((change) => change.alias === "db_password")).toEqual({
      alias: "db_password",
    });
    expect(request.execute).toBe(false);
    expect(
      screen.getByRole("button", { name: `Upgrade schema & ship to ${environment.namespace.env}` }),
    ).toBeEnabled();
    expect(screen.getByRole("region", { name: "Upgrade scope" })).toHaveTextContent(
      "Application change:",
    );
  });
  it("blocks an artifact that belongs to another schema", async () => {
    render(
      <SchemaMigrationModal
        application={overview.application}
        environments={overview.environments}
        initialEnvironment={environment.namespace.env}
        open
        onClose={vi.fn()}
        onApplied={vi.fn()}
      />,
    );
    await screen.findByLabelText("Starting values");
    fireEvent.change(screen.getByLabelText("Starting values"), { target: { value: "artifact" } });
    fireEvent.change(screen.getByLabelText("Defaults artifact"), {
      target: {
        files: [
          new File(
            [
              JSON.stringify({
                format: "kms-config-defaults/v1",
                profile: "dev",
                schema_sha256: "wrong",
                contract: [],
                parameters: [],
              }),
            ],
            "wrong.json",
          ),
        ],
      },
    });
    await screen.findByRole("alert");
    expect(screen.getByRole("button", { name: /Review contract/ })).toBeDisabled();
    expect(mocks.migrateApplicationSchema).not.toHaveBeenCalled();
  });
});
