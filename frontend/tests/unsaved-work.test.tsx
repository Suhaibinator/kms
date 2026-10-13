import { act, fireEvent, render, renderHook, screen, within } from "@testing-library/react";
import type { NextRouter } from "next/router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installUnsavedWorkGuard, UnsavedWorkGuard } from "@/components/UnsavedWorkGuard";
import {
  answerDiscardPrompt,
  confirmDiscardWork,
  discardUnsavedWork,
  hasUnsavedWork,
  isDiscardPromptOpen,
  useUnsavedWork,
} from "@/lib/unsaved-work";

const INDEX = "kmsDraftHistoryIndex";
let cleanup: (() => void) | undefined;
let pop: (() => boolean) | undefined;

const mocks = vi.hoisted(() => ({ router: undefined as NextRouter | undefined }));
vi.mock("next/router", () => ({ useRouter: () => mocks.router }));

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

/** Answers the open prompt and lets the navigations waiting on it settle. */
async function answer(discard: boolean) {
  expect(isDiscardPromptOpen()).toBe(true);
  await act(async () => answerDiscardPrompt(discard));
}

beforeEach(() => {
  discardUnsavedWork();
  window.history.replaceState({ __N: true }, "", "/applications/environment?app=billing&env=prod");
  vi.stubGlobal("confirm", vi.fn());
});
afterEach(() => {
  answerDiscardPrompt(false);
  cleanup?.();
  cleanup = undefined;
  pop = undefined;
  mocks.router = undefined;
  discardUnsavedWork();
  // The native dialog is never used for in-app navigation.
  expect(window.confirm).not.toHaveBeenCalled();
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

describe("discard prompt", () => {
  it("resolves immediately when nothing is unsaved", async () => {
    expect(await confirmDiscardWork()).toBe(true);
    expect(isDiscardPromptOpen()).toBe(false);
  });

  it("shares one prompt between concurrent requests", async () => {
    renderHook(() => useUnsavedWork(true));
    const first = confirmDiscardWork();
    const second = confirmDiscardWork();
    await answer(false);
    expect(await first).toBe(false);
    expect(await second).toBe(false);
    expect(hasUnsavedWork()).toBe(true);
  });

  it("resolves true when the work is saved while the prompt is open", async () => {
    const draft = renderHook(() => useUnsavedWork(true));
    const pending = confirmDiscardWork();
    act(() => draft.result.current());
    expect(await pending).toBe(true);
    expect(isDiscardPromptOpen()).toBe(false);
  });

  it("renders as an in-app dialog whose buttons answer it", async () => {
    renderHook(() => useUnsavedWork(true));
    mocks.router = fakeRouter();
    render(<UnsavedWorkGuard />);
    cleanup = () => undefined;
    expect(screen.queryByRole("dialog")).toBeNull();

    let pending!: Promise<boolean>;
    act(() => {
      pending = confirmDiscardWork();
    });
    const prompt = await screen.findByRole("dialog", { name: "Discard unsaved changes?" });
    fireEvent.click(within(prompt).getByRole("button", { name: "Keep editing" }));
    expect(await pending).toBe(false);

    act(() => {
      pending = confirmDiscardWork();
    });
    const again = await screen.findByRole("dialog", { name: "Discard unsaved changes?" });
    fireEvent.click(within(again).getByRole("button", { name: "Discard and leave" }));
    expect(await pending).toBe(true);
  });
});

describe("route guard", () => {
  it("holds push and replace behind the prompt without destroying a draft", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const push = router.push;
    const replace = router.replace;
    cleanup = installUnsavedWorkGuard(router);

    const pushed = router.push("/secrets");
    await answer(false);
    expect(await pushed).toBe(false);
    const replaced = router.replace("/parameters");
    await answer(false);
    expect(await replaced).toBe(false);
    expect(push).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
    expect(hasUnsavedWork()).toBe(true);

    const confirmed = router.push("/secrets");
    expect(push).not.toHaveBeenCalled();
    await answer(true);
    expect(await confirmed).toBe(true);
    expect(push).toHaveBeenCalledWith("/secrets");
  });

  it("runs only the latest navigation requested while the prompt is open", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const push = router.push;
    cleanup = installUnsavedWorkGuard(router);
    const first = router.push("/secrets");
    const second = router.push("/parameters");
    await answer(true);
    expect(await first).toBe(false);
    expect(await second).toBe(true);
    expect(push).toHaveBeenCalledTimes(1);
    expect(push).toHaveBeenCalledWith("/parameters");
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
    expect(isDiscardPromptOpen()).toBe(false);
    for (const query of [
      { app: "other", env: "prod" },
      { app: "billing", env: "dev" },
      { app: "billing", env: "prod", schema_version: "2" },
    ]) {
      const replaced = router.replace({ pathname: target.pathname, query }, undefined, {
        shallow: true,
      });
      await answer(false);
      expect(await replaced).toBe(false);
    }
  });

  it("does not intercept clean navigation or unloading", async () => {
    const router = fakeRouter();
    const push = router.push;
    cleanup = installUnsavedWorkGuard(router);
    void router.push("/secrets");
    // Synchronous: a clean route change is never deferred behind a promise.
    expect(push).toHaveBeenCalledWith("/secrets");
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(isDiscardPromptOpen()).toBe(false);
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

  it("restores cancelled multi-entry Back without rewriting entries or losing forward history", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const nativeReplace = window.history.replaceState.bind(window.history);
    cleanup = installUnsavedWorkGuard(router);
    window.history.pushState({ __N: true }, "", "/one");
    window.history.pushState({ __N: true }, "", "/two");
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});
    nativeReplace({ __N: true, [INDEX]: 0 }, "", "/initial");
    // The workspace is restored before the prompt is answered.
    expect(pop?.()).toBe(false);
    expect(go).toHaveBeenCalledWith(2);
    nativeReplace({ __N: true, [INDEX]: 2 }, "", "/two");
    expect(pop?.()).toBe(false);
    await answer(false);
    expect(go).toHaveBeenCalledTimes(1);
  });

  it("replays a confirmed Back to the entry the operator chose", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const nativeReplace = window.history.replaceState.bind(window.history);
    cleanup = installUnsavedWorkGuard(router);
    window.history.pushState({ __N: true }, "", "/one");
    window.history.pushState({ __N: true }, "", "/two");
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});
    nativeReplace({ __N: true, [INDEX]: 0 }, "", "/initial");
    expect(pop?.()).toBe(false);
    nativeReplace({ __N: true, [INDEX]: 2 }, "", "/two");
    expect(pop?.()).toBe(false);
    await answer(true);
    expect(go).toHaveBeenLastCalledWith(-2);
    nativeReplace({ __N: true, [INDEX]: 0 }, "", "/initial");
    expect(pop?.()).toBe(true);
  });

  it("re-enters the workspace for an untracked entry and replays one step back", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const nativeReplace = window.history.replaceState.bind(window.history);
    cleanup = installUnsavedWorkGuard(router);
    const workspace = window.location.href;
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});
    nativeReplace({ __N: true }, "", "/before-the-console");
    expect(pop?.()).toBe(false);
    expect(window.location.href).toBe(workspace);
    expect(go).not.toHaveBeenCalled();
    await answer(true);
    expect(go).toHaveBeenCalledWith(-1);
  });

  it("restores cancelled Forward after an accepted Back", async () => {
    renderHook(() => useUnsavedWork(true));
    const router = fakeRouter();
    const nativeReplace = window.history.replaceState.bind(window.history);
    cleanup = installUnsavedWorkGuard(router);
    window.history.pushState({ __N: true }, "", "/one");
    window.history.pushState({ __N: true }, "", "/two");
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});
    nativeReplace({ __N: true, [INDEX]: 0 }, "", "/initial");
    expect(pop?.()).toBe(false);
    nativeReplace({ __N: true, [INDEX]: 2 }, "", "/two");
    expect(pop?.()).toBe(false);
    await answer(true);
    nativeReplace({ __N: true, [INDEX]: 0 }, "", "/initial");
    expect(pop?.()).toBe(true);
    go.mockClear();
    nativeReplace({ __N: true, [INDEX]: 2 }, "", "/two");
    expect(pop?.()).toBe(false);
    expect(go).toHaveBeenCalledWith(-2);
    await answer(false);
    expect(go).toHaveBeenCalledTimes(1);
  });
});
