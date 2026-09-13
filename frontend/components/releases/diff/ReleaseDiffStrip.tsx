import { Ident } from "@/components/Ident";
import { Badge } from "@/components/ui";
import type { OverviewRollout, ReleaseDiffResponse } from "@/lib/types";

function Cell({
  label,
  tone,
  value,
  sub,
  testId,
}: {
  label: string;
  tone?: "changed" | "added" | "removed";
  value: React.ReactNode;
  sub?: React.ReactNode;
  testId?: string;
}) {
  return (
    <div className="stat" data-testid={testId}>
      <div className="stat-label">
        {tone ? <span className="release-diff-stat-dot" data-tone={tone} aria-hidden /> : null}
        {label}
      </div>
      {typeof value === "number" ? (
        <div className="stat-value">{value}</div>
      ) : (
        <div className="stat-value-sm">{value}</div>
      )}
      {sub ? <div className="stat-sub">{sub}</div> : null}
    </div>
  );
}

/**
 * The counts an operator reads first: changed / added / removed, secrets
 * repinned, the schema pair, and (on a full page, for the current release)
 * how the rollout is going.
 */
export function ReleaseDiffStrip({
  diff,
  rollout,
}: {
  diff: ReleaseDiffResponse;
  /** undefined hides the cell; null renders it as unknown. */
  rollout?: OverviewRollout | null;
}) {
  const { counts, from, to } = diff;
  return (
    <div className="stat-strip release-diff-strip" data-testid="release-diff-strip">
      <Cell
        label="Changed"
        tone="changed"
        value={counts.changed}
        testId="release-diff-count-changed"
      />
      <Cell label="Added" tone="added" value={counts.added} testId="release-diff-count-added" />
      <Cell
        label="Removed"
        tone="removed"
        value={counts.removed}
        testId="release-diff-count-removed"
      />
      <Cell
        label="Secrets repinned"
        value={counts.secrets_changed}
        sub="values never shown"
        testId="release-diff-count-secrets"
      />
      <Cell
        label="Schema"
        value={
          <span className="release-diff-secret-sides">
            <Ident kind="schema" value={`v${from.schema_version}`} tooltip={false} />
            {diff.schema_changed ? (
              <>
                <span className="release-diff-arrow" aria-hidden>
                  →
                </span>
                <Ident kind="schema" value={`v${to.schema_version}`} tooltip={false} />
              </>
            ) : null}
          </span>
        }
        sub={diff.schema_changed ? <Badge kind="warning">different tracks</Badge> : "same"}
        testId="release-diff-schema"
      />
      {rollout !== undefined ? (
        <Cell
          label="Rollout"
          value={
            rollout ? (
              <span className="release-diff-secret-sides">
                <span>
                  {rollout.applied_current}/{rollout.total} applied
                </span>
                {rollout.rejected > 0 ? (
                  <Badge kind="danger">{rollout.rejected} rejected</Badge>
                ) : null}
              </span>
            ) : (
              <span className="faint">—</span>
            )
          }
          sub={rollout && rollout.pending > 0 ? `${rollout.pending} pending` : undefined}
          testId="release-diff-rollout"
        />
      ) : null}
    </div>
  );
}
