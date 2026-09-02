// Every keyboard shortcut the console answers to, in one place: the `?` sheet
// renders this list, so a handler added without a line here is a shortcut
// nobody can discover.

import { useEffect, useState } from "react";

/** Stands in for the platform modifier; `shortcutKeys` swaps in ⌘ or Ctrl. */
export const MOD = "$mod";

export interface Shortcut {
  /** Key chips in press order. `MOD` becomes the platform modifier. */
  keys: readonly string[];
  description: string;
}

export interface ShortcutGroup {
  title: string;
  /** Where the shortcuts apply, for the group's caption. */
  scope: string;
  shortcuts: readonly Shortcut[];
}

export const SHORTCUT_GROUPS: readonly ShortcutGroup[] = [
  {
    title: "Anywhere",
    scope: "Available on every page of the console.",
    shortcuts: [
      { keys: [MOD, "K"], description: "Open (or close) the command palette" },
      { keys: ["?"], description: "Show this shortcut sheet" },
      { keys: ["Esc"], description: "Close the open dialog, menu, or palette" },
    ],
  },
  {
    title: "Command palette",
    scope: "While the palette is open.",
    shortcuts: [
      { keys: ["↑"], description: "Move to the previous result" },
      { keys: ["↓"], description: "Move to the next result" },
      { keys: ["Home"], description: "Jump to the first result" },
      { keys: ["End"], description: "Jump to the last result" },
      { keys: ["↵"], description: "Open the highlighted result" },
    ],
  },
  {
    title: "Forms and editors",
    scope: "While a field or a JSON editor has focus.",
    shortcuts: [
      { keys: [MOD, "↵"], description: "Submit the surrounding form from a JSON editor" },
      { keys: ["Tab"], description: "Insert two spaces in a JSON editor" },
      { keys: ["↵"], description: "New line, keeping the current indentation" },
    ],
  },
  {
    title: "Lists and tables",
    scope: "Tables are ordered from their column headers.",
    shortcuts: [
      { keys: ["Tab"], description: "Move to the next column header or row control" },
      { keys: ["↵"], description: "Sort by the focused column header" },
      { keys: ["Space"], description: "Select or clear the focused row checkbox" },
    ],
  },
];

/** True on macOS and iOS, where the modifier is the Command key. */
export function isApplePlatform(nav: Navigator = navigator): boolean {
  const withHints = nav as Navigator & { userAgentData?: { platform?: string } };
  const platform = withHints.userAgentData?.platform || nav.platform || "";
  return /mac|iphone|ipad|ipod/i.test(platform);
}

/**
 * The platform, resolved after hydration. The server has no platform to ask,
 * so it assumes Apple and the client corrects it in an effect — swapping there
 * rather than during render keeps the two markups identical.
 */
export function useApplePlatform(): boolean {
  const [apple, setApple] = useState(true);
  useEffect(() => setApple(isApplePlatform()), []);
  return apple;
}

export function modifierLabel(apple: boolean): string {
  return apple ? "⌘" : "Ctrl";
}

/** A shortcut's chips with the platform modifier filled in. */
export function shortcutKeys(shortcut: Shortcut, apple: boolean): string[] {
  return shortcut.keys.map((key) => (key === MOD ? modifierLabel(apple) : key));
}

/** True while the event target is somewhere the user is typing. */
export function isTypingTarget(target: EventTarget | null): boolean {
  const element = target as (HTMLElement & { tagName?: unknown }) | null;
  if (!element || typeof element.tagName !== "string") return false;
  if (element.isContentEditable) return true;
  const tag = element.tagName.toLowerCase();
  return tag === "input" || tag === "textarea" || tag === "select";
}

/**
 * `?` with no modifier, outside a field. Typing a question mark into the
 * palette or a key-prefix filter must not fling a dialog over it.
 */
export function isShortcutSheetKey(event: KeyboardEvent): boolean {
  if (event.key !== "?" || event.metaKey || event.ctrlKey || event.altKey) return false;
  return !isTypingTarget(event.target);
}
