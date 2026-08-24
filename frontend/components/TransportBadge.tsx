import { formatRelative } from "@/lib/format";
import { cn } from "@/lib/utils";

export type Transport = "stream" | "poll" | "off";

export interface TransportBadgeProps {
  transport: Transport;
  /** The last refresh failed; what is shown may be behind. */
  stale: boolean;
  lastUpdatedAt: number | null;
  className?: string;
}

const LABEL: Record<Transport, string> = {
  stream: "Live",
  poll: "Polling",
  off: "Paused",
};

const TITLE: Record<Transport, string> = {
  stream: "Updates arrive as instances report.",
  poll: "Refreshed every 5 seconds while this tab is visible.",
  off: "Not refreshing.",
};

/** How fresh the rollout view is: live stream, 5 s polling, or paused/stale. */
export function TransportBadge({
  transport,
  stale,
  lastUpdatedAt,
  className,
}: TransportBadgeProps) {
  const label = stale ? "Stale" : LABEL[transport];
  const title = stale ? "The last refresh failed; retrying." : TITLE[transport];
  return (
    <span
      role="status"
      title={title}
      className={cn("transport-badge", `transport-${transport}`, stale && "is-stale", className)}
    >
      <span className="transport-dot" aria-hidden="true" />
      <span className="transport-label">{label}</span>
      {lastUpdatedAt ? (
        <span className="transport-time">· {formatRelative(lastUpdatedAt)}</span>
      ) : null}
    </span>
  );
}
