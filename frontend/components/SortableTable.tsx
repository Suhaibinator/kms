import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { type ReactNode, useCallback, useMemo } from "react";
import { useQueryParams } from "@/lib/hooks";
import {
  ariaSort,
  nextSort,
  parseSort,
  type SortColumn,
  SORT_DIRECTION_KEY,
  SORT_KEY,
  type SortState,
  sortQuery,
  sortRows,
} from "@/lib/sort";
import { useQueryReplace } from "@/lib/url";
import { cn } from "@/lib/utils";

export interface SortController<T> {
  /** The active sort, or null for the order the page loaded rows in. */
  sort: SortState | null;
  columns: readonly SortColumn<T>[];
  /** The rows in the active order (a copy when nothing is sorted). */
  apply: (rows: readonly T[]) => T[];
  /** Cycles a column: unsorted → ascending → descending → unsorted. */
  toggle: (column: string) => void;
}

/**
 * Table ordering held in the URL rather than in component state, so a sorted
 * list survives a reload and can be pasted to someone else. `columns` must be
 * referentially stable — define it at module scope, or memoise it.
 */
export function useSort<T>(pathname: string, columns: readonly SortColumn<T>[]): SortController<T> {
  const { values } = useQueryParams([SORT_KEY, SORT_DIRECTION_KEY]);
  const replaceQuery = useQueryReplace(pathname);

  const sort = useMemo(
    () => parseSort(columns, values[SORT_KEY], values[SORT_DIRECTION_KEY]),
    [columns, values],
  );

  // From a click, never from an effect: the URL must not fight the table.
  const toggle = useCallback(
    (column: string) => replaceQuery(sortQuery(nextSort(sort, column))),
    [replaceQuery, sort],
  );

  const apply = useCallback((rows: readonly T[]) => sortRows(rows, columns, sort), [columns, sort]);

  return { sort, columns, apply, toggle };
}

function SortHeaderCell<T>({
  controller,
  column,
  hint,
}: {
  controller: SortController<T>;
  column: SortColumn<T>;
  hint?: string;
}) {
  if (!column.value) {
    return <th className={column.className}>{column.label}</th>;
  }
  const state = ariaSort(controller.sort, column.id);
  const Indicator = state === "ascending" ? ArrowUp : state === "descending" ? ArrowDown : null;
  return (
    // aria-sort is what announces the order; the button carries only its label,
    // so the accessible name stays the column name.
    <th className={cn("sortable", column.className)} aria-sort={state}>
      <button
        type="button"
        className="sort-button"
        data-sort={column.id}
        title={hint}
        onClick={() => controller.toggle(column.id)}
      >
        {column.label}
        <span className="sort-indicator" aria-hidden>
          {Indicator ? <Indicator size={12} /> : <ChevronsUpDown size={12} />}
        </span>
      </button>
    </th>
  );
}

/**
 * The header row of a sortable `table.data`. `before`/`after` take the cells
 * that are not columns of data — a select-all checkbox, a row-actions gutter.
 */
export function SortHeaderRow<T>({
  controller,
  before,
  after,
  hint,
}: {
  controller: SortController<T>;
  before?: ReactNode;
  after?: ReactNode;
  /** A limitation to disclose on every header, e.g. that only the loaded page sorts. */
  hint?: string;
}) {
  return (
    <tr>
      {before}
      {controller.columns.map((column) => (
        <SortHeaderCell key={column.id} controller={controller} column={column} hint={hint} />
      ))}
      {after}
    </tr>
  );
}

/** The header labels, for a `TableSkeleton` that must match the loaded table. */
export function headerLabels<T>(columns: readonly SortColumn<T>[]): string[] {
  return columns.map((column) => column.label);
}
