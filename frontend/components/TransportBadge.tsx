import { formatRelative, formatUnixMs } from "@/lib/format";
import { useNow } from "@/lib/useNow";
import { cn } from "@/lib/utils";

/** `manual` is a load-once page: fresh only as of the last Refresh. */
export type Transport = "stream" | "poll" | "off" | "manual";

export interface TransportBadgeProps {
  transport: Transport;
  /** The last refresh failed, or a later change was detected; what is shown may be behind. */
  stale: boolean;
  lastUpdatedAt: number | null;
  /** Replaces the default explanation of how the view stays fresh. */
  title?: string;
  /** Replaces the default explanation while `stale`. */
  staleTitle?: string;
  className?: string;
}

const LABEL: Record<Transport, string> = {
  stream: "Live",
  poll: "Polling",
  off: "Paused",
  manual: "Loaded",
};

const TITLE: Record<Transport, string> = {
  stream: "Updates arrive as instances report.",
  poll: "Refreshed every 5 seconds while this tab is visible.",
  off: "Not refreshing.",
  manual: "Loaded when the page opened or you pressed Refresh.",
};

const STALE_TITLE: Record<Transport, string> = {
  stream: "The last refresh failed; retrying.",
  poll: "The last refresh failed; retrying.",
  off: "The last refresh failed.",
  manual: "The last refresh failed; what is shown may be behind.",
};

/** How fresh a view is: live stream, polling, load-once, or paused/stale.
 *  The relative time ticks so "· 2m ago" stays honest without a reload; the
 *  absolute time is in the tooltip. */
export function TransportBadge({
  transport,
  stale,
  lastUpdatedAt,
  title,
  staleTitle,
  className,
}: TransportBadgeProps) {
  const now = useNow(15_000);
  const label = stale ? "Stale" : LABEL[transport];
  const explanation = stale ? (staleTitle ?? STALE_TITLE[transport]) : (title ?? TITLE[transport]);
  const tooltip = lastUpdatedAt
    ? `${explanation} Last updated ${formatUnixMs(lastUpdatedAt)}.`
    : explanation;
  return (
    <span
      role="status"
      title={tooltip}
      className={cn("transport-badge", `transport-${transport}`, stale && "is-stale", className)}
    >
      <span className="transport-dot" aria-hidden="true" />
      <span className="transport-label">{label}</span>
      {lastUpdatedAt ? (
        <span className="transport-time">· {formatRelative(lastUpdatedAt, now)}</span>
      ) : null}
    </span>
  );
}
