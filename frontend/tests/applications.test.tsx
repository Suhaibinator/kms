import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ShipModalProps } from "@/components/applications/contracts";
import { ApiError } from "@/lib/api";
import type { ApplicationOverview, EnvironmentOverview } from "@/lib/types";
import ApplicationsPage from "@/pages/applications";
import incidentJson from "./fixtures/backend/overview-incident.json";
import readyJson from "./fixtures/backend/overview-ready.json";
import setupJson from "./fixtures/backend/overview-setup.json";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  isReady: true,
  push: vi.fn(async () => true),
  replace: vi.fn(async () => true),
  listApplications: vi.fn(),
  applicationOverview: vi.fn(),
  createNamespace: vi.fn(),
  createSecret: vi.fn(),
  putApplicationParameter: vi.fn(),
  importApplicationDefaults: vi.fn(),
  health: vi.fn(),
  shipModal: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    query: mocks.query,
    pathname: "/applications",
    isReady: mocks.isReady,
    push: mocks.push,
    replace: mocks.replace,
  }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    isAbortError: () => false,
    api: {
      ...actual.api,
      listApplications: mocks.listApplications,
      applicationOverview: mocks.applicationOverview,
      createNamespace: mocks.createNamespace,
      createSecret: mocks.createSecret,
      putApplicationParameter: mocks.putApplicationParameter,
      importApplicationDefaults: mocks.importApplicationDefaults,
      health: mocks.health,
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

const ready = readyJson as unknown as ApplicationOverview;
const incident = incidentJson as unknown as ApplicationOverview;
const setup = setupJson as unknown as ApplicationOverview;

const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

function env(overview: ApplicationOverview, name: string): EnvironmentOverview {
  const found = overview.environments.find((environment) => environment.namespace.env === name);
  if (!found) throw new Error(`fixture has no ${name} environment`);
  return found;
}

describe("ApplicationsPage", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.isReady = true;
    mocks.push.mockClear();
    mocks.replace.mockClear();
    mocks.listApplications.mockReset();
    mocks.applicationOverview.mockReset();
    mocks.createNamespace.mockReset();
    mocks.createSecret.mockReset();
    mocks.putApplicationParameter.mockReset();
    mocks.importApplicationDefaults.mockReset();
    mocks.health.mockReset().mockRejectedValue(new Error("offline"));
    mocks.shipModal.mockClear();
    mocks.toast.error.mockClear();
    mocks.toast.success.mockClear();
    Element.prototype.scrollIntoView = vi.fn();
  });

  it("lists applications and opens the creation wizard", async () => {
    mocks.listApplications.mockResolvedValue({
      applications: [ready.application],
      next_page_token: "",
    });
    render(<ApplicationsPage />);
    expect(await screen.findByText(ready.application.name)).toBeVisible();
    expect(screen.getByRole("link", { name: `Manage ${ready.application.name}` })).toHaveAttribute(
      "href",
      `/applications?app=${ready.application.name}`,
    );
    fireEvent.click(screen.getByRole("button", { name: "New application" }));
    const modal = screen.getByRole("dialog");
    expect(within(modal).getByLabelText("Application name")).toBeVisible();
    expect(within(modal).getByRole("list", { name: "New application progress" })).toBeVisible();
  });

  it("shows a skeleton instead of the list until the router has hydrated", () => {
    mocks.isReady = false;
    mocks.query = { app: ready.application.name };
    render(<ApplicationsPage />);
    expect(screen.queryByRole("button", { name: "New application" })).toBeNull();
    expect(mocks.listApplications).not.toHaveBeenCalled();
    expect(mocks.applicationOverview).not.toHaveBeenCalled();
  });

  it("renders the application header, breadcrumbs and readiness status", async () => {
    mocks.query = { app: ready.application.name };
    mocks.applicationOverview.mockResolvedValue(ready);
    render(<ApplicationsPage />);
    await screen.findByRole("region", { name: "Definition" });
    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading).toHaveTextContent(ready.application.name);
    expect(within(heading).getByText("Ready")).toBeVisible();
    const trail = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(trail).getByRole("link", { name: "Applications" })).toHaveAttribute(
      "href",
      "/applications",
    );
    expect(mocks.applicationOverview).toHaveBeenCalledWith(
      ready.application.name,
      undefined,
      expect.anything(),
    );
    // One column per environment, production last.
    const columns = screen.getAllByRole("region", { name: /environment$/ });
    expect(columns.map((column) => column.getAttribute("data-env"))).toEqual(["dev", "prod"]);
    expect(columns[1]).toHaveClass("pipeline-column-prod");
    expect(screen.getByRole("button", { name: "Quick change" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Edit definition" })).toBeEnabled();
  });

  it("shows unreleased changes as drift with a ship call to action", async () => {
    mocks.query = { app: incident.application.name };
    mocks.applicationOverview.mockResolvedValue(incident);
    render(<ApplicationsPage />);
    const prod = await screen.findByRole("region", { name: "prod environment" });
    const drifted = env(incident, "prod").values.find(
      (value) => value.present && (value.current_version ?? 0) > (value.pinned_version ?? 0),
    );
    expect(drifted).toBeDefined();
    expect(within(prod).getByText(`v${drifted?.current_version} unreleased`)).toBeVisible();
    expect(within(prod).getByRole("button", { name: /unreleased change.*Ship/ })).toBeEnabled();
    expect(within(prod).getByText("Degraded")).toBeVisible();
  });

  it("opens the parameter write for a missing value targeting only its environment", async () => {
    const overview = clone(ready);
    const dev = env(overview, "dev");
    const database = dev.values.find((value) => value.alias === "database");
    if (!database) throw new Error("fixture has no database alias");
    database.present = false;
    database.key = undefined;
    database.current_version = undefined;
    database.pinned_version = undefined;
    dev.values_state = "incomplete";
    mocks.query = { app: overview.application.name };
    mocks.applicationOverview.mockResolvedValue(overview);
    render(<ApplicationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Add value" }));
    const modal = screen.getByRole("dialog");
    expect(within(modal).getByText("Update database")).toBeVisible();
    expect(within(modal).getByRole("checkbox", { name: "dev" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(within(modal).getByRole("checkbox", { name: "prod" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(within(modal).getByRole("button", { name: "Apply to 1 environment(s)" })).toBeVisible();
  });

  it("disables Create first release until every value exists, and says why", async () => {
    const overview = clone(ready);
    const dev = env(overview, "dev");
    dev.release = { latest_version: 0, release_count: 0 };
    dev.release_state = "none";
    dev.status = "incomplete";
    dev.values_state = "incomplete";
    const secret = dev.values.find((value) => value.kind === "secret");
    if (!secret) throw new Error("fixture has no secret alias");
    secret.present = false;
    mocks.query = { app: overview.application.name };
    mocks.applicationOverview.mockResolvedValue(overview);
    render(<ApplicationsPage />);
    const column = await screen.findByRole("region", { name: "dev environment" });
    const create = within(column).getByRole("button", { name: "Create first release" });
    expect(create).toBeDisabled();
    expect(within(column).getByText(`Add values for \`${secret.alias}\` first.`)).toBeVisible();
    expect(within(column).getByRole("button", { name: "Add secret" })).toBeEnabled();
  });

  it("focuses the ?env column", async () => {
    mocks.query = { app: ready.application.name, env: "prod" };
    mocks.applicationOverview.mockResolvedValue(ready);
    render(<ApplicationsPage />);
    const prod = await screen.findByRole("region", { name: "prod environment" });
    expect(prod).toHaveClass("pipeline-column-focused");
    expect(screen.getByRole("region", { name: "dev environment" })).not.toHaveClass(
      "pipeline-column-focused",
    );
    await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalled());
  });

  it("?ship=alias opens the ship modal prefilled for the first non-production environment", async () => {
    mocks.query = { app: ready.application.name, ship: "rate_limits" };
    mocks.applicationOverview.mockResolvedValue(ready);
    render(<ApplicationsPage />);
    const dialog = await screen.findByRole("dialog", { name: "Ship" });
    expect(dialog).toHaveTextContent("dev:rate_limits");
    const props = mocks.shipModal.mock.calls.at(-1)?.[0] as ShipModalProps;
    expect(props).toMatchObject({
      open: true,
      initialEnvironment: "dev",
      initialAlias: "rate_limits",
      application: expect.objectContaining({ name: ready.application.name }),
    });
    expect(props.environments).toHaveLength(ready.environments.length);
    // Closing clears the param from the URL, from the handler.
    props.onClose();
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Ship" })).toBeNull());
    expect(mocks.replace).toHaveBeenCalledWith(
      { pathname: "/applications", query: { app: ready.application.name } },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("Quick change opens the ship modal for the focused environment", async () => {
    mocks.query = { app: ready.application.name, env: "prod" };
    mocks.applicationOverview.mockResolvedValue(ready);
    render(<ApplicationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Quick change" }));
    expect(await screen.findByRole("dialog", { name: "Ship" })).toHaveTextContent("prod:");
  });

  it("opens the defaults importer for a selected application environment", async () => {
    mocks.query = { app: ready.application.name };
    mocks.applicationOverview.mockResolvedValue(ready);
    render(<ApplicationsPage />);

    fireEvent.click(await screen.findByRole("button", { name: "Import defaults" }));
    const menu = await screen.findByRole("menu", { name: "Import defaults" });
    fireEvent.click(within(menu).getByRole("menuitem", { name: "dev" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Import defaults to dev")).toBeVisible();
    expect(within(dialog).getByText(`dev/${ready.application.name}`)).toBeVisible();
    expect(within(dialog).getByLabelText("Defaults artifact")).toBeEnabled();
  });

  it("renders the matrix tab from the overview rows", async () => {
    mocks.query = { app: ready.application.name, tab: "matrix" };
    mocks.applicationOverview.mockResolvedValue(ready);
    render(<ApplicationsPage />);
    expect(await screen.findByRole("heading", { name: "Configuration matrix" })).toBeVisible();
    const table = screen.getByRole("table");
    for (const row of ready.rows) {
      expect(within(table).getByText(row.key)).toBeVisible();
    }
    expect(screen.queryByRole("region", { name: "dev environment" })).toBeNull();
    fireEvent.click(screen.getByRole("tab", { name: "Environments" }));
    expect(mocks.replace).toHaveBeenCalledWith(
      { pathname: "/applications", query: { app: ready.application.name } },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("explains a non-admin deep link instead of a bare error", async () => {
    mocks.query = { app: "gradethis" };
    mocks.applicationOverview.mockRejectedValue(new ApiError("forbidden", "admin only", 403));
    render(<ApplicationsPage />);
    expect(await screen.findByRole("heading", { name: "Not permitted" })).toBeVisible();
    expect(screen.getByRole("link", { name: "Open namespaces" })).toHaveAttribute(
      "href",
      "/namespaces",
    );
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("explains an unknown application instead of offering to add environments", async () => {
    mocks.query = { app: "ghost" };
    mocks.applicationOverview.mockRejectedValue(
      new ApiError("not_found", "application not found", 404),
    );
    render(<ApplicationsPage />);
    expect(await screen.findByRole("heading", { name: "Application not found" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Add environment" })).toBeNull();
    expect(screen.getByRole("link", { name: /Back to applications/ })).toHaveAttribute(
      "href",
      "/applications",
    );
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("offers a retry when the overview cannot be loaded", async () => {
    mocks.query = { app: ready.application.name };
    mocks.applicationOverview.mockRejectedValueOnce(new Error("offline"));
    render(<ApplicationsPage />);
    expect(
      await screen.findByRole("heading", { name: "Could not load application" }),
    ).toBeVisible();
    expect(mocks.toast.error).toHaveBeenCalledTimes(1);
    mocks.applicationOverview.mockResolvedValueOnce(ready);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("region", { name: "dev environment" })).toBeVisible();
  });

  it("never renders an overview that arrives after the app has changed", async () => {
    let resolveFirst: (value: unknown) => void = () => undefined;
    mocks.applicationOverview.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve;
        }),
    );
    mocks.query = { app: ready.application.name };
    const { rerender } = render(<ApplicationsPage />);
    expect(mocks.applicationOverview).toHaveBeenCalledWith(
      ready.application.name,
      undefined,
      expect.anything(),
    );

    const orders = clone(ready);
    orders.application.name = "orders";
    orders.application.description = "Order intake";
    mocks.applicationOverview.mockResolvedValueOnce(orders);
    mocks.query = { app: "orders" };
    rerender(<ApplicationsPage />);
    expect(await screen.findByText("Order intake")).toBeVisible();

    // The first request was aborted when the second began.
    const firstSignal = mocks.applicationOverview.mock.calls[0][2].signal as AbortSignal;
    expect(firstSignal.aborted).toBe(true);

    resolveFirst(ready);
    await waitFor(() => expect(screen.getByText("Order intake")).toBeVisible());
    expect(screen.queryByText(ready.application.description)).toBeNull();
  });

  it("shows the definition and setup findings for an application with no environments", async () => {
    mocks.query = { app: setup.application.name };
    mocks.applicationOverview.mockResolvedValue(setup);
    render(<ApplicationsPage />);
    await screen.findByRole("region", { name: "Definition" });
    const heading = screen.getByRole("heading", { level: 1 });
    expect(within(heading).getByText("Setup")).toBeVisible();
    expect(screen.getByText("No environments")).toBeVisible();
    const definition = screen.getByRole("region", { name: "Definition" });
    expect(within(definition).getByText("Not pinned")).toBeVisible();
    expect(within(definition).getByRole("button", { name: "Register schema" })).toBeEnabled();
    expect(within(definition).getByText(/No schema is pinned/)).toBeVisible();
    fireEvent.click(within(definition).getByRole("button", { name: "Pin schema" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Derive schema from contract")).toBeVisible();
    expect(within(dialog).getByLabelText("Schema ID")).toHaveValue(
      `${setup.application.name}-${setup.application.release_name}`,
    );
    expect((within(dialog).getByLabelText("Schema JSON") as HTMLTextAreaElement).value).toContain(
      '"additionalProperties": false',
    );
  });

  it("creates a secret from a pipeline row without leaving the page", async () => {
    const overview = clone(ready);
    const prod = env(overview, "prod");
    const secret = prod.values.find((value) => value.kind === "secret");
    if (!secret) throw new Error("fixture has no secret alias");
    secret.present = false;
    mocks.query = { app: overview.application.name };
    mocks.applicationOverview.mockResolvedValue(overview);
    mocks.createSecret.mockResolvedValue({ version: 1, revision: 9 });
    render(<ApplicationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Add secret" }));
    const modal = screen.getByRole("dialog");
    expect(within(modal).getByLabelText("Secret key")).toHaveValue(secret.alias);
    fireEvent.change(within(modal).getByLabelText("Secret value"), {
      target: { value: "hunter2" },
    });
    fireEvent.click(within(modal).getByRole("button", { name: "Create secret" }));
    await waitFor(() => expect(mocks.createSecret).toHaveBeenCalledTimes(1));
    expect(mocks.createSecret.mock.calls[0][0]).toMatchObject({
      env: "prod",
      app: overview.application.name,
      key: secret.alias,
    });
    await waitFor(() => expect(mocks.applicationOverview).toHaveBeenCalledTimes(2));
  });
});
