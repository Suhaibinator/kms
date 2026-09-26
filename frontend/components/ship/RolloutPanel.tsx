import { type ReactNode, useEffect, useState } from "react";
import { Ident } from "@/components/Ident";
import { RefreshControl } from "@/components/RefreshControl";
import { Badge, Button } from "@/components/ui";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import { rejectionGuidance } from "@/lib/glossary";
import type { NamespaceRef, SubscriberInstance } from "@/lib/types";
import { useReleaseSubscribers } from "@/lib/useReleaseSubscribers";
import { sortForRollout } from "./model";
import { ReleasePinDialog } from "./ReleasePinDialog";

export interface RolloutPanelProps {
  namespace: NamespaceRef;
  releaseName: string;
  schemaVersion: number;
  /** The shipped activation; used to identify a superseding live snapshot. */
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
  /** Disable actions tied to a shipped activation once a newer one appears. */
  onSupersededChange?: (superseded: boolean) => void;
}

function stateTone(
  instance: SubscriberInstance,
): "success" | "danger" | "accent" | "neutral" | "warning" {
  if (instance.classification === "rejected") return "danger";
  if (instance.classification === "pinned") return "warning";
  if (instance.classification === "applied") {
    return instance.applied_divergent ? "warning" : "success";
  }
  return instance.classification === "pending" ? "accent" : "neutral";
}

function stateLabel(instance: SubscriberInstance): string {
  if (instance.classification === "applied" && instance.applied_divergent)
    return "applied · divergent";
  return instance.classification || "unknown";
}

/**
 * Live rollout for one release name: current snapshot counts and rows,
 * explicitly distinguished from the shipped activation when it was superseded.
 * Rejected instances come first with their category and remediation, plus the
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
  onSupersededChange,
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
  // Counts and classifications describe the feed's current activation. Never
  // attach them to the immutable activation that opened the Ship dialog.
  const rolloutRevision = live.lastUpdatedAt !== null ? live.currentRevision : activationRevision;
  const superseded =
    !followCurrentActivation &&
    live.lastUpdatedAt !== null &&
    activationRevision > 0 &&
    live.currentRevision > activationRevision;
  const awaitingSnapshot =
    !followCurrentActivation &&
    live.lastUpdatedAt !== null &&
    live.currentRevision < activationRevision;
  useEffect(() => {
    onSupersededChange?.(superseded);
  }, [superseded, onSupersededChange]);
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
          setActiveRelease({
            scope: activeScope,
            // A second activation may race this separate read. Only decorate
            // the snapshot with a version when the activation is identical.
            version: result.activation_revision === rolloutRevision ? result.release.version : 0,
          });
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
  const counts = live.summary;
  const ordered = sortForRollout(live.instances, rolloutRevision);
  const divergentGuidance = rejectionGuidance("default_mismatch");

  return (
    <section className="rollout-panel" data-testid="ship-rollout" aria-label="Rollout">
      {superseded ? (
        <p className="info-panel" role="status" data-testid="rollout-superseded">
          The activation you shipped (revision {activationRevision}) has been superseded. Counts and
          instances below describe the current track activation, revision {rolloutRevision}.
        </p>
      ) : null}
      {awaitingSnapshot ? (
        <p className="info-panel" role="status">
          Waiting for the shipped activation (revision {activationRevision}) in live status. Counts
          and instances below describe the last reported activation, revision {rolloutRevision}.
        </p>
      ) : null}
      <div className="rollout-head">
        <div className="rollout-progress" data-testid="rollout-progress">
          {!counts?.complete || live.stale ? (
            <strong>Unknown · status unavailable</strong>
          ) : counts.connected === 0 ? (
            <strong>Unknown · no connected subscribers</strong>
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
          <RefreshControl
            size="sm"
            disabled={!enabled}
            onRefresh={() => void live.refresh()}
            freshness={{
              transport: live.transport,
              stale: live.stale,
              lastUpdatedAt: live.lastUpdatedAt,
            }}
          />
          {onRollback ? (
            <Button
              type="button"
              variant="destructive"
              size="sm"
              disabled={rollbackDisabled || superseded}
              onClick={onRollback}
              data-testid="rollout-rollback"
            >
              Roll back
            </Button>
          ) : null}
        </div>
      </div>
      {caption ? <div className="faint text-sm rollout-caption">{caption}</div> : null}
      {live.truncated ? (
        <p className="faint text-sm">
          Showing the first {live.instances.length} instances. Counts include all instances.
        </p>
      ) : null}

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
                <th>Last applied</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {ordered.map((instance) => {
                const atCurrent =
                  instance.classification === "applied" || instance.classification === "pinned";
                const rejected = instance.classification === "rejected";
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
                    data-state={stateLabel(instance)}
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
                        <Badge kind={stateTone(instance)}>{stateLabel(instance)}</Badge>
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
                    <td data-label="Last applied" className="mono">
                      {instance.last_applied_version ? `v${instance.last_applied_version}` : "—"}
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
