import Head from "next/head";
import type { ReactNode } from "react";
import type { SecretVersionState } from "@/lib/types";

export function Spinner() {
  return <span className="spinner" aria-hidden />;
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="loading-block">
      <Spinner />
      <span>{label}</span>
    </div>
  );
}

/** Sets the browser tab title. Every page gets its own, suffixed with the app
 *  name so tabs stay distinguishable when several are open. */
export function PageTitle({ title }: { title: string }) {
  return (
    <Head>
      <title>{`${title} · KMS Console`}</title>
    </Head>
  );
}

export function EmptyState({
  title,
  icon,
  actions,
  children,
}: {
  title: string;
  /** Optional glyph rendered in a circular well above the title. */
  icon?: ReactNode;
  /** Optional call to action, so an empty list is not a dead end. */
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="empty-state">
      {icon ? <div className="empty-icon">{icon}</div> : null}
      <div className="empty-title">{title}</div>
      {children ? <div className="text-sm">{children}</div> : null}
      {actions ? <div className="empty-actions">{actions}</div> : null}
    </div>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
  documentTitle,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  /** Tab title. Defaults to `title` when it is a plain string; pass this
   *  explicitly when the heading is composed JSX. */
  documentTitle?: string;
}) {
  const docTitle =
    documentTitle ?? (typeof title === "string" ? title : undefined);
  return (
    <>
      {docTitle ? <PageTitle title={docTitle} /> : null}
      <div className="page-header">
        <div>
          <h1 className="page-title">{title}</h1>
          {subtitle ? <div className="page-subtitle">{subtitle}</div> : null}
        </div>
        {actions ? <div className="page-actions">{actions}</div> : null}
      </div>
    </>
  );
}

/** A single shimmering placeholder bar. */
export function Skeleton({
  width = "100%",
  height = 11,
}: {
  width?: number | string;
  height?: number | string;
}) {
  return <span className="skeleton" style={{ width, height }} aria-hidden />;
}

// Deterministic widths — Math.random() here would differ between the
// prerendered HTML and the client render and trip a hydration mismatch.
const SKELETON_WIDTHS = ["72%", "45%", "60%", "38%", "54%", "66%"];

/** Placeholder rows rendered inside a real table, so the column layout and
 *  cell padding match the loaded state exactly and nothing shifts on arrival. */
export function TableSkeleton({
  headers,
  rows = 5,
}: {
  headers: string[];
  rows?: number;
}) {
  return (
    <div className="table-wrap" aria-busy="true">
      <span className="sr-only">Loading…</span>
      <table className="data">
        <thead>
          <tr>
            {headers.map((h) => (
              <th key={h}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: rows }, (_, r) => (
            <tr key={r} className="skeleton-row">
              {headers.map((h, c) => (
                <td key={h}>
                  <Skeleton
                    width={SKELETON_WIDTHS[(r + c) % SKELETON_WIDTHS.length]}
                  />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Stat-card placeholder matching the loaded card's geometry. */
export function StatSkeleton({ label }: { label: string }) {
  return (
    <div className="stat" aria-busy="true">
      <div className="stat-label">{label}</div>
      <div className="skeleton skeleton-stat" aria-hidden />
      <div className="stat-sub">
        <Skeleton width="50%" height={9} />
      </div>
    </div>
  );
}

export function Field({
  label,
  hint,
  htmlFor,
  children,
}: {
  label: string;
  hint?: ReactNode;
  htmlFor?: string;
  children: ReactNode;
}) {
  return (
    <div className="field">
      <label className="field-label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {hint ? <div className="field-hint">{hint}</div> : null}
    </div>
  );
}

type BadgeKind = "neutral" | "accent" | "success" | "warning" | "danger";

export function Badge({
  kind = "neutral",
  children,
}: {
  kind?: BadgeKind;
  children: ReactNode;
}) {
  return <span className={`badge badge-${kind}`}>{children}</span>;
}

export function SecretStateBadge({ state }: { state: SecretVersionState }) {
  const kind: BadgeKind =
    state === "enabled" ? "success" : state === "disabled" ? "warning" : "danger";
  return <Badge kind={kind}>{state}</Badge>;
}

export function JsonView({ raw }: { raw: string }) {
  return <pre className="json-block">{raw}</pre>;
}

export function KeyValue({ rows }: { rows: Array<[string, ReactNode]> }) {
  return (
    <dl className="kv">
      {rows.map(([k, v], i) => (
        <div key={i} style={{ display: "contents" }}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Pagination({
  onNext,
  hasNext,
  onReset,
  showReset,
  page,
}: {
  onNext: () => void;
  hasNext: boolean;
  onReset?: () => void;
  showReset?: boolean;
  page?: number;
}) {
  if (!hasNext && !showReset) return null;
  return (
    <div className="pagination">
      {showReset && onReset ? (
        <button className="btn btn-sm" onClick={onReset}>
          ← First page
        </button>
      ) : null}
      {typeof page === "number" ? (
        <span className="text-sm faint">Page {page}</span>
      ) : null}
      <div className="spacer" />
      {hasNext ? (
        <button className="btn btn-sm" onClick={onNext}>
          Next page →
        </button>
      ) : (
        <span className="text-sm faint">End of results</span>
      )}
    </div>
  );
}
