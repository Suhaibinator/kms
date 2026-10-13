import { type NextRouter, useRouter } from "next/router";
import { useEffect } from "react";
import { DiscardWorkPrompt } from "@/components/Modal";
import { confirmDiscardWork, hasUnsavedWork, useDiscardPromptUnhosted } from "@/lib/unsaved-work";

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

  // Only the latest navigation blocked behind the prompt runs once it is
  // confirmed; earlier ones resolve false, as a superseded route change would.
  let latest: symbol | undefined;
  function afterPrompt(navigate: () => Promise<boolean>): Promise<boolean> {
    const request = Symbol("navigation");
    latest = request;
    return confirmDiscardWork().then((discard) =>
      discard && latest === request ? navigate() : false,
    );
  }

  const push = router.push;
  const replace = router.replace;
  router.push = (...args) => {
    if (!hasUnsavedWork() || !changesWorkspace(router, args[1] ?? args[0], args[2]?.shallow))
      return push.apply(router, args);
    return afterPrompt(() => push.apply(router, args));
  };
  router.replace = (...args) => {
    if (!hasUnsavedWork() || !changesWorkspace(router, args[1] ?? args[0], args[2]?.shallow))
      return replace.apply(router, args);
    return afterPrompt(() => replace.apply(router, args));
  };

  // Mark browser entries so rejecting Back/Forward can restore the existing
  // entry without replacing it or deleting the user's forward history.
  const history = window.history;
  const pushState = history.pushState;
  const replaceState = history.replaceState;
  let index: number = history.state?.[HISTORY_INDEX] ?? 0;
  // The pop that puts back the entry a blocked Back/Forward left.
  let restoring = false;
  // The pop that replays a Back/Forward once the prompt is confirmed.
  let leaving = false;
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
    if (leaving || !hasUnsavedWork()) {
      leaving = false;
      index = target ?? index - 1;
      return true;
    }
    // The browser has already moved. Put the workspace back while the prompt
    // is open, so the URL and the editor keep referring to the same resource,
    // and replay the move only if the operator chooses to discard.
    let replay: number;
    if (target === undefined) {
      // A preexisting entry has no knowable delta (the history menu may jump
      // several entries). Re-enter the current workspace directly after it
      // rather than guessing, which leaves the target exactly one step back.
      pushState.call(history, currentState, "", currentUrl);
      replay = -1;
    } else {
      restoring = true;
      history.go(index - target);
      replay = target - index;
    }
    void afterPrompt(async () => {
      leaving = true;
      history.go(replay);
      return true;
    });
    return false;
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
  // An open Modal nests the prompt itself (see useDiscardPromptHost).
  return useDiscardPromptUnhosted() ? <DiscardWorkPrompt /> : null;
}
