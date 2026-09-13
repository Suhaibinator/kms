import type { OverviewRollout, SubscriberInstance } from "@/lib/types";

/** A fixture producer playing the server; UI tests must supply the whole projection. */
export function projectedSubscribers<T extends { state: string; connected: boolean }>(
  rows: T[],
  revision: number,
) {
  const instances = rows.map((row, index) => ({
    applied_divergent: false,
    divergent_field_count: 0,
    ...row,
    session_id: `session-${index}`,
    classification: !row.connected
      ? "stale"
      : row.state === "applied"
        ? "applied"
        : row.state === "rejected"
          ? "rejected"
          : "pending",
    reason: "fixture",
  }));
  const summary: OverviewRollout = {
    total: rows.length,
    connected: rows.filter((row) => row.connected).length,
    applied_current: instances.filter((row) => row.classification === "applied").length,
    rejected: instances.filter((row) => row.classification === "rejected").length,
    pending: instances.filter((row) => row.classification === "pending").length,
    stale: instances.filter((row) => row.classification === "stale").length,
    applied_divergent: 0,
    pinned: 0,
    unknown: 0,
    complete: true,
    other_release_names: [],
    rejected_instances: [],
    truncated: false,
  };
  return {
    instances: instances as unknown as SubscriberInstance[],
    summary,
    projection_revision: `projection-${revision}`,
    subscribers: [],
    current_revision: revision,
    next_page_token: "",
  };
}
