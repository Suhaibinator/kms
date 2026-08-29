import { useSyncExternalStore } from "react";

/** The environment/application pair an operator last worked in. */
export interface NamespaceRef {
  env: string;
  app: string;
}

const STORAGE_KEY = "kms.lastNamespace";

let current: NamespaceRef | null = null;
let loaded = false;
const listeners = new Set<() => void>();

function isNamespaceRef(value: unknown): value is NamespaceRef {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as NamespaceRef).env === "string" &&
    typeof (value as NamespaceRef).app === "string" &&
    (value as NamespaceRef).env !== "" &&
    (value as NamespaceRef).app !== ""
  );
}

function load(): void {
  if (loaded) return;
  loaded = true;
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const parsed: unknown = JSON.parse(raw);
    if (isNamespaceRef(parsed)) current = parsed;
  } catch {
    // Storage is a convenience; a blocked or absent store just means no memory.
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * Records the namespace a list page has settled on, so the sidebar can carry
 * it to the next page instead of asking again. Pass null to forget.
 */
export function rememberNamespace(next: NamespaceRef | null): void {
  load();
  const same =
    (next === null && current === null) ||
    (next !== null && current !== null && next.env === current.env && next.app === current.app);
  if (same) return;
  current = next ? { env: next.env, app: next.app } : null;
  try {
    if (current) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(current));
    else sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // See load().
  }
  for (const listener of listeners) listener();
}

/** The remembered namespace outside React (e.g. when building a link). */
export function lastNamespace(): NamespaceRef | null {
  load();
  return current;
}

function getSnapshot(): NamespaceRef | null {
  load();
  return current;
}

function getServerSnapshot(): NamespaceRef | null {
  return null;
}

/** The remembered namespace, re-rendering when it changes. Null on the server and before hydration. */
export function useLastNamespace(): NamespaceRef | null {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

/** Test hook: clears memory and storage. */
export function resetNamespaceMemory(): void {
  current = null;
  loaded = false;
  try {
    sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // See load().
  }
  for (const listener of listeners) listener();
}
