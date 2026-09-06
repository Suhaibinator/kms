import { Plus } from "lucide-react";
import type { ComponentProps } from "react";
import { Button } from "@/components/ui/button";
import type { ReleaseEntryKind } from "@/lib/types";

/**
 * "+ Add secret" / "+ Add value": the one affordance for a contract alias
 * (or matrix cell) with nothing behind it. Outline in row lists, ghost inside
 * table cells; callers pass `variant` accordingly.
 */
export function AddResourceButton({
  kind,
  variant = "outline",
  size = "sm",
  children,
  ...props
}: { kind: ReleaseEntryKind } & Omit<ComponentProps<typeof Button>, "type">) {
  return (
    <Button type="button" variant={variant} size={size} {...props}>
      <Plus size={13} />
      {children ?? (kind === "secret" ? "Add secret" : "Add value")}
    </Button>
  );
}
