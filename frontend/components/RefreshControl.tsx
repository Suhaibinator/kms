import { RefreshCw } from "lucide-react";
import { TransportBadge, type TransportBadgeProps } from "@/components/TransportBadge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** Everything TransportBadge needs; omitted entirely on a page with no
 *  freshness of its own to report (audit, health, posture, releases). */
export type RefreshFreshness = Omit<TransportBadgeProps, "className">;

/**
 * The console's one "how fresh is this, and make it fresher" control.
 *
 * Refresh had been spelled six ways — icon-only here, text there, a manual
 * spinner/label swap on three pages, a `loading` prop on two more — and the
 * freshness pill sat beside it as a loose sixth button. This renders the pair
 * as one segmented control (layout rule 9): the badge is the left segment, the
 * button the right, and they share top and bottom edges.
 *
 * Accessible name: "Refresh", or "Refreshing…" while `loading`, on every
 * flavour — the button is disabled and `aria-busy` for the duration, and the
 * box does not change size (see `.refresh-label` in globals.css).
 */
export function RefreshControl({
  onRefresh,
  loading = false,
  disabled = false,
  freshness,
  label = "Refresh",
  busyLabel = "Refreshing…",
  size = "default",
  className,
}: {
  onRefresh: () => void;
  /** A refresh is in flight: spinner, disabled, named `busyLabel`. */
  loading?: boolean;
  /** Refreshing is not available at all (no namespace picked, an action running). */
  disabled?: boolean;
  /** Freshness to show beside the button. Given, the button is icon-only. */
  freshness?: RefreshFreshness;
  label?: string;
  busyLabel?: string;
  /** `sm` inside a card or a dialog; `default` in page chrome. */
  size?: "default" | "sm";
  className?: string;
}) {
  const name = loading ? busyLabel : label;
  const icon = <RefreshCw size={size === "sm" ? 14 : 16} aria-hidden />;
  const onClick = () => onRefresh();

  if (!freshness) {
    return (
      <Button
        type="button"
        variant="outline"
        size={size}
        className={className}
        loading={loading}
        disabled={disabled}
        onClick={onClick}
        title={name}
      >
        {icon}
        {/* The ghost holds the wider of the two strings so the row does not
            shift when the label swaps; the visible copy is what names the
            button. */}
        <span className="refresh-label">
          <span>{name}</span>
          <span className="refresh-label-ghost" aria-hidden>
            {busyLabel}
          </span>
        </span>
      </Button>
    );
  }

  return (
    <div className={cn("refresh-control", className)} data-size={size}>
      <TransportBadge {...freshness} />
      <Button
        type="button"
        variant="outline"
        // Square inner corners are a utility, not a rule: a border-radius in
        // the component layer cannot reach a shadcn primitive (layout rule 5).
        className="rounded-l-none"
        size={size === "sm" ? "icon-sm" : "icon"}
        loading={loading}
        disabled={disabled}
        onClick={onClick}
        aria-label={name}
        title={name}
      >
        {icon}
      </Button>
    </div>
  );
}
