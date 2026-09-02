// Ordering for the console's list tables. Pure: a page describes its columns
// once, hands the rows over, and renders what comes back — so every table
// compares values the same way and round-trips the same two query keys.

export type SortDirection = "asc" | "desc";

/** A comparable cell value. null, undefined and "" all mean "no value". */
export type SortValue = string | number | boolean | null | undefined;

export interface SortState {
  /** The `SortColumn.id` being ordered by. */
  column: string;
  direction: SortDirection;
}

export interface SortColumn<T> {
  /** Stable, URL-safe id; also the React key of the header cell. */
  id: string;
  /** Header text. */
  label: string;
  /** Omit for a column that cannot be ordered (badge stacks, action buttons). */
  value?: (row: T) => SortValue;
  /** Extra class for the `<th>`. */
  className?: string;
}

/** The query keys the sort state lives in, so a sorted list is shareable. */
export const SORT_KEY = "sort";
export const SORT_DIRECTION_KEY = "dir";

const PRESENT = 0;
const MISSING = 1;

function rank(value: SortValue): number {
  return value === null || value === undefined || value === "" ? MISSING : PRESENT;
}

/** Ascending order for two present values of the same column. */
function comparePresent(a: SortValue, b: SortValue): number {
  if (typeof a === "number" && typeof b === "number") return a - b;
  // false before true, so "disabled" rows group at one end rather than interleave.
  if (typeof a === "boolean" && typeof b === "boolean") return Number(a) - Number(b);
  // Locale-aware, and numeric so v2 comes before v10 rather than after it.
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: "base" });
}

/**
 * Compares two cell values. Rows with no value sort last in *both* directions —
 * a column of dashes at the top of a descending sort is never what was asked
 * for.
 */
export function compareValues(
  a: SortValue,
  b: SortValue,
  direction: SortDirection = "asc",
): number {
  const missing = rank(a) - rank(b);
  if (missing !== 0) return missing;
  if (rank(a) === MISSING) return 0;
  const ordered = comparePresent(a, b);
  return direction === "asc" ? ordered : -ordered;
}

/**
 * The rows in the active sort order, or a copy of the input when nothing is
 * sorted (which is how a page keeps the default order it shipped with). Stable:
 * rows that compare equal keep the order the page loaded them in.
 */
export function sortRows<T>(
  rows: readonly T[],
  columns: readonly SortColumn<T>[],
  sort: SortState | null,
): T[] {
  const column = sort ? columns.find((candidate) => candidate.id === sort.column) : undefined;
  const value = column?.value;
  if (!sort || !value) return [...rows];
  const direction = sort.direction;
  return rows
    .map((row, index) => ({ row, index }))
    .sort((a, b) => compareValues(value(a.row), value(b.row), direction) || a.index - b.index)
    .map((entry) => entry.row);
}

/**
 * The state one more click on `column` produces: unsorted → ascending →
 * descending → unsorted. The third click is what gives the page its original
 * order back.
 */
export function nextSort(current: SortState | null, column: string): SortState | null {
  if (current?.column !== column) return { column, direction: "asc" };
  return current.direction === "asc" ? { column, direction: "desc" } : null;
}

/** The sort a URL asks for, or null when it names no orderable column. */
export function parseSort<T>(
  columns: readonly SortColumn<T>[],
  column: string | null | undefined,
  direction: string | null | undefined,
): SortState | null {
  if (!column) return null;
  const known = columns.some((candidate) => candidate.id === column && candidate.value);
  if (!known) return null;
  return { column, direction: direction === "desc" ? "desc" : "asc" };
}

/** The query patch that records `sort` (empty strings delete the keys). */
export function sortQuery(sort: SortState | null): Record<string, string> {
  return {
    [SORT_KEY]: sort?.column ?? "",
    [SORT_DIRECTION_KEY]: sort?.direction ?? "",
  };
}

/** The `aria-sort` a header cell announces. */
export function ariaSort(
  sort: SortState | null,
  column: string,
): "ascending" | "descending" | "none" {
  if (sort?.column !== column) return "none";
  return sort.direction === "asc" ? "ascending" : "descending";
}
