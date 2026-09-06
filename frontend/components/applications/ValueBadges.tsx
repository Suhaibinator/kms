import { Badge } from "@/components/ui";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { OverviewValue } from "@/lib/types";

/** True when a present value is newer than (or absent from) the active release's pins. */
export function isUnreleased(value: OverviewValue, hasActiveRelease: boolean): boolean {
  if (!value.present || !hasActiveRelease) return false;
  if (value.pinned_version === undefined) return true;
  return (value.current_version ?? 0) > value.pinned_version;
}

/**
 * "v{n} unreleased" with the active pin in the tooltip. Shared by the pipeline
 * row and the matrix cell so drift reads identically in both tabs.
 */
export function UnreleasedBadge({
  value,
  hasActiveRelease,
}: {
  value: OverviewValue;
  hasActiveRelease: boolean;
}) {
  if (!isUnreleased(value, hasActiveRelease)) return null;
  return (
    <Tooltip>
      <TooltipTrigger render={<span className="badge-tip" />}>
        <Badge kind="warning">v{value.current_version ?? 0} unreleased</Badge>
      </TooltipTrigger>
      <TooltipContent>
        {value.pinned_version === undefined
          ? "Not in the active release; clients do not receive it."
          : `The active release pins v${value.pinned_version}.`}
      </TooltipContent>
    </Tooltip>
  );
}
