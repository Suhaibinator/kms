import Link from "next/link";
import { useMemo } from "react";
import { Ident } from "@/components/Ident";
import { isUnreachableError, type ReleaseDiffQuery } from "@/lib/api";
import { buildRows } from "@/lib/release-diff";
import { useReleaseDiff } from "./useReleaseDiff";

export interface ReleaseDiffSummaryProps {
  query: ReleaseDiffQuery;
  /** Aliases named before "See all"; default 5. */
  maxAliases?: number;
  /** The full comparison, when the caller has somewhere to send the reader. */
  href?: string;
}

/**
 * One line of counts plus the first aliases, for the Rollback dialog and an
 * audit row: entries only (`values: false`), so it is cheap and never
 * carries a value.
 */
export function ReleaseDiffSummary({ query, maxAliases = 5, href }: ReleaseDiffSummaryProps) {
  const { diff, loading, error } = useReleaseDiff(
    useMemo(() => ({ ...query, values: false }), [query]),
  );
  const rows = useMemo(
    () => (diff ? buildRows(diff).filter((row) => row.change !== "unchanged") : []),
    [diff],
  );
  return (
    <div className="release-diff-summary" data-testid="release-diff-summary" aria-busy={loading}>
      {loading ? (
        <span className="faint">Comparing…</span>
      ) : error ? (
        <span className="faint">
          {isUnreachableError(error)
            ? "Could not reach the server to compare."
            : "Comparison unavailable."}
        </span>
      ) : diff ? (
        <>
          {diff.identical ? (
            <span className="faint">No differences.</span>
          ) : (
            <span className="release-diff-summary-counts">
              <span>
                <strong>{diff.counts.changed}</strong> changed
              </span>
              <span>
                <strong>{diff.counts.added}</strong> added
              </span>
              <span>
                <strong>{diff.counts.removed}</strong> removed
              </span>
              {diff.counts.secrets_changed > 0 ? (
                <span>
                  <strong>{diff.counts.secrets_changed}</strong> secrets repinned
                </span>
              ) : null}
            </span>
          )}
          {rows.length > 0 ? (
            <span className="release-diff-summary-aliases">
              {rows.slice(0, maxAliases).map((row) => (
                <Ident key={row.alias} kind="alias" value={row.alias} tooltip={false} />
              ))}
              {rows.length > maxAliases ? (
                <span className="faint">+{rows.length - maxAliases} more</span>
              ) : null}
            </span>
          ) : null}
          {href ? (
            <Link href={href} className="text-sm">
              See all →
            </Link>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
