// The command palette's index and search. Pure: the palette
// component feeds it the application and namespace lists and renders what
// comes back. Scoring is token-prefix / substring / subsequence per query
// token; every token must match somewhere or the item drops out.

import { links, type NamespaceRef } from "@/lib/links";
import type { Application, Namespace } from "@/lib/types";

export type PaletteGroup = "Applications" | "Environments" | "Aliases" | "Pages" | "Actions";

export const PALETTE_GROUPS: readonly PaletteGroup[] = [
  "Applications",
  "Environments",
  "Aliases",
  "Pages",
  "Actions",
];

export interface PaletteItem {
  id: string;
  group: PaletteGroup;
  title: string;
  subtitle?: string;
  href: string;
  /** Extra searchable words beyond title and subtitle. */
  keywords: string[];
  adminOnly?: boolean;
}

export interface PaletteIndexInput {
  applications: readonly Application[];
  namespaces: readonly Namespace[];
  isAdmin: boolean;
}

// Mirrors AppShell's NAV (href, label, adminOnly) — tests/command-palette
// asserts the two stay in step. Kept here so lib/ never imports a component.
export const PALETTE_PAGES: ReadonlyArray<{
  href: string;
  label: string;
  keywords: string[];
  adminOnly?: boolean;
}> = [
  { href: "/", label: "Overview", keywords: ["home", "dashboard", "fleet"] },
  { href: "/applications", label: "Applications", keywords: ["apps"], adminOnly: true },
  { href: "/namespaces", label: "App environments", keywords: ["namespaces", "envs"] },
  { href: "/parameters", label: "Parameters", keywords: ["values", "config"] },
  { href: "/secrets", label: "Secrets", keywords: ["passwords", "keys"] },
  { href: "/releases", label: "Releases", keywords: ["activate", "schemas"] },
  { href: "/policies", label: "Policies", keywords: ["access", "grants"], adminOnly: true },
  {
    href: "/identities",
    label: "Identities",
    keywords: ["clients", "tokens", "mtls"],
    adminOnly: true,
  },
  { href: "/audit", label: "Audit log", keywords: ["events", "history"] },
  {
    href: "/posture",
    label: "Security posture",
    keywords: ["expiring", "certificates", "kek", "rotation", "security"],
    adminOnly: true,
  },
  {
    href: "/subscribers",
    label: "Subscribers",
    keywords: ["instances", "streams"],
    adminOnly: true,
  },
  {
    href: "/health",
    label: "Health & keys",
    keywords: ["status", "kek", "revision"],
    adminOnly: true,
  },
];

export const NEW_APPLICATION_HREF = "/applications?new=1";

/** The one palette entry that opens a dialog instead of navigating. */
export const SHORTCUTS_ACTION_ID = "action:shortcuts";

/** How many ranked matches the palette renders before asking for a narrower query. */
export const PALETTE_RESULT_LIMIT = 12;

function nsLabel(ns: { env: string; app: string }): string {
  return `${ns.env}/${ns.app}`;
}

/** Builds the full index in group order, already filtered for the caller's role. */
export function buildPaletteIndex({
  applications,
  namespaces,
  isAdmin,
}: PaletteIndexInput): PaletteItem[] {
  const byName = new Map(applications.map((app) => [app.name, app]));
  const items: PaletteItem[] = [];

  for (const app of applications) {
    items.push({
      id: `app:${app.name}`,
      group: "Applications",
      title: app.name,
      subtitle: app.description || `release ${app.release_name}`,
      href: links.application(app.name),
      keywords: ["application", app.release_name],
      adminOnly: true,
    });
  }

  for (const ns of namespaces) {
    const app = byName.get(ns.app);
    items.push({
      id: `env:${ns.env}/${ns.app}`,
      group: "Environments",
      title: nsLabel(ns),
      subtitle: app ? "Open environment" : ns.description || "Browse parameters",
      href: app ? links.application(ns.app, { env: ns.env }) : links.parameters(ns),
      keywords: ["environment", "namespace", ns.env, ns.app],
      adminOnly: Boolean(app),
    });
  }

  for (const ns of namespaces) {
    const app = byName.get(ns.app);
    if (!app) continue;
    for (const field of app.contract) {
      items.push({
        id: `alias:${ns.env}/${ns.app}/${field.alias}`,
        group: "Aliases",
        title: field.alias,
        subtitle: `Ship a change · ${nsLabel(ns)}`,
        href: links.application(ns.app, { env: ns.env, ship: field.alias }),
        keywords: ["ship", "alias", field.kind, ns.env, ns.app],
        adminOnly: true,
      });
    }
  }

  for (const page of PALETTE_PAGES) {
    items.push({
      id: `page:${page.href}`,
      group: "Pages",
      title: page.label,
      subtitle: "Go to page",
      href: page.href,
      keywords: ["page", "go", ...page.keywords],
      adminOnly: page.adminOnly,
    });
  }

  items.push({
    id: "action:new-application",
    group: "Actions",
    title: "New application",
    subtitle: "Open the create wizard",
    href: NEW_APPLICATION_HREF,
    keywords: ["create", "add", "app", "wizard"],
    adminOnly: true,
  });
  // Not a route: CommandPalette recognises this id and opens the sheet. The
  // href is the fragment the sheet would live at if it ever became one.
  items.push({
    id: SHORTCUTS_ACTION_ID,
    group: "Actions",
    title: "Keyboard shortcuts",
    subtitle: "Every shortcut the console answers to",
    href: "#keyboard-shortcuts",
    keywords: ["keys", "hotkeys", "help", "bindings"],
  });
  for (const ns of namespaces) {
    if (!byName.has(ns.app)) continue;
    items.push({
      id: `action:rollback:${ns.env}/${ns.app}`,
      group: "Actions",
      title: `Roll back ${nsLabel(ns)}`,
      subtitle: "Re-activate the previous release",
      href: links.application(ns.app, { env: ns.env, rollback: true }),
      keywords: ["rollback", "revert", "undo", "release", ns.env, ns.app],
      adminOnly: true,
    });
  }

  return isAdmin ? items : items.filter((item) => !item.adminOnly);
}

