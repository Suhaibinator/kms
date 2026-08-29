import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  OVERVIEW_CHECK_MS,
  releaseMovements,
  useApplicationOverview,
} from "@/components/applications/useApplicationOverview";
import type { ApplicationOverview } from "@/lib/types";
import readyJson from "./fixtures/backend/overview-ready.json";

const mocks = vi.hoisted(() => ({
  applicationOverview: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn(), dismiss: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    isAbortError: () => false,
    api: { ...actual.api, applicationOverview: mocks.applicationOverview },
  };
});

const ready = readyJson as unknown as ApplicationOverview;
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

/** The fixture with prod's active release moved to a later activation. */
function shipped(): ApplicationOverview {
  const next = clone(ready);
  const prod = next.environments.find((env) => env.namespace.env === "prod");
  if (!prod?.release.active) throw new Error("fixture has no active release in prod");
  prod.release.active.version += 1;
  prod.release.active.activation_revision += 5;
  prod.release.active.created_by = "alice";
  return next;
}

describe("releaseMovements", () => {
  it("is empty for identical overviews and value-only changes", () => {
    expect(releaseMovements(ready, clone(ready))).toEqual([]);
    const values = clone(ready);
    const dev = values.environments.find((env) => env.namespace.env === "dev");
    if (dev?.values[0]) dev.values[0].current_version = 99;
    expect(releaseMovements(ready, values)).toEqual([]);
  });

  it("names the environment, release and actor of a moved activation", () => {
    const next = shipped();
    const active = next.environments.find((env) => env.namespace.env === "prod")?.release.active;
    expect(releaseMovements(ready, next)).toEqual([
      `prod: ${active?.name}@${active?.version} activated at rev ${active?.activation_revision} by alice`,
    ]);
  });

  it("reports environments that appeared or disappeared", () => {
    const next = clone(ready);
    next.environments = next.environments.filter((env) => env.namespace.env !== "dev");
    expect(releaseMovements(ready, next)).toEqual(["dev: environment removed"]);
    expect(releaseMovements(next, ready)).toEqual(["dev: environment added"]);
  });
});

describe("useApplicationOverview", () => {
  beforeEach(() => {
    // waitFor polls on real timers; the check interval still moves only when
    // advanced explicitly.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mocks.applicationOverview.mockReset();
    mocks.toast.info.mockClear();
    mocks.toast.dismiss.mockClear();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("records when the data was loaded and clears staleness on reload", async () => {
    mocks.applicationOverview.mockResolvedValue(ready);
    const { result } = renderHook(() => useApplicationOverview("gradethis"));
    await waitFor(() => expect(result.current.slot?.status).toBe("success"));
    expect(result.current.freshness.lastLoadedAt).not.toBeNull();
    expect(result.current.freshness.staleReason).toBeNull();

    mocks.applicationOverview.mockRejectedValueOnce(new Error("offline"));
    await act(async () => {
      await result.current.reload();
    });
    expect(result.current.slot?.data).toEqual(ready);
    expect(result.current.freshness.staleReason).toBe("failed");

    await act(async () => {
      await result.current.reload();
    });
    expect(result.current.freshness.staleReason).toBeNull();
  });

  it("announces a release activated elsewhere with a Reload action instead of swapping the data", async () => {
    mocks.applicationOverview.mockResolvedValue(ready);
    const { result } = renderHook(() => useApplicationOverview("gradethis"));
    await waitFor(() => expect(result.current.slot?.status).toBe("success"));
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(1);

    // Nothing moved: the check stays silent.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(OVERVIEW_CHECK_MS);
    });
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(2);
    expect(mocks.toast.info).not.toHaveBeenCalled();

    const moved = shipped();
    mocks.applicationOverview.mockResolvedValue(moved);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(OVERVIEW_CHECK_MS);
    });
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(3);
    expect(result.current.slot?.data).toEqual(ready);
    expect(result.current.freshness.staleReason).toBe("changed");
    expect(mocks.toast.info).toHaveBeenCalledTimes(1);
    const [title, description, options] = mocks.toast.info.mock.calls[0] as [
      string,
      string,
      { id: string; action: { label: string; onClick: () => void } },
    ];
    expect(title).toMatch(/changed since/);
    expect(description).toMatch(/^prod: .* by alice$/);
    expect(options.id).toBe("overview-changed:gradethis");
    expect(options.action.label).toBe("Reload");

    await act(async () => {
      options.action.onClick();
      await vi.runOnlyPendingTimersAsync();
    });
    await waitFor(() => expect(result.current.slot?.data).toEqual(moved));
    expect(result.current.freshness.staleReason).toBeNull();
    expect(mocks.toast.dismiss).toHaveBeenCalledWith("overview-changed:gradethis");
  });

  it("does not check while hidden or while a reload is in flight", async () => {
    mocks.applicationOverview.mockResolvedValue(ready);
    const { result } = renderHook(() => useApplicationOverview("gradethis"));
    await waitFor(() => expect(result.current.slot?.status).toBe("success"));

    Object.defineProperty(document, "hidden", { value: true, configurable: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(OVERVIEW_CHECK_MS);
    });
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(1);
    Object.defineProperty(document, "hidden", { value: false, configurable: true });

    mocks.applicationOverview.mockReturnValue(new Promise(() => undefined));
    act(() => {
      void result.current.reload();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(OVERVIEW_CHECK_MS);
    });
    // Only the reload itself asked for the overview.
    expect(mocks.applicationOverview).toHaveBeenCalledTimes(2);
  });
});
