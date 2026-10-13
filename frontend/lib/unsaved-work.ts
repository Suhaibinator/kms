import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";

// Only ownership is recorded here, never form values, credentials or secrets.
const owners = new Set<symbol>();
const listeners = new Set<() => void>();
// The in-app "Discard unsaved changes?" prompt. One is open at a time; every
// request made while it is open shares its answer.
let prompt: { answer: Promise<boolean>; resolve: (discard: boolean) => void } | null = null;
// Open Modals, oldest first. The prompt nests inside the newest, as a child
// dialog, so Base UI stacks it above that modal, moves focus into it, and
// routes Escape to it alone. Two sibling modal roots fight over all three.
const hosts: symbol[] = [];

function publish() {
  if (prompt && owners.size === 0) {
    // Work saved while the prompt is open leaves nothing to discard.
    const open = prompt;
    prompt = null;
    open.resolve(true);
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function hasUnsavedWork(): boolean {
  return owners.size > 0;
}

export function discardUnsavedWork(): void {
  owners.clear();
  publish();
}

/**
 * Resolves true once nothing would be lost: immediately when no work is
 * unsaved, otherwise when the operator confirms the prompt (or the work is
 * saved while it is open). Callers with nothing unsaved should act
 * synchronously instead of awaiting this, so clean navigation stays instant.
 */
export function confirmDiscardWork(): Promise<boolean> {
  if (!hasUnsavedWork()) return Promise.resolve(true);
  if (!prompt) {
    let resolve!: (discard: boolean) => void;
    const answer = new Promise<boolean>((done) => {
      resolve = done;
    });
    prompt = { answer, resolve };
    publish();
  }
  return prompt.answer;
}

/** Called by the prompt's buttons; false keeps the work and stays put. */
export function answerDiscardPrompt(discard: boolean): void {
  const open = prompt;
  if (!open) return;
  prompt = null;
  publish();
  open.resolve(discard);
}

export function isDiscardPromptOpen(): boolean {
  return prompt !== null;
}

export function useDiscardPromptOpen(): boolean {
  return useSyncExternalStore(subscribe, isDiscardPromptOpen, () => false);
}

/** True for the open Modal that should render the prompt: the newest one. */
export function useDiscardPromptHost(open: boolean): boolean {
  const host = useRef(Symbol("modal"));
  useEffect(() => {
    if (!open) return;
    const self = host.current;
    hosts.push(self);
    publish();
    return () => {
      hosts.splice(hosts.indexOf(self), 1);
      publish();
    };
  }, [open]);
  return useSyncExternalStore(
    subscribe,
    () => hosts.at(-1) === host.current,
    () => false,
  );
}

/** With no Modal open, the route guard renders the prompt at the root. */
export function useDiscardPromptUnhosted(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => hosts.length === 0,
    () => true,
  );
}

export function useHasUnsavedWork(): boolean {
  return useSyncExternalStore(subscribe, hasUnsavedWork, () => false);
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
