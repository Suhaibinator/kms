import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { links } from "@/lib/links";
import type { Subscriber } from "@/lib/types";
import SubscribersPage from "@/pages/subscribers";

const mocks = vi.hoisted(() => ({
  subscribers: vi.fn(),
  router: { isReady: true, query: {} as Record<string, string>, replace: vi.fn() },
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

// The table's sort state lives in the URL, so the page reads and writes a router.
vi.mock("next/router", () => ({ useRouter: () => mocks.router }));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", () => ({
  isAbortError: (error: unknown) => error instanceof Error && error.name === "AbortError",
  api: { subscribers: mocks.subscribers },
}));

const empty = { subscribers: [], current_revision: 7 };

describe("SubscribersPage", () => {
  beforeEach(() => {
    mocks.subscribers.mockReset();
    mocks.router.query = {};
    mocks.router.replace.mockReset();
    mocks.toast.error.mockClear();
  });

  it("renders a failed load as an error state with a retry, not as an empty list", async () => {
    mocks.subscribers.mockRejectedValueOnce(new Error("offline"));
    render(<SubscribersPage />);
    expect(await screen.findByText("Could not load subscribers")).toBeVisible();
    expect(screen.queryByText("No applications are currently subscribed")).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent("Stale");
    expect(mocks.toast.error).toHaveBeenCalledTimes(1);

    mocks.subscribers.mockResolvedValueOnce(empty);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No applications are currently subscribed")).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent(/^Polling· (just now|\d+s ago)$/);
  });

  it("links each row to the identity and to the namespace's releases", async () => {
    const subscriber: Subscriber = {
      client_name: "api",
      instance_id: "prod-3",
      identity: "gradethis-be",
      namespaces: [{ env: "prod", app: "gradethis" }],
      remote_addr: "10.0.0.9:5001",
      connected_at_unix_ms: Date.now() - 60_000,
      last_heartbeat_unix_ms: Date.now(),
      last_acked_revision: 7,
    };
    mocks.subscribers.mockResolvedValueOnce({ subscribers: [subscriber], current_revision: 7 });
    render(<SubscribersPage />);
    expect(await screen.findByRole("link", { name: "gradethis-be" })).toHaveAttribute(
      "href",
      links.identities({ name: "gradethis-be" }),
    );
    expect(screen.getByRole("link", { name: "prod/gradethis" })).toHaveAttribute(
      "href",
      links.releases({ app: "gradethis", env: "prod" }),
    );
    expect(screen.getByText("up to date")).toBeVisible();
  });

  it("reorders every namespace table from a column header and records it in the URL", async () => {
    const subscriber = (client_name: string): Subscriber => ({
      client_name,
      instance_id: "",
      identity: "gradethis-be",
      namespaces: [{ env: "prod", app: "gradethis" }],
      remote_addr: "10.0.0.9:5001",
      connected_at_unix_ms: 1,
      last_heartbeat_unix_ms: 2,
      last_acked_revision: 7,
    });
    mocks.subscribers.mockResolvedValue({
      subscribers: [subscriber("zulu"), subscriber("alpha")],
      current_revision: 7,
    });
    const clients = () =>
      [...document.querySelectorAll("table.data tbody tr > td:first-child")].map(
        (cell) => cell.textContent ?? "",
      );

    const { rerender } = render(<SubscribersPage />);
    await screen.findByText("zulu");
    expect(clients()).toEqual(["zulu", "alpha"]);
    // Every live subscriber is loaded at once, so "of" is the real total.
    expect(screen.getByTestId("table-summary")).toHaveTextContent("Showing 2 of 2 subscribers");

    fireEvent.click(screen.getByRole("button", { name: "Client" }));
    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      { pathname: "/subscribers", query: { sort: "client", dir: "asc" } },
      undefined,
      { shallow: true, scroll: false },
    );

    // The URL is the source of truth, so land the router on what the click asked for.
    mocks.router.query = { sort: "client", dir: "asc" };
    rerender(<SubscribersPage />);
    expect(clients()).toEqual(["alpha", "zulu"]);
    expect(screen.getByRole("button", { name: "Client" }).closest("th")).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
  });

  it("shows stat placeholders instead of zeros during the first load", async () => {
    mocks.subscribers.mockResolvedValueOnce(empty);
    render(<SubscribersPage />);
    expect(screen.queryByText("0")).toBeNull();
    expect(screen.getByText("Current revision").closest("[aria-busy]")).toHaveAttribute(
      "aria-busy",
      "true",
    );
    expect(await screen.findByText("7")).toBeVisible();
  });

  it("lets a manual refresh preempt an in-flight poll, while a poll yields to it", async () => {
    mocks.subscribers.mockResolvedValueOnce(empty);
    render(<SubscribersPage />);
    expect(await screen.findByText("No applications are currently subscribed")).toBeVisible();

    // A tab becoming visible triggers an immediate background refresh; leave
    // it hanging so it is still in flight when the user clicks Refresh.
    mocks.subscribers.mockImplementation(() => new Promise(() => undefined));
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(mocks.subscribers).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(mocks.subscribers).toHaveBeenCalledTimes(3);
    const pollSignal = mocks.subscribers.mock.calls[1][0].signal as AbortSignal;
    expect(pollSignal.aborted).toBe(true);

    // The manual refresh is now in flight; a background poll must not pile up.
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(mocks.subscribers).toHaveBeenCalledTimes(3);
    await waitFor(() => expect(mocks.toast.error).not.toHaveBeenCalled());
  });
});
