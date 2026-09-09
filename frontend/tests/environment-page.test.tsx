import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ShipModalProps } from "@/components/applications/contracts";
import { ApiError } from "@/lib/api";
import { links } from "@/lib/links";
import type { ApplicationOverview } from "@/lib/types";
import EnvironmentPage from "@/pages/applications/environment";
import incidentJson from "./fixtures/backend/overview-incident.json";
import { chooseSelectOption } from "./select-test-utils";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  isReady: true,
  push: vi.fn(async () => true),
  replace: vi.fn(async () => true),
  listSchemas: vi.fn(),
  applicationOverview: vi.fn(),
  updateNamespace: vi.fn(),
  deleteNamespace: vi.fn(),
  listIdentities: vi.fn(),
  health: vi.fn(),
  shipModal: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn(), dismiss: vi.fn() },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    query: mocks.query,
    pathname: "/applications/environment",
    isReady: mocks.isReady,
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
    isAbortError: () => false,
    api: {
      ...actual.api,
      listSchemas: mocks.listSchemas,
      applicationOverview: mocks.applicationOverview,
      updateNamespace: mocks.updateNamespace,
      deleteNamespace: mocks.deleteNamespace,
      listIdentities: mocks.listIdentities,
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

const incident = incidentJson as unknown as ApplicationOverview;
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

/** The overview the page will load, with the environment's namespace patched. */
function overviewWith(
  env: string,
  patch: Partial<ApplicationOverview["environments"][number]["namespace"]> = {},
): ApplicationOverview {
  const overview = clone(incident);
  const environment = overview.environments.find((candidate) => candidate.namespace.env === env);
  if (!environment) throw new Error(`fixture has no ${env}`);
  environment.namespace = { ...environment.namespace, ...patch };
  return overview;
}

async function renderPage(overview: ApplicationOverview = clone(incident)) {
  mocks.applicationOverview.mockResolvedValue(overview);
  render(<EnvironmentPage />);
  // The skeleton has a level-1 heading too; the Ship button only exists once
  // the overview has landed.
  await screen.findByRole("button", { name: /Ship to/ });
  return screen.getByRole("heading", { level: 1 });
}

describe("EnvironmentPage", () => {
  beforeEach(() => {
    mocks.query = { app: "gradethis", env: "dev", schema_version: "1" };
    mocks.isReady = true;
    mocks.push.mockClear();
    mocks.replace.mockClear();
    mocks.listSchemas.mockReset().mockResolvedValue({ schemas: [], next_page_token: "" });
    mocks.applicationOverview.mockReset();
    mocks.updateNamespace.mockReset();
    mocks.deleteNamespace.mockReset();
    mocks.listIdentities.mockReset().mockResolvedValue({ identities: [], next_page_token: "" });
    mocks.health.mockReset().mockRejectedValue(new Error("offline"));
    mocks.shipModal.mockClear();
    mocks.toast.success.mockClear();
    mocks.toast.error.mockClear();
    Element.prototype.scrollIntoView = vi.fn();
  });

  it("renders the environment's identity, status and description", async () => {
    const heading = await renderPage();
    expect(within(heading).getByTitle("dev")).toBeVisible();
    expect(document.querySelector(".page-subtitle")).toHaveTextContent("dev/gradethis");
    // The status chip for the fixture's dev environment.
    expect(screen.getByText("Ready")).toBeVisible();
    // The breadcrumb trail ends on this environment, unlinked.
    const trail = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(trail).getByRole("link", { name: "Applications" })).toBeVisible();
    expect(within(trail).queryByRole("link", { name: /dev/ })).toBeNull();
  });

  it("switches environments and links back to the whole application", async () => {
    await renderPage();
    expect(screen.getByRole("link", { name: "All environments" })).toHaveAttribute(
      "href",
      links.application("gradethis", { schemaVersion: 1, env: "dev" }),
    );
    await chooseSelectOption(
      screen.getByRole("combobox", { name: "Environment" }),
      "prod · production",
    );
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith(links.environment("gradethis", "prod")),
    );
  });

  it("ships from the header for this environment", async () => {
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /Ship to dev/ }));
    const dialog = await screen.findByRole("dialog", { name: "Ship" });
    expect(dialog).toHaveTextContent("dev:");
  });

  it("sorts the values table through the URL", async () => {
    await renderPage();
    const table = screen.getByRole("table");
    fireEvent.click(within(table).getByRole("button", { name: "Alias" }));
    await waitFor(() => expect(mocks.replace).toHaveBeenCalled());
    const lastCall = mocks.replace.mock.calls[mocks.replace.mock.calls.length - 1] as unknown as [
      { pathname: string; query: Record<string, string> },
    ];
    const target = lastCall[0];
    expect(target.pathname).toBe("/applications/environment");
    expect(target.query.sort).toBe("alias");
    expect(target.query.dir).toBe("asc");
  });

  it("filters the values and counts the filter in the summary", async () => {
    await renderPage();
    expect(screen.getByTestId("table-summary")).toHaveTextContent("Showing 3 of 3 values");
    fireEvent.change(screen.getByLabelText("Filter values"), {
      target: { value: "rate_limits" },
    });
    await waitFor(() =>
      expect(screen.getByTestId("table-summary")).toHaveTextContent("Showing 1 of 3 values"),
    );
    expect(screen.getByTestId("table-summary")).toHaveTextContent("1 filter active");
    expect(screen.queryByText("database")).toBeNull();

    fireEvent.change(screen.getByLabelText("Filter values"), { target: { value: "nothing" } });
    expect(await screen.findByText(/No values match/)).toBeVisible();
  });

  it("offers Add value for an alias with nothing behind it", async () => {
    const overview = clone(incident);
    const dev = overview.environments.find((candidate) => candidate.namespace.env === "dev");
    const missing = dev?.values.find((value) => value.alias === "rate_limits");
    if (!missing) throw new Error("fixture changed");
    missing.present = false;
    missing.current_version = undefined;
    await renderPage(overview);
    fireEvent.click(screen.getByRole("button", { name: "Add value for rate_limits in dev" }));
    expect(await screen.findByRole("dialog", { name: "Update rate_limits" })).toBeVisible();
  });

  it("saves environment settings and reloads the overview", async () => {
    mocks.updateNamespace.mockResolvedValue({});
    await renderPage();
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Edit environment settings" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit dev/gradethis" });
    fireEvent.change(within(dialog).getByLabelText("Description"), {
      target: { value: "the dev box" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(mocks.updateNamespace).toHaveBeenCalledWith({
        env: "dev",
        app: "gradethis",
        description: "the dev box",
        allowed_auth_methods: ["token"],
      }),
    );
    await waitFor(() => expect(mocks.applicationOverview).toHaveBeenCalledTimes(2));
  });

  it("explains why a populated environment cannot be deleted", async () => {
    await renderPage();
    const remove = screen.getByRole("button", { name: /Delete environment/ });
    expect(remove).toBeDisabled();
    expect(screen.getByText("2 parameters and 1 secret must be removed first.")).toBeVisible();
    expect(mocks.deleteNamespace).not.toHaveBeenCalled();
  });

  it("deletes an empty environment and returns to the application", async () => {
    mocks.deleteNamespace.mockResolvedValue({});
    await renderPage(
      overviewWith("dev", { parameter_count: 0, secret_count: 0, identity_count: 0 }),
    );
    fireEvent.click(screen.getByRole("button", { name: /Delete environment/ }));
    const confirm = await screen.findByRole("dialog", { name: "Delete namespace?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete namespace" }));
    await waitFor(() =>
      expect(mocks.deleteNamespace).toHaveBeenCalledWith({ env: "dev", app: "gradethis" }),
    );
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith(links.application("gradethis")));
  });

  it("says when the environment is not part of the application", async () => {
    mocks.query = { app: "gradethis", env: "staging", schema_version: "1" };
    mocks.applicationOverview.mockResolvedValue(clone(incident));
    render(<EnvironmentPage />);
    expect(await screen.findByText("Environment not found in gradethis")).toBeVisible();
    expect(screen.getByRole("link", { name: /Open gradethis/ })).toHaveAttribute(
      "href",
      links.application("gradethis"),
    );
  });

  it("keeps namespaces reachable when the overview is admin-only", async () => {
    mocks.applicationOverview.mockRejectedValue(new ApiError("forbidden", "admin only", 403));
    render(<EnvironmentPage />);
    expect(await screen.findByText("Admin only")).toBeVisible();
    expect(screen.getByRole("link", { name: "Open namespaces" })).toHaveAttribute(
      "href",
      "/namespaces",
    );
  });

  it("offers a retry when the overview fails to load", async () => {
    mocks.applicationOverview.mockRejectedValue(new ApiError("internal", "boom", 500));
    render(<EnvironmentPage />);
    expect(await screen.findByText("Application unavailable")).toBeVisible();
    mocks.applicationOverview.mockResolvedValue(clone(incident));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: /Ship to/ })).toBeVisible();
  });
});
