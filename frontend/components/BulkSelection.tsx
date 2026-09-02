import { Trash2 } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { ConfirmDialog } from "@/components/Modal";
import { Checkbox } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { countNoun } from "@/lib/format";

/** How many of the selected names the confirmation lists before it summarises. */
const NAMES_SHOWN = 20;

export interface BulkSelection {
  /** Ticked rows that are still on screen, in row order. */
  selected: string[];
  count: number;
  /** Rows that can be ticked at all. */
  selectable: number;
  has: (id: string) => boolean;
  set: (id: string, on: boolean) => void;
  setAll: (on: boolean) => void;
  clear: () => void;
  /** Every selectable row is ticked (and there is at least one). */
  all: boolean;
  /** Some, but not all, selectable rows are ticked. */
  some: boolean;
}

const EMPTY: ReadonlySet<string> = new Set();

/**
 * Which rows of the current list are ticked. `scope` is whatever identifies the
 * list — namespace, filter, page cursor — and a change to it clears the
 * selection, so a tick can never follow the operator onto a different list.
 * Ids that leave the page (a reload after a delete) drop out on their own.
 */
export function useBulkSelection(ids: readonly string[], scope: string): BulkSelection {
  const [state, setState] = useState<{ scope: string; ids: ReadonlySet<string> }>(() => ({
    scope,
    ids: EMPTY,
  }));

  useEffect(() => {
    setState((current) => (current.scope === scope ? current : { scope, ids: EMPTY }));
  }, [scope]);

  // Read through the scope as well: the effect above lands a render later, and
  // until it does the previous list's ticks must already be invisible.
  const ticked = state.scope === scope ? state.ids : EMPTY;
  const selected = ids.filter((id) => ticked.has(id));

  const update = (mutate: (next: Set<string>) => void) => {
    setState((current) => {
      const next = new Set(current.scope === scope ? current.ids : EMPTY);
      mutate(next);
      return { scope, ids: next };
    });
  };

  return {
    selected,
    count: selected.length,
    selectable: ids.length,
    all: ids.length > 0 && selected.length === ids.length,
    some: selected.length > 0 && selected.length < ids.length,
    has: (id) => ticked.has(id),
    set: (id, on) =>
      update((next) => {
        if (on) next.add(id);
        else next.delete(id);
      }),
    setAll: (on) => setState({ scope, ids: on ? new Set(ids) : EMPTY }),
    clear: () => setState({ scope, ids: EMPTY }),
  };
}

/** The select-all header cell. Pass it to `SortHeaderRow`'s `before`. */
export function SelectAllCell({
  selection,
  label,
}: {
  selection: BulkSelection;
  /** e.g. "Select all parameters on this page". */
  label: string;
}) {
  return (
    <th className="select-cell">
      <Checkbox
        aria-label={label}
        checked={selection.all}
        indeterminate={selection.some}
        disabled={selection.selectable === 0}
        onCheckedChange={(checked) => selection.setAll(Boolean(checked))}
      />
    </th>
  );
}

/** One row's checkbox cell. */
export function SelectRowCell({
  selection,
  id,
  label,
  disabled,
}: {
  selection: BulkSelection;
  id: string;
  /** e.g. "Select billing/timeout". */
  label: string;
  disabled?: boolean;
}) {
  return (
    <td className="select-cell" data-label="Select">
      <Checkbox
        aria-label={label}
        checked={selection.has(id)}
        disabled={disabled}
        onCheckedChange={(checked) => selection.set(id, Boolean(checked))}
      />
    </td>
  );
}

/**
 * The bar that appears once anything is ticked. Sticky, so the action stays
 * reachable however far down a hundred-row page the operator has scrolled.
 */
export function BulkActionBar({
  selection,
  noun,
  actionLabel,
  busy,
  onAction,
}: {
  selection: BulkSelection;
  /** Plural noun for the rows ("parameters"). */
  noun: string;
  /** The destructive action, e.g. "Delete selected". */
  actionLabel: string;
  busy?: boolean;
  onAction: () => void;
}) {
  if (selection.count === 0) return null;
  return (
    // Named, so the bar is a landmark a screen-reader user can jump straight to
    // once they have ticked something far down a long page.
    <section className="bulk-bar" aria-label="Bulk actions">
      <span className="bulk-bar-count" role="status" aria-live="polite">
        {selection.count} {countNoun(selection.count, noun)} selected
      </span>
      <div className="spacer" />
      <Button type="button" variant="ghost" size="sm" onClick={selection.clear} disabled={busy}>
        Clear selection
      </Button>
      <Button
        type="button"
        variant="destructive-solid"
        size="sm"
        onClick={onAction}
        disabled={busy}
      >
        <Trash2 size={14} aria-hidden />
        {actionLabel}
      </Button>
    </section>
  );
}

/**
 * The confirmation for a bulk destructive run. Same shape as every other
 * destructive confirmation in the console — a danger panel and a type-to-confirm
 * field — but it names each item and, because a slip here costs more than one
 * row, always asks for the count rather than only doing so per resource.
 */
export function BulkDeleteDialog({
  open,
  names,
  noun,
  verb,
  verbing,
  scope,
  production,
  consequence,
  busy,
  completed,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  names: readonly string[];
  /** Plural noun for the rows ("parameters"). */
  noun: string;
  /** "Delete", "Revoke". */
  verb: string;
  /** "Deleting", "Revoking" — the progress label while the run is in flight. */
  verbing: string;
  /** Where the items live, e.g. "prod/billing". */
  scope?: string;
  /** `scope` is a production environment: say so before anything is destroyed. */
  production?: boolean;
  /** One sentence on what is lost. */
  consequence: ReactNode;
  busy: boolean;
  /** Items finished so far, for the progress label. */
  completed: number;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const count = names.length;
  const shown = names.slice(0, NAMES_SHOWN);
  const hidden = count - shown.length;
  return (
    <ConfirmDialog
      open={open}
      danger
      title={`${verb} ${count} ${countNoun(count, noun)}?`}
      message={
        <>
          {production ? (
            <p className="mb-2">
              <strong>{scope} is a production environment.</strong> Running applications read these
              values.
            </p>
          ) : null}
          <p>
            {verb} the {count} {countNoun(count, noun)} below
            {scope ? (
              <>
                {" "}
                from <span className="mono">{scope}</span>
              </>
            ) : null}
            ? {consequence}
          </p>
          <ul className="bulk-list">
            {shown.map((name) => (
              <li key={name} className="mono">
                {name}
              </li>
            ))}
            {hidden > 0 ? <li className="faint">and {hidden} more</li> : null}
          </ul>
        </>
      }
      confirmLabel={
        busy
          ? `${verbing} ${Math.min(completed + 1, count)} of ${count}…`
          : `${verb} ${count} ${countNoun(count, noun)}`
      }
      requireText={String(count)}
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
