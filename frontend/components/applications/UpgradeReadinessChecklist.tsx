import { Button } from "@/components/ui";
import type { UpgradeFieldChange } from "@/lib/upgrade-field-changes";
import { CAUSE_COPY, type ReadinessStatus } from "@/lib/upgrade-readiness";
import { issuePath } from "./UpgradeChangeNavigator";

/** Groups in the order an operator fixes them; statuses absent here have no group. */
const GROUPS: { status: ReadinessStatus; title: (targetVersion: number) => string }[] = [
  { status: "needs_value", title: () => "Provide a value" },
  { status: "fails_schema", title: (version) => `Update to satisfy schema v${version}` },
  { status: "invalid_draft", title: () => "Fix invalid drafts" },
  { status: "needs_version", title: () => "Pin an exact version" },
  { status: "load_error", title: () => "Retry loading" },
];

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

/**
 * Everything the local checks say must change before the release can
 * validate, grouped by the kind of fix. Local checks are a subset of the
 * server's, so an all-clear here still hands the last word to the preview.
 */
export function UpgradeReadinessChecklist({
  changes,
  targetVersion,
  loading,
  onJump,
}: {
  changes: UpgradeFieldChange[];
  targetVersion: number;
  loading: boolean;
  onJump: (id: number, control: boolean, path?: string[]) => void;
}) {
  const withStatus = (status: ReadinessStatus) =>
    changes.filter((change) => change.readiness?.status === status);
  const checking = withStatus("loading").length;
  const ready = withStatus("ready").length;
  const groups = GROUPS.map((group) => ({ ...group, items: withStatus(group.status) })).filter(
    (group) => group.items.length > 0,
  );
  const unchecked = withStatus("unchecked").length;
  return (
    <section className="upgrade-readiness" aria-label="Release readiness">
      <div className="upgrade-readiness-heading">
        <strong>Release readiness</strong>
        {loading || checking ? (
          <span className="faint" role="status">
            Checking {plural(checking || changes.length, "value")}…
          </span>
        ) : null}
      </div>
      <div className="upgrade-change-counts">
        <span>
          <span>Values to provide</span>
          <strong>{withStatus("needs_value").length}</strong>
        </span>
        <span>
          <span>Values to update</span>
          <strong>{withStatus("fails_schema").length + withStatus("invalid_draft").length}</strong>
        </span>
        <span>
          <span>Versions to pin</span>
          <strong>{withStatus("needs_version").length}</strong>
        </span>
        <span>
          <span>Pass local checks</span>
          <strong>{ready}</strong>
        </span>
      </div>
      {groups.map((group) => (
        <div className="upgrade-readiness-group" key={group.status}>
          <h4>{group.title(targetVersion)}</h4>
          <ul>
            {group.items.map((change) => {
              const issues = change.readiness?.issues ?? [];
              return (
                <li key={change.id}>
                  <Button
                    type="button"
                    variant="ghost"
                    className="justify-start whitespace-normal text-left"
                    onClick={() => onJump(change.id, true, issues[0]?.path)}
                  >
                    <span className="mono">{change.alias || "Unnamed field"}</span>
                    {change.readiness?.summary ? ` · ${change.readiness.summary}` : ""}
                  </Button>
                  {issues.length > 0 && (
                    <ul className="upgrade-change-issues">
                      {issues.map((issue) => (
                        <li key={`${issue.path.join("\0")}:${issue.message}`}>
                          <span>
                            <span className="mono">{issuePath(change.alias, issue)}</span> ·{" "}
                            {issue.message} · {CAUSE_COPY[issue.cause]}
                          </span>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            aria-label={`Go to ${issuePath(change.alias, issue)}`}
                            onClick={() => onJump(change.id, true, issue.path)}
                          >
                            Go to
                          </Button>
                        </li>
                      ))}
                    </ul>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      ))}
      {!groups.length && !loading && !checking && ready > 0 ? (
        <p role="status">
          All {plural(ready, "value")} pass local checks.
          {unchecked ? ` ${plural(unchecked, "value")} could not be checked locally.` : ""} Preview
          migration runs the authoritative validation.
        </p>
      ) : null}
    </section>
  );
}
