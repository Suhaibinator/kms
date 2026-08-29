import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { formatUnixMs } from "@/lib/format";
import { links } from "@/lib/links";
import type { ApplicationOverview, AuditEvent, FleetOverview, Identity } from "@/lib/types";
import HealthPage from "@/pages/health";
import DashboardPage from "@/pages/index";
import fleetJson from "./fixtures/backend/fleet.json";
import readyJson from "./fixtures/backend/overview-ready.json";
import setupJson from "./fixtures/backend/overview-setup.json";

const mocks = vi.hoisted(() => ({
  health: vi.fn(),
  keys: vi.fn(),
  listNamespaces: vi.fn(),
  subscribers: vi.fn(),
  listAudit: vi.fn(),
  listApplications: vi.fn(),
  fleetOverview: vi.fn(),
  applicationOverview: vi.fn(),
  push: vi.fn(async () => true),
  identity: null as Identity | null,
  wizard: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    pathname: "/",
    query: {},
    isReady: true,
    push: mocks.push,
    events: { on: vi.fn(), off: vi.fn() },
  }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: mocks.identity, logout: vi.fn() }),
}));
// The wizard belongs to the application lane; the overview only needs to open
// it and follow `onCreated` to the new application's page.
vi.mock("@/components/applications/CreateApplicationWizard", () => ({
  default: (props: {
    open: boolean;
    onClose: () => void;
    onCreated: (app: { name: string }) => void;
  }) => {
    mocks.wizard(props);
    return props.open ? (
      <div role="dialog" aria-label="Create application">
        <button type="button" onClick={() => props.onCreated({ name: "billing" })}>
          fake create
        </button>
      </div>
    ) : null;
  },
}));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      health: mocks.health,
      keys: mocks.keys,
      listNamespaces: mocks.listNamespaces,
      subscribers: mocks.subscribers,
      listAudit: mocks.listAudit,
      listApplications: mocks.listApplications,
      fleetOverview: mocks.fleetOverview,
      applicationOverview: mocks.applicationOverview,
    },
  };
});

const healthy = {
  healthy: true,
  ready: true,
  version: "1.2.3",
  current_revision: 42,
  grpc_addr: "kms:8443",
  tls_enabled: true,
};
const ready = readyJson as unknown as ApplicationOverview;
const setup = setupJson as unknown as ApplicationOverview;
// Applications from the backend fixture, statuses pinned here so the card
// assertions do not move when the fixture is regenerated.
const fixtureApps = (fleetJson as unknown as FleetOverview).applications.map(
  (entry) => entry.application,
);
const appNamed = (name: string) =>
  fixtureApps.find((app) => app.name === name) ?? { ...fixtureApps[0], name };
