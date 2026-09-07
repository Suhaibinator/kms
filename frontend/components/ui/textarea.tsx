import type * as React from "react";

import { cn } from "@/lib/utils";

function Textarea({ className, rows, style, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      rows={rows}
      // `field-sizing: content` makes the browser size the box to its content
      // and ignore the `rows` attribute outright, so every textarea in the app
      // opened at the 64px min-height whatever it asked for. Translating rows
      // into a min-height floor gives the attribute its meaning back; `1lh` is
      // the element's own line box, so the floor follows the mobile type bump
      // instead of being computed from a fixed token.
      style={rows ? ({ "--textarea-rows": rows, ...style } as React.CSSProperties) : style}
      className={cn(
        "flex field-sizing-content min-h-[max(4rem,calc(var(--textarea-rows,2)*1lh+2*var(--space-2)+2px))] w-full rounded-md border border-input bg-transparent px-3 py-2 text-base transition-colors outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-(--ring-glow) read-only:bg-muted/40 read-only:text-muted-foreground disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 md:text-sm dark:bg-input/30 dark:disabled:bg-input/80 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40",
        className,
      )}
      {...props}
    />
  );
}

export { Textarea };
