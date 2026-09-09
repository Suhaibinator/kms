import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { INDEX_MAX_PAGES } from "@/lib/key-search";
import type { IndexPage } from "@/lib/useNamespaceIndex";
import { useNamespaceIndex } from "@/lib/useNamespaceIndex";

describe("useNamespaceIndex", () => {
  it("walks pages until the server stops handing back a next token", async () => {
    const fetchPage = vi
      .fn<(token: string) => Promise<IndexPage<string>>>()
      .mockResolvedValueOnce({ items: ["a", "b"], next: "p2" })
      .mockResolvedValueOnce({ items: ["c"], next: "" });

    const { result } = renderHook(() => useNamespaceIndex("scope-1", true, fetchPage));
    await waitFor(() => expect(result.current.ready).toBe(true));

    expect(result.current.rows).toEqual(["a", "b", "c"]);
    expect(result.current.complete).toBe(true);
    expect(result.current.loading).toBe(false);
    expect(fetchPage).toHaveBeenCalledTimes(2);
    expect(fetchPage.mock.calls[0]?.[0]).toBe("");
    expect(fetchPage.mock.calls[1]?.[0]).toBe("p2");
  });

  it("stops at INDEX_MAX_PAGES with complete: false when the server always hands back a fresh token", async () => {
    let calls = 0;
    const fetchPage = vi.fn<(token: string) => Promise<IndexPage<number>>>(async () => {
      const page = calls;
      calls += 1;
      // A fresh token every time, so the walk only ends via the page cap,
      // never via the "server repeated itself" guard.
      return { items: [page], next: `token-${page}` };
    });

    const { result } = renderHook(() => useNamespaceIndex("scope-1", true, fetchPage));
    await waitFor(() => expect(result.current.ready).toBe(true));

    expect(fetchPage).toHaveBeenCalledTimes(INDEX_MAX_PAGES);
    expect(result.current.rows).toHaveLength(INDEX_MAX_PAGES);
    expect(result.current.complete).toBe(false);
  });

  it("discards a page that resolves after the scope has already changed", async () => {
    let resolveStale!: (page: IndexPage<string>) => void;
    const stale = new Promise<IndexPage<string>>((resolve) => {
      resolveStale = resolve;
    });
    let calls = 0;
    const fetchPage = vi.fn<(token: string) => Promise<IndexPage<string>>>(() => {
      calls += 1;
      return calls === 1 ? stale : Promise.resolve({ items: ["b1"], next: "" });
    });

    const { result, rerender } = renderHook(
      ({ scope }: { scope: string }) => useNamespaceIndex(scope, true, fetchPage),
      { initialProps: { scope: "a" } },
    );
    expect(result.current.loading).toBe(true);
    expect(result.current.ready).toBe(false);

    rerender({ scope: "b" });
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.rows).toEqual(["b1"]);

    // The scope-"a" fetch finally resolves; it must not overwrite scope "b".
    await act(async () => {
      resolveStale({ items: ["a1"], next: "" });
    });
    expect(result.current.rows).toEqual(["b1"]);
    expect(result.current.ready).toBe(true);
  });

  it("refetches from the first page when invalidate() is called", async () => {
    const fetchPage = vi
      .fn<(token: string) => Promise<IndexPage<string>>>()
      .mockResolvedValueOnce({ items: ["a"], next: "" })
      .mockResolvedValueOnce({ items: ["a2"], next: "" });

    const { result } = renderHook(() => useNamespaceIndex("scope-1", true, fetchPage));
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.rows).toEqual(["a"]);
    expect(fetchPage).toHaveBeenCalledTimes(1);

    act(() => result.current.invalidate());
    await waitFor(() => expect(result.current.rows).toEqual(["a2"]));
    expect(fetchPage).toHaveBeenCalledTimes(2);
    expect(fetchPage.mock.calls[1]?.[0]).toBe("");
  });

  it("reports a failed walk instead of an empty index, and retries on invalidate", async () => {
    const onError = vi.fn();
    const fetchPage = vi
      .fn<(token: string) => Promise<IndexPage<string>>>()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({ items: ["a"], next: "" });

    const { result } = renderHook(() => useNamespaceIndex("scope-1", true, fetchPage, onError));
    await waitFor(() => expect(result.current.error).toBeInstanceOf(Error));

    // `ready` stays false: an index that failed to load must never read as a
    // namespace that simply has nothing in it.
    expect(result.current.ready).toBe(false);
    expect(result.current.loading).toBe(false);
    expect(result.current.rows).toEqual([]);
    expect(onError).toHaveBeenCalledTimes(1);

    // Nothing was cached, so the retry is a real one.
    act(() => result.current.invalidate());
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.rows).toEqual(["a"]);
    expect(result.current.error).toBeNull();
    expect(fetchPage).toHaveBeenCalledTimes(2);
  });

  it("does not fetch at all while enabled is false", () => {
    const fetchPage = vi.fn<(token: string) => Promise<IndexPage<string>>>();
    const { result } = renderHook(() => useNamespaceIndex("scope-1", false, fetchPage));

    expect(fetchPage).not.toHaveBeenCalled();
    expect(result.current).toMatchObject({
      rows: [],
      complete: true,
      loading: false,
      ready: false,
      error: null,
    });
  });
});
