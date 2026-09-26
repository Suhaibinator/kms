import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";

// Only ownership is recorded here, never form values, credentials or secrets.
const owners = new Set<symbol>();
const listeners = new Set<() => void>();

function publish() {
  for (const listener of listeners) listener();
}

export function hasUnsavedWork(): boolean {
  return owners.size > 0;
}

export function discardUnsavedWork(): void {
  owners.clear();
  publish();
}

export function confirmDiscardWork(): boolean {
  return !hasUnsavedWork() || window.confirm("Discard unsaved changes and leave this page?");
}

export function useHasUnsavedWork(): boolean {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    hasUnsavedWork,
    () => false,
  );
}

/** Release synchronously after a successful save, before navigating away. */
export function useUnsavedWork(dirty: boolean): () => void {
  const owner = useRef(Symbol("unsaved-work"));
  const release = useCallback(() => {
    if (owners.delete(owner.current)) publish();
  }, []);
  useEffect(() => {
    if (dirty) {
      owners.add(owner.current);
      publish();
    }
    return release;
  }, [dirty, release]);
  return release;
}
