import { cloneElement, isValidElement, type ReactElement, type ReactNode, useId } from "react";
import { cn } from "@/lib/utils";

/**
 * A labelled control on one row: caption left, control right, the whole thing
 * one control tall.
 *
 * `<Field>` stacks its label above the control, which is right in a form and
 * wrong in page chrome — a stacked label inside a flex row adds a second line
 * and knocks everything beside it a label-block out of alignment. This is the
 * row flavour, for the selects that say what a page is showing (layout rule 9:
 * they live in a ContextBar, not in the header's action row).
 *
 * The caption is a real `<label for>` beside the control rather than a label
 * wrapped around it: Base UI's Select renders a hidden input next to its
 * trigger, and a wrapping label labels both, which makes `getByLabelText`
 * ambiguous and gives the row two things to activate. Pass `as="span"` for a
 * placeholder whose "control" cannot be labelled.
 */
export function InlineField({
  label,
  htmlFor,
  as = "label",
  className,
  children,
}: {
  label: ReactNode;
  /** The control's id. Defaults to the child's own `id`, else a generated one. */
  htmlFor?: string;
  /** `span` for a decorative row (a skeleton) with nothing to label. */
  as?: "label" | "span";
  className?: string;
  children: ReactNode;
}) {
  const generated = useId();

  if (as === "span") {
    return (
      <span className={cn("inline-field", className)}>
        <span className="inline-field-label">{label}</span>
        {children}
      </span>
    );
  }

  const child = isValidElement(children) ? (children as ReactElement<{ id?: string }>) : null;
  const controlId = htmlFor ?? child?.props.id ?? `${generated}-control`;
  // Only clone when the child has no id of its own: the caption's `for` has to
  // reach it, and a caller that already ids its control keeps that id.
  const control = child && !child.props.id ? cloneElement(child, { id: controlId }) : children;

  return (
    <span className={cn("inline-field", className)}>
      <label className="inline-field-label" htmlFor={controlId}>
        {label}
      </label>
      {control}
    </span>
  );
}
