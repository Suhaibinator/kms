import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AppShell from "@/components/AppShell";
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

describe("AppShell", () => {
  beforeEach(() => {
    mocks.identity = admin;
    mocks.pathname = "/";
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
});
