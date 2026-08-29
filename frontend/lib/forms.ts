import { type Ref, type RefObject, useCallback, useLayoutEffect, useRef, useState } from "react";

/** Assigns `node` to a callback or object ref, if one was given. */
export function assignRef<T>(ref: Ref<T> | undefined | null, node: T | null): void {
  if (!ref) return;
  if (typeof ref === "function") {
    ref(node);
    return;
  }
  (ref as { current: T | null }).current = node;
}

const FOCUSABLE =
  'input:not([type="hidden"]), textarea, select, button, [role="combobox"], [tabindex]';

/**
 * Moves focus to the first control flagged invalid inside `root` — the
 * element itself when it is focusable, otherwise its first focusable
 * descendant (a JSON editor flags its frame, not its textarea). Returns the
 * element focused, or null when nothing is invalid.
 */
export function focusFirstInvalid(root: ParentNode | null | undefined): HTMLElement | null {
  const flagged = root?.querySelector<HTMLElement>('[aria-invalid="true"], [data-invalid]');
  if (!flagged) return null;
  const target = flagged.matches(FOCUSABLE)
    ? flagged
    : (flagged.querySelector<HTMLElement>(FOCUSABLE) ?? flagged);
  target.scrollIntoView?.({ block: "center" });
  target.focus({ preventScroll: true });
  return target;
}

/**
 * Focus-the-first-invalid-field for a form whose errors appear on submit.
 * Call `requestFocus()` right after `markAllTouched()` in the blocked branch
 * of a submit handler; the focus move runs once the revealed errors have
 * rendered, so a message below the fold of a scrolling modal is brought
 * into view instead of leaving the button looking dead.
 */
export function useFocusFirstInvalid<T extends HTMLElement = HTMLFormElement>(): {
  formRef: RefObject<T | null>;
  requestFocus: () => void;
} {
  const formRef = useRef<T | null>(null);
  const [tick, setTick] = useState(0);
  const requestFocus = useCallback(() => setTick((current) => current + 1), []);
  useLayoutEffect(() => {
    if (tick === 0) return;
    focusFirstInvalid(formRef.current);
  }, [tick]);
  return { formRef, requestFocus };
}
