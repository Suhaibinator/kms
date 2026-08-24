import Link from "next/link";
import { Ident } from "@/components/Ident";
import type { Crumb } from "@/lib/crumbs";

/**
 * `<nav aria-label="Breadcrumb">` over a Crumb[] trail. Every crumb but the
 * last links; the last is the current page (`aria-current="page"`). Typed
 * crumbs render as Ident chips so the trail reads `Applications › app
 * gradethis › env prod`.
 */
export function Breadcrumbs({ items }: { items: Crumb[] }) {
  if (items.length === 0) return null;
  return (
    <nav aria-label="Breadcrumb" className="crumbs-nav">
      <ol className="crumbs">
        {items.map((crumb, index) => {
          const last = index === items.length - 1;
          const href = last ? undefined : crumb.href;
          let content: React.ReactNode;
          if (crumb.ident) {
            content = <Ident kind={crumb.ident.kind} value={crumb.ident.value} href={href} />;
          } else if (href) {
            content = (
              <Link href={href} className="crumb-link">
                {crumb.label}
              </Link>
            );
          } else {
            content = <span className="crumb-label">{crumb.label}</span>;
          }
          return (
            <li
              key={`${index}:${crumb.ident?.value ?? String(crumb.href ?? "")}`}
              className="crumb"
              aria-current={last ? "page" : undefined}
            >
              {content}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
