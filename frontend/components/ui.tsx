import { ArrowLeft, ArrowRight, ChevronsLeft } from "lucide-react";
import Head from "next/head";
import {
  type CSSProperties,
  cloneElement,
  isValidElement,
  type ReactElement,
  type ReactNode,
  useId,
} from "react";
import { Breadcrumbs } from "@/components/Breadcrumbs";
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
import { countNoun } from "@/lib/format";
import type { SecretVersionState } from "@/lib/types";
import { cn } from "@/lib/utils";

export { JsonView } from "@/components/JsonView";
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

/** The row count a list skeleton reserves when the caller has no better
 *  estimate. List pages fetch 50–100 rows, so a handful of placeholder rows
 *  would let the page grow by hundreds of pixels under the cursor on arrival. */
export const SKELETON_ROWS_DEFAULT = 20;

/** Placeholder rows rendered inside a real table, so the column layout and
 *  cell padding match the loaded state exactly and nothing shifts on arrival.
 *  Pass `rows` ≈ the last-known row count where the page has one. */
export function TableSkeleton({
  headers,
  rows = SKELETON_ROWS_DEFAULT,
  rowHeight,
  leading = 0,
  trailing = 0,
  tableClassName,
  toolbar = false,
  summary = false,
}: {
  headers: string[];
  rows?: number;
  /** Override for tables whose real rows are taller than the common
   *  one-line-plus-actions case `.skeleton-row td` is sized for. */
  rowHeight?: number | string;
  /** Structural columns the loaded table adds before the data columns (a
   *  select-all cell) and after them (an actions cell), so the column count
   *  matches and nothing shifts on arrival. */
  leading?: number;
  trailing?: number;
  /** The loaded table's own class (`namespace-table`), which carries its
   *  column widths and header wrapping. Without it the skeleton's header row
   *  is 17px shorter than the one that replaces it. */
  tableClassName?: string;
  /** Reserve the `MobileListToolbar` the loaded list renders below 640px;
   *  without it the list jumps down by up to 198px on arrival. */
  toolbar?: boolean;
  /** Reserve the `TableSummary` caption the loaded list renders (34px). */
  summary?: boolean;
}) {
  const pad = (count: number, tag: "th" | "td", prefix: string) =>
    Array.from({ length: count }, (_, i) =>
      tag === "th" ? (
        <th key={`${prefix}${i}`} className="skeleton-structural" />
      ) : (
        <td key={`${prefix}${i}`} className="skeleton-structural" />
      ),
    );
  return (
    <div className="table-wrap card-table" aria-busy="true">
      <span className="sr-only">Loading…</span>
      {/* The loaded toolbar's two sort controls, as empty boxes: the fieldset's
          own gap and padding then give it the loaded height. */}
      {toolbar ? (
        <fieldset className="mobile-list-toolbar" aria-hidden>
          <span className="mobile-sort-field">
            <Skeleton width="45%" height="1.5em" />
            <Skeleton height={44} />
          </span>
          <span className="mobile-sort-field">
            <Skeleton width="45%" height="1.5em" />
            <Skeleton height={44} />
          </span>
        </fieldset>
      ) : null}
      <table className={cn("data", tableClassName)}>
        {summary ? <caption className="table-summary">&nbsp;</caption> : null}
        <thead>
          <tr>
            {pad(leading, "th", "l")}
            {headers.map((h) => (
              <th key={h}>{h}</th>
            ))}
            {pad(trailing, "th", "t")}
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: rows }, (_, r) => (
            <tr
              key={r}
              className="skeleton-row"
              // Through the cell, not the <tr>: a row is at least as tall as
              // its tallest cell, so an inline height on the row could only
              // ever make it taller than `.skeleton-row td` — which is why
              // rowHeight={44} produced a 54px row.
              style={
                rowHeight === undefined
                  ? undefined
                  : ({
                      "--skeleton-row-h":
                        typeof rowHeight === "number" ? `${rowHeight}px` : rowHeight,
                    } as CSSProperties)
              }
            >
              {pad(leading, "td", "l")}
              {headers.map((h, c) => (
                <td key={h}>
                  <Skeleton width={SKELETON_WIDTHS[(r + c) % SKELETON_WIDTHS.length]} />
                </td>
              ))}
              {pad(trailing, "td", "t")}
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
      <div className="stat-value flex items-center" style={{ height: "1.25em" }}>
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
    // gap-1, like the labelled branch below: the two flavours land in one form
    // (every SchemaForm list field is a fieldset) and gap-2 put their controls
    // 3.56px apart from each other's.
    <FieldSet
      className={cn(error ? "field field-invalid gap-1" : "field gap-1", className)}
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

/**
 * A table's own footer line: how much of the list is on screen, what is
 * narrowing it, and anything the ordering cannot promise. Rendered as the
 * table's `<caption>` (flipped to the bottom in CSS) so it needs no colspan and
 * screen readers announce it with the table.
 *
 * Must be the first child of `<table>` — the HTML parser moves a caption there
 * anyway, and React would warn about the misplaced node.
 */
export function TableSummary({
  shown,
  total,
  filters = 0,
  noun,
  hint,
}: {
  /** Rows rendered right now. */
  shown: number;
  /** Rows the list holds in all, where that is knowable; defaults to `shown`. */
  total?: number;
  /** Active filters narrowing the list; omitted from the line when 0. */
  filters?: number;
  /** Plural noun for the rows ("parameters"). */
  noun: string;
  /** A caveat, e.g. that sorting only reaches the loaded page. */
  hint?: ReactNode;
}) {
  const of = total ?? shown;
  return (
    <caption className="table-summary" data-testid="table-summary">
      <span>
        Showing {shown} of {of} {countNoun(of, noun)}
      </span>
      {filters > 0 ? (
        <span>
          {" · "}
          {filters} {countNoun(filters, "filters")} active
        </span>
      ) : null}
      {hint ? (
        <span className="faint">
          {" · "}
          {hint}
        </span>
      ) : null}
    </caption>
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
  count,
  loading = false,
  noun = "results",
}: {
  onNext: () => void;
  hasNext: boolean;
  onPrevious?: () => void;
  hasPrevious?: boolean;
  onReset?: () => void;
  showReset?: boolean;
  page?: number;
  /** Rows on the current page; renders "{count} {noun} · Page {n}" and a
   *  polite status line so screen readers hear each settled page. */
  count?: number;
  /** True while the page is being fetched: "End of results" is withheld and
   *  the status line reads "Loading…" instead of a stale count. */
  loading?: boolean;
  /** Plural noun for `count` ("events"). */
  noun?: string;
}) {
  const hasCount = typeof count === "number";
  const showSummary = hasCount && count > 0;
  if (!hasNext && !hasPrevious && !showReset && !showSummary) return null;
  const summary = hasCount ? `${count} ${countNoun(count, noun)}` : null;
  const status = loading
    ? "Loading…"
    : [summary, typeof page === "number" ? `page ${page}` : null].filter(Boolean).join(", ");
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
      {summary || typeof page === "number" ? (
        // "Page n" keeps its own element so it stays findable on its own.
        <span className="text-sm faint pagination-summary">
          {summary ? (
            <>
              <span>{summary}</span>
              {typeof page === "number" ? " · " : null}
            </>
          ) : null}
          {typeof page === "number" ? <span>Page {page}</span> : null}
        </span>
      ) : null}
      <span className="sr-only" role="status" aria-live="polite">
        {status}
      </span>
      <div className="spacer" />
      {hasNext ? (
        <Button type="button" variant="outline" size="sm" onClick={onNext} disabled={loading}>
          Next page
          <ArrowRight size={15} aria-hidden />
        </Button>
      ) : loading ? null : (
        <span className="text-sm faint">End of results</span>
      )}
    </div>
  );
}
