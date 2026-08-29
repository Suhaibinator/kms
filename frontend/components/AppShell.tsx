import { Search } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/router";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import CommandPalette from "@/components/palette/CommandPalette";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useAuth } from "@/context/AuthContext";
import { displayNamespace } from "@/lib/format";
import { links } from "@/lib/links";
import { type NamespaceRef, useLastNamespace } from "@/lib/namespace-memory";
import type { Identity } from "@/lib/types";
import { Icon } from "./icons";
import { LogoMark } from "./LogoMark";
import { ThemeSwitch } from "./ThemeSwitch";

interface NavItem {
  href: string;
  label: string;
  icon: ReactNode;
  exact?: boolean;
  adminOnly?: boolean;
  /**
   * Deep link into the page with the operator's last namespace, so the picker
   * does not reset on every lateral move. `href` stays bare for active matching.
   */
  withNamespace?: (ns: NamespaceRef) => string;
}

interface NavGroup {
  label?: string;
  items: NavItem[];
}

// Applications is the primary destination, so it stands alone above the
// per-resource "Browse" pages that are entered from an application page.
export const NAV: NavGroup[] = [
  {
    items: [{ href: "/", label: "Overview", icon: <Icon.dashboard />, exact: true }],
  },
  {
    items: [
      { href: "/applications", label: "Applications", icon: <Icon.application />, adminOnly: true },
    ],
  },
  {
    label: "Browse",
    items: [
      { href: "/namespaces", label: "App environments", icon: <Icon.namespace /> },
      {
        href: "/parameters",
        label: "Parameters",
        icon: <Icon.parameter />,
        withNamespace: (ns) => links.parameters(ns),
      },
      {
        href: "/secrets",
        label: "Secrets",
        icon: <Icon.secret />,
        withNamespace: (ns) => links.secrets(ns),
      },
      {
        href: "/releases",
        label: "Releases",
        icon: <Icon.release />,
        withNamespace: (ns) => links.releases({ app: ns.app, env: ns.env }),
      },
    ],
  },
  {
    label: "Access control",
    items: [
      { href: "/policies", label: "Policies", icon: <Icon.policy />, adminOnly: true },
      { href: "/identities", label: "Identities", icon: <Icon.identity />, adminOnly: true },
    ],
  },
  {
    label: "Operations",
    items: [
      { href: "/audit", label: "Audit log", icon: <Icon.audit /> },
      { href: "/subscribers", label: "Subscribers", icon: <Icon.subscribers />, adminOnly: true },
      { href: "/health", label: "Health & keys", icon: <Icon.health />, adminOnly: true },
    ],
  },
];

function isActive(pathname: string, item: NavItem): boolean {
  if (item.exact) return pathname === item.href;
  return pathname === item.href || pathname.startsWith(`${item.href}/`);
}

const MAC_SHORTCUT_LABEL = "⌘K";
const OTHER_SHORTCUT_LABEL = "Ctrl K";

/** True on macOS and iOS, where the palette shortcut is the Command key. */
export function isApplePlatform(nav: Navigator = navigator): boolean {
  const withHints = nav as Navigator & { userAgentData?: { platform?: string } };
  const platform = withHints.userAgentData?.platform || nav.platform || "";
  return /mac|iphone|ipad|ipod/i.test(platform);
}

/**
 * The visible shortcut chip. The server has no platform to ask, so it renders
 * the Mac label and the client corrects it after hydration — swapping in an
 * effect rather than during render keeps the two markups identical.
 */
function useShortcutLabel(): string {
  const [label, setLabel] = useState(MAC_SHORTCUT_LABEL);
  useEffect(() => {
    if (!isApplePlatform()) setLabel(OTHER_SHORTCUT_LABEL);
  }, []);
  return label;
}

