import { act, renderHook } from "@testing-library/react";
import type { NextRouter } from "next/router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installUnsavedWorkGuard } from "@/components/UnsavedWorkGuard";
import { discardUnsavedWork, hasUnsavedWork, useUnsavedWork } from "@/lib/unsaved-work";

const INDEX = "kmsDraftHistoryIndex";
let cleanup: (() => void) | undefined;
let pop: (() => boolean) | undefined;

function fakeRouter(): NextRouter {
  return {
    asPath: "/applications/environment?app=billing&env=prod",
    push: vi.fn(async () => true),
    replace: vi.fn(async () => true),
    beforePopState: vi.fn((callback: () => boolean) => {
      pop = callback;
    }),
  } as unknown as NextRouter;
}

beforeEach(() => {
  discardUnsavedWork();
  window.history.replaceState({ __N: true }, "", "/applications/environment?app=billing&env=prod");
  vi.stubGlobal(
    "confirm",
    vi.fn(() => false),
  );
});
afterEach(() => {
  cleanup?.();
  cleanup = undefined;
  pop = undefined;
  discardUnsavedWork();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("unsaved work owners", () => {
  it("keeps independent drafts registered until both owners release", () => {
    const first = renderHook(() => useUnsavedWork(true));
    const second = renderHook(() => useUnsavedWork(true));
    expect(hasUnsavedWork()).toBe(true);
    act(() => first.result.current());
    expect(hasUnsavedWork()).toBe(true);
    second.unmount();
    expect(hasUnsavedWork()).toBe(false);
  });

  it("releases saved drafts and registers subsequent new changes", () => {
    const draft = renderHook(({ dirty }) => useUnsavedWork(dirty), {
      initialProps: { dirty: true },
    });
    act(() => draft.result.current());
    expect(hasUnsavedWork()).toBe(false);
    draft.rerender({ dirty: false });
    draft.rerender({ dirty: true });
    expect(hasUnsavedWork()).toBe(true);
    draft.unmount();
    expect(hasUnsavedWork()).toBe(false);
  });
});

describe("route guard", () => {
  it("blocks push and replace without destroying a draft, then permits confirmed navigation", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const push = router.push;
    const replace = router.replace;
    cleanup = installUnsavedWorkGuard(router);
    expect(await router.push("/secrets")).toBe(false);
    expect(await router.replace("/parameters")).toBe(false);
    expect(push).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
    expect(hasUnsavedWork()).toBe(true);
    vi.mocked(window.confirm).mockReturnValue(true);
    expect(await router.push("/secrets")).toBe(true);
    expect(push).toHaveBeenCalledWith("/secrets");
  });

  it("allows a shallow filter change but guards application, environment, and schema changes", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    cleanup = installUnsavedWorkGuard(router);
    const target = {
      pathname: "/applications/environment",
      query: { app: "billing", env: "prod", q: "host" },
    };
    expect(await router.replace(target, undefined, { shallow: true })).toBe(true);
    expect(window.confirm).not.toHaveBeenCalled();
    for (const query of [
      { app: "other", env: "prod" },
      { app: "billing", env: "dev" },
      { app: "billing", env: "prod", schema_version: "2" },
    ]) {
      expect(
        await router.replace({ pathname: target.pathname, query }, undefined, { shallow: true }),
      ).toBe(false);
    }
    expect(window.confirm).toHaveBeenCalledTimes(3);
  });

  it("does not intercept clean navigation or unloading", async () => {
    const router = fakeRouter();
    cleanup = installUnsavedWorkGuard(router);
    expect(await router.push("/secrets")).toBe(true);
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(window.confirm).not.toHaveBeenCalled();
  });

  it("protects document unload and restores native methods on cleanup", () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const push = router.push;
    const nativePush = window.history.pushState;
    cleanup = installUnsavedWorkGuard(router);
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    cleanup();
    cleanup = undefined;
    expect(router.push).toBe(push);
    expect(window.history.pushState).toBe(nativePush);
    const unguarded = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unguarded);
    expect(unguarded.defaultPrevented).toBe(false);
  });

  it("restores cancelled multi-entry Back without rewriting entries or losing forward history", () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const nativeReplace = window.history.replaceState.bind(window.history);
    cleanup = installUnsavedWorkGuard(router);
    window.history.pushState({ __N: true }, "", "/one");
    window.history.pushState({ __N: true }, "", "/two");
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});
    nativeReplace({ __N: true, [INDEX]: 0 }, "", "/initial");
    expect(pop?.()).toBe(false);
    expect(go).toHaveBeenCalledWith(2);
    nativeReplace({ __N: true, [INDEX]: 2 }, "", "/two");
    expect(pop?.()).toBe(false);
    expect(window.confirm).toHaveBeenCalledTimes(1);
  });

  it("restores cancelled Forward after an accepted Back", () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const nativeReplace = window.history.replaceState.bind(window.history);
    cleanup = installUnsavedWorkGuard(router);
    window.history.pushState({ __N: true }, "", "/one");
    window.history.pushState({ __N: true }, "", "/two");
    vi.mocked(window.confirm).mockReturnValue(true);
    nativeReplace({ __N: true, [INDEX]: 0 }, "", "/initial");
    expect(pop?.()).toBe(true);
    vi.mocked(window.confirm).mockReturnValue(false);
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});
    nativeReplace({ __N: true, [INDEX]: 2 }, "", "/two");
    expect(pop?.()).toBe(false);
    expect(go).toHaveBeenCalledWith(-2);
  });
});