const wordsOf = (text: string): string[] =>
  text
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);

function isSubsequence(needle: string, haystack: string): boolean {
  let i = 0;
  for (const ch of haystack) {
    if (ch === needle[i]) i += 1;
    if (i === needle.length) return true;
  }
  return needle.length === 0;
}

function scoreToken(token: string, item: PaletteItem): number {
  const title = item.title.toLowerCase();
  if (title === token) return 100;
  if (title.startsWith(token)) return 60;
  const titleWords = wordsOf(item.title);
  if (titleWords.some((word) => word.startsWith(token))) return 40;
  const rest = wordsOf(`${item.subtitle ?? ""} ${item.keywords.join(" ")}`);
  if (rest.some((word) => word === token)) return 30;
  if (rest.some((word) => word.startsWith(token))) return 20;
  if (title.includes(token)) return 15;
  const all = `${title} ${rest.join(" ")}`;
  if (all.includes(token)) return 10;
  if (token.length >= 3 && isSubsequence(token, title.replace(/[^a-z0-9]/g, ""))) return 4;
  return 0;
}

/** 0 when any query token fails to match; otherwise the sum of per-token scores. */
export function fuzzyScore(query: string, item: PaletteItem): number {
  const tokens = wordsOf(query);
  if (tokens.length === 0) return 1;
  let total = 0;
  for (const token of tokens) {
    const score = scoreToken(token, item);
    if (score === 0) return 0;
    total += score;
  }
  return total;
}

const groupRank = (group: PaletteGroup): number => PALETTE_GROUPS.indexOf(group);

// With nothing typed the palette is a launcher: pages and actions first, then
// the applications, so the top of the list is stable across sessions.
const EMPTY_QUERY_ORDER: readonly PaletteGroup[] = [
  "Pages",
  "Actions",
  "Applications",
  "Environments",
  "Aliases",
];

/** Every match, highest score first; ties keep group order then index order. */
export function rankPalette(index: readonly PaletteItem[], query: string): PaletteItem[] {
  const trimmed = query.trim();
  if (!trimmed) {
    return [...index]
      .map((item, position) => ({ item, position }))
      .sort(
        (a, b) =>
          EMPTY_QUERY_ORDER.indexOf(a.item.group) - EMPTY_QUERY_ORDER.indexOf(b.item.group) ||
          a.position - b.position,
      )
      .map(({ item }) => item);
  }
  return index
    .map((item, position) => ({ item, position, score: fuzzyScore(trimmed, item) }))
    .filter((entry) => entry.score > 0)
    .sort(
      (a, b) =>
        b.score - a.score ||
        groupRank(a.item.group) - groupRank(b.item.group) ||
        a.position - b.position,
    )
    .map(({ item }) => item);
}

/** Best `limit` matches of `rankPalette`. */
export function searchPalette(
  index: readonly PaletteItem[],
  query: string,
  limit = PALETTE_RESULT_LIMIT,
): PaletteItem[] {
  return rankPalette(index, query).slice(0, limit);
}

/**
 * Parameter and secret keys are namespace-scoped and unbounded, so they are
 * not indexed; instead a non-empty query gets two fall-through actions that
 * hand it to the list pages as a key prefix. Rendered after the ranked
 * results, outside the cap, so they are always reachable.
 */
export function fallthroughActions(query: string, ns: NamespaceRef | null): PaletteItem[] {
  const trimmed = query.trim();
  if (!trimmed || !ns) return [];
  const where = nsLabel(ns);
  return [
    {
      id: "action:search-parameters",
      group: "Actions",
      title: `Search parameters for "${trimmed}"`,
      subtitle: `Keys starting with ${trimmed} · ${where}`,
      href: links.parameters(ns, trimmed),
      keywords: [],
    },
    {
      id: "action:search-secrets",
      group: "Actions",
      title: `Search secrets for "${trimmed}"`,
      subtitle: `Keys starting with ${trimmed} · ${where}`,
      href: links.secrets(ns, trimmed),
      keywords: [],
    },
  ];
}

/** Groups a result list for rendering, preserving the ranked order inside each group. */
export function groupResults(
  results: readonly PaletteItem[],
): Array<{ group: PaletteGroup; items: PaletteItem[] }> {
  const groups = new Map<PaletteGroup, PaletteItem[]>();
  for (const item of results) {
    const list = groups.get(item.group);
    if (list) list.push(item);
    else groups.set(item.group, [item]);
  }
  return PALETTE_GROUPS.filter((group) => groups.has(group)).map((group) => ({
    group,
    items: groups.get(group) ?? [],
  }));
}
