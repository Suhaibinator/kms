import { useSyncExternalStore } from "react";
import type { WorkspaceRef } from "./links";

export type { WorkspaceRef };

const STORAGE_KEY = "kms.lastNamespace";

let current: WorkspaceRef | null = null;
let loaded = false;
const listeners = new Set<() => void>();

/**
 * Accepts both the original `{ env, app }` shape and an app-only selection.
 * An empty `env` is read as "no environment chosen yet".
 */
function parseWorkspace(value: unknown): WorkspaceRef | null {
  if (typeof value !== "object" || value === null) return null;
  const { app, env } = value as { app?: unknown; env?: unknown };
  if (typeof app !== "string" || app === "") return null;
  if (env === undefined || env === "") return { app };
  if (typeof env !== "string") return null;
  return { env, app };
}

function load(): void {
  if (loaded) return;
  loaded = true;
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    current = parseWorkspace(JSON.parse(raw));
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
 * Records the workspace the operator has settled on — an application, and its
 * environment once chosen — so the sidebar can carry it to the next page
 * instead of asking again. Pass null to forget. Pages do not call this:
 * lib/navigation-namespace.ts records whatever workspace the URL settles on.
 */
export function rememberNamespace(next: WorkspaceRef | null): void {
  load();
  const normalized = next ? parseWorkspace(next) : null;
  const same =
    (normalized === null && current === null) ||
    (normalized !== null &&
      current !== null &&
      normalized.app === current.app &&
      (normalized.env ?? "") === (current.env ?? ""));
  if (same) return;
  current = normalized;
  try {
    if (current) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(current));
    else sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // See load().
  }
  for (const listener of listeners) listener();
}

/** The remembered workspace outside React (e.g. when building a link). */
export function lastNamespace(): WorkspaceRef | null {
  load();
  return current;
}

function getSnapshot(): WorkspaceRef | null {
  load();
  return current;
}

function getServerSnapshot(): WorkspaceRef | null {
  return null;
}

/** The remembered workspace, re-rendering when it changes. Null on the server and before hydration. */
export function useLastNamespace(): WorkspaceRef | null {
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
