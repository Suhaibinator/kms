import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { type ReactNode, useCallback, useId, useMemo } from "react";
import type { BulkSelection } from "@/components/BulkSelection";
import { Checkbox } from "@/components/ui";
import { useQueryParams } from "@/lib/hooks";
import {
  ariaSort,
  nextSort,
  parseSort,
  SORT_DIRECTION_KEY,
  SORT_KEY,
  type SortColumn,
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
  setSort: (state: SortState | null) => void;
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

  const setSort = useCallback(
    (state: SortState | null) =>
      replaceQuery(
        sortQuery(
          state && columns.some((column) => column.id === state.column && column.value)
            ? state
            : null,
        ),
      ),
    [columns, replaceQuery],
  );
  return { sort, columns, apply, toggle, setSort };
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

/** Alternate controls for card lists; the desktop table header shares this state. */
export function MobileListToolbar<T>({
  controller,
  selection,
  selectionLabel = "Select all on this page",
  hint,
}: {
  controller: SortController<T>;
  selection?: BulkSelection;
  selectionLabel?: string;
  hint?: string;
}) {
  const selectionId = useId();
  return (
    <fieldset className="mobile-list-toolbar" aria-label="List controls">
      <label className="mobile-sort-field">
        Sort by
        <select
          value={controller.sort?.column ?? ""}
          onChange={(event) =>
            controller.setSort(
              event.target.value
                ? { column: event.target.value, direction: controller.sort?.direction ?? "asc" }
                : null,
            )
          }
        >
          <option value="">Default order</option>
          {controller.columns
            .filter((column) => column.value)
            .map((column) => (
              <option key={column.id} value={column.id}>
                {column.label}
              </option>
            ))}
        </select>
      </label>
      <label className="mobile-sort-field">
        Direction
        <select
          disabled={!controller.sort}
          value={controller.sort?.direction ?? "asc"}
          onChange={(event) =>
            controller.sort &&
            controller.setSort({
              column: controller.sort.column,
              direction: event.target.value === "desc" ? "desc" : "asc",
            })
          }
        >
          <option value="asc">Ascending</option>
          <option value="desc">Descending</option>
        </select>
      </label>
      {hint ? <p className="mobile-list-hint">{hint}</p> : null}
      {selection ? (
        <label className="mobile-list-selection" htmlFor={selectionId}>
          <Checkbox
            id={selectionId}
            checked={selection.all}
            indeterminate={selection.some}
            disabled={selection.selectable === 0}
            onCheckedChange={(checked) => selection.setAll(Boolean(checked))}
          />
          {selectionLabel} ({selection.count} selected)
        </label>
      ) : null}
    </fieldset>
  );
}
