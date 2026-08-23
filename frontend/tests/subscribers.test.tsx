import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import SubscribersPage from "@/pages/subscribers";

const mocks = vi.hoisted(() => ({
  subscribers: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", () => ({
  isAbortError: (error: unknown) => error instanceof Error && error.name === "AbortError",
  api: { subscribers: mocks.subscribers },
}));

const empty = { subscribers: [], current_revision: 7 };

describe("SubscribersPage", () => {
  beforeEach(() => {
    mocks.subscribers.mockReset();
    mocks.toast.error.mockClear();
  });

  it("renders a failed load as an error state with a retry, not as an empty list", async () => {
    mocks.subscribers.mockRejectedValueOnce(new Error("offline"));
    render(<SubscribersPage />);
    expect(await screen.findByText("Could not load subscribers")).toBeVisible();
    expect(screen.queryByText("No applications are currently subscribed")).toBeNull();
    expect(screen.getByText("refresh failed")).toBeVisible();
    expect(mocks.toast.error).toHaveBeenCalledTimes(1);

    mocks.subscribers.mockResolvedValueOnce(empty);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No applications are currently subscribed")).toBeVisible();
    expect(screen.queryByText("refresh failed")).toBeNull();
    expect(screen.getByText(/live · updated/)).toBeVisible();
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
