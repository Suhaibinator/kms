import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { NAV } from "@/components/AppShell";
import CommandPalette, { resetPaletteCache } from "@/components/palette/CommandPalette";
import { links } from "@/lib/links";
import { rememberNamespace, resetNamespaceMemory } from "@/lib/namespace-memory";
import {
  buildPaletteIndex,
  fallthroughActions,
  fuzzyScore,
  NEW_APPLICATION_HREF,
  PALETTE_PAGES,
  PALETTE_RESULT_LIMIT,
  type PaletteItem,
  rankPalette,
  searchPalette,
} from "@/lib/palette";
import type { Application, Identity, Namespace } from "@/lib/types";
import fleetJson from "./fixtures/backend/fleet.json";

const mocks = vi.hoisted(() => ({
  push: vi.fn(async () => true),
  listApplications: vi.fn(),
  namespaces: [] as Namespace[],
  identity: null as Identity | null,
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    pathname: "/",
    query: {},
    isReady: true,
    push: mocks.push,
    events: { on: vi.fn(), off: vi.fn() },
  }),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: mocks.identity, logout: vi.fn() }),
}));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return { ...actual, api: { ...actual.api, listApplications: mocks.listApplications } };
});
vi.mock("@/lib/hooks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/hooks")>();
  return {
    ...actual,
    useNamespaces: () => ({
      namespaces: mocks.namespaces,
      loading: false,
      error: null,
      reload: vi.fn(),
    }),
  };
});

const applications = (
  fleetJson as { applications: Array<{ application: Application }> }
).applications.map((entry) => entry.application);

function namespace(env: string, app: string): Namespace {
  return {
    env,
    app,
    description: "",
    allowed_auth_methods: ["mtls"],
    created_by: "admin",
    created_at_unix_ms: 1,
    parameter_count: 0,
    secret_count: 0,
  };
}

const namespaces = [
  namespace("dev", "gradethis"),
  namespace("prod", "gradethis"),
  namespace("prod-eu", "reports"),
  namespace("prod", "legacy"),
];

const admin: Identity = { name: "root", kind: "admin", namespace: null };
const client: Identity = {
  name: "gradethis-prod",
  kind: "client",
  namespace: { env: "prod", app: "gradethis" },
};

describe("palette index", () => {
  it("mirrors the shell navigation one page per nav item, with the same admin gating", () => {
    const nav = NAV.flatMap((group) => group.items).map((item) => ({
      href: item.href,
      label: item.label,
      adminOnly: item.adminOnly ?? false,
    }));
    const pages = PALETTE_PAGES.map((page) => ({
      href: page.href,
      label: page.label,
      adminOnly: page.adminOnly ?? false,
    }));
    expect(pages).toEqual(nav);
  });

  it("builds applications, environments, aliases, pages and actions for an admin", () => {
    const index = buildPaletteIndex({ applications, namespaces, isAdmin: true });
    const ids = index.map((item) => item.id);
    expect(ids).toContain("app:gradethis");
    expect(ids).toContain("env:prod/gradethis");
    expect(ids).toContain("alias:prod/gradethis/rate_limits");
    expect(ids).toContain("page:/identities");
    expect(ids).toContain("action:new-application");
    expect(ids).toContain("action:rollback:prod/gradethis");
    // A namespace without an application gets no alias or rollback items.
    expect(ids).not.toContain("action:rollback:prod/legacy");
    expect(ids.filter((id) => id.startsWith("alias:prod/legacy"))).toEqual([]);

    const alias = index.find((item) => item.id === "alias:prod/gradethis/rate_limits");
    expect(alias).toMatchObject({
      group: "Aliases",
      title: "rate_limits",
      subtitle: "Ship a change · prod/gradethis",
      href: links.application("gradethis", { env: "prod", ship: "rate_limits" }),
    });
    // The applications page opens the create wizard on `?new=1`.
    const newApplication = index.find((item) => item.id === "action:new-application");
    expect(newApplication?.href).toBe(NEW_APPLICATION_HREF);
    expect(newApplication?.href).toBe("/applications?new=1");
    expect(newApplication?.href).toContain("?new=1");
    expect(index.find((item) => item.id === "action:rollback:prod/gradethis")?.href).toBe(
      links.application("gradethis", { env: "prod", rollback: true }),
    );
  });

  it("filters admin-only items for a client identity", () => {
    const index = buildPaletteIndex({ applications: [], namespaces, isAdmin: false });
    expect(index.some((item) => item.adminOnly)).toBe(false);
    expect(index.map((item) => item.group)).not.toContain("Aliases");
    expect(index.find((item) => item.id === "page:/identities")).toBeUndefined();
    expect(index.find((item) => item.id === "env:prod/legacy")?.href).toBe(
      links.parameters({ env: "prod", app: "legacy" }),
    );
  });
});

