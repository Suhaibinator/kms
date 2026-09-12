import { JsonLine } from "@/components/JsonHighlight";
import { countNoun } from "@/lib/format";
import { formatValuePath, type StructuralDiff as StructuralDiffResult } from "@/lib/value-diff";

/**
 * The leaf changes of a JSON value, one per line: path, old value, arrow,
 * new value. Unchanged subtrees are a single fold line; the side-by-side
 * tab is where they open.
 */
export function StructuralDiff({ structural }: { structural: StructuralDiffResult }) {
  return (
    <div className="release-diff-structural" data-testid="release-diff-structural">
      <ul className="release-diff-leaves">
        {structural.changes.map((change) => {
          const path = formatValuePath(change.path);
          return (
            <li
              key={`${change.kind}:${path}`}
              className="release-diff-leaf"
              data-kind={change.kind}
            >
              <span className="release-diff-leaf-path">{path || "(root)"}</span>
              <span className="release-diff-leaf-values">
                {change.before !== undefined ? (
                  <span className="release-diff-old">
                    <JsonLine text={change.before} />
                  </span>
                ) : (
                  <span className="release-diff-missing">—</span>
                )}
                <span className="release-diff-arrow" aria-hidden>
                  →
                </span>
                {change.after !== undefined ? (
                  <span className="release-diff-new">
                    <JsonLine text={change.after} />
                  </span>
                ) : (
                  <span className="release-diff-missing">—</span>
                )}
              </span>
            </li>
          );
        })}
        {structural.unchangedLeaves > 0 ? (
          <li className="release-diff-fold">
            {countNoun(structural.unchangedLeaves, "unchanged fields")} hidden · side-by-side shows
            everything
          </li>
        ) : null}
        {structural.truncated ? (
          <li className="release-diff-fold">
            Listing stopped after {structural.changes.length} changes; use side-by-side for the
            rest.
          </li>
        ) : null}
      </ul>
    </div>
  );
}
