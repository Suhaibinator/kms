import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ShipModalProps } from "@/components/applications/contracts";
import { ApiError } from "@/lib/api";
import { links } from "@/lib/links";
import { findingCopy } from "@/lib/readiness";
import type { ApplicationOverview, EnvironmentOverview, Finding } from "@/lib/types";
import ApplicationsPage from "@/pages/applications";
import incidentJson from "./fixtures/backend/overview-incident.json";
import readyJson from "./fixtures/backend/overview-ready.json";
import setupJson from "./fixtures/backend/overview-setup.json";
import { chooseSelectOption } from "./select-test-utils";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  isReady: true,
  push: vi.fn(async () => true),
  replace: vi.fn(async () => true),
  listApplications: vi.fn(),
  applicationOverview: vi.fn(),
  archiveApplication: vi.fn(),
  unarchiveApplication: vi.fn(),
  createNamespace: vi.fn(),
  createSecret: vi.fn(),
  putApplicationParameter: vi.fn(),
  importApplicationDefaults: vi.fn(),
  health: vi.fn(),
  shipModal: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn(), dismiss: vi.fn() },
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
      archiveApplication: mocks.archiveApplication,
      unarchiveApplication: mocks.unarchiveApplication,
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
    mocks.archiveApplication.mockReset();
    mocks.unarchiveApplication.mockReset();
    mocks.createNamespace.mockReset();
    mocks.createSecret.mockReset();
    mocks.putApplicationParameter.mockReset();
    mocks.importApplicationDefaults.mockReset();
    mocks.health.mockReset().mockRejectedValue(new Error("offline"));
    mocks.shipModal.mockClear();
    mocks.toast.error.mockClear();
    mocks.toast.success.mockClear();
    mocks.toast.info.mockClear();
    Element.prototype.scrollIntoView = vi.fn();
  });

  /** Opens the header's More menu and returns it. */
  async function openMore() {
    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    return screen.findByRole("menu", { name: "More actions" });
  }

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
    expect(mocks.listApplications).toHaveBeenCalledWith(
      50,
      undefined,
      expect.anything(),
      "exclude",
    );
    expect(screen.getByText("1 application")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "New application" }));
    const modal = screen.getByRole("dialog");
    expect(within(modal).getByLabelText("Application name")).toBeVisible();
    expect(within(modal).getByRole("list", { name: "New application progress" })).toBeVisible();
  });

  it("?new=1 (the palette's New application) opens the wizard and closing clears it", async () => {
    mocks.query = { new: "1" };
    mocks.listApplications.mockResolvedValue({ applications: [], next_page_token: "" });
    render(<ApplicationsPage />);
    const modal = await screen.findByRole("dialog");
    expect(within(modal).getByLabelText("Application name")).toBeVisible();
    fireEvent.click(within(modal).getByRole("button", { name: "Dismiss dialog" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(mocks.replace).toHaveBeenCalledWith(
      { pathname: "/applications", query: {} },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("filters the list by name or description and pages through it", async () => {
    const orders = clone(ready.application);
    orders.name = "orders";
    orders.description = "Order intake";
    mocks.listApplications.mockResolvedValue({
      applications: [ready.application, orders],
      next_page_token: "page-2",
    });
    render(<ApplicationsPage />);
    await screen.findByText("orders");
    expect(screen.getByText("2 applications")).toBeVisible();
    const filter = screen.getByRole("searchbox", { name: "Filter applications" });
    fireEvent.change(filter, { target: { value: "INTAKE" } });
    expect(screen.getByRole("link", { name: "Manage orders" })).toBeVisible();
    expect(screen.queryByRole("link", { name: `Manage ${ready.application.name}` })).toBeNull();
    expect(screen.getByText("1 of 2 shown")).toBeVisible();
    fireEvent.change(filter, { target: { value: "zzz" } });
    expect(screen.getByText("No matching applications")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(screen.getByRole("link", { name: "Manage orders" })).toBeVisible();

    mocks.listApplications.mockResolvedValue({ applications: [orders], next_page_token: "" });
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    await waitFor(() =>
      expect(mocks.listApplications).toHaveBeenLastCalledWith(
        50,
        "page-2",
        expect.anything(),
        "exclude",
      ),
    );
    expect(await screen.findByText("Page 2")).toBeVisible();
  });

  it("can explicitly list archived applications", async () => {
    mocks.listApplications.mockResolvedValue({ applications: [], next_page_token: "" });
    render(<ApplicationsPage />);
    await screen.findByText("No applications yet");
    await chooseSelectOption(screen.getByLabelText("Lifecycle"), "Archived applications");
    await waitFor(() =>
      expect(mocks.listApplications).toHaveBeenLastCalledWith(
        50,
        undefined,
        expect.anything(),
        "only",
      ),
    );
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
    expect(screen.getByRole("button", { name: "Roll back" })).toBeEnabled();
    // Freshness sits in the header with a compact refresh beside it.
    expect(document.querySelector(".transport-badge")).toHaveTextContent(/^Polling/);
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();
    // The rest of the actions live behind More.
    expect(screen.queryByRole("button", { name: "Edit definition" })).toBeNull();
    const menu = await openMore();
    for (const name of ["Import defaults", "Add environment", "Edit definition", "Connect SDK"]) {
      expect(within(menu).getByRole("menuitem", { name })).toBeVisible();
    }
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Edit definition" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(/definition/i);
  });

  it("archives empty applications and restores archived applications", async () => {
    const archived = clone(setup);
    archived.application.archived_at_unix_ms = 1;
    archived.application.archived_by = "admin";
    mocks.query = { app: setup.application.name };
    mocks.applicationOverview.mockResolvedValue(setup);
    mocks.archiveApplication.mockResolvedValue({ application: archived.application });
    const { rerender } = render(<ApplicationsPage />);
    await screen.findByText("No environments");
    let menu = await openMore();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Archive application" }));
    await waitFor(() =>
      expect(mocks.archiveApplication).toHaveBeenCalledWith(setup.application.name),
    );

    mocks.applicationOverview.mockResolvedValue(archived);
    mocks.unarchiveApplication.mockResolvedValue({ application: setup.application });
    rerender(<ApplicationsPage key="archived" />);
    expect(await screen.findByText(/archived and read-only/i)).toBeVisible();
    expect(screen.getByRole("button", { name: "Quick change" })).toBeDisabled();
    menu = await openMore();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Unarchive application" }));
    await waitFor(() =>
      expect(mocks.unarchiveApplication).toHaveBeenCalledWith(setup.application.name),
    );
  });

  it("opens the ship modal when ?ship= arrives while already on the application", async () => {
    mocks.query = { app: ready.application.name };
    mocks.applicationOverview.mockResolvedValue(ready);
    const { rerender } = render(<ApplicationsPage />);
    await screen.findByRole("region", { name: "dev environment" });
    expect(screen.queryByRole("dialog", { name: "Ship" })).toBeNull();

    // The palette's alias action: a query-only navigation, no remount.
    mocks.query = { app: ready.application.name, ship: "rate_limits" };
    rerender(<ApplicationsPage />);
    expect(await screen.findByRole("dialog", { name: "Ship" })).toHaveTextContent(
      "dev:rate_limits",
    );
    const props = mocks.shipModal.mock.calls.at(-1)?.[0] as ShipModalProps;
    props.onClose();
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Ship" })).toBeNull());
    // Until the router has dropped the param, a re-render must not reopen it.
    rerender(<ApplicationsPage />);
    expect(screen.queryByRole("dialog", { name: "Ship" })).toBeNull();
    mocks.query = { app: ready.application.name };
    rerender(<ApplicationsPage />);
    expect(screen.queryByRole("dialog", { name: "Ship" })).toBeNull();

    // The same action again is a new arrival and opens it again.
    mocks.query = { app: ready.application.name, ship: "rate_limits" };
    rerender(<ApplicationsPage />);
    expect(await screen.findByRole("dialog", { name: "Ship" })).toBeVisible();
  });

  it("opens Roll back when ?rollback=1 arrives while already on the application", async () => {
    mocks.query = { app: ready.application.name, env: "prod" };
    mocks.applicationOverview.mockResolvedValue(ready);
    const { rerender } = render(<ApplicationsPage />);
    await screen.findByRole("region", { name: "prod environment" });
    mocks.query = { app: ready.application.name, env: "prod", rollback: "1" };
    rerender(<ApplicationsPage />);
    expect(await screen.findByRole("dialog", { name: /Roll back/ })).toBeVisible();
  });

  it("shows readiness findings with their Fix actions", async () => {
    const overview = clone(ready);
    const listener: Finding = {
      code: "insecure_listener",
      severity: "warning",
      scope: {},
      params: {},
    };
    overview.findings.push(listener);
    const prod = env(overview, "prod");
    const unreadable: Finding = {
      code: "secret_unreadable",
      severity: "blocking",
      scope: { env: "prod", alias: "db_password" },
      params: { alias: "db_password", state: "disabled" },
    };
    prod.findings.push(unreadable);
    overview.status = "blocked";
    mocks.query = { app: overview.application.name };
    mocks.applicationOverview.mockResolvedValue(overview);
    render(<ApplicationsPage />);
    await screen.findByRole("region", { name: "Definition" });
    const heading = screen.getByRole("heading", { level: 1 });
    expect(within(heading).getByText("Blocked")).toBeVisible();

    // App-level, under the header.
    const appFinding = screen.getByText(findingCopy(listener)).closest("li") as HTMLElement;
    expect(appFinding.closest(".application-findings")).not.toBeNull();
    fireEvent.click(within(appFinding).getByRole("button", { name: "Open health" }));
    expect(mocks.push).toHaveBeenCalledWith(links.health());

    // Environment-scoped, inside its column, resolving the alias to its key.
    const column = screen.getByRole("region", { name: "prod environment" });
    const envFinding = within(column)
      .getByText(findingCopy(unreadable))
      .closest("li") as HTMLElement;
    fireEvent.click(within(envFinding).getByRole("button", { name: "Open secret" }));
    const key = prod.values.find((value) => value.alias === "db_password")?.key ?? "db_password";
    expect(mocks.push).toHaveBeenCalledWith(
      links.secretDetail({ env: "prod", app: overview.application.name, key }),
    );
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
    expect(within(modal).getByRole("button", { name: "Apply to 1 environment" })).toBeVisible();
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

    await screen.findByRole("region", { name: "dev environment" });
    const more = await openMore();
    fireEvent.click(within(more).getByRole("menuitem", { name: "Import defaults" }));
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
    // Production is ringed in the column header; kind badges are neutral.
    const prodHeader = table.querySelector("th .ident-env.ident-prod");
    expect(prodHeader).toHaveTextContent("prod");
    expect(table.querySelector("th .ident-env:not(.ident-prod)")).toHaveTextContent("dev");
    for (const badge of within(table).getAllByText(/^(parameter|secret)$/)) {
      expect(badge).not.toHaveClass("text-warning");
    }
    // A present parameter value links to its detail page and can be copied.
    const present = ready.rows.filter((row) => row.kind === "parameter");
    const cells = present.flatMap((row) =>
      Object.entries(row.environments)
        .filter(([, cell]) => cell.present)
        .map(([environment]) => ({ row, environment })),
    );
    expect(cells.length).toBeGreaterThan(0);
    for (const { row, environment } of cells) {
      expect(
        within(table).getByRole("link", { name: `Open ${row.key} in ${environment}` }),
      ).toHaveAttribute(
        "href",
        links.parameterDetail({ env: environment, app: ready.application.name, key: row.key }),
      );
    }
    expect(within(table).getAllByRole("button", { name: "Copy" })).toHaveLength(cells.length);
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
    expect(within(dialog).getByText(/gradethis\/runtime/)).toBeVisible();
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
