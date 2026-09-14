import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * Row 3 of a page's chrome (layout rule 9): one section's heading with its own
 * toolbar, on a single centred row.
 *
 * This replaces a dozen hand-written `.between mb-2` rows that each solved the
 * same problem slightly differently — `items-end` here so a stacked field label
 * lined up, `items-start` there so buttons did not float below a two-line
 * description, `grow basis-80` on some leading children and not others.
 *
 * `as="none"` puts something that is not a heading in the title slot: a
 * `TabsList`, or a caption row of badges. The toolbar's search boxes hide their
 * labels (`SearchField labelHidden`), so the input is the row's only control
 * and shares a centreline with the heading.
 */
export function SectionHeader({
  title,
  description,
  actions,
  as = "h2",
  className,
  "aria-hidden": ariaHidden,
}: {
  title: ReactNode;
  /** A line under the heading. Its presence top-aligns the row, since the
   *  toolbar belongs to the heading rather than to the middle of two lines. */
  description?: ReactNode;
  actions?: ReactNode;
  as?: "h2" | "h3" | "none";
  className?: string;
  /** For a skeleton's inert copy of a real toolbar: none of it is operable. */
  "aria-hidden"?: boolean;
}) {
  const Heading = as === "h3" ? "h3" : "h2";
  return (
    <div className={cn("section-head", className)} aria-hidden={ariaHidden}>
      <div className="section-head-text">
        {as === "none" ? title : <Heading className="section-title">{title}</Heading>}
        {description ? <div className="section-head-description">{description}</div> : null}
      </div>
      {actions ? <div className="section-head-tools">{actions}</div> : null}
    </div>
  );
}
