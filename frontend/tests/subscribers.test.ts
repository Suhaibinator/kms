import { describe, expect, it } from "vitest";
import {
  countSubscribers,
  groupSubscriberInstances,
  groupSubscriberLifecycles,
} from "@/lib/subscribers";
import type { ReleaseSubscriberState } from "@/lib/types";

const base: ReleaseSubscriberState = {
  namespace: { env: "prod", app: "gradethis" },
  release_name: "runtime",
  client_name: "api",
  instance_id: "api-1",
  identity: "gradethis-prod",
  state: "applied",
  release_version: 12,
  activation_revision: 41,
  rejection_category: "",
  diagnostic: "",
  client_timestamp_unix_ms: 1_000,
  server_timestamp_unix_ms: 1_000,
  connected: true,
  applied_divergent: false,
  divergent_field_count: 0,
};

const row = (patch: Partial<ReleaseSubscriberState>): ReleaseSubscriberState => ({
  ...base,
  ...patch,
});

describe("groupSubscriberInstances", () => {
  it("collapses lifecycle rows to the highest revision, then latest, then strongest state", () => {
    const instances = groupSubscriberInstances([
      row({ state: "received", activation_revision: 41, server_timestamp_unix_ms: 1 }),
      row({ state: "prepared", activation_revision: 41, server_timestamp_unix_ms: 2 }),
      row({ state: "applied", activation_revision: 41, server_timestamp_unix_ms: 3 }),
      // An older revision never wins, whatever its timestamp or state.
      row({ state: "rejected", activation_revision: 40, server_timestamp_unix_ms: 99 }),
      // Same revision and timestamp: rejected outranks applied.
      row({
        instance_id: "api-2",
        state: "applied",
        activation_revision: 41,
        server_timestamp_unix_ms: 5,
      }),
      row({
        instance_id: "api-2",
        state: "rejected",
        activation_revision: 41,
        server_timestamp_unix_ms: 5,
        rejection_category: "config_validation_failed",
        diagnostic: "rate_limits.per_minute must be > 0",
      }),
    ]);
    expect(instances).toEqual([
      expect.objectContaining({
        instance_id: "api-1",
        state: "applied",
        activation_revision: 41,
        server_timestamp_unix_ms: 3,
      }),
      expect.objectContaining({
        instance_id: "api-2",
        state: "rejected",
        rejection_category: "config_validation_failed",
        diagnostic: "rate_limits.per_minute must be > 0",
      }),
    ]);
  });

  it("marks an instance connected when any of its rows is, and sorts by identity/client/instance", () => {
    const instances = groupSubscriberInstances([
      row({ identity: "z", instance_id: "b", state: "", connected: true, activation_revision: 0 }),
      row({ identity: "z", instance_id: "b", state: "applied", connected: false }),
      row({ identity: "a", client_name: "worker", instance_id: "w-1", connected: false }),
      row({ identity: "a", client_name: "api", instance_id: "api-9", connected: false }),
    ]);
    expect(instances.map((i) => [i.identity, i.client_name, i.instance_id, i.connected])).toEqual([
      ["a", "api", "api-9", false],
      ["a", "worker", "w-1", false],
      ["z", "api", "b", true],
    ]);
    expect(instances[2]?.state).toBe("applied");
  });

  it("returns an empty list for no rows", () => {
    expect(groupSubscriberInstances([])).toEqual([]);
  });
});

describe("countSubscribers", () => {
  it("buckets instances against the current revision", () => {
    const instances = groupSubscriberInstances([
      row({ instance_id: "applied", state: "applied", activation_revision: 41 }),
      row({ instance_id: "behind", state: "applied", activation_revision: 40 }),
      row({ instance_id: "preparing", state: "prepared", activation_revision: 41 }),
      row({ instance_id: "fresh", state: "", activation_revision: 0 }),
      row({ instance_id: "rejected", state: "rejected", activation_revision: 41 }),
      row({ instance_id: "old-rejection", state: "rejected", activation_revision: 39 }),
      row({
        instance_id: "gone-applied",
        state: "applied",
        activation_revision: 41,
        connected: false,
      }),
      row({
        instance_id: "gone-stale",
        state: "applied",
        activation_revision: 40,
        connected: false,
      }),
      row({ instance_id: "gone-fresh", state: "", activation_revision: 0, connected: false }),
    ]);
    expect(countSubscribers(instances, 41)).toEqual({
      total: 9,
      connected: 6,
      applied_current: 1,
      applied_divergent: 0,
      rejected: 1,
      pending: 4,
      stale: 2,
    });
  });

  it("counts nothing for an empty list", () => {
    expect(countSubscribers([], 7)).toEqual({
      total: 0,
      connected: 0,
      applied_current: 0,
      applied_divergent: 0,
      rejected: 0,
      pending: 0,
      stale: 0,
    });
  });
});

describe("divergent applied instances", () => {
  it("carries divergence only from applied rows and counts it as a warning, not a failure", () => {
    const instances = groupSubscriberInstances([
      row({
        instance_id: "drifted",
        state: "applied",
        applied_divergent: true,
        divergent_field_count: 3,
      }),
      // A stale prepared row for the same instance must not leak its flag.
      row({
        instance_id: "drifted",
        state: "prepared",
        server_timestamp_unix_ms: 999,
        applied_divergent: true,
        divergent_field_count: 9,
      }),
      row({ instance_id: "clean", state: "applied" }),
      // Divergence on a non-applied row is meaningless and dropped.
      row({
        instance_id: "rejecting",
        state: "rejected",
        rejection_category: "restart_required",
        applied_divergent: true,
        divergent_field_count: 2,
      }),
    ]);
    const drifted = instances.find((i) => i.instance_id === "drifted");
    expect(drifted?.applied_divergent).toBe(true);
    expect(drifted?.divergent_field_count).toBe(3);
    expect(instances.find((i) => i.instance_id === "rejecting")?.applied_divergent).toBe(false);
    expect(countSubscribers(instances, 41)).toEqual({
      total: 3,
      connected: 3,
      applied_current: 2,
      applied_divergent: 1,
      rejected: 1,
      pending: 0,
      stale: 0,
    });
  });

  it("does not count divergence for an instance still behind the current revision", () => {
    const instances = groupSubscriberInstances([
      row({
        state: "applied",
        activation_revision: 40,
        applied_divergent: true,
        divergent_field_count: 1,
      }),
    ]);
    expect(countSubscribers(instances, 41).applied_divergent).toBe(0);
  });
});

describe("groupSubscriberLifecycles", () => {
  it("keeps one row per lifecycle state and the latest revision seen", () => {
    const groups = groupSubscriberLifecycles([
      row({ state: "received", activation_revision: 40 }),
      row({ state: "applied", activation_revision: 41 }),
      row({ state: "", activation_revision: 0, connected: false }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0]).toMatchObject({
      identity: "gradethis-prod",
      client_name: "api",
      instance_id: "api-1",
      connected: true,
      latestRevision: 41,
    });
    expect(Object.keys(groups[0]?.states ?? {}).sort()).toEqual(["applied", "received"]);
  });
});
