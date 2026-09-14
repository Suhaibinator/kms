import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * Row 2 of a page's chrome (layout rule 9): what the page is currently
 * showing, and the links that change it.
 *
 * A select that switches the environment or the schema track is not an
 * action — it does not do anything to the system — so it does not belong in
 * the header's button row, where it sat between the status pill and Ship and
 * made both harder to find. Children are `InlineField`s and navigation
 * `ButtonLink`s, all one control tall.
 */
export function ContextBar({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("context-bar", className)}>{children}</div>;
}
