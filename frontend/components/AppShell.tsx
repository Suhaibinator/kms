import Link from "next/link";
import { useRouter } from "next/router";
import { useEffect, useState, type ReactNode } from "react";
import { useAuth } from "@/context/AuthContext";
import { Icon } from "./icons";

interface NavItem {
  href: string;
  label: string;
  icon: ReactNode;
  exact?: boolean;
}

interface NavGroup {
  label?: string;
  items: NavItem[];
}

const NAV: NavGroup[] = [
  {
    items: [
      { href: "/", label: "Overview", icon: <Icon.dashboard />, exact: true },
    ],
  },
  {
    label: "Configuration",
    items: [
      { href: "/namespaces", label: "Namespaces", icon: <Icon.namespace /> },
      { href: "/parameters", label: "Parameters", icon: <Icon.parameter /> },
      { href: "/secrets", label: "Secrets", icon: <Icon.secret /> },
    ],
  },
  {
    label: "Access control",
    items: [
      { href: "/policies", label: "Policies", icon: <Icon.policy /> },
      { href: "/identities", label: "Identities", icon: <Icon.identity /> },
    ],
  },
  {
    label: "Operations",
    items: [
      { href: "/audit", label: "Audit log", icon: <Icon.audit /> },
      { href: "/subscribers", label: "Subscribers", icon: <Icon.subscribers /> },
      { href: "/health", label: "Health & keys", icon: <Icon.health /> },
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

  // Close the drawer whenever the route changes (including query-only
  // navigation between detail pages).
  useEffect(() => {
    setNavOpen(false);
  }, [router.asPath]);

  // While the drawer is open: close on Escape and lock background scroll so
  // the page behind it doesn't scroll along with a touch drag.
  useEffect(() => {
    if (!navOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setNavOpen(false);
    };
    window.addEventListener("keydown", onKey);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [navOpen]);

  return (
    <div className="app-shell">
      <header className="mobile-topbar">
        <button
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
        <div
          className="sidebar-backdrop"
          onClick={() => setNavOpen(false)}
          aria-hidden
        />
      ) : null}

      <aside id="app-sidebar" className={`sidebar ${navOpen ? "open" : ""}`}>
        <div className="sidebar-brand">
          <div className="logo">K</div>
          <div>
            <div className="brand-name">KMS</div>
            <div className="brand-sub">Parameter &amp; Secret Store</div>
          </div>
        </div>

        <nav className="nav">
          {NAV.map((group, gi) => (
            <div key={gi}>
              {group.label ? (
                <div className="nav-section-label">{group.label}</div>
              ) : null}
              {group.items.map((item) => (
                <Link
                  key={item.href}
                  href={item.href}
                  className={`nav-link ${isActive(router.pathname, item) ? "active" : ""}`}
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
          <button className="btn btn-ghost btn-block" onClick={logout}>
            <Icon.logout /> Log out
          </button>
        </div>
      </aside>

      <main className="main">
        <div className="page">{children}</div>
      </main>
    </div>
  );
}
