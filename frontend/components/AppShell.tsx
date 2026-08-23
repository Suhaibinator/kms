import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import Link from "next/link";
import { useRouter } from "next/router";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { Icon } from "./icons";
import { LogoMark } from "./LogoMark";

interface NavItem {
  href: string;
  label: string;
  icon: ReactNode;
  exact?: boolean;
  adminOnly?: boolean;
}

interface NavGroup {
  label?: string;
  items: NavItem[];
}

const NAV: NavGroup[] = [
  {
    items: [{ href: "/", label: "Overview", icon: <Icon.dashboard />, exact: true }],
  },
  {
    label: "Configuration",
    items: [
      { href: "/applications", label: "Applications", icon: <Icon.application />, adminOnly: true },
      { href: "/namespaces", label: "App environments", icon: <Icon.namespace /> },
      { href: "/parameters", label: "Parameters", icon: <Icon.parameter /> },
      { href: "/secrets", label: "Secrets", icon: <Icon.secret /> },
      { href: "/releases", label: "Releases", icon: <Icon.release /> },
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

function SidebarContent({
  groups,
  pathname,
  identity,
  logout,
  onNavigate,
}: {
  groups: NavGroup[];
  pathname: string;
  identity: { name: string; kind: string } | null;
  logout: () => void;
  onNavigate?: () => void;
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

      <nav className="nav" aria-label="Primary navigation">
        {groups.map((group) => (
          <div key={group.label ?? group.items[0]?.href}>
            {group.label ? <div className="nav-section-label">{group.label}</div> : null}
            {group.items.map((item) => (
              <Link
                key={item.href}
                href={item.href}
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

export default function AppShell({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { identity, logout } = useAuth();
  const [navOpen, setNavOpen] = useState(false);
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

  return (
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
                className="nav-toggle"
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
          />
        </SheetContent>
      </Sheet>

      <aside className="sidebar desktop-sidebar">
        <SidebarContent
          groups={visibleNav}
          pathname={router.pathname}
          identity={identity}
          logout={logout}
        />
      </aside>

      <main id="main-content" className="main">
        <div className="page">{children}</div>
      </main>
    </div>
  );
}
