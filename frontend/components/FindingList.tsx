import { Ident } from "@/components/Ident";
import { Badge, Button } from "@/components/ui";
import { FIX_LABEL, type FixAction, findingCopy, fixActionFor } from "@/lib/readiness";
import type { Finding, FindingSeverity } from "@/lib/types";

const SEVERITY_TONE: Record<FindingSeverity, "danger" | "warning" | "neutral"> = {
  blocking: "danger",
  warning: "warning",
  info: "neutral",
};

export interface FindingListProps {
  findings: Finding[];
  onFix: (action: FixAction, finding: Finding) => void;
  className?: string;
}

/**
 * Readiness findings as a list: severity, value-free copy, the scope as typed
 * chips, and a Fix button when the code has one (lib/readiness.ts FIX_FOR).
 */
export function FindingList({ findings, onFix, className }: FindingListProps) {
  if (findings.length === 0) return null;
  return (
    <ul className={className ? `finding-list ${className}` : "finding-list"}>
      {findings.map((finding, index) => {
        const action = fixActionFor(finding);
        return (
          <li
            key={`${finding.code}:${finding.scope.env ?? ""}:${finding.scope.alias ?? ""}:${finding.scope.instance ?? ""}:${index}`}
            className={`finding finding-${finding.severity}`}
            data-code={finding.code}
          >
            <Badge kind={SEVERITY_TONE[finding.severity]} className="finding-severity">
              {finding.severity}
            </Badge>
            <div className="finding-body">
              <div className="finding-copy">{findingCopy(finding)}</div>
              {finding.scope.env || finding.scope.alias || finding.scope.instance ? (
                <div className="finding-scope">
                  {finding.scope.env ? <Ident kind="env" value={finding.scope.env} /> : null}
                  {finding.scope.alias ? <Ident kind="alias" value={finding.scope.alias} /> : null}
                  {finding.scope.instance ? (
                    <Ident kind="instance" value={finding.scope.instance} />
                  ) : null}
                </div>
              ) : null}
            </div>
            {action ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="finding-fix"
                onClick={() => onFix(action, finding)}
              >
                {FIX_LABEL[action]}
              </Button>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}
