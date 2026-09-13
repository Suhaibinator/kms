import { RefreshCw } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { Ident } from "@/components/Ident";
import { TransportBadge } from "@/components/TransportBadge";
import { Badge, Button } from "@/components/ui";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import { rejectionGuidance } from "@/lib/glossary";
import { countSubscribers } from "@/lib/subscribers";
import type { NamespaceRef, SubscriberInstance } from "@/lib/types";
import { useReleaseSubscribers } from "@/lib/useReleaseSubscribers";
import { ReleasePinDialog } from "./ReleasePinDialog";
import { sortForRollout } from "./model";

export interface RolloutPanelProps {
  namespace: NamespaceRef;
  releaseName: string;
  schemaVersion: number;
  /** The activation instances are expected to reach; counts are relative to it. */
  activationRevision: number;
  /** Follow the revision currently reported by the release feed. */
  followCurrentActivation?: boolean;
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
): "success" | "danger" | "accent" | "neutral" | "warning" {
  if (instance.state === "rejected" && atCurrent) return "danger";
  if (instance.state === "applied" && atCurrent) {
    return instance.applied_divergent ? "warning" : "success";
  }
  if (!instance.connected) return "neutral";
  return "accent";
}

function stateLabel(instance: SubscriberInstance, atCurrent: boolean): string {
  if (instance.pin_version)
    return atCurrent && instance.state === "applied"
      ? "pinned · applied"
      : `pinned · ${instance.state || "pending"}`;
  if (!atCurrent) {
    return instance.state === "applied" ? "pending" : instance.state || "connected";
  }
  if (instance.state === "applied" && instance.applied_divergent) return "applied · divergent";
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
  schemaVersion,
  activationRevision,
  followCurrentActivation = false,
  enabled,
  caption,
  onRollback,
  rollbackDisabled,
  refreshToken,
}: RolloutPanelProps) {
  const [pinDialog, setPinDialog] = useState<{ session: string; unpin: boolean } | null>(null);
  const live = useReleaseSubscribers(namespace, releaseName, { enabled, schemaVersion });
  const refresh = live.refresh;
  const selectedInstance = pinDialog
    ? live.instances.find((item) => item.session_id === pinDialog.session)
    : undefined;
  // biome-ignore lint/correctness/useExhaustiveDependencies: changing tracks invalidates the selected process.
  useEffect(() => {
    setPinDialog(null);
  }, [namespace.env, namespace.app, releaseName, schemaVersion]);
  useEffect(() => {
    if (refreshToken) void refresh();
  }, [refreshToken, refresh]);
  // Keep the supplied target until the feed has yielded a snapshot, so a
  // newly opened tab does not briefly render every row as revision zero.
  const rolloutRevision =
    followCurrentActivation && live.lastUpdatedAt !== null
      ? live.currentRevision
      : activationRevision;
  const activeScope = JSON.stringify([
    namespace.env,
    namespace.app,
    releaseName,
    schemaVersion,
    rolloutRevision,
  ]);
  const [activeRelease, setActiveRelease] = useState<{ scope: string; version: number } | null>(
    null,
  );
  useEffect(() => {
    if (!enabled || !rolloutRevision) return;
    const controller = new AbortController();
    void api
      .getActiveRelease({ env: namespace.env, app: namespace.app }, releaseName, schemaVersion, {
        signal: controller.signal,
      })
      .then((result) => {
        if (!controller.signal.aborted)
          setActiveRelease({ scope: activeScope, version: result.release.version });
      })
      .catch(() => {
        if (!controller.signal.aborted) setActiveRelease({ scope: activeScope, version: 0 });
      });
    return () => controller.abort();
  }, [
    enabled,
    namespace.env,
    namespace.app,
    releaseName,
    schemaVersion,
    rolloutRevision,
    activeScope,
  ]);
  const counts = countSubscribers(live.instances, rolloutRevision);
  const ordered = sortForRollout(live.instances, rolloutRevision);
  const divergentGuidance = rejectionGuidance("default_mismatch");

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
              {(counts.pinned ?? 0) > 0 ? (
                <Badge kind="warning">{counts.pinned} pinned</Badge>
              ) : null}
              {counts.rejected > 0 ? <Badge kind="danger">{counts.rejected} rejected</Badge> : null}
              {counts.applied_divergent > 0 ? (
                <Badge kind="warning">{counts.applied_divergent} divergent</Badge>
              ) : null}
              {counts.pending > 0 ? <Badge kind="accent">{counts.pending} pending</Badge> : null}
            </>
          )}
          <span className="faint text-sm">
            Track active: schema {schemaVersion},{" "}
            {rolloutRevision === 0
              ? "awaiting first activation"
              : activeRelease?.scope === activeScope
                ? activeRelease.version
                  ? `v${activeRelease.version}`
                  : "unavailable"
                : "loading release"}{" "}
            · at <Ident kind="revision" value={String(rolloutRevision)} />
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
        <div className="table-wrap card-table">
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
                const atCurrent = instance.session_id
                  ? instance.target_revision === instance.desired_revision &&
                    instance.release_version === instance.desired_version
                  : instance.activation_revision >= rolloutRevision;
                const rejected = instance.state === "rejected" && atCurrent;
                const guidance = rejected ? rejectionGuidance(instance.rejection_category) : null;
                return (
                  <tr
                    key={JSON.stringify([
                      instance.identity,
                      instance.client_name,
                      instance.instance_id,
                      instance.session_id,
                    ])}
                    className={rejected ? "rollout-rejected" : undefined}
                    data-testid="rollout-instance"
                    data-state={rejected ? "rejected" : stateLabel(instance, atCurrent)}
                  >
                    <td data-label="Instance">
                      <div className="rollout-instance">
                        <Ident
                          kind="instance"
                          value={`${instance.client_name}/${instance.instance_id}`}
                        />
                        <span className="faint text-sm">{instance.identity}</span>
                        {!instance.connected ? <Badge>disconnected</Badge> : null}
                      </div>
                    </td>
                    <td data-label="State">
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
                    <td data-label="Serving" className="mono">
                      {instance.session_id
                        ? instance.last_applied_version
                          ? `v${instance.last_applied_version}`
                          : "—"
                        : rejected
                          ? `still serving v${instance.release_version}`
                          : instance.release_version > 0
                            ? `v${instance.release_version}`
                            : "—"}
                      <span className="faint text-sm"> · schema {schemaVersion}</span>
                    </td>
                    <td data-label="Detail">
                      {instance.session_id ? (
                        <div>
                          <p>
                            Schema {schemaVersion} · target{" "}
                            {instance.desired_version
                              ? `v${instance.desired_version}`
                              : "waiting for activation"}
                          </p>
                          {instance.pin_version ? (
                            <p>
                              Pinned by {instance.pinned_by} ·{" "}
                              {instance.pinned_at_unix_ms
                                ? new Date(instance.pinned_at_unix_ms).toLocaleString()
                                : ""}
                            </p>
                          ) : null}
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={!instance.connected}
                            onClick={() =>
                              setPinDialog({ session: instance.session_id ?? "", unpin: false })
                            }
                          >
                            {instance.pin_version ? "Change pin" : "Pin to release"}
                          </Button>
                          {instance.pin_version ? (
                            <Button
                              size="sm"
                              variant="outline"
                              onClick={() =>
                                setPinDialog({ session: instance.session_id ?? "", unpin: true })
                              }
                            >
                              Unpin
                            </Button>
                          ) : null}
                        </div>
                      ) : (
                        <span className="faint text-sm">Upgrade SDK to enable pinning.</span>
                      )}
                      {rejected ? (
                        <div className="rollout-remedy">
                          <div className="text-sm">{guidance?.response}</div>
                          {instance.diagnostic ? (
                            <div className="rollout-diagnostic mono">{instance.diagnostic}</div>
                          ) : null}
                        </div>
                      ) : atCurrent &&
                        instance.state === "applied" &&
                        instance.applied_divergent ? (
                        <div className="rollout-remedy" data-testid="rollout-divergent">
                          <div className="text-sm">
                            {instance.divergent_field_count > 0
                              ? `${instance.divergent_field_count} ${instance.divergent_field_count === 1 ? "field differs" : "fields differ"} from source defaults. `
                              : "Values differ from source defaults. "}
                            {divergentGuidance.response}
                          </div>
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
      {selectedInstance && pinDialog ? (
        <ReleasePinDialog
          key={`${namespace.env}/${namespace.app}/${releaseName}/${schemaVersion}/${selectedInstance.session_id}/${pinDialog.unpin}`}
          namespace={namespace}
          name={releaseName}
          schemaVersion={schemaVersion}
          instance={selectedInstance}
          unpin={pinDialog.unpin}
          onClose={() => setPinDialog(null)}
          onSaved={() => void refresh()}
        />
      ) : null}
    </section>
  );
}
