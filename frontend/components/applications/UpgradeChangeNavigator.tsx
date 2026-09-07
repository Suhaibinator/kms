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
    <section className="upgrade-change-navigator" aria-label="Field changes">
      <div className="upgrade-change-counts" aria-live="polite">
        <span>
          <span>Needs attention</span>
          <strong>{changes.filter((c) => c.attention).length}</strong>
        </span>
        <span>
          <span>Changed</span>
          <strong>{changes.filter((c) => c.changed).length}</strong>
        </span>
        <span>
          <span>Unchanged</span>
          <strong>{changes.filter((c) => !c.changed).length}</strong>
        </span>
        <span>
          <span>Removed</span>
          <strong>{removed.length}</strong>
        </span>
      </div>
      <div className="upgrade-change-controls">
        <Input
          aria-label="Search fields or schema paths"
          placeholder="Search fields or schema paths…"
          value={search}
          onChange={(e) => onSearch(e.target.value)}
        />
        <label className="upgrade-change-filter">
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
    <div className="upgrade-change-labels">
      <div className="upgrade-change-badges">
        {change.labels.map((label) => (
          <span
            key={label}
            className={
              label === "Needs attention"
                ? "upgrade-change-badge upgrade-change-badge-alert"
                : "upgrade-change-badge"
            }
          >
            {label}
          </span>
        ))}
        {!change.changed && <span className="upgrade-change-unchanged">Unchanged</span>}
      </div>
      {change.paths.length > 0 && (
        <ul className="upgrade-change-paths">
          {change.paths.map((d) => (
            <li key={JSON.stringify(d.segments)}>
              <span className="mono">{d.path}</span>
              <span className="upgrade-change-path-kind">{d.change}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
