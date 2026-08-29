import { ArrowLeft, ArrowRight, ChevronsLeft } from "lucide-react";
import Head from "next/head";
import { cloneElement, isValidElement, type ReactElement, type ReactNode, useId } from "react";
import { Breadcrumbs } from "@/components/Breadcrumbs";
import { JsonHighlight } from "@/components/JsonHighlight";
import { Badge as ShadcnBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  FieldDescription,
  FieldError,
  FieldLabel,
  FieldLegend,
  FieldSet,
  Field as ShadcnField,
} from "@/components/ui/field";
import { Skeleton as ShadcnSkeleton } from "@/components/ui/skeleton";
import { Spinner as ShadcnSpinner } from "@/components/ui/spinner";
import type { Crumb } from "@/lib/crumbs";
import { checkJson, overHighlightCap } from "@/lib/json-text";
import type { SecretVersionState } from "@/lib/types";
import { cn } from "@/lib/utils";

export { Button } from "@/components/ui/button";
export { Checkbox } from "@/components/ui/checkbox";
export { Input } from "@/components/ui/input";
export { Textarea } from "@/components/ui/textarea";

export function Spinner() {
  return <ShadcnSpinner className="size-4" aria-hidden />;
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
  breadcrumbs,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  /** Tab title. Defaults to `title` when it is a plain string; pass this
   *  explicitly when the heading is composed JSX. */
  documentTitle?: string;
  /** Trail rendered above the header (see lib/crumbs.ts). */
  breadcrumbs?: Crumb[];
}) {
  const docTitle = documentTitle ?? (typeof title === "string" ? title : undefined);
  return (
    <>
      {docTitle ? <PageTitle title={docTitle} /> : null}
      {breadcrumbs && breadcrumbs.length > 0 ? <Breadcrumbs items={breadcrumbs} /> : null}
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
  return <ShadcnSkeleton className="inline-block" style={{ width, height }} aria-hidden />;
}

// Deterministic widths — Math.random() here would differ between the
// prerendered HTML and the client render and trip a hydration mismatch.
const SKELETON_WIDTHS = ["72%", "45%", "60%", "38%", "54%", "66%"];

/** Placeholder rows rendered inside a real table, so the column layout and
 *  cell padding match the loaded state exactly and nothing shifts on arrival. */
export function TableSkeleton({
  headers,
  rows = 5,
  rowHeight,
}: {
  headers: string[];
  rows?: number;
  /** Override for tables whose real rows are taller than the common
   *  one-line-plus-actions case `.skeleton-row td` is sized for. */
  rowHeight?: number | string;
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
            <tr
              key={r}
              className="skeleton-row"
              style={rowHeight === undefined ? undefined : { height: rowHeight }}
            >
              {headers.map((h, c) => (
                <td key={h}>
                  <Skeleton width={SKELETON_WIDTHS[(r + c) % SKELETON_WIDTHS.length]} />
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
      {/* Wrapped in .stat-value so the placeholder reserves the same line box
          the real number occupies and the card does not resize on arrival. */}
      <div className="stat-value">
        <Skeleton width="60%" height={28} />
      </div>
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
  error,
  required,
  className,
  children,
}: {
  label: ReactNode;
  hint?: ReactNode;
  htmlFor?: string;
  /**
   * Validation message for the control. When set, the field renders the message
   * in an assertive live region and marks the control `aria-invalid`, so screen
   * readers announce the problem as it appears. Pass null/undefined when valid.
   */
  error?: string | null;
  /** Marks the control `aria-required` and appends a decorative asterisk. */
  required?: boolean;
  className?: string;
  children: ReactNode;
}) {
  const generatedId = useId();
  const controlId = htmlFor ?? `${generatedId}-control`;
  const labelId = `${generatedId}-label`;
  const hintId = hint ? `${generatedId}-hint` : undefined;
  const errorId = error ? `${generatedId}-error` : undefined;
  // The error is named first so assistive tech reads the problem before the hint.
  const describedBy = [errorId, hintId].filter(Boolean).join(" ") || undefined;
  // shadcn controls are React components rather than literal `input` elements.
  // A single non-layout child is labelable; groups still use a fieldset/legend.
  const isLabelableControl =
    isValidElement(children) &&
    (typeof children.type !== "string" ||
      ["button", "input", "select", "textarea"].includes(children.type));
  const control = isLabelableControl
    ? cloneElement(children as ReactElement<Record<string, unknown>>, {
        id: (children.props as { id?: string }).id ?? controlId,
        "aria-describedby":
          [(children.props as { "aria-describedby"?: string })["aria-describedby"], describedBy]
            .filter(Boolean)
            .join(" ") || undefined,
        "aria-invalid": error
          ? true
          : ((children.props as { "aria-invalid"?: boolean })["aria-invalid"] ?? undefined),
        "aria-required": required
          ? true
          : ((children.props as { "aria-required"?: boolean })["aria-required"] ?? undefined),
      })
    : children;
  const resolvedFor = isLabelableControl
    ? ((children as ReactElement<{ id?: string }>).props.id ?? controlId)
    : htmlFor;

  const messages = (
    <>
      {error ? <FieldError id={errorId}>{error}</FieldError> : null}
      {hint ? <FieldDescription id={hintId}>{hint}</FieldDescription> : null}
    </>
  );

  // Hidden from the accessibility tree: `aria-required` on the control already
  // says this, and "Name star" is not how it should be read out.
  const labelContent = (
    <>
      {label}
      {required ? (
        <span aria-hidden="true" className="text-danger">
          {" *"}
        </span>
      ) : null}
    </>
  );

  return !isLabelableControl && !htmlFor ? (
    <FieldSet
      className={cn(error ? "field field-invalid gap-2" : "field gap-2", className)}
      aria-describedby={describedBy}
      data-invalid={error ? true : undefined}
    >
      {/* Muted + 600 matches the app's own .field-label, so forms that mix the
          two label systems render identically. */}
      <FieldLegend variant="label" id={labelId} className="font-semibold text-muted-foreground">
        {labelContent}
      </FieldLegend>
      {children}
      {messages}
    </FieldSet>
  ) : (
    <ShadcnField
      className={cn(error ? "field field-invalid gap-1" : "field gap-1", className)}
      data-invalid={error ? true : undefined}
    >
      <FieldLabel
        htmlFor={resolvedFor}
        id={labelId}
        className="font-semibold text-muted-foreground"
      >
        {labelContent}
      </FieldLabel>
      {control}
      {messages}
    </ShadcnField>
  );
}

export type BadgeKind = "neutral" | "accent" | "success" | "warning" | "danger";

export function Badge({
  kind = "neutral",
  className,
  title,
  children,
}: {
  kind?: BadgeKind;
  className?: string;
  title?: string;
  children: ReactNode;
}) {
  const variant = kind === "accent" ? "default" : kind === "danger" ? "destructive" : "outline";
  const tone =
    kind === "success"
      ? "border-success/40 bg-success/15 text-success"
      : kind === "warning"
        ? "border-warning/40 bg-warning/15 text-warning"
        : undefined;
  return (
    <ShadcnBadge variant={variant} className={cn(tone, className)} title={title}>
      {children}
    </ShadcnBadge>
  );
}

export function SecretStateBadge({ state }: { state: SecretVersionState }) {
  const kind: BadgeKind =
    state === "enabled" ? "success" : state === "disabled" ? "warning" : "danger";
  return <Badge kind={kind}>{state}</Badge>;
}

/** A read-only JSON block; colour-coded when the text is well-formed JSON. */
export function JsonView({ raw, highlight = true }: { raw: string; highlight?: boolean }) {
  const coloured = highlight && !overHighlightCap(raw) && checkJson(raw) === null;
  return <pre className="json-block">{coloured ? <JsonHighlight text={raw} /> : raw}</pre>;
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
  onPrevious,
  hasPrevious = false,
  onReset,
  showReset,
  page,
}: {
  onNext: () => void;
  hasNext: boolean;
  onPrevious?: () => void;
  hasPrevious?: boolean;
  onReset?: () => void;
  showReset?: boolean;
  page?: number;
}) {
  if (!hasNext && !hasPrevious && !showReset) return null;
  return (
    <div className="pagination">
      {showReset && onReset ? (
        <Button type="button" variant="outline" size="sm" onClick={onReset}>
          <ChevronsLeft size={15} aria-hidden />
          First page
        </Button>
      ) : null}
      {hasPrevious && onPrevious ? (
        <Button type="button" variant="outline" size="sm" onClick={onPrevious}>
          <ArrowLeft size={15} aria-hidden />
          Previous page
        </Button>
      ) : null}
      {typeof page === "number" ? <span className="text-sm faint">Page {page}</span> : null}
      <div className="spacer" />
      {hasNext ? (
        <Button type="button" variant="outline" size="sm" onClick={onNext}>
          Next page
          <ArrowRight size={15} aria-hidden />
        </Button>
      ) : (
        <span className="text-sm faint">End of results</span>
      )}
    </div>
  );
}
