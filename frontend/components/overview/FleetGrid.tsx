import ApplicationCard from "@/components/overview/ApplicationCard";
import type { ApplicationOverview, FleetApplication } from "@/lib/types";

export interface FleetGridProps {
  applications: FleetApplication[];
  /** Per-app overviews keyed by name; absent = not fetched, null = failed. */
  overviews: Record<string, ApplicationOverview | null>;
  /** A ticking clock (lib/useNow.ts) for the cards' relative times. */
  now?: number;
  detailLoading?: Record<string, boolean>;
  onLoadDetail?: (name: string) => void;
}

/** One card per application, blocked/attention first so trouble is at the top. */
export default function FleetGrid({
  applications,
  overviews,
  now,
  detailLoading,
  onLoadDetail,
}: FleetGridProps) {
  const rank: Record<FleetApplication["status"], number> = {
    blocked: 0,
    attention: 1,
    setup: 2,
    ready: 3,
  };
  const sorted = [...applications].sort(
    (a, b) =>
      rank[a.status] - rank[b.status] || a.application.name.localeCompare(b.application.name),
  );
  return (
    <div className="fleet-grid" data-count={sorted.length}>
      {sorted.map((fleet) => (
        <ApplicationCard
          key={fleet.application.name}
          fleet={fleet}
          overview={overviews[fleet.application.name]}
          now={now}
          loading={detailLoading?.[fleet.application.name]}
          onLoadDetail={onLoadDetail ? () => onLoadDetail(fleet.application.name) : undefined}
        />
      ))}
    </div>
  );
}