const fleet: FleetOverview = {
  applications: [
    {
      application: { ...appNamed("gradethis"), name: "gradethis" },
      status: "attention",
      environments: [
        { env: "dev", status: "ready", production: false },
        { env: "prod", status: "degraded", production: true },
      ],
    },
    { application: { ...appNamed("billing"), name: "billing" }, status: "setup", environments: [] },
    {
      application: { ...appNamed("reports"), name: "reports" },
      status: "ready",
      environments: [{ env: "prod-eu", status: "ready", production: true }],
    },
  ] as FleetOverview["applications"],
};
const readyReleases = Array.from(
  new Set(
    ready.environments.map((env) => `${env.release.active?.name}@${env.release.active?.version}`),
  ),
);
const admin: Identity = { name: "root", kind: "admin", namespace: null };
const client: Identity = {
  name: "gradethis-prod",
  kind: "client",
  namespace: { env: "prod", app: "gradethis" },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const namespace = (env: string, app: string) => ({
  env,
  app,
  description: "",
  allowed_auth_methods: ["mtls"],
  created_by: "admin",
  created_at_unix_ms: 1,
  parameter_count: 2,
  secret_count: 1,
});

beforeEach(() => {
  for (const mock of Object.values(mocks)) {
    if (typeof mock === "function" && "mockReset" in mock) mock.mockReset();
  }
  mocks.toast.error.mockClear();
  mocks.identity = admin;
  mocks.push.mockResolvedValue(true);
  mocks.health.mockResolvedValue(healthy);
  mocks.keys.mockResolvedValue({ keys: [] });
  mocks.listNamespaces.mockResolvedValue({ namespaces: [], next_page_token: "" });
  mocks.subscribers.mockResolvedValue({ subscribers: [], current_revision: 0 });
  mocks.listAudit.mockResolvedValue({ events: [], next_page_token: "" });
  mocks.listApplications.mockResolvedValue({ applications: [], next_page_token: "" });
  mocks.fleetOverview.mockResolvedValue({ applications: [] });
  mocks.applicationOverview.mockResolvedValue(ready);
});

describe("DashboardPage", () => {
  it("shows an unknown service status when /health cannot be reached", async () => {
    mocks.health.mockRejectedValue(new Error("connection refused"));

    render(<DashboardPage />);
    expect(await screen.findByText("unknown")).toBeVisible();
    expect(screen.getByText("could not reach the API")).toBeVisible();
    expect(screen.queryByText("unhealthy")).toBeNull();
    expect(screen.queryByText("not ready")).toBeNull();
    expect(mocks.toast.error).toHaveBeenCalledWith(
      expect.any(Error),
      "Could not load service health",
    );
    expect(screen.getByRole("status")).toHaveTextContent("Stale");
  });

  it("names every failed section and marks the affected cards", async () => {
    mocks.identity = client;
    mocks.subscribers.mockRejectedValue(new Error("boom"));
    mocks.listAudit.mockRejectedValue(new Error("boom"));
    mocks.listNamespaces.mockRejectedValue(new Error("boom"));
    render(<DashboardPage />);
    expect(await screen.findByText("Could not load recent activity")).toBeVisible();
    expect(screen.getByText("Could not load subscribers")).toBeVisible();
    expect(screen.getAllByText(/not loaded/)).toHaveLength(3);
    expect(mocks.toast.error).toHaveBeenCalledWith(
      expect.any(Error),
      "Could not load namespace counts, live subscribers and recent activity",
    );
    expect(screen.queryByText("No recent events")).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: "Try again" })[0] as HTMLElement);
    await waitFor(() => expect(mocks.listAudit).toHaveBeenCalledTimes(2));
  });

  it("links recent activity to the resource and shows how long ago it happened", async () => {
    mocks.identity = client;
    const event: AuditEvent = {
      id: 1,
      event_type: "secret.read",
      actor_identity: "billing-api",
      actor_type: "client",
      resource_type: "secret",
      resource_env: "prod",
      resource_app: "billing",
      resource_key: "db/password",
      resource_version: 1,
      decision: "allow",
      source_ip: "",
      user_agent: "",
      request_id: "",
      created_at_unix_ms: Date.now() - 2 * 60_000,
      metadata_json: "",
    };
    mocks.listAudit.mockResolvedValue({ events: [event], next_page_token: "" });
    render(<DashboardPage />);
    const link = await screen.findByRole("link", { name: "/prod/billing/db/password" });
    expect(link).toHaveAttribute(
      "href",
      links.secretDetail({ env: "prod", app: "billing", key: "db/password" }),
    );
    const when = screen.getByText("2m ago");
    expect(when).toHaveAttribute("title", formatUnixMs(event.created_at_unix_ms));
    expect(screen.getByRole("heading", { level: 2, name: /Recent activity/ })).toBeVisible();
    expect(screen.getByRole("heading", { level: 2, name: /Live subscribers/ })).toBeVisible();
  });

  it("reports the service status when /health responds", async () => {
    render(<DashboardPage />);
    expect(await screen.findByText("healthy")).toBeVisible();
    expect(screen.getByText("ready")).toBeVisible();
    expect(screen.getByText("version 1.2.3")).toBeVisible();
    expect(mocks.health).toHaveBeenCalledWith(
      expect.objectContaining({ signal: expect.anything() }),
    );
  });

  it("keeps the classic service view for a client identity and never asks for the fleet", async () => {
    mocks.identity = client;
    render(<DashboardPage />);
    expect(await screen.findByText("healthy")).toBeVisible();
    expect(screen.getByText("Live subscribers")).toBeVisible();
    expect(document.querySelector(".card-grid")).not.toBeNull();
    expect(document.querySelector(".stat-strip")).toBeNull();
    expect(mocks.listApplications).not.toHaveBeenCalled();
    expect(mocks.fleetOverview).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "New application" })).toBeNull();
  });

  it("shows the first-run checklist for an admin with no applications and no namespaces", async () => {
    render(<DashboardPage />);
    expect(
      await screen.findByRole("heading", { name: "Set up your first application" }),
    ).toBeVisible();
    expect(screen.getByRole("list", { name: "Setup steps" })).toBeVisible();
    expect(document.querySelector(".stat-strip")).not.toBeNull();
    expect(mocks.applicationOverview).not.toHaveBeenCalled();
    // No fleet grid, no "New application" header button — step 1 is the CTA.
    expect(document.querySelector(".fleet-grid")).toBeNull();
    expect(screen.queryByRole("button", { name: "New application" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Create application" }));
    expect(screen.getByRole("dialog", { name: "Create application" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "fake create" }));
    expect(mocks.push).toHaveBeenCalledWith(links.application("billing"));
  });

  it("offers the adopt variant when namespaces exist without applications", async () => {
    mocks.listNamespaces.mockResolvedValue({
      namespaces: [namespace("prod", "legacy"), namespace("dev", "legacy")],
      next_page_token: "",
    });
    render(<DashboardPage />);
    expect(
      await screen.findByRole("heading", { name: "Adopt your existing environments" }),
    ).toBeVisible();
    expect(screen.getByText(/2 environment namespaces exist without an application/)).toBeVisible();
  });

  it("renders the fleet grid with per-environment dots, releases and rejected counts", async () => {
    mocks.listApplications.mockResolvedValue({
      applications: fleet.applications.map((entry) => entry.application),
      next_page_token: "",
    });
    mocks.fleetOverview.mockResolvedValue(fleet);
    mocks.applicationOverview.mockImplementation(async (name: string) => {
      if (name === "gradethis") {
        return {
          ...ready,
          application: { ...ready.application, name: "gradethis" },
          environments: ready.environments.map((env) =>
            env.namespace.env === "prod"
              ? { ...env, rollout: { ...env.rollout, rejected: 2 } }
              : env,
          ),
        };
      }
      if (name === "billing") return setup;
      throw new Error("overview unavailable");
    });

    render(<DashboardPage />);
    const grid = await screen.findByRole("region", { name: "Applications" });
    await waitFor(() => expect(grid.querySelectorAll(".fleet-card")).toHaveLength(3));
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(3);
    expect(document.querySelector(".stat-strip")).not.toBeNull();
    expect(screen.getByRole("button", { name: "New application" })).toBeVisible();

    // Blocked/attention first: gradethis (attention), then billing (setup), reports (ready).
    const cards = Array.from(grid.querySelectorAll(".fleet-card")).map((card) =>
      card.getAttribute("data-app"),
    );
    expect(cards).toEqual(["gradethis", "billing", "reports"]);

    const gradethis = grid.querySelector("[data-app='gradethis']") as HTMLElement;
    expect(within(gradethis).getByRole("link", { name: "gradethis" })).toHaveAttribute(
      "href",
      links.application("gradethis"),
    );
    expect(within(gradethis).getByText("Needs attention")).toBeVisible();
    const prodLink = within(gradethis).getByRole("link", { name: "prod: degraded (production)" });
    expect(prodLink).toHaveAttribute("href", links.application("gradethis", { env: "prod" }));
    expect(prodLink.querySelector(".status-dot")).toHaveClass("status-degraded", "status-prod");
    const devLink = within(gradethis).getByRole("link", { name: "dev: ready" });
    expect(devLink.querySelector(".status-dot")).toHaveClass("status-ready");
    expect(devLink.querySelector(".status-dot")).not.toHaveClass("status-prod");
    // Release detail arrives after the grid has painted.
    await waitFor(() => {
      for (const label of readyReleases) {
        expect(within(gradethis).getAllByText(label).length).toBeGreaterThan(0);
      }
    });
    expect(within(gradethis).getByText("2 rejected")).toHaveClass("fleet-card-rejected-some");
    expect(within(gradethis).getByText(/^activated /)).toBeVisible();

    const billing = grid.querySelector("[data-app='billing']") as HTMLElement;
    expect(within(billing).getByText("No environments yet.")).toBeVisible();
    expect(within(billing).getByText("0 rejected")).toBeVisible();
    expect(within(billing).getByText("never activated")).toBeVisible();

    // A failed per-app overview degrades that card only.
    const reports = grid.querySelector("[data-app='reports']") as HTMLElement;
    expect(within(reports).getByText("no release")).toBeVisible();
    expect(within(reports).getByText("—")).toBeVisible();

    // The status chips narrow the grid; a second click on the active one clears it.
    const filter = screen.getByRole("group", { name: "Filter applications by status" });
    expect(within(filter).getByRole("button", { name: /^All/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    fireEvent.click(within(filter).getByRole("button", { name: /Needs attention/ }));
    expect(
      Array.from(grid.querySelectorAll(".fleet-card")).map((card) => card.getAttribute("data-app")),
    ).toEqual(["gradethis"]);
    expect(within(filter).getByRole("button", { name: /Needs attention/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    fireEvent.click(within(filter).getByRole("button", { name: /Needs attention/ }));
    expect(grid.querySelectorAll(".fleet-card")).toHaveLength(3);
    expect(screen.getByText("Recent activity")).toBeVisible();
    expect(screen.queryByText("Live subscribers")).toBeNull();
  });

  it("paints the grid from the fleet overview before any per-app overview resolves", async () => {
    mocks.listApplications.mockResolvedValue({
      applications: fleet.applications.map((entry) => entry.application),
      next_page_token: "",
    });
    mocks.fleetOverview.mockResolvedValue(fleet);
    const pending = deferred<ApplicationOverview>();
    mocks.applicationOverview.mockReturnValue(pending.promise);

    render(<DashboardPage />);
    const grid = await screen.findByRole("region", { name: "Applications" });
    await waitFor(() => expect(grid.querySelectorAll(".fleet-card")).toHaveLength(3));
    const gradethis = grid.querySelector("[data-app='gradethis']") as HTMLElement;
    expect(within(gradethis).getByText("Needs attention")).toBeVisible();
    expect(
      within(gradethis).getByRole("link", { name: "prod: degraded (production)" }),
    ).toBeVisible();
    // Release and rejected counts are still unknown, shown as dashes — not "no release".
    expect(within(gradethis).queryByText("no release")).toBeNull();
    expect(within(gradethis).getAllByText("—").length).toBeGreaterThan(0);
    expect(screen.getByRole("status")).toHaveTextContent("Loaded");

    pending.resolve(ready);
    await waitFor(() => {
      for (const label of readyReleases) {
        expect(within(gradethis).getAllByText(label).length).toBeGreaterThan(0);
      }
    });
  });

  it("caps per-application overview calls at 25", async () => {
    const many = Array.from({ length: 30 }, (_, i) => ({
      ...fleet.applications[0],
      application: { ...fleet.applications[0]?.application, name: `app${i}` },
    }));
    mocks.listApplications.mockResolvedValue({
      applications: many.map((entry) => entry.application),
      next_page_token: "",
    });
    mocks.fleetOverview.mockResolvedValue({ applications: many });

    render(<DashboardPage />);
    const grid = await screen.findByRole("region", { name: "Applications" });
    await waitFor(() => expect(grid.querySelectorAll(".fleet-card")).toHaveLength(30));
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(25);
    expect(screen.getByText(/Release detail is shown for the first 25/)).toBeVisible();
  });

  it("does not mistake a failed fleet load for an empty store", async () => {
    mocks.listApplications.mockRejectedValue(new Error("boom"));
    mocks.fleetOverview.mockRejectedValue(new Error("boom"));
    render(<DashboardPage />);
    expect(await screen.findByText("Could not load application status")).toBeVisible();
    expect(screen.queryByRole("list", { name: "Setup steps" })).toBeNull();
    expect(mocks.toast.error).toHaveBeenCalledWith(
      expect.any(Error),
      "Failed to load applications",
    );
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mocks.fleetOverview).toHaveBeenCalledTimes(2));
  });
});

describe("HealthPage", () => {
  it("renders key ages relative to now with the absolute time in the tooltip", async () => {
    const created = Date.now() - 3 * 60 * 60_000;
    mocks.keys.mockResolvedValue({
      keys: [{ id: "k1", source: "file", state: "active", created_at_unix_ms: created }],
    });
    render(<HealthPage />);
    const cell = await screen.findByText("3h ago");
    expect(cell).toHaveAttribute("title", formatUnixMs(created));
    expect(screen.getByRole("heading", { level: 2, name: "Encryption keys" })).toBeVisible();
    expect(screen.getByRole("heading", { level: 2, name: "Backup & recovery" })).toBeVisible();
  });

  it("shows unknown badges when /health cannot be reached", async () => {
    mocks.health.mockRejectedValue(new Error("connection refused"));

    render(<HealthPage />);
    expect(await screen.findAllByText("unknown")).toHaveLength(2);
    expect(screen.queryByText("unhealthy")).toBeNull();
    expect(screen.queryByText("not ready")).toBeNull();
    expect(mocks.toast.error).toHaveBeenCalledWith(expect.any(Error), "Failed to load health");
  });

  it("keeps the loaded stats on screen while a refresh is in flight", async () => {
    render(<HealthPage />);
    expect(await screen.findByText("healthy")).toBeVisible();

    const pending = deferred<typeof healthy>();
    mocks.health.mockReturnValue(pending.promise);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));

    expect(screen.getByRole("button", { name: "Refreshing…" })).toBeDisabled();
    expect(screen.getByText("healthy")).toBeVisible();
    expect(screen.getByText("1.2.3")).toBeVisible();

    pending.resolve({ ...healthy, version: "1.2.4" });
    await waitFor(() => expect(screen.getByText("1.2.4")).toBeVisible());
  });
});
