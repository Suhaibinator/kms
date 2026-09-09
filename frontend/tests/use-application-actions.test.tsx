import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ShipModalProps } from "@/components/applications/contracts";
import {
  useApplicationActions,
  type UseApplicationActionsOptions,
} from "@/components/applications/useApplicationActions";
import type { ApplicationOverview } from "@/lib/types";
import incidentJson from "./fixtures/backend/overview-incident.json";

// Mirrors tests/applications.test.tsx's mocking conventions: this hook wires
// up the same modals the application page renders, so exercising it directly
// needs the same router/toast/api doubles, minus ApplicationsPage itself.
const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  push: vi.fn(async () => true),
  replace: vi.fn(async () => true),
  listSchemas: vi.fn(),
  health: vi.fn(),
  validateRelease: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn(), dismiss: vi.fn() },
  shipModal: vi.fn(),
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    query: mocks.query,
    pathname: "/applications/environment",
    isReady: true,
    push: mocks.push,
    replace: mocks.replace,
  }),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: { name: "root", kind: "admin", namespace: null } }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      listSchemas: mocks.listSchemas,
      health: mocks.health,
      validateRelease: mocks.validateRelease,
    },
  };
});
vi.mock("@/components/ship/ShipModal", () => ({
  default: (props: ShipModalProps) => {
    mocks.shipModal(props);
    return props.open ? (
      <div role="dialog" aria-label="Ship">
        {props.initialEnvironment}:{props.initialAlias ?? ""}
      </div>
    ) : null;
  },
}));

const incident = incidentJson as unknown as ApplicationOverview;
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

/** A thin harness: one button per action under test, plus every modal the hook owns. */
function Harness({
  overview,
  defaultEnv,
  onWritingChange,
}: Pick<UseApplicationActionsOptions, "overview" | "defaultEnv" | "onWritingChange">) {
  const { actions, modals, writing } = useApplicationActions({
    overview,
    reload: async () => undefined,
    onWritingChange,
    pathname: "/applications/environment",
    defaultEnv,
  });
  return (
    <div>
      <span data-testid="writing">{String(writing)}</span>
      <button type="button" onClick={() => actions.openShip()}>
        open-ship
      </button>
      <button type="button" onClick={() => actions.openRollback("prod")}>
        open-rollback
      </button>
      <button type="button" onClick={() => actions.openConnect("prod")}>
        open-connect
      </button>
      <button type="button" onClick={() => actions.openImportDefaults("prod")}>
        open-import-defaults
      </button>
      <button type="button" onClick={() => actions.openMigrate("prod")}>
        open-migrate
      </button>
      <button type="button" onClick={() => actions.openAddEnvironment()}>
        open-add-environment
      </button>
      <button type="button" onClick={() => actions.openAddEnvironment("dev")}>
        open-add-environment-from-dev
      </button>
      <button type="button" onClick={() => actions.openDefinition()}>
        open-definition
      </button>
      <button type="button" onClick={() => actions.openDerive()}>
        open-derive
      </button>
      <button type="button" onClick={() => actions.openSecret("prod", "db_password")}>
        open-secret
      </button>
      <button type="button" onClick={() => actions.openWriteRow(overview.rows[0])}>
        open-write-row
      </button>
      {modals}
    </div>
  );
}

describe("useApplicationActions", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.push.mockClear();
    mocks.replace.mockClear();
    mocks.listSchemas.mockReset().mockResolvedValue({ schemas: [], next_page_token: "" });
    mocks.health.mockReset().mockRejectedValue(new Error("offline"));
    mocks.validateRelease.mockReset().mockResolvedValue({ valid: true, errors: [] });
    mocks.shipModal.mockClear();
    mocks.toast.error.mockClear();
    mocks.toast.success.mockClear();
  });

  it("openShip opens the Ship modal for the given environment", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-ship" }));
    expect(await screen.findByRole("dialog", { name: "Ship" })).toHaveTextContent("prod:");
  });

  it("openRollback opens the Roll back dialog for the given environment", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-rollback" }));
    expect(await screen.findByRole("dialog", { name: /Roll back|Re-activate/ })).toBeVisible();
  });

  it("openConnect opens the Connect SDK dialog", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    expect(screen.queryByRole("dialog", { name: "Connect SDK" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "open-connect" }));
    expect(await screen.findByRole("dialog", { name: "Connect SDK" })).toBeVisible();
  });

  it("openImportDefaults opens the importer titled for the given environment", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-import-defaults" }));
    expect(await screen.findByRole("dialog", { name: "Import defaults to prod" })).toBeVisible();
  });

  it("openMigrate opens the schema migration wizard for the given environment", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-migrate" }));
    const dialog = await screen.findByRole("dialog", { name: "Upgrade application schema" });
    expect(await within(dialog).findByText(`${incident.application.name} · prod`)).toBeVisible();
  });

  it("openAddEnvironment opens an empty Add environment form", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-add-environment" }));
    const dialog = await screen.findByRole("dialog", { name: /Add environment to/ });
    expect(within(dialog).getByLabelText("Start from")).toHaveTextContent("Empty");
  });

  it("openAddEnvironment(env) preselects 'Copy values from <env>' in Start from", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-add-environment-from-dev" }));
    const dialog = await screen.findByRole("dialog", { name: /Add environment to/ });
    expect(within(dialog).getByLabelText("Start from")).toHaveTextContent("Copy values from dev");
  });

  it("openDefinition opens the application definition editor", async () => {
    const overview = clone(incident);
    render(<Harness overview={overview} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-definition" }));
    expect(
      await screen.findByRole("dialog", { name: `Edit ${overview.application.name}` }),
    ).toBeVisible();
  });

  it("openDerive opens the derive-schema dialog", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-derive" }));
    expect(
      await screen.findByRole("dialog", { name: "Derive schema from contract" }),
    ).toBeVisible();
  });

  it("openSecret seeds the quick secret modal with the resolved key", async () => {
    render(<Harness overview={clone(incident)} defaultEnv="prod" />);
    fireEvent.click(screen.getByRole("button", { name: "open-secret" }));
    const dialog = await screen.findByRole("dialog", { name: "New secret" });
    expect(within(dialog).getByLabelText("Secret key")).toHaveValue("db_password");
  });

  it("openWriteRow opens the bulk parameter modal and reports writing while it is open", async () => {
    const overview = clone(incident);
    const onWritingChange = vi.fn();
    render(<Harness overview={overview} defaultEnv="prod" onWritingChange={onWritingChange} />);
    expect(screen.getByTestId("writing")).toHaveTextContent("false");

    fireEvent.click(screen.getByRole("button", { name: "open-write-row" }));
    const dialog = await screen.findByRole("dialog", { name: `Update ${overview.rows[0].key}` });
    expect(screen.getByTestId("writing")).toHaveTextContent("true");
    await waitFor(() => expect(onWritingChange).toHaveBeenLastCalledWith(true));

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByTestId("writing")).toHaveTextContent("false");
    await waitFor(() => expect(onWritingChange).toHaveBeenLastCalledWith(false));
  });
});
