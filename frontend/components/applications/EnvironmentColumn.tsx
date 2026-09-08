import { MoreHorizontal } from "lucide-react";
import { useEffect, useMemo, useRef } from "react";
import { FindingList } from "@/components/FindingList";
import { Ident } from "@/components/Ident";
import { StatusChip } from "@/components/StatusChip";
import { Button } from "@/components/ui/button";
import { links } from "@/lib/links";
import { countOtherKeys } from "@/lib/overview";
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
  onOpenSecret?: (env: string, key: string) => void;
  onOpenParameter?: (env: string, key: string) => void;
  onShip: (env: string, alias?: string) => void;
  onRollback: (env: string) => void;
  onConnect: (env: string) => void;
  onImportDefaults?: (env: string) => void;
  onMigrateSchema?: (env: string) => void;
  /** Open the selected track's releases to establish a contract on first adoption. */
  onEditContract?: (env: string) => void;
  /** A finding's Fix button (lib/readiness.ts FIX_FOR). */
  onFix: (action: FixAction, finding: Finding) => void;
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
  const staleFindings = findings.filter((finding) => finding.code === "instance_stale");
  const otherKeys = useMemo(() => countOtherKeys(environment, rows), [environment, rows]);
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
              href: links.releases({
                app: ns.app,
                env: ns.env,
                name: application.release_name,
                schemaVersion: application.schema_version,
              }),
            },
            { key: "connect", label: "Connect SDK", onSelect: () => callbacks.onConnect(ns.env) },
            ...(callbacks.onImportDefaults
              ? [
                  {
                    key: "import-defaults",
                    label: `Import defaults to ${ns.env}…`,
                    onSelect: () => callbacks.onImportDefaults?.(ns.env),
                  },
                ]
              : []),
            ...(callbacks.onMigrateSchema && environment.release.active
              ? [
                  {
                    key: "migrate-schema",
                    label: `Upgrade schema in ${ns.env}…`,
                    onSelect: () => callbacks.onMigrateSchema?.(ns.env),
                  },
                ]
              : []),
          ]}
        />
      </header>
      {ns.description ? (
        <div className="pipeline-description faint text-sm">{ns.description}</div>
      ) : null}
      <FindingList
        findings={findings.filter((f) => f.code !== "instance_stale")}
        onFix={callbacks.onFix}
        className="pipeline-findings"
      />
      {findings.some((f) => f.code === "instance_stale") && (
        <details className="info-panel text-sm">
          <summary className="cursor-pointer">
            {staleFindings.length} stale instances · View details
          </summary>
          <FindingList findings={staleFindings} onFix={callbacks.onFix} />
        </details>
      )}
      <ValuesSection
        environment={environment}
        otherKeys={otherKeys}
        onAddValue={callbacks.onAddValue}
        onAddSecret={callbacks.onAddSecret}
        onOpenSecret={callbacks.onOpenSecret}
        onOpenParameter={callbacks.onOpenParameter}
        onShip={callbacks.onShip}
        onEditContract={() => callbacks.onEditContract?.(ns.env)}
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
