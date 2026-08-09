import Link from "next/link";
import { useRouter } from "next/router";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useAuth } from "@/context/AuthContext";
import { Icon } from "./icons";

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
      { href: "/namespaces", label: "Namespaces", icon: <Icon.namespace /> },
      { href: "/parameters", label: "Parameters", icon: <Icon.parameter /> },
      { href: "/secrets", label: "Secrets", icon: <Icon.secret /> },
      { href: "/releases", label: "Releases", icon: <Icon.parameter /> },
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

export default function AppShell({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { identity, logout } = useAuth();
  const [navOpen, setNavOpen] = useState(false);
  const [isMobile, setIsMobile] = useState(false);
  const toggleRef = useRef<HTMLButtonElement | null>(null);
  const sidebarRef = useRef<HTMLElement | null>(null);
  const visibleNav = useMemo(
    () =>
      NAV.map((group) => ({
        ...group,
        items: group.items.filter((item) => !item.adminOnly || identity?.kind === "admin"),
      })).filter((group) => group.items.length > 0),
    [identity?.kind],
  );

  useEffect(() => {
    const query = window.matchMedia("(max-width: 768px)");
    const update = () => setIsMobile(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  // Close the drawer whenever the route changes (including query-only
  // navigation between detail pages).
  useEffect(() => {
    const close = () => setNavOpen(false);
    router.events.on("routeChangeStart", close);
    return () => router.events.off("routeChangeStart", close);
  }, [router.events]);

  // While the drawer is open: close on Escape and lock background scroll so
  // the page behind it doesn't scroll along with a touch drag.
  useEffect(() => {
    if (!navOpen || !isMobile) return;
    const root = sidebarRef.current;
    root?.querySelector<HTMLElement>("a[href], button:not([disabled])")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setNavOpen(false);
        return;
      }
      if (e.key !== "Tab" || !root) return;
      const focusable = Array.from(
        root.querySelectorAll<HTMLElement>(
          'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])',
        ),
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prevOverflow;
      toggleRef.current?.focus();
    };
  }, [navOpen, isMobile]);

  return (
    <div className="app-shell">
      <header className="mobile-topbar">
        <button
          ref={toggleRef}
          type="button"
          className="nav-toggle"
          aria-expanded={navOpen}
          aria-controls="app-sidebar"
          aria-label={navOpen ? "Close navigation" : "Open navigation"}
          onClick={() => setNavOpen((v) => !v)}
        >
          {navOpen ? <Icon.close /> : <Icon.menu />}
        </button>
        <div className="mobile-topbar-brand">
          <div className="logo">K</div>
          <span>KMS</span>
        </div>
      </header>

      {navOpen ? (
        <div className="sidebar-backdrop" onClick={() => setNavOpen(false)} aria-hidden />
      ) : null}

      <aside
        ref={sidebarRef}
        id="app-sidebar"
        className={`sidebar ${navOpen ? "open" : ""}`}
        aria-label="Primary navigation"
        aria-hidden={isMobile && !navOpen ? true : undefined}
        inert={isMobile && !navOpen}
      >
        <div className="sidebar-brand">
          <div className="logo">K</div>
          <div>
            <div className="brand-name">KMS</div>
            <div className="brand-sub">Parameter &amp; Secret Store</div>
          </div>
        </div>

        <nav className="nav">
          {visibleNav.map((group) => (
            <div key={group.label ?? group.items[0]?.href}>
              {group.label ? <div className="nav-section-label">{group.label}</div> : null}
              {group.items.map((item) => (
                <Link
                  key={item.href}
                  href={item.href}
                  className={`nav-link ${isActive(router.pathname, item) ? "active" : ""}`}
                  aria-current={isActive(router.pathname, item) ? "page" : undefined}
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
          <button type="button" className="btn btn-ghost btn-block" onClick={logout}>
            <Icon.logout /> Log out
          </button>
        </div>
      </aside>

      <main
        className="main"
        aria-hidden={isMobile && navOpen ? true : undefined}
        inert={isMobile && navOpen}
      >
        <div className="page">{children}</div>
      </main>
    </div>
  );
}
