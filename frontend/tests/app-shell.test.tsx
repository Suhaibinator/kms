import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AppShell, { isApplePlatform } from "@/components/AppShell";
import { rememberNamespace, resetNamespaceMemory } from "@/lib/namespace-memory";
import type { Identity } from "@/lib/types";

const mocks = vi.hoisted(() => ({
  identity: null as Identity | null,
  logout: vi.fn(),
  pathname: "/",
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    pathname: mocks.pathname,
    query: {},
    isReady: true,
    events: { on: vi.fn(), off: vi.fn() },
  }),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: mocks.identity, logout: mocks.logout }),
}));
vi.mock("@/components/palette/CommandPalette", () => ({
  default: ({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) =>
    open ? (
      <div data-testid="palette">
        <button type="button" onClick={() => onOpenChange(false)}>
          close palette
        </button>
      </div>
    ) : null,
}));

const admin: Identity = { name: "root", kind: "admin", namespace: null };
const client: Identity = {
  name: "gradethis-prod",
  kind: "client",
  namespace: { env: "prod", app: "gradethis" },
  auth_method: "mtls",
};

function desktopNav(): HTMLElement {
  const aside = document.querySelector("aside.desktop-sidebar") as HTMLElement;
  return within(aside).getByRole("navigation", { name: "Primary navigation" });
}

/** happy-dom reports "X11; Darwin arm64"; pin what a real browser would say. */
function setPlatform(platform: string): void {
  Object.defineProperty(window.navigator, "platform", { value: platform, configurable: true });
}

describe("AppShell", () => {
  beforeEach(() => {
    mocks.identity = admin;
    mocks.pathname = "/";
    resetNamespaceMemory();
    setPlatform("MacIntel");
  });
  afterEach(() => {
    // Restores the prototype getter.
    delete (window.navigator as { platform?: string }).platform;
  });

  it("groups the admin nav: Overview, Applications alone, Browse, Access control, Operations", () => {
    render(
      <AppShell>
        <p>page</p>
      </AppShell>,
    );
    const nav = desktopNav();
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual([
      "Overview",
      "Applications",
      "App environments",
      "Parameters",
      "Secrets",
      "Releases",
      "Policies",
      "Identities",
      "Audit log",
      "Security posture",
      "Subscribers",
      "Health & keys",
    ]);
    const labels = [...nav.querySelectorAll(".nav-section-label")].map((el) => el.textContent);
    expect(labels).toEqual(["Browse", "Access control", "Operations"]);
    expect(within(nav).getByRole("link", { name: "Overview" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.queryByRole("note")).toBeNull();
  });

  it("makes the skip link's target focusable", () => {
    render(
      <AppShell>
        <p>page</p>
      </AppShell>,
    );
    const skip = screen.getByRole("link", { name: "Skip to content" });
    expect(skip).toHaveAttribute("href", "#main-content");
    const main = screen.getByRole("main");
    expect(main).toHaveAttribute("id", "main-content");
    expect(main).toHaveAttribute("tabindex", "-1");
    main.focus();
    expect(main).toHaveFocus();
  });

  it("carries the last namespace into the Parameters, Secrets and Releases links", () => {
    render(
      <AppShell>
        <p>page</p>
      </AppShell>,
    );
    const nav = desktopNav();
    const href = (name: string) => within(nav).getByRole("link", { name }).getAttribute("href");
    expect(href("Parameters")).toBe("/parameters");
    expect(href("Secrets")).toBe("/secrets");
    expect(href("Releases")).toBe("/releases");

    act(() => rememberNamespace({ env: "prod", app: "gradethis" }));
    expect(href("Parameters")).toBe("/parameters?env=prod&app=gradethis");
    expect(href("Secrets")).toBe("/secrets?env=prod&app=gradethis");
    expect(href("Releases")).toBe("/releases?app=gradethis&env=prod");
    // Pages without a namespace picker stay bare.
    expect(href("App environments")).toBe("/namespaces");
    expect(href("Audit log")).toBe("/audit");
    // Active matching still works on the bare pathname.
    expect(within(nav).getByRole("link", { name: "Overview" })).toHaveAttribute(
      "aria-current",
      "page",
    );

    act(() => rememberNamespace(null));
    expect(href("Parameters")).toBe("/parameters");
  });

  it("marks the namespaced link as current on its page", () => {
    mocks.pathname = "/parameters/detail";
    rememberNamespace({ env: "dev", app: "reports" });
    render(
      <AppShell>
        <p>page</p>
      </AppShell>,
    );
    const link = within(desktopNav()).getByRole("link", { name: "Parameters" });
    expect(link).toHaveAttribute("href", "/parameters?env=dev&app=reports");
    expect(link).toHaveAttribute("aria-current", "page");
    expect(link).toHaveClass("active");
  });

  it("hides admin-only items for a client identity and shows the binding strip", () => {
    mocks.identity = client;
    render(
      <AppShell>
        <p>page</p>
      </AppShell>,
    );
    const nav = desktopNav();
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual(["Overview", "App environments", "Parameters", "Secrets", "Releases", "Audit log"]);
    expect([...nav.querySelectorAll(".nav-section-label")].map((el) => el.textContent)).toEqual([
      "Browse",
      "Operations",
    ]);
    const strip = screen.getByRole("note");
    expect(strip).toHaveTextContent(
      "Signed in as client identity gradethis-prod (mtls) bound to prod/gradethis. Application management requires an admin identity.",
    );
  });

  it("opens the command palette from the search button and toggles it with ⌘K / Ctrl+K", () => {
    render(
      <AppShell>
        <p>page</p>
      </AppShell>,
    );
    expect(screen.queryByTestId("palette")).toBeNull();
    const aside = document.querySelector("aside.desktop-sidebar") as HTMLElement;
    const search = within(aside).getByRole("button", { name: /Search…/ });
    expect(search).toHaveClass("nav-search");
    expect(search.querySelector("kbd")).toHaveTextContent("⌘K");
    fireEvent.click(search);
    expect(screen.getByTestId("palette")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "close palette" }));
    expect(screen.queryByTestId("palette")).toBeNull();

    // fireEvent returns false when the handler called preventDefault.
    expect(fireEvent.keyDown(window, { key: "k", metaKey: true })).toBe(false);
    expect(screen.getByTestId("palette")).toBeVisible();
    expect(fireEvent.keyDown(window, { key: "K", ctrlKey: true })).toBe(false);
    expect(screen.queryByTestId("palette")).toBeNull();

    expect(fireEvent.keyDown(window, { key: "k" })).toBe(true);
    expect(screen.queryByTestId("palette")).toBeNull();
    expect(fireEvent.keyDown(window, { key: "k", metaKey: true, altKey: true })).toBe(true);
    expect(screen.queryByTestId("palette")).toBeNull();
  });

  it("shows Ctrl K instead of ⌘K off Apple platforms, while declaring both", () => {
    setPlatform("Win32");
    render(
      <AppShell>
        <p>page</p>
      </AppShell>,
    );
    const aside = document.querySelector("aside.desktop-sidebar") as HTMLElement;
    const search = within(aside).getByRole("button", { name: /Search…/ });
    expect(search.querySelector("kbd")).toHaveTextContent("Ctrl K");
    expect(search).toHaveAttribute("aria-keyshortcuts", "Meta+K Control+K");
    // Both chips (drawer and desktop sidebar) agree.
    for (const kbd of document.querySelectorAll(".nav-search kbd")) {
      expect(kbd).toHaveTextContent("Ctrl K");
    }
    expect(fireEvent.keyDown(window, { key: "k", ctrlKey: true })).toBe(false);
    expect(screen.getByTestId("palette")).toBeVisible();
  });

  it("detects Apple platforms from client hints first, then navigator.platform", () => {
    const nav = (platform: string, hint?: string) =>
      ({ platform, userAgentData: hint ? { platform: hint } : undefined }) as unknown as Navigator;
    expect(isApplePlatform(nav("MacIntel"))).toBe(true);
    expect(isApplePlatform(nav("iPhone"))).toBe(true);
    expect(isApplePlatform(nav("iPad"))).toBe(true);
    expect(isApplePlatform(nav("Win32"))).toBe(false);
    expect(isApplePlatform(nav("Linux x86_64"))).toBe(false);
    expect(isApplePlatform(nav("", "macOS"))).toBe(true);
    expect(isApplePlatform(nav("MacIntel", "Windows"))).toBe(false);
    expect(isApplePlatform(nav(""))).toBe(false);
  });
});
