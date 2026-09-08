import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { BulkParameterModal } from "@/components/applications/BulkParameterModal";
import CloneEnvironmentModal from "@/components/applications/CloneEnvironmentModal";
import { QuickSecretModal } from "@/components/applications/QuickSecretModal";
import { ToastProvider } from "@/context/ToastContext";
import { api } from "@/lib/api";
import type { Application, ApplicationConfigurationRow, EnvironmentOverview } from "@/lib/types";

vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: { name: "root", kind: "admin", namespace: null } }),
}));

describe("application value writes", () => {
  it("marks matrix edits as metadata-preserving writes", () => {
    const onSave = vi.fn(async () => undefined);
    const row: ApplicationConfigurationRow = {
      key: "rate-limit",
      kind: "parameter",
      environments: {
        dev: { present: true, value: "10", content_type: "integer", version: 1 },
      },
    };
    render(
      <BulkParameterModal
        app="api"
        environments={["dev"]}
        row={row}
        retryEnvironments={null}
        saving={false}
        onClose={vi.fn()}
        onSave={onSave}
      />,
    );
    const modal = screen.getByRole("dialog", { name: "Update rate-limit" });
    fireEvent.change(within(modal).getByLabelText("Value"), { target: { value: "11" } });
    fireEvent.click(within(modal).getByRole("button", { name: "Apply to 1 environment" }));
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({ metadata_json: "{}", preserve_metadata: true }),
    );
  });

  it("does not erase a typed secret when environment options refresh", () => {
    const seed = { environment: "staging", key: "physical-secret-key" };
    const props = {
      app: "api",
      seed,
      saving: false,
      onClose: vi.fn(),
      onSave: vi.fn(),
      onCreated: vi.fn(),
    };
    const view = render(
      <ToastProvider>
        <QuickSecretModal {...props} environments={["dev", "staging"]} />
      </ToastProvider>,
    );
    const modal = screen.getByRole("dialog", { name: "New secret" });
    expect(within(modal).getByLabelText("Secret key")).toHaveValue("physical-secret-key");
    fireEvent.change(within(modal).getByLabelText("Secret value"), {
      target: { value: "typed-after-clone" },
    });
    view.rerender(
      <ToastProvider>
        <QuickSecretModal {...props} environments={["dev", "staging", "prod"]} />
      </ToastProvider>,
    );
    expect(within(modal).getByLabelText("Secret value")).toHaveValue("typed-after-clone");
  });

  it("returns the cloned secret's physical key for recovery", async () => {
    vi.spyOn(api, "cloneEnvironment").mockResolvedValueOnce({
      namespace: {
        env: "staging",
        app: "api",
        description: "",
        allowed_auth_methods: ["mtls"],
        created_by: "root",
        created_at_unix_ms: 1,
        parameter_count: 0,
        secret_count: 0,
      },
      namespace_created: true,
      needs_value: ["database"],
      items: [
        {
          alias: "database",
          key: "physical-database-password",
          kind: "secret",
          action: "needs_value",
        },
      ],
    });
    const onAddSecret = vi.fn();
    render(
      <ToastProvider>
        <CloneEnvironmentModal
          application={
            {
              name: "api",
              description: "",
              release_name: "runtime",
              schema_version: 0,
              contract: [{ alias: "database", kind: "secret" }],
              created_by: "root",
              created_at_unix_ms: 1,
              updated_at_unix_ms: 1,
              archived_at_unix_ms: 0,
              archived_by: "",
              environment_count: 1,
            } satisfies Application
          }
          environments={[
            {
              namespace: {
                env: "dev",
                app: "api",
                description: "",
                allowed_auth_methods: ["mtls"],
                created_by: "root",
                created_at_unix_ms: 1,
                parameter_count: 0,
                secret_count: 0,
              },
              values: [],
              findings: [],
              release: { latest_version: 0, release_count: 0 },
              production: false,
            } as unknown as EnvironmentOverview,
          ]}
          seed={{ source: "dev", target: "staging", description: "", methods: ["mtls"] }}
          open
          onClose={vi.fn()}
          onCreated={vi.fn()}
          onAddSecret={onAddSecret}
        />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Create environment" }));
    fireEvent.click(await screen.findByRole("button", { name: "Add secret" }));
    expect(onAddSecret).toHaveBeenCalledWith("staging", "database", "physical-database-password");
  });
});
