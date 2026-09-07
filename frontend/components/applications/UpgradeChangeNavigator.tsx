import { Button, Input } from "@/components/ui";
import type { UpgradeFieldChange } from "@/lib/upgrade-field-changes";

export function UpgradeChangeNavigator({
  changes,
  removed,
  search,
  onlyChanged,
  onSearch,
  onFilter,
  onSort,
  onJump,
  current,
}: {
  changes: UpgradeFieldChange[];
  removed: string[];
  search: string;
  onlyChanged: boolean;
  onSearch: (value: string) => void;
  onFilter: (value: boolean) => void;
  onSort: () => void;
  onJump: (id: number) => void;
  current: number | null;
}) {
  const targets = changes.filter((c) => c.changed && matchesUpgradeSearch(c, search));
  const index = targets.findIndex((c) => c.id === current);
  return (
    <section className="card p-4 stack" aria-label="Field changes">
      <div className="row-wrap" aria-live="polite">
        <span>Needs attention: {changes.filter((c) => c.attention).length}</span>
        <span>Changed: {changes.filter((c) => c.changed).length}</span>
        <span>Unchanged: {changes.filter((c) => !c.changed).length}</span>
        <span>Removed: {removed.length}</span>
      </div>
      <div className="row-wrap">
        <Input
          aria-label="Search fields or schema paths"
          placeholder="Search fields or schema paths…"
          value={search}
          onChange={(e) => onSearch(e.target.value)}
        />
        <label className="row-wrap">
          <input
            type="checkbox"
            checked={onlyChanged}
            onChange={(e) => onFilter(e.target.checked)}
          />{" "}
          Changed only
        </label>
        <Button variant="outline" onClick={onSort}>
          Sort changes first
        </Button>
        <Button
          variant="outline"
          disabled={!targets.length}
          onClick={() => onJump(targets[(index <= 0 ? targets.length : index) - 1].id)}
        >
          Previous change
        </Button>
        <Button
          variant="outline"
          disabled={!targets.length}
          onClick={() => onJump(targets[(index + 1) % targets.length].id)}
        >
          Next change
        </Button>
      </div>
      <details>
        <summary className="cursor-pointer">Jump to changed field ({targets.length})</summary>
        <div className="stack max-h-48 overflow-y-auto">
          {targets.map((c) => (
            <Button
              key={c.id}
              variant="ghost"
              className="justify-start whitespace-normal text-left"
              onClick={() => onJump(c.id)}
            >
              {c.alias || "Unnamed field"} · {c.labels.join(" · ")}
            </Button>
          ))}
          {!targets.length && <p>No changed fields match.</p>}
        </div>
      </details>
    </section>
  );
}
export function matchesUpgradeSearch(change: UpgradeFieldChange, search: string): boolean {
  const query = search.trim().toLowerCase();
  return (
    !query ||
    [change.alias, ...change.paths.map((d) => d.path)].some((text) =>
      text.toLowerCase().includes(query),
    )
  );
}
export function UpgradeChangeLabels({ change }: { change: UpgradeFieldChange }) {
  return (
    <div className="stack text-sm">
      <div className="row-wrap">
        {change.labels.map((label) => (
          <span
            key={label}
            className={
              label === "Needs attention"
                ? "text-destructive font-medium"
                : "text-primary font-medium"
            }
          >
            {label}
          </span>
        ))}
        {!change.changed && <span className="faint">Unchanged</span>}
      </div>
      {change.paths.length > 0 && (
        <ul className="break-words">
          {change.paths.map((d) => (
            <li key={JSON.stringify(d.segments)}>
              <span className="mono">{d.path}</span> · {d.change}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
