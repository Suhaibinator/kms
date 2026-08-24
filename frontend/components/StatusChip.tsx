import { Badge } from "@/components/ui";
import { STATUS_LABEL, STATUS_TONE } from "@/lib/readiness";
import type { AppStatus, EnvStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

export interface StatusChipProps {
  status: AppStatus | EnvStatus;
  /** Marks a production environment (border + label suffix). */
  production?: boolean;
  /** `dot` is the fleet-card form: a coloured dot with the label as its name. */
  size?: "md" | "dot";
  className?: string;
}

/** Readiness status as a Badge (`md`) or a status dot (`dot`). */
export function StatusChip({ status, production, size = "md", className }: StatusChipProps) {
  const label = STATUS_LABEL[status];
  const name = production ? `${label} (production)` : label;
  if (size === "dot") {
    return (
      <span
        role="img"
        aria-label={name}
        title={name}
        className={cn("status-dot", `status-${status}`, production && "status-prod", className)}
      />
    );
  }
  return (
    <Badge
      kind={STATUS_TONE[status]}
      className={cn("status-chip", `status-${status}`, production && "status-prod", className)}
      title={production ? name : undefined}
    >
      {label}
    </Badge>
  );
}