describe("fuzzyScore / searchPalette", () => {
  const index = buildPaletteIndex({ applications, namespaces, isAdmin: true });
  const item = (id: string): PaletteItem => index.find((entry) => entry.id === id) as PaletteItem;

  it("requires every token to match and prefers prefix over subsequence", () => {
    expect(fuzzyScore("gradethis", item("app:gradethis"))).toBeGreaterThan(0);
    expect(fuzzyScore("grd", item("app:gradethis"))).toBeGreaterThan(0);
    expect(fuzzyScore("gradethis zzz", item("app:gradethis"))).toBe(0);
    expect(fuzzyScore("gra", item("app:gradethis"))).toBeGreaterThan(
      fuzzyScore("grd", item("app:gradethis")),
    );
    expect(fuzzyScore("", item("app:gradethis"))).toBeGreaterThan(0);
  });

  it("ranks the alias item first for 'prod gradethis rate'", () => {
    const results = searchPalette(index, "prod gradethis rate");
    expect(results[0]?.id).toBe("alias:prod/gradethis/rate_limits");
    expect(results.length).toBeLessThanOrEqual(12);
  });

  it("caps results at 12 and leads with pages and actions when nothing is typed", () => {
    const results = searchPalette(index, "");
    expect(PALETTE_RESULT_LIMIT).toBe(12);
    expect(results).toHaveLength(12);
    expect(results[0]?.group).toBe("Pages");
    expect(searchPalette(index, "", 3)).toHaveLength(3);
  });

  it("ranks every match so the palette can say how many the cap hid", () => {
    const all = rankPalette(index, "");
    expect(all).toHaveLength(index.length);
    expect(all.slice(0, 12)).toEqual(searchPalette(index, ""));
    const matches = rankPalette(index, "gradethis");
    expect(matches.length).toBeGreaterThan(0);
    expect(matches.every((item) => fuzzyScore("gradethis", item) > 0)).toBe(true);
    expect(rankPalette(index, "zzzz-nothing")).toEqual([]);
  });

  it("offers parameter and secret key searches for a query inside a namespace", () => {
    const ns = { env: "prod", app: "gradethis" };
    expect(fallthroughActions("", ns)).toEqual([]);
    expect(fallthroughActions("db", null)).toEqual([]);
    const actions = fallthroughActions("  db/host ", ns);
    expect(actions.map((item) => item.group)).toEqual(["Actions", "Actions"]);
    expect(actions[0]).toMatchObject({
      id: "action:search-parameters",
      title: 'Search parameters for "db/host"',
      href: links.parameters(ns, "db/host"),
    });
    expect(actions[1]).toMatchObject({
      id: "action:search-secrets",
      title: 'Search secrets for "db/host"',
      href: links.secrets(ns, "db/host"),
    });
    expect(actions[0]?.href).toBe("/parameters?env=prod&app=gradethis&key_prefix=db%2Fhost");
  });
});

