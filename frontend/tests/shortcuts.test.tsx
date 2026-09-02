import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AppShell from "@/components/AppShell";
import { resetNamespaceMemory } from "@/lib/namespace-memory";
import {
  isShortcutSheetKey,
  isTypingTarget,
  MOD,
  modifierLabel,
  SHORTCUT_GROUPS,
  type Shortcut,
  shortcutKeys,
} from "@/lib/shortcuts";
import type { Identity } from "@/lib/types";

const mocks = vi.hoisted(() => ({
  identity: { name: "root", kind: "admin", namespace: null } as Identity | null,
  logout: vi.fn(),
  shortcutsFromPalette: null as null | (() => void),
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    pathname: "/",
    query: {},
    isReady: true,
    events: { on: vi.fn(), off: vi.fn() },
  }),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: mocks.identity, logout: mocks.logout }),
}));
// The real palette is exercised in command-palette.test.tsx; here it only has to
// hand back the callback the shell wires into its "Keyboard shortcuts" entry.
vi.mock("@/components/palette/CommandPalette", () => ({
  default: ({ open, onShortcuts }: { open: boolean; onShortcuts?: () => void }) => {
    mocks.shortcutsFromPalette = onShortcuts ?? null;
    return open ? <div data-testid="palette" /> : null;
  },
}));

/** happy-dom reports "X11; Darwin arm64"; pin what a real browser would say. */
function setPlatform(platform: string): void {
  Object.defineProperty(window.navigator, "platform", { value: platform, configurable: true });
}

function renderShell(children = <p>page</p>) {
  return render(<AppShell>{children}</AppShell>);
}

describe("keyboard shortcut sheet", () => {
  beforeEach(() => {
    mocks.identity = { name: "root", kind: "admin", namespace: null };
    mocks.shortcutsFromPalette = null;
    resetNamespaceMemory();
    setPlatform("MacIntel");
  });
  afterEach(() => {
    // Restores the prototype getter.
    delete (window.navigator as { platform?: string }).platform;
  });

  it("opens on ? from anywhere and closes on Escape", async () => {
    renderShell();
    expect(screen.queryByRole("dialog")).toBeNull();

    // fireEvent returns false when the handler called preventDefault.
    expect(fireEvent.keyDown(window, { key: "?" })).toBe(false);
    const sheet = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    expect(sheet).toBeVisible();

    fireEvent.keyDown(sheet, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("stays shut while the visitor is typing a question mark", () => {
    renderShell(
      <>
        <input aria-label="Key prefix" />
        <textarea aria-label="Value" />
      </>,
    );

    for (const label of ["Key prefix", "Value"]) {
      const field = screen.getByLabelText(label);
      // Not prevented, so the character reaches the field as typed.
      expect(fireEvent.keyDown(field, { key: "?" })).toBe(true);
      expect(screen.queryByRole("dialog")).toBeNull();
    }

    // A modified ? is some other binding; the sheet keeps out of its way.
    expect(fireEvent.keyDown(window, { key: "?", metaKey: true })).toBe(true);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("lists every documented shortcut with the platform's modifier", async () => {
    renderShell();
    fireEvent.keyDown(window, { key: "?" });
    const sheet = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });

    for (const group of SHORTCUT_GROUPS) {
      expect(within(sheet).getByText(group.title)).toBeVisible();
      for (const shortcut of group.shortcuts) {
        expect(within(sheet).getAllByText(shortcut.description).length).toBeGreaterThan(0);
      }
    }
    // Rendered on a Mac, so the palette chip is ⌘ and never Ctrl.
    const keys = [...sheet.querySelectorAll("kbd")].map((kbd) => kbd.textContent);
    expect(keys).toContain("⌘");
    expect(keys).not.toContain("Ctrl");
  });

  it("shows Ctrl instead of ⌘ off Apple platforms", async () => {
    setPlatform("Win32");
    renderShell();
    fireEvent.keyDown(window, { key: "?" });
    const sheet = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    await waitFor(() => {
      const keys = [...sheet.querySelectorAll("kbd")].map((kbd) => kbd.textContent);
      expect(keys).toContain("Ctrl");
      expect(keys).not.toContain("⌘");
    });
  });

  it("is reachable from the command palette as well as the key", async () => {
    renderShell();
    expect(mocks.shortcutsFromPalette).toBeTypeOf("function");
    mocks.shortcutsFromPalette?.();
    expect(await screen.findByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();
  });
});

describe("shortcut helpers", () => {
  const event = (init: Partial<KeyboardEvent> & { key: string }) =>
    ({ metaKey: false, ctrlKey: false, altKey: false, target: null, ...init }) as KeyboardEvent;

  it("accepts a bare ? and nothing else", () => {
    expect(isShortcutSheetKey(event({ key: "?" }))).toBe(true);
    expect(isShortcutSheetKey(event({ key: "/" }))).toBe(false);
    expect(isShortcutSheetKey(event({ key: "?", metaKey: true }))).toBe(false);
    expect(isShortcutSheetKey(event({ key: "?", ctrlKey: true }))).toBe(false);
    expect(isShortcutSheetKey(event({ key: "?", altKey: true }))).toBe(false);
  });

  it("treats fields and rich-text areas as places the visitor is typing", () => {
    expect(isTypingTarget(document.createElement("input"))).toBe(true);
    expect(isTypingTarget(document.createElement("textarea"))).toBe(true);
    expect(isTypingTarget(document.createElement("select"))).toBe(true);
    expect(isTypingTarget(document.createElement("div"))).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
    expect(isTypingTarget(new EventTarget())).toBe(false);
  });

  it("swaps the modifier placeholder for the platform's own symbol", () => {
    const shortcut: Shortcut = { keys: [MOD, "K"], description: "palette" };
    expect(shortcutKeys(shortcut, true)).toEqual(["⌘", "K"]);
    expect(shortcutKeys(shortcut, false)).toEqual(["Ctrl", "K"]);
    expect(modifierLabel(true)).toBe("⌘");
    expect(modifierLabel(false)).toBe("Ctrl");
  });

  it("documents the palette, the sheet itself and Escape", () => {
    const all = SHORTCUT_GROUPS.flatMap((group) => group.shortcuts);
    expect(all.some((s) => s.keys.includes(MOD) && s.keys.includes("K"))).toBe(true);
    expect(all.some((s) => s.keys.length === 1 && s.keys[0] === "?")).toBe(true);
    expect(all.some((s) => s.keys.length === 1 && s.keys[0] === "Esc")).toBe(true);
    // No group ships without a scope line saying where its keys apply.
    expect(SHORTCUT_GROUPS.every((group) => group.scope.length > 0)).toBe(true);
  });
});
