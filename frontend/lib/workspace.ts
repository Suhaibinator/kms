import type { MouseEvent } from "react";

export function shouldOpenWorkspace(event: MouseEvent<HTMLElement>): boolean {
  if (
    event.defaultPrevented ||
    event.button !== 0 ||
    event.metaKey ||
    event.ctrlKey ||
    event.shiftKey ||
    event.altKey
  ) {
    return false;
  }
  event.preventDefault();
  // Pointer activation does not focus links in every browser (notably WebKit).
  // The dialog's focus restoration target must be established before it opens.
  event.currentTarget.focus();
  return true;
}
