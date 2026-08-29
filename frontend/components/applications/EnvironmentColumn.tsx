import { MoreHorizontal } from "lucide-react";
import { useEffect, useMemo, useRef } from "react";
import { FindingList } from "@/components/FindingList";
import { Ident } from "@/components/Ident";
import { StatusChip } from "@/components/StatusChip";
import { Button } from "@/components/ui/button";
import { links } from "@/lib/links";
import type { FixAction } from "@/lib/readiness";
import type {
  Application,
  ApplicationConfigurationRow,
  EnvironmentOverview,
  Finding,
  FindingCode,
} from "@/lib/types";
import { cn } from "@/lib/utils";
import { ActionMenu } from "./ActionMenu";
import { ReleaseSection } from "./ReleaseSection";
import { SubscribersSection } from "./SubscribersSection";
import { ValuesSection } from "./ValuesSection";

export interface EnvironmentCallbacks {
  onAddValue: (env: string, alias: string) => void;
  onAddSecret: (env: string, alias: string) => void;
  onShip: (env: string, alias?: string) => void;
  onRollback: (env: string) => void;
  onConnect: (env: string) => void;
  /** A finding's Fix button (lib/readiness.ts FIX_FOR). */
  onFix: (action: FixAction, finding: Finding) => void;
}

/** Parameters present in this environment that no contract alias resolves to. */
export function countOtherKeys(
  environment: EnvironmentOverview,
  rows: ApplicationConfigurationRow[],
): number {
  const env = environment.namespace.env;
  const resolved = new Set(
    environment.values.filter((value) => value.key).map((value) => value.key as string),
  );
  return rows.filter(
    (row) => row.kind === "parameter" && row.environments[env]?.present && !resolved.has(row.key),
  ).length;
}

// Findings the column's own sections already show in a richer form (the drift
// badge, the Add value button, the rejected-instance panel, …) or that are
// chrome rather than problems. Everything else would otherwise be invisible
// outside the setup checklist and the Ship preview.
const SURFACED_BY_SECTIONS: ReadonlySet<FindingCode> = new Set<FindingCode>([
  "production",
  "previous_unavailable",
  "unreleased_changes",
  "resource_missing",
  "no_active_release",
  "no_subscribers",
  "subscriber_other_release",
  "instance_rejected",
  "rolled_back",
]);

/** The environment's findings that need their own line in the column. */
export function columnFindings(environment: EnvironmentOverview): Finding[] {
  return environment.findings.filter((finding) => !SURFACED_BY_SECTIONS.has(finding.code));
}

export function EnvironmentColumn({
  application,
  environment,
  rows,
  focused,
  callbacks,
}: {
  application: Application;
  environment: EnvironmentOverview;
  rows: ApplicationConfigurationRow[];
  focused: boolean;
  callbacks: EnvironmentCallbacks;
}) {
  const ns = environment.namespace;
  const column = useRef<HTMLElement>(null);
  const findings = useMemo(() => columnFindings(environment), [environment]);
  // `?env=` deep links land on the column: scroll it into view. Focus stays
  // where it is — the ring (.pipeline-column-focused) marks the target, and a
  // query-only navigation is not a request to move the keyboard cursor.
  useEffect(() => {
    if (!focused || !column.current) return;
    column.current.scrollIntoView?.({ block: "nearest", inline: "center" });
  }, [focused]);

  return (
    <section
      ref={column}
      tabIndex={-1}
      data-env={ns.env}
      aria-label={`${ns.env} environment`}
      className={cn(
        "pipeline-column",
        environment.production && "pipeline-column-prod",
        focused && "pipeline-column-focused",
      )}
    >
      <header className="pipeline-head">
        <div className="row-wrap">
          <Ident kind="env" value={ns.env} production={environment.production} />
          <StatusChip status={environment.status} production={environment.production} />
        </div>
        <ActionMenu
          label={`${ns.env} links`}
          trigger={
            <Button type="button" variant="ghost" size="icon-sm" aria-label={`More for ${ns.env}`}>
              <MoreHorizontal size={16} />
            </Button>
          }
          items={[
            { key: "parameters", label: "Parameters", href: links.parameters(ns) },
            { key: "secrets", label: "Secrets", href: links.secrets(ns) },
            {
              key: "releases",
              label: "Releases",
              href: links.releases({ app: ns.app, env: ns.env, name: application.release_name }),
            },
          ]}
        />
      </header>
      {ns.description ? (
        <div className="pipeline-description faint text-sm">{ns.description}</div>
      ) : null}
      <FindingList findings={findings} onFix={callbacks.onFix} className="pipeline-findings" />
      <ValuesSection
        environment={environment}
        otherKeys={countOtherKeys(environment, rows)}
        onAddValue={callbacks.onAddValue}
        onAddSecret={callbacks.onAddSecret}
        onShip={callbacks.onShip}
      />
      <ReleaseSection
        environment={environment}
        onShip={callbacks.onShip}
        onRollback={callbacks.onRollback}
      />
      <SubscribersSection
        environment={environment}
        releaseName={application.release_name}
        onConnect={callbacks.onConnect}
      />
    </section>
  );
}