function SidebarContent({
  groups,
  pathname,
  identity,
  logout,
  onNavigate,
  onSearch,
  shortcutLabel,
  namespace,
}: {
  groups: NavGroup[];
  pathname: string;
  identity: { name: string; kind: string } | null;
  logout: () => void;
  onNavigate?: () => void;
  onSearch: () => void;
  shortcutLabel: string;
  namespace: NamespaceRef | null;
}) {
  return (
    <>
      <div className="sidebar-brand">
        <LogoMark />
        <div>
          <div className="brand-name">KMS</div>
          <div className="brand-sub">Parameter &amp; Secret Store</div>
        </div>
      </div>

      <button
        type="button"
        className="nav-search"
        onClick={onSearch}
        aria-keyshortcuts="Meta+K Control+K"
      >
        <Search size={15} strokeWidth={1.9} aria-hidden />
        <span className="nav-search-label">Search…</span>
        <Kbd>{shortcutLabel}</Kbd>
      </button>

      <nav className="nav" aria-label="Primary navigation">
        {groups.map((group) => (
          <div key={group.label ?? group.items[0]?.href}>
            {group.label ? <div className="nav-section-label">{group.label}</div> : null}
            {group.items.map((item) => (
              <Link
                key={item.href}
                href={namespace && item.withNamespace ? item.withNamespace(namespace) : item.href}
                className={`nav-link ${isActive(pathname, item) ? "active" : ""}`}
                aria-current={isActive(pathname, item) ? "page" : undefined}
                onClick={onNavigate}
              >
                <span className="nav-icon">{item.icon}</span>
                {item.label}
              </Link>
            ))}
          </div>
        ))}
      </nav>

      <div className="sidebar-footer">
        <div className="sidebar-theme">
          <span>Appearance</span>
          <ThemeSwitch />
        </div>
        <div className="identity-card">
          <div className="who">{identity?.name ?? "Unknown"}</div>
          <div className="kind">{identity?.kind ?? "session"}</div>
        </div>
        <Button type="button" variant="ghost" className="w-full" onClick={logout}>
          <Icon.logout />
          Log out
        </Button>
      </div>
    </>
  );
}

/** The strip a client identity sees: what it is bound to, and why admin pages are missing. */
function IdentityStrip({ identity }: { identity: Identity }) {
  const method = identity.auth_method ? ` (${identity.auth_method})` : "";
  const binding = identity.namespace
    ? ` bound to ${displayNamespace(identity.namespace)}`
    : ", not bound to an environment";
  return (
    <div className="identity-strip" role="note">
      Signed in as client identity <span className="mono">{identity.name}</span>
      {method}
      {binding}. Application management requires an admin identity.
    </div>
  );
}

export function isPaletteShortcut(event: KeyboardEvent): boolean {
  return (event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === "k";
}

export default function AppShell({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { identity, logout } = useAuth();
  const [navOpen, setNavOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const shortcutLabel = useShortcutLabel();
  const namespace = useLastNamespace();
  const visibleNav = useMemo(
    () =>
      NAV.map((group) => ({
        ...group,
        items: group.items.filter((item) => !item.adminOnly || identity?.kind === "admin"),
      })).filter((group) => group.items.length > 0),
    [identity?.kind],
  );

  // Close the drawer whenever the route changes (including query-only
  // navigation between detail pages).
  useEffect(() => {
    const close = () => setNavOpen(false);
    router.events.on("routeChangeStart", close);
    return () => router.events.off("routeChangeStart", close);
  }, [router.events]);

  // ⌘K / Ctrl+K toggles the command palette from anywhere in the shell.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!isPaletteShortcut(event)) return;
      event.preventDefault();
      setPaletteOpen((open) => !open);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  const openPalette = () => setPaletteOpen(true);

  return (
    <TooltipProvider delay={300}>
      <div className="app-shell">
        {/* First focus stop on every page, so keyboard users can jump the nav. */}
        <a
          href="#main-content"
          className="sr-only rounded-md focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-100 focus:bg-popover focus:px-3 focus:py-2 focus:text-sm focus:ring-1 focus:ring-border"
        >
          Skip to content
        </a>
        <Sheet open={navOpen} onOpenChange={setNavOpen}>
          <header className="mobile-topbar">
            <SheetTrigger
              render={
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="hidden shrink-0 max-md:inline-flex max-md:size-11 max-md:min-w-11"
                  aria-label="Open navigation"
                />
              }
            >
              <Icon.menu />
            </SheetTrigger>
            <div className="mobile-topbar-brand">
              <LogoMark />
              <span>KMS</span>
            </div>
          </header>
          <SheetContent
            side="left"
            closeLabel="Close navigation"
            className="mobile-sidebar w-[min(84vw,300px)] max-w-none gap-0 p-0"
          >
            <SheetTitle className="sr-only">Primary navigation</SheetTitle>
            <SidebarContent
              groups={visibleNav}
              pathname={router.pathname}
              identity={identity}
              logout={logout}
              onNavigate={() => setNavOpen(false)}
              onSearch={() => {
                setNavOpen(false);
                openPalette();
              }}
              shortcutLabel={shortcutLabel}
              namespace={namespace}
            />
          </SheetContent>
        </Sheet>

        <aside className="sidebar desktop-sidebar">
          <SidebarContent
            groups={visibleNav}
            pathname={router.pathname}
            identity={identity}
            logout={logout}
            onSearch={openPalette}
            shortcutLabel={shortcutLabel}
            namespace={namespace}
          />
        </aside>

        {/* tabIndex lets the skip link land focus here in every browser, not just
            move the scroll. */}
        <main id="main-content" className="main" tabIndex={-1}>
          {identity && identity.kind !== "admin" ? <IdentityStrip identity={identity} /> : null}
          <div className="page">{children}</div>
        </main>
        <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      </div>
    </TooltipProvider>
  );
}
