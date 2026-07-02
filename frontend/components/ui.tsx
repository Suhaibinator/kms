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

export function EmptyState({
  title,
  children,
}: {
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="empty-state">
      <div className="empty-title">{title}</div>
      {children ? <div className="text-sm">{children}</div> : null}
    </div>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div>
        <h1 className="page-title">{title}</h1>
        {subtitle ? <div className="page-subtitle">{subtitle}</div> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
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
