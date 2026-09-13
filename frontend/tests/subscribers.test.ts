import { describe, expect, it } from "vitest";
import { sortForRollout } from "@/components/ship/model";
import type { SubscriberInstance } from "@/lib/types";

const instance = (
  classification: string,
  patch: Partial<SubscriberInstance> = {},
): SubscriberInstance => ({
  identity: "identity",
  client_name: "api",
  instance_id: classification,
  state: "applied",
  release_version: 4,
  activation_revision: 153,
  rejection_category: "",
  diagnostic: "",
  connected: true,
  server_timestamp_unix_ms: 1,
  applied_divergent: false,
  divergent_field_count: 0,
  classification,
  reason: classification,
  ...patch,
});

describe("server projected subscriber presentation", () => {
  it("orders classifications without reclassifying lifecycle state or revisions", () => {
    const rows = ["stale", "applied", "pinned", "unknown", "pending", "rejected"].map(
      (classification) => instance(classification),
    );
    expect(sortForRollout(rows, 999).map((row) => row.classification)).toEqual([
      "rejected",
      "pending",
      "unknown",
      "pinned",
      "applied",
      "stale",
    ]);
    expect(rows[0].classification).toBe("stale");
  });

  it("retains distinct sessions and their atomic metadata", () => {
    const old = instance("stale", {
      session_id: "old",
      connected: false,
      diagnostic: "old failure",
    });
    const current = instance("applied", {
      session_id: "new",
      sequence: 3,
      applied_divergent: true,
      divergent_field_count: 2,
    });
    const sorted = sortForRollout([old, current], 153);
    expect(sorted).toEqual([current, old]);
    expect(sorted[0].diagnostic).toBe("");
    expect(sorted[0].divergent_field_count).toBe(2);
  });
});