describe("CommandPalette", () => {
  beforeEach(() => {
    resetPaletteCache();
    resetNamespaceMemory();
    mocks.push.mockClear();
    mocks.listApplications.mockReset();
    mocks.listApplications.mockResolvedValue({ applications, next_page_token: "" });
    mocks.namespaces = namespaces;
    mocks.identity = admin;
  });

  it("renders nothing while closed and fetches applications on first open", async () => {
    const { rerender } = render(<CommandPalette open={false} onOpenChange={vi.fn()} />);
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(mocks.listApplications).not.toHaveBeenCalled();

    rerender(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    expect(mocks.listApplications).toHaveBeenCalledWith(
      200,
      undefined,
      expect.objectContaining({ signal: expect.anything() }),
    );
    expect(input).toHaveAttribute("aria-controls", screen.getByRole("listbox").id);
    // Default results: pages first.
    const listbox = screen.getByRole("listbox");
    await waitFor(() => expect(within(listbox).getByText("Overview")).toBeVisible());
    expect(within(listbox).getByText("Pages")).toBeVisible();
  });

  it("navigates to the ship deep link on Enter for 'prod gradethis rate' and closes", async () => {
    const onOpenChange = vi.fn();
    render(<CommandPalette open onOpenChange={onOpenChange} />);
    const input = await screen.findByRole("combobox");
    await waitFor(() => expect(mocks.listApplications).toHaveBeenCalled());

    fireEvent.change(input, { target: { value: "prod gradethis rate" } });
    const options = await screen.findAllByRole("option");
    expect(options[0]).toHaveTextContent("rate_limits");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(input).toHaveAttribute("aria-activedescendant", options[0]?.id);

    fireEvent.keyDown(input, { key: "Enter" });
    expect(mocks.push).toHaveBeenCalledWith(
      links.application("gradethis", { env: "prod", ship: "rate_limits" }),
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("moves the highlight with the arrow keys and wraps, and follows the mouse", async () => {
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    fireEvent.change(input, { target: { value: "gradethis" } });
    const options = await screen.findAllByRole("option");
    expect(options.length).toBeGreaterThan(2);

    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", options[1]?.id);
    fireEvent.keyDown(input, { key: "ArrowUp" });
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(input).toHaveAttribute("aria-activedescendant", options[options.length - 1]?.id);
    fireEvent.keyDown(input, { key: "Home" });
    expect(input).toHaveAttribute("aria-activedescendant", options[0]?.id);

    fireEvent.mouseEnter(options[2] as HTMLElement);
    expect(options[2]).toHaveAttribute("aria-selected", "true");
    fireEvent.click(options[2] as HTMLElement);
    expect(mocks.push).toHaveBeenCalledTimes(1);
  });

  it("closes on Escape and shows an empty state for no matches", async () => {
    const onOpenChange = vi.fn();
    render(<CommandPalette open onOpenChange={onOpenChange} />);
    const input = await screen.findByRole("combobox");
    fireEvent.change(input, { target: { value: "zzzz-nothing" } });
    expect(await screen.findByText(/No matches for/)).toBeVisible();
    expect(screen.queryAllByRole("option")).toEqual([]);
    expect(input).not.toHaveAttribute("aria-activedescendant");

    fireEvent.keyDown(input, { key: "Escape" });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("hides admin-only items and never lists applications for a client identity", async () => {
    mocks.identity = client;
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    expect(mocks.listApplications).not.toHaveBeenCalled();
    fireEvent.change(input, { target: { value: "identities" } });
    expect(await screen.findByText(/No matches for/)).toBeVisible();
    fireEvent.change(input, { target: { value: "prod legacy" } });
    const options = await screen.findAllByRole("option");
    expect(options[0]).toHaveTextContent("prod/legacy");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(mocks.push).toHaveBeenCalledWith(links.parameters({ env: "prod", app: "legacy" }));
  });

  it("labels each result group so the listbox owns its options", async () => {
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    fireEvent.change(input, { target: { value: "gradethis" } });
    await screen.findAllByRole("option");
    const listbox = screen.getByRole("listbox");
    const groups = within(listbox).getAllByRole("group");
    expect(groups.length).toBeGreaterThan(1);
    for (const group of groups) {
      const label = document.getElementById(group.getAttribute("aria-labelledby") ?? "");
      expect(label).not.toBeNull();
      expect(label).toHaveClass("palette-group-label");
      expect(label).not.toHaveAttribute("role");
      expect(within(group).getAllByRole("option").length).toBeGreaterThan(0);
    }
    expect(within(listbox).getByRole("group", { name: "Applications" })).toBeVisible();
  });

  it("says how many matches the cap hid, and drops the line once the query narrows", async () => {
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    await waitFor(() => expect(mocks.listApplications).toHaveBeenCalled());
    // With nothing typed the whole index is a match; the fixture has more than 12 items.
    const total = rankPalette(
      buildPaletteIndex({ applications, namespaces, isAdmin: true }),
      "",
    ).length;
    expect(total).toBeGreaterThan(12);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(12));
    const more = screen.getByTestId("palette-more");
    expect(more).toHaveTextContent(`12 of ${total} — type to narrow`);
    expect(more).toHaveClass("palette-empty");

    fireEvent.change(input, { target: { value: "identities" } });
    await waitFor(() => expect(screen.queryByTestId("palette-more")).toBeNull());
  });

  it("phrases the cap line as matches once something is typed", async () => {
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    await waitFor(() => expect(mocks.listApplications).toHaveBeenCalled());
    const index = buildPaletteIndex({ applications, namespaces, isAdmin: true });
    // "e" is in almost everything; only assert the phrasing when the cap really hits.
    const total = rankPalette(index, "e").length;
    expect(total).toBeGreaterThan(12);
    fireEvent.change(input, { target: { value: "e" } });
    await waitFor(() =>
      expect(screen.getByTestId("palette-more")).toHaveTextContent(
        `12 of ${total} matches — keep typing`,
      ),
    );
  });

  it("falls through to a parameter or secret key search in the remembered namespace", async () => {
    rememberNamespace({ env: "prod", app: "gradethis" });
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    // Nothing typed: no fall-through.
    expect(screen.queryByText(/Search parameters for/)).toBeNull();

    fireEvent.change(input, { target: { value: "db/host" } });
    const options = await screen.findAllByRole("option");
    // Beyond the cap and always last: the two searches close the list.
    expect(options.length).toBeLessThanOrEqual(PALETTE_RESULT_LIMIT + 2);
    const parameters = screen.getByRole("option", { name: /Search parameters for "db\/host"/ });
    const secrets = screen.getByRole("option", { name: /Search secrets for "db\/host"/ });
    expect(options.at(-2)).toBe(parameters);
    expect(options.at(-1)).toBe(secrets);
    expect(
      within(screen.getByRole("listbox")).getByRole("group", { name: "Actions" }),
    ).toContainElement(parameters);

    fireEvent.keyDown(input, { key: "End" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(mocks.push).toHaveBeenCalledWith(
      links.secrets({ env: "prod", app: "gradethis" }, "db/host"),
    );
  });

  it("scopes the fall-through to a client identity's bound namespace", async () => {
    mocks.identity = client;
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    fireEvent.change(input, { target: { value: "rate" } });
    const option = await screen.findByRole("option", { name: /Search parameters for "rate"/ });
    fireEvent.click(option);
    expect(mocks.push).toHaveBeenCalledWith(
      links.parameters({ env: "prod", app: "gradethis" }, "rate"),
    );
  });

  it("offers no key search without a namespace in play", async () => {
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    const input = await screen.findByRole("combobox");
    fireEvent.change(input, { target: { value: "zzzz-nothing" } });
    expect(await screen.findByText(/No matches for/)).toBeVisible();
    expect(screen.queryByRole("option", { name: /Search parameters/ })).toBeNull();
  });

  it("shows keyboard hints in the footer", async () => {
    render(<CommandPalette open onOpenChange={vi.fn()} />);
    await screen.findByRole("combobox");
    const foot = document.querySelector(".palette-foot") as HTMLElement;
    expect(foot).toHaveTextContent("navigate");
    expect(foot).toHaveTextContent("open");
    expect(foot).toHaveTextContent("close");
    expect(foot.querySelectorAll("kbd").length).toBeGreaterThanOrEqual(4);
  });
});
