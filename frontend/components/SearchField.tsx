import { Search } from "lucide-react";
import { type ReactNode, useId, useRef } from "react";
import { Field, Input } from "@/components/ui";
import { Kbd } from "@/components/ui/kbd";
import { useSearchShortcut } from "@/lib/shortcuts";

/**
 * The console's one search box: the parameter and secret lists, and the
 * application page's value filter. `/` focuses it from anywhere on the page
 * (lib/shortcuts.ts) and Esc clears it while it has focus.
 */
export function SearchField({
  value,
  onChange,
  onClear,
  label,
  placeholder,
  disabled = false,
  hint,
  labelHidden = false,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  /** Called by Esc and by the input's own clear button. */
  onClear: () => void;
  label: string;
  placeholder?: string;
  disabled?: boolean;
  /** A note under the box, e.g. how much of the namespace was searched. */
  hint?: ReactNode;
  /**
   * Renders the label `sr-only`. For a section toolbar, where the box is the
   * only control on the row and a floating caption above it is what stopped
   * the heading and the input sharing a centreline. `getByLabelText` still
   * resolves — the label is hidden, not removed.
   */
  labelHidden?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLInputElement | null>(null);
  const id = useId();
  useSearchShortcut(ref);
  return (
    <Field label={label} labelHidden={labelHidden} hint={hint} htmlFor={id} className={className}>
      <div className="search-field">
        <Search size={15} className="search-field-icon" aria-hidden />
        <Input
          ref={ref}
          id={id}
          type="search"
          // The two insets are utilities, not a .search-field-input rule: the
          // Input primitive's own px-3 is a utility and beats a component-layer
          // rule (layout rule 5), so the box kept 12px of left padding and the
          // 15px icon at var(--space-2) drew over the first characters typed.
          // Each inset clears its overlay: the icon on the left, the `/` hint
          // on the right, both offset var(--space-2) from their edge.
          className="search-field-input pl-[calc(var(--space-2)*2_+_15px)] pr-[calc(var(--space-2)*2_+_12px)] font-mono"
          placeholder={placeholder}
          value={value}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={(event) => {
            // Stop here rather than bubbling: Esc in a search box empties it,
            // it does not close whatever dialog the page has open behind it.
            if (event.key === "Escape" && value !== "") {
              event.preventDefault();
              event.stopPropagation();
              onClear();
            }
          }}
        />
        {/* Decorative: the shortcut is in the `?` sheet, and a screen reader
            reading "slash" after the field name would only be noise. */}
        <Kbd className="search-field-kbd" aria-hidden>
          /
        </Kbd>
      </div>
    </Field>
  );
}
