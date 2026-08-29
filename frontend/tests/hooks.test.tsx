import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useCursorPagination, useFieldErrors, useLatestRequest, useQueryParams } from "@/lib/hooks";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string | string[]>,
  isReady: true,
}));

vi.mock("next/router", () => ({
  useRouter: () => ({ query: mocks.query, isReady: mocks.isReady, replace: vi.fn() }),
}));

describe("useLatestRequest", () => {
  it("invalidates and aborts the previous run when a new one begins", () => {
    const { result } = renderHook(() => useLatestRequest());
    const first = result.current.begin();
    expect(first.current).toBe(true);
    expect(first.signal.aborted).toBe(false);

    const second = result.current.begin();
    expect(first.current).toBe(false);
    expect(first.signal.aborted).toBe(true);
    expect(second.current).toBe(true);
    expect(second.signal.aborted).toBe(false);
  });

  it("abort() invalidates the in-flight run", () => {
    const { result } = renderHook(() => useLatestRequest());
    const run = result.current.begin();
    result.current.abort();
    expect(run.current).toBe(false);
    expect(run.signal.aborted).toBe(true);
  });

  it("aborts the in-flight run on unmount", () => {
    const { result, unmount } = renderHook(() => useLatestRequest());
    const run = result.current.begin();
    unmount();
    expect(run.current).toBe(false);
    expect(run.signal.aborted).toBe(true);
  });

  it("keeps begin/abort referentially stable across renders", () => {
    const { result, rerender } = renderHook(() => useLatestRequest());
    const before = result.current;
    rerender();
    expect(result.current.begin).toBe(before.begin);
    expect(result.current.abort).toBe(before.abort);
  });
});

describe("useFieldErrors", () => {
  it("hides a message until the field is touched", () => {
    const { result } = renderHook(() => useFieldErrors<"name" | "env">());
    expect(result.current.shown("name", "Required")).toBeNull();
    expect(result.current.submitted).toBe(false);

    act(() => result.current.touch("name"));
    expect(result.current.shown("name", "Required")).toBe("Required");
    expect(result.current.shown("env", "Required")).toBeNull();
    expect(result.current.shown("name", null)).toBeNull();
  });

  it("reveals every message after markAllTouched and clears on reset", () => {
    const { result } = renderHook(() => useFieldErrors<"name" | "env">());
    act(() => result.current.markAllTouched());
    expect(result.current.submitted).toBe(true);
    expect(result.current.shown("name", "Required")).toBe("Required");
    expect(result.current.shown("env", "Choose one")).toBe("Choose one");

    act(() => result.current.reset());
    expect(result.current.submitted).toBe(false);
    expect(result.current.shown("name", "Required")).toBeNull();
    expect(result.current.shown("env", "Choose one")).toBeNull();
  });
});

describe("useQueryParams", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.isReady = true;
  });

  it("returns a referentially stable values object across re-renders with the same query", () => {
    mocks.query = { env: "prod", app: ["billing", "other"] };
    const { result, rerender } = renderHook(() => useQueryParams(["env", "app", "key"]));
    const before = result.current.values;
    expect(before).toEqual({ env: "prod", app: "billing", key: null });
    expect(result.current.ready).toBe(true);

    // A fresh query object with the same content must not produce a new values object.
    mocks.query = { env: "prod", app: ["billing", "other"] };
    rerender();
    expect(result.current.values).toBe(before);

    mocks.query = { env: "dev", app: "billing" };
    rerender();
    expect(result.current.values).not.toBe(before);
    expect(result.current.values).toEqual({ env: "dev", app: "billing", key: null });
  });

  it("reports null values until the router is ready", () => {
    mocks.isReady = false;
    mocks.query = { env: "prod" };
    const { result, rerender } = renderHook(() => useQueryParams(["env"]));
    expect(result.current).toEqual({ values: { env: null }, ready: false });

    mocks.isReady = true;
    rerender();
    expect(result.current).toEqual({ values: { env: "prod" }, ready: true });
  });
});

describe("useCursorPagination", () => {
  it("walks forward and back through server tokens", () => {
    const { result } = renderHook(() => useCursorPagination("scope"));
    expect(result.current).toMatchObject({ pageToken: "", page: 1, hasPrevious: false });
    act(() => result.current.setNextToken("t2"));
    expect(result.current.hasNext).toBe(true);
    expect(result.current.nextToken).toBe("t2");
    act(() => result.current.next());
    expect(result.current).toMatchObject({ pageToken: "t2", page: 2, previousToken: "" });
    act(() => result.current.setNextToken("t3"));
    act(() => result.current.next());
    expect(result.current).toMatchObject({ pageToken: "t3", page: 3, previousToken: "t2" });
    act(() => result.current.previous());
    expect(result.current).toMatchObject({ pageToken: "t2", page: 2 });
  });

  it("starts on a token restored from the URL and returns to page 1 from it", () => {
    const { result } = renderHook(() => useCursorPagination("scope", { pageToken: "t4", page: 4 }));
    // The pages between 1 and the seeded token are unknown: no "previous",
    // only "first" (reset), which callers show from `page > 1`.
    expect(result.current).toMatchObject({
      pageToken: "t4",
      page: 4,
      hasPrevious: false,
      previousToken: "",
    });
    act(() => result.current.setNextToken("t5"));
    act(() => result.current.next());
    expect(result.current).toMatchObject({
      pageToken: "t5",
      page: 5,
      hasPrevious: true,
      previousToken: "t4",
    });
    act(() => result.current.previous());
    expect(result.current).toMatchObject({ page: 4, hasPrevious: false });
    act(() => result.current.reset());
    expect(result.current).toMatchObject({ pageToken: "", page: 1, hasPrevious: false });
  });

  it("ignores a seed without a token and resets when the scope changes", () => {
    const { result, rerender } = renderHook(
      ({ scope }) => useCursorPagination(scope, { page: 3 }),
      {
        initialProps: { scope: "a" },
      },
    );
    expect(result.current.page).toBe(1);
    act(() => result.current.setNextToken("t2"));
    act(() => result.current.next());
    expect(result.current.page).toBe(2);
    rerender({ scope: "b" });
    expect(result.current).toMatchObject({ pageToken: "", page: 1 });
  });
});
