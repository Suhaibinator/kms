import { Cable } from "lucide-react";
import { Ident } from "@/components/Ident";
import { Badge } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { rejectionGuidance } from "@/lib/glossary";
import { findingCopy } from "@/lib/readiness";
import type { EnvironmentOverview } from "@/lib/types";

export function SubscribersSection({
  environment,
  releaseName,
  onConnect,
}: {
  environment: EnvironmentOverview;
  releaseName: string;
  onConnect: (env: string) => void;
}) {
  const env = environment.namespace.env;
  const rollout = environment.rollout;
  const otherRelease = environment.findings.find(
    (finding) => finding.code === "subscriber_other_release",
  );
  const otherNames = rollout.other_release_names;
  return (
    <section className="pipeline-section" aria-label={`Subscribers in ${env}`}>
      <h3 className="pipeline-section-title">Subscribers</h3>
      {rollout.total === 0 ? (
        <div className="pipeline-row">
          <span className="faint text-sm">No subscribers</span>
          <Button type="button" variant="ghost" size="sm" onClick={() => onConnect(env)}>
            <Cable size={13} />
            Connect SDK
          </Button>
        </div>
      ) : (
        <div className="pipeline-pills">
          <Badge kind="neutral">connected {rollout.connected}</Badge>
          <Badge kind="success">applied {rollout.applied_current}</Badge>
          {rollout.pending > 0 ? <Badge kind="accent">pending {rollout.pending}</Badge> : null}
          {rollout.rejected > 0 ? <Badge kind="danger">rejected {rollout.rejected}</Badge> : null}
          {rollout.stale > 0 ? <Badge kind="warning">stale {rollout.stale}</Badge> : null}
        </div>
      )}
      {rollout.rejected_instances.length > 0 ? (
        <details className="advanced-panel pipeline-rejected">
          <summary>
            {rollout.rejected_instances.length} rejected{" "}
            {rollout.rejected_instances.length === 1 ? "instance" : "instances"}
          </summary>
          <div className="advanced-panel-content">
            {rollout.rejected_instances.map((instance) => {
              const guidance = rejectionGuidance(instance.rejection_category);
              return (
                <div
                  className="pipeline-instance"
                  key={`${instance.identity}:${instance.client_name}:${instance.instance_id}`}
                >
                  <div className="row-wrap">
                    <Ident
                      kind="instance"
                      value={`${instance.client_name}/${instance.instance_id}`}
                      tooltip={false}
                    />
                    <Tooltip>
                      <TooltipTrigger render={<span className="pipeline-category" />}>
                        <Badge kind="danger">{instance.rejection_category || "unknown"}</Badge>
                      </TooltipTrigger>
                      <TooltipContent>
                        <span>
                          <strong>{guidance.summary}</strong> {guidance.response}
                        </span>
                      </TooltipContent>
                    </Tooltip>
                    {instance.release_version > 0 ? (
                      <span className="faint text-sm">
                        still serving v{instance.release_version}
                      </span>
                    ) : null}
                  </div>
                  {instance.diagnostic ? (
                    <div className="pipeline-diagnostic mono text-sm">{instance.diagnostic}</div>
                  ) : null}
                </div>
              );
            })}
            {rollout.truncated ? (
              <div className="faint text-sm">Only the first 50 rejected instances are listed.</div>
            ) : null}
          </div>
        </details>
      ) : null}
      {otherNames.length > 0 ? (
        <div className="warn-panel text-sm">
          {otherRelease
            ? findingCopy(otherRelease)
            : "Some instances subscribe to a different release name than the application's."}{" "}
          Expected <span className="mono">{releaseName}</span>; seen{" "}
          <span className="mono">{otherNames.join(", ")}</span>.
        </div>
      ) : null}
    </section>
  );
}
