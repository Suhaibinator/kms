import { Check, Wrench } from "lucide-react";
import { useMemo } from "react";
import { FindingList } from "@/components/FindingList";
import { Ident } from "@/components/Ident";
import { Badge } from "@/components/ui";
import { Button } from "@/components/ui/button";
import {
  type AlignmentIssue,
  type ContractEntry,
  checkContractAlignment,
  deriveContractFromSchema,
} from "@/lib/contract-derive";
import type { FixAction } from "@/lib/readiness";
import type { ApplicationOverview, Finding, FindingCode } from "@/lib/types";
import { ActionMenu } from "./ActionMenu";

// App-level findings that belong to the contract ⇄ schema relationship and so
// render in the Alignment row rather than anywhere else (ApplicationHome
// excludes them from its own finding list).
export const ALIGNMENT_CODES: ReadonlySet<FindingCode> = new Set<FindingCode>([
  "contract_empty",
  "schema_unpinned",
  "schema_missing",
  "schema_property_missing_alias",
  "schema_required_missing_alias",
  "alias_not_in_schema",
  "contract_type_mismatch",
]);

export interface Alignment {
  issues: AlignmentIssue[];
  /** Server findings not already covered by a client-side issue on the same alias. */
  findings: Finding[];
  aligned: boolean;
}

/** Client-side alignment merged with the server's app-level findings. */
export function mergeAlignment(overview: ApplicationOverview): Alignment {
  const { issues } = checkContractAlignment(overview.application.contract, overview.schema_json);
  const covered = new Set(issues.map((issue) => issue.alias).filter(Boolean));
  const findings = overview.findings.filter(
    (finding) =>
      ALIGNMENT_CODES.has(finding.code) &&
      !finding.scope.env &&
      !(finding.scope.alias && covered.has(finding.scope.alias)),
  );
  return { issues, findings, aligned: issues.length === 0 && findings.length === 0 };
}

export function DefinitionCard({
  overview,
  onEdit,
  onDeriveSchema,
}: {
  overview: ApplicationOverview;
  /** Opens the definition modal, optionally with a derived contract prefilled. */
  onEdit: (prefill?: ContractEntry[]) => void;
  /** Register a schema derived from the contract and pin it. */
  onDeriveSchema: () => void;
}) {
  const application = overview.application;
  const alignment = useMemo(() => mergeAlignment(overview), [overview]);
  const pinned = application.schema_version !== 0;

  function onFix(action: FixAction) {
    if (action === "pin_schema") onDeriveSchema();
    else onEdit();
  }

  return (
    <section className="card definition-card" aria-label="Definition">
      <div className="definition-grid">
        <div>
          <span className="faint text-sm">Release name</span>
          <strong className="mono">{application.release_name}</strong>
        </div>
        <div>
          <span className="faint text-sm">Schema</span>
          {pinned ? (
            <Ident
              kind="schema"
              value={`${application.name}/${application.release_name}@${application.schema_version}`}
            />
          ) : (
            <div className="row-wrap">
              <span className="faint">Not pinned</span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={application.archived_at_unix_ms > 0}
                onClick={onDeriveSchema}
              >
                Register schema
              </Button>
            </div>
          )}
        </div>
        <div>
          <span className="faint text-sm">Contract</span>
          <div className="row-wrap">
            {application.contract.length ? (
              application.contract.map((field) => (
                <span className="definition-alias" key={field.alias}>
                  <Ident kind="alias" value={field.alias} tooltip={false} />
                  <span className="faint text-xs">
                    {field.kind}
                    {field.content_type ? `/${field.content_type}` : ""}
                  </span>
                </span>
              ))
            ) : (
              <span className="faint">No aliases</span>
            )}
          </div>
        </div>
      </div>
      <div className="definition-alignment">
        <div className="between">
          <span className="faint text-sm">Alignment</span>
          <ActionMenu
            label="Fix alignment"
            trigger={
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={application.archived_at_unix_ms > 0}
              >
                <Wrench size={13} />
                Fix
              </Button>
            }
            items={[
              {
                key: "derive-contract",
                label: "Derive contract from schema",
                disabled: !overview.schema_json,
                onSelect: () =>
                  onEdit(
                    deriveContractFromSchema(overview.schema_json ?? "", application.contract)
                      .contract,
                  ),
              },
              {
                key: "derive-schema",
                label: "Derive schema from contract",
                onSelect: onDeriveSchema,
              },
              { key: "edit", label: "Edit contract", onSelect: () => onEdit() },
            ]}
          />
        </div>
        {alignment.aligned ? (
          <div className="definition-aligned text-sm text-success">
            <Check size={14} aria-hidden /> Aligned
          </div>
        ) : (
          <>
            {alignment.issues.length > 0 ? (
              <ul className="definition-issues">
                {alignment.issues.map((issue) => (
                  <li
                    key={`${issue.code}:${issue.alias ?? ""}`}
                    className={`definition-issue definition-issue-${issue.severity}`}
                  >
                    <Badge kind={issue.severity === "error" ? "danger" : "warning"}>
                      {issue.severity}
                    </Badge>
                    <span className="text-sm">{issue.detail}</span>
                  </li>
                ))}
              </ul>
            ) : null}
            <FindingList findings={alignment.findings} onFix={onFix} />
          </>
        )}
      </div>
    </section>
  );
}
