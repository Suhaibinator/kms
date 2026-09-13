import { Button, Input } from "@/components/ui";
import type { UpgradeFieldChange } from "@/lib/upgrade-field-changes";
import { CAUSE_COPY, type ReadinessIssue } from "@/lib/upgrade-readiness";

/** Nested issue rows shown per field before the list collapses to "+n more". */
const ISSUE_ROW_CAP = 8;

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
              {[c.alias || "Unnamed field", ...c.labels, c.readiness?.summary]
                .filter(Boolean)
                .join(" · ")}
            </Button>
          ))}
          {!targets.length && <p>No changed fields match.</p>}
        </div>
      </details>
    </section>
  );
}

/** `alias.nested.path` for an issue, or the alias alone for a root issue. */
export function issuePath(alias: string, issue: Pick<ReadinessIssue, "path">): string {
  return [alias, ...issue.path].join(".");
}

export function matchesUpgradeSearch(change: UpgradeFieldChange, search: string): boolean {
  const query = search.trim().toLowerCase();
  return (
    !query ||
    [
      change.alias,
      ...change.paths.map((d) => d.path),
      ...(change.readiness?.issues ?? []).map((issue) => issuePath(change.alias, issue)),
    ].some((text) => text.toLowerCase().includes(query))
  );
}

export function UpgradeChangeLabels({
  change,
  effects,
  onJumpPath,
}: {
  change: UpgradeFieldChange;
  /** Effect text per structured difference path, from `describeSchemaEffect`. */
  effects?: Map<string, string>;
  /** Focuses the control at a nested path inside this field's editor. */
  onJumpPath?: (path: string[]) => void;
}) {
  const issues = change.readiness?.issues ?? [];
  const shown = issues.slice(0, ISSUE_ROW_CAP);
  return (
    <div className="upgrade-change-labels">
      <div className="upgrade-change-badges">
        {change.labels.map((label) => (
          <span
            key={label}
            className={
              label === "Needs attention" || label === "Fails target schema"
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
          {change.paths.map((d) => {
            const effect = effects?.get(d.path);
            return (
              <li key={JSON.stringify(d.segments)}>
                <span className="mono">{d.path}</span>
                <span className="upgrade-change-path-kind">
                  {d.change}
                  {effect ? ` · ${effect}` : ""}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {issues.length > 0 && (
        <ul className="upgrade-change-issues" aria-label={`${change.alias} local schema issues`}>
          {shown.map((issue) => (
            <li key={`${issue.path.join("\0")}:${issue.message}`}>
              <span>
                <span className="mono">{issuePath(change.alias, issue)}</span> · {issue.message} ·{" "}
                {CAUSE_COPY[issue.cause]}
              </span>
              {onJumpPath ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={`Go to ${issuePath(change.alias, issue)}`}
                  onClick={() => onJumpPath(issue.path)}
                >
                  Go to
                </Button>
              ) : null}
            </li>
          ))}
          {issues.length > shown.length && (
            <li className="faint">+{issues.length - shown.length} more</li>
          )}
        </ul>
      )}
    </div>
  );
}
