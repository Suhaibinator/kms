import { RefreshCw } from "lucide-react";
import { type ReactNode, useEffect } from "react";
import { Ident } from "@/components/Ident";
import { TransportBadge } from "@/components/TransportBadge";
import { Badge, Button } from "@/components/ui";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { rejectionGuidance } from "@/lib/glossary";
import { countSubscribers } from "@/lib/subscribers";
import type { NamespaceRef, SubscriberInstance } from "@/lib/types";
import { useReleaseSubscribers } from "@/lib/useReleaseSubscribers";
import { sortForRollout } from "./model";

export interface RolloutPanelProps {
  namespace: NamespaceRef;
  releaseName: string;
  /** The activation instances are expected to reach; counts are relative to it. */
  activationRevision: number;
  /** Streams/polls only while true (a closed tab must not keep a stream open). */
  enabled: boolean;
  /** Extra line under the progress (the workspace's row cap, for instance). */
  caption?: ReactNode;
  /** Inline Roll back; omitted when there is nothing to roll back to. */
  onRollback?: () => void;
  rollbackDisabled?: boolean;
  /** Bump to force a refresh (after a rollback, say) without waiting for the next poll. */
  refreshToken?: number;
}

function stateTone(
  instance: SubscriberInstance,
  atCurrent: boolean,
): "success" | "danger" | "accent" | "neutral" {
  if (instance.state === "rejected" && atCurrent) return "danger";
  if (instance.state === "applied" && atCurrent) return "success";
  if (!instance.connected) return "neutral";
  return "accent";
}

function stateLabel(instance: SubscriberInstance, atCurrent: boolean): string {
  if (!atCurrent) {
    return instance.state === "applied" ? "pending" : instance.state || "connected";
  }
  return instance.state || "connected";
}

/**
 * Live rollout for one release name: progress toward `activationRevision`,
 * rejected instances first with their category and remediation, and the
 * transport badge. Data comes from useReleaseSubscribers (stream, else poll).
 */
export function RolloutPanel({
  namespace,
  releaseName,
  activationRevision,
  enabled,
  caption,
  onRollback,
  rollbackDisabled,
  refreshToken,
}: RolloutPanelProps) {
  const live = useReleaseSubscribers(namespace, releaseName, { enabled });
  const refresh = live.refresh;
  useEffect(() => {
    if (refreshToken) void refresh();
  }, [refreshToken, refresh]);
  const counts = countSubscribers(live.instances, activationRevision);
  const ordered = sortForRollout(live.instances, activationRevision);

  return (
    <section className="rollout-panel" data-testid="ship-rollout" aria-label="Rollout">
      <div className="rollout-head">
        <div className="rollout-progress" data-testid="rollout-progress">
          {counts.total === 0 ? (
            <strong>No subscribers</strong>
          ) : (
            <>
              <strong>
                {counts.applied_current}/{counts.total} applied
              </strong>
              {counts.rejected > 0 ? <Badge kind="danger">{counts.rejected} rejected</Badge> : null}
              {counts.pending > 0 ? <Badge kind="accent">{counts.pending} pending</Badge> : null}
              {counts.stale > 0 ? <Badge>{counts.stale} stale</Badge> : null}
            </>
          )}
          <span className="faint text-sm">
            at <Ident kind="revision" value={String(activationRevision)} />
          </span>
        </div>
        <div className="rollout-tools">
          <TransportBadge
            transport={live.transport}
            stale={live.stale}
            lastUpdatedAt={live.lastUpdatedAt}
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={!enabled}
            onClick={() => void live.refresh()}
          >
            <RefreshCw size={14} aria-hidden />
            Refresh
          </Button>
          {onRollback ? (
            <Button
              type="button"
              variant="destructive"
              size="sm"
              disabled={rollbackDisabled}
              onClick={onRollback}
              data-testid="rollout-rollback"
            >
              Roll back
            </Button>
          ) : null}
        </div>
      </div>
      {caption ? <div className="faint text-sm rollout-caption">{caption}</div> : null}

      {ordered.length === 0 ? (
        <p className="faint text-sm rollout-empty">
          No client is subscribed to <span className="mono">{releaseName}</span> in this environment
          yet. Connect the SDK to see instances apply the release.
        </p>
      ) : (
        <div className="table-wrap">
          <table className="data rollout-table">
            <thead>
              <tr>
                <th>Instance</th>
                <th>State</th>
                <th>Serving</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {ordered.map((instance) => {
                const atCurrent = instance.activation_revision >= activationRevision;
                const rejected = instance.state === "rejected" && atCurrent;
                const guidance = rejected ? rejectionGuidance(instance.rejection_category) : null;
                return (
                  <tr
                    key={JSON.stringify([
                      instance.identity,
                      instance.client_name,
                      instance.instance_id,
                    ])}
                    className={rejected ? "rollout-rejected" : undefined}
                    data-testid="rollout-instance"
                    data-state={rejected ? "rejected" : stateLabel(instance, atCurrent)}
                  >
                    <td>
                      <div className="rollout-instance">
                        <Ident
                          kind="instance"
                          value={`${instance.client_name}/${instance.instance_id}`}
                        />
                        <span className="faint text-sm">{instance.identity}</span>
                        {!instance.connected ? <Badge>disconnected</Badge> : null}
                      </div>
                    </td>
                    <td>
                      <div className="rollout-state">
                        <Badge kind={stateTone(instance, atCurrent)}>
                          {stateLabel(instance, atCurrent)}
                        </Badge>
                        {rejected && instance.rejection_category ? (
                          <Tooltip>
                            <TooltipTrigger
                              render={<button type="button" className="rollout-category-tip" />}
                            >
                              <Badge kind="danger" className="rollout-category">
                                {instance.rejection_category}
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>
                              <span>
                                <strong>{guidance?.summary}</strong> {guidance?.response}
                              </span>
                            </TooltipContent>
                          </Tooltip>
                        ) : null}
                      </div>
                    </td>
                    <td className="mono">
                      {rejected
                        ? `still serving v${instance.release_version}`
                        : instance.release_version > 0
                          ? `v${instance.release_version}`
                          : "—"}
                    </td>
                    <td>
                      {rejected ? (
                        <div className="rollout-remedy">
                          <div className="text-sm">{guidance?.response}</div>
                          {instance.diagnostic ? (
                            <div className="rollout-diagnostic mono">{instance.diagnostic}</div>
                          ) : null}
                        </div>
                      ) : (
                        <span className="faint text-sm">
                          rev {instance.activation_revision || "—"}
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
