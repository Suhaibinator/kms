import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import HealthPage from "@/pages/health";
import DashboardPage from "@/pages/index";

const mocks = vi.hoisted(() => ({
  health: vi.fn(),
  keys: vi.fn(),
  listNamespaces: vi.fn(),
  subscribers: vi.fn(),
  listAudit: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
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
    },
  };
});

const healthy = { healthy: true, ready: true, version: "1.2.3", current_revision: 42 };

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) {
    if (typeof mock === "function" && "mockReset" in mock) mock.mockReset();
  }
  mocks.toast.error.mockClear();
  mocks.health.mockResolvedValue(healthy);
  mocks.keys.mockResolvedValue({ keys: [] });
  mocks.listNamespaces.mockResolvedValue({ namespaces: [], next_page_token: "" });
  mocks.subscribers.mockResolvedValue({ subscribers: [], current_revision: 0 });
  mocks.listAudit.mockResolvedValue({ events: [], next_page_token: "" });
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
      "Some dashboard data failed to load",
    );
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
});

describe("HealthPage", () => {
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
