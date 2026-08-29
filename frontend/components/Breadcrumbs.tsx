import Link from "next/link";
import { Ident } from "@/components/Ident";
import type { Crumb } from "@/lib/crumbs";

/**
 * `<nav aria-label="Breadcrumb">` over a Crumb[] trail. Every crumb but the
 * last links; the last is the current page, and `aria-current="page"` sits on
 * its content (the label span or the chip wrapper) rather than the `<li>`, so
 * assistive tech attaches it to the node it reads out. Typed crumbs render as
 * Ident chips so the trail reads `Applications › app gradethis › env prod`.
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
            const chip = <Ident kind={crumb.ident.kind} value={crumb.ident.value} href={href} />;
            content = last ? <span aria-current="page">{chip}</span> : chip;
          } else if (href) {
            content = (
              <Link href={href} className="crumb-link">
                {crumb.label}
              </Link>
            );
          } else {
            content = (
              <span className="crumb-label" aria-current={last ? "page" : undefined}>
                {crumb.label}
              </span>
            );
          }
          return (
            <li
              key={`${index}:${crumb.ident?.value ?? String(crumb.href ?? "")}`}
              className="crumb"
            >
              {content}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
