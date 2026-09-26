import { type NextRouter, useRouter } from "next/router";
import { useEffect } from "react";
import { confirmDiscardWork, hasUnsavedWork } from "@/lib/unsaved-work";

const HISTORY_INDEX = "kmsDraftHistoryIndex";
const SCOPE_KEYS = ["app", "env", "key", "schema_version", "name", "release"];

function changesWorkspace(
  router: NextRouter,
  target: Parameters<NextRouter["push"]>[0],
  shallow?: boolean,
) {
  if (!shallow) return true;
  const current = new URL(router.asPath, window.location.origin);
  const destination =
    typeof target === "string"
      ? new URL(target, current)
      : new URL(target.pathname ?? current.pathname, current);
  if (destination.pathname !== current.pathname) return true;
  for (const key of SCOPE_KEYS) {
    const next =
      typeof target === "string"
        ? destination.searchParams.get(key)
        : String(target.query && typeof target.query !== "string" ? (target.query[key] ?? "") : "");
    if ((current.searchParams.get(key) ?? "") !== (next ?? "")) return true;
  }
  // Filters and modal URL cleanup do not leave the workspace. Modal dismissal
  // has its own guard; browser Back is checked separately, even for shallow URLs.
  return false;
}

export function installUnsavedWorkGuard(router: NextRouter): () => void {
  const onUnload = (event: BeforeUnloadEvent) => {
    if (!hasUnsavedWork()) return;
    event.preventDefault();
    event.returnValue = "";
  };
  window.addEventListener("beforeunload", onUnload);

  const push = router.push;
  const replace = router.replace;
  router.push = (...args) => {
    if (changesWorkspace(router, args[1] ?? args[0], args[2]?.shallow) && !confirmDiscardWork())
      return Promise.resolve(false);
    return push.apply(router, args);
  };
  router.replace = (...args) => {
    if (changesWorkspace(router, args[1] ?? args[0], args[2]?.shallow) && !confirmDiscardWork())
      return Promise.resolve(false);
    return replace.apply(router, args);
  };

  // Mark browser entries so rejecting Back/Forward can restore the existing
  // entry without replacing it or deleting the user's forward history.
  const history = window.history;
  const pushState = history.pushState;
  const replaceState = history.replaceState;
  let index: number = history.state?.[HISTORY_INDEX] ?? 0;
  let restoring = false;
  replaceState.call(history, { ...history.state, [HISTORY_INDEX]: index }, "");
  let currentState = history.state;
  let currentUrl = window.location.href;
  history.pushState = (data, unused, url) => {
    pushState.call(history, { ...data, [HISTORY_INDEX]: ++index }, unused, url);
    currentState = history.state;
    currentUrl = window.location.href;
  };
  history.replaceState = (data, unused, url) => {
    replaceState.call(history, { ...data, [HISTORY_INDEX]: index }, unused, url);
    currentState = history.state;
    currentUrl = window.location.href;
  };
  router.beforePopState?.(() => {
    if (restoring) {
      restoring = false;
      return false;
    }
    const target: number | undefined = history.state?.[HISTORY_INDEX];
    if (!confirmDiscardWork()) {
      if (target === undefined) {
        // A preexisting entry has no knowable delta (the history menu may jump
        // several entries). Restore the current workspace rather than guessing
        // +1 and leaving the URL and editor referring to different resources.
        pushState.call(history, currentState, "", currentUrl);
      } else {
        restoring = true;
        history.go(index - target);
      }
      return false;
    }
    index = target ?? index - 1;
    return true;
  });

  return () => {
    window.removeEventListener("beforeunload", onUnload);
    router.push = push;
    router.replace = replace;
    history.pushState = pushState;
    history.replaceState = replaceState;
    router.beforePopState?.(() => true);
  };
}

export function UnsavedWorkGuard() {
  const router = useRouter();
  useEffect(() => installUnsavedWorkGuard(router), [router]);
  return null;
}
