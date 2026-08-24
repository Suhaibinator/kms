// Grouping of release-subscriber state rows. The server keeps one row per
// (identity, client, instance, lifecycle state); the console shows one row per
// instance, so the rows collapse to the instance's effective state.

import type { OverviewRollout, ReleaseSubscriberState, SubscriberInstance } from "@/lib/types";

export type LifecycleState = "received" | "prepared" | "applied" | "rejected";

const LIFECYCLE_STATES: readonly LifecycleState[] = ["received", "prepared", "applied", "rejected"];

// Rejected outranks applied: an instance that applied revision N and then
// rejected N+1 is in trouble now, whatever it managed to do before.
const STATE_RANK: Record<ReleaseSubscriberState["state"], number> = {
  rejected: 4,
  applied: 3,
  prepared: 2,
  received: 1,
  "": 0,
};

export function instanceKey(row: {
  identity: string;
  client_name: string;
  instance_id: string;
}): string {
  return JSON.stringify([row.identity, row.client_name, row.instance_id]);
}

function byIdentity(
  a: { identity: string; client_name: string; instance_id: string },
  b: { identity: string; client_name: string; instance_id: string },
): number {
  return (
    a.identity.localeCompare(b.identity) ||
    a.client_name.localeCompare(b.client_name) ||
    a.instance_id.localeCompare(b.instance_id)
  );
}

/** True when `candidate` is a better "effective state" row than `current`. */
function outranks(candidate: ReleaseSubscriberState, current: ReleaseSubscriberState): boolean {
  if (candidate.activation_revision !== current.activation_revision) {
    return candidate.activation_revision > current.activation_revision;
  }
  if (candidate.server_timestamp_unix_ms !== current.server_timestamp_unix_ms) {
    return candidate.server_timestamp_unix_ms > current.server_timestamp_unix_ms;
  }
  return STATE_RANK[candidate.state] > STATE_RANK[current.state];
}

/**
 * One effective row per (identity, client_name, instance_id): the row with
 * the highest activation revision, then the latest server timestamp, then the
 * strongest state (rejected > applied > prepared > received > none).
 * `connected` is true when any of the instance's rows says so. Sorted by
 * identity, client, instance.
 */
export function groupSubscriberInstances(
  states: readonly ReleaseSubscriberState[],
): SubscriberInstance[] {
  const chosen = new Map<string, { row: ReleaseSubscriberState; connected: boolean }>();
  for (const row of states) {
    const key = instanceKey(row);
    const current = chosen.get(key);
    if (!current) {
      chosen.set(key, { row, connected: row.connected });
      continue;
    }
    current.connected ||= row.connected;
    if (outranks(row, current.row)) current.row = row;
  }
  return [...chosen.values()]
    .map(({ row, connected }) => ({
      identity: row.identity,
      client_name: row.client_name,
      instance_id: row.instance_id,
      state: row.state,
      release_version: row.release_version,
      activation_revision: row.activation_revision,
      rejection_category: row.rejection_category,
      diagnostic: row.diagnostic,
      connected,
      server_timestamp_unix_ms: row.server_timestamp_unix_ms,
    }))
    .sort(byIdentity);
}

export type SubscriberCounts = Pick<
  OverviewRollout,
  "total" | "connected" | "applied_current" | "rejected" | "pending" | "stale"
>;

/**
 * Rollout counts over grouped instances, matching the server's summary:
 * applied_current / rejected are connected instances at (or past) the current
 * activation revision; pending is every other connected instance; stale is a
 * disconnected instance that never applied the current revision.
 */
export function countSubscribers(
  instances: readonly SubscriberInstance[],
  currentRevision: number,
): SubscriberCounts {
  const counts: SubscriberCounts = {
    total: instances.length,
    connected: 0,
    applied_current: 0,
    rejected: 0,
    pending: 0,
    stale: 0,
  };
  for (const instance of instances) {
    const atCurrent = instance.activation_revision >= currentRevision;
    const applied = instance.state === "applied" && atCurrent;
    if (!instance.connected) {
      if (!applied) counts.stale += 1;
      continue;
    }
    counts.connected += 1;
    if (applied) counts.applied_current += 1;
    else if (instance.state === "rejected" && atCurrent) counts.rejected += 1;
    else counts.pending += 1;
  }
  return counts;
}

/** The Releases workspace's per-lifecycle view: every state row kept, by column. */
export interface SubscriberLifecycle {
  identity: string;
  client_name: string;
  instance_id: string;
  connected: boolean;
  states: Partial<Record<LifecycleState, ReleaseSubscriberState>>;
  latestRevision: number;
}

export function groupSubscriberLifecycles(
  states: readonly ReleaseSubscriberState[],
): SubscriberLifecycle[] {
  const grouped = new Map<string, SubscriberLifecycle>();
  for (const state of states) {
    const key = instanceKey(state);
    const row = grouped.get(key) ?? {
      identity: state.identity,
      client_name: state.client_name,
      instance_id: state.instance_id,
      connected: false,
      states: {},
      latestRevision: 0,
    };
    if ((LIFECYCLE_STATES as readonly string[]).includes(state.state)) {
      row.states[state.state as LifecycleState] = state;
    }
    row.connected ||= state.connected;
    row.latestRevision = Math.max(row.latestRevision, state.activation_revision);
    grouped.set(key, row);
  }
  return [...grouped.values()].sort(byIdentity);
}
