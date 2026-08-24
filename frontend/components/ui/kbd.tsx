import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

/** A keyboard shortcut hint, e.g. `<Kbd>⌘K</Kbd>`. */
export function Kbd({ className, ...props }: ComponentProps<"kbd">) {
  return <kbd data-slot="kbd" className={cn("kbd", className)} {...props} />;
}
