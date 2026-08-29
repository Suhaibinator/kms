import { type Ref, useState } from "react";
import { Input } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { SECRET_CONTENT_TYPES } from "@/lib/encoding";

const OTHER = "\0other";

/**
 * A secret's content type: a short MIME list with a free-text escape, so the
 * common cases are one pick and a typo cannot persist silently.
 */
export function SecretContentTypeSelect({
  value,
  onValueChange,
  disabled,
  id,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  ref,
}: {
  value: string;
  onValueChange: (contentType: string) => void;
  disabled?: boolean;
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  ref?: Ref<HTMLButtonElement>;
}) {
  const known = (SECRET_CONTENT_TYPES as readonly string[]).includes(value);
  const [custom, setCustom] = useState(!known && value !== "");
  const showCustom = custom || (!known && value !== "");
  return (
    <div className="value-type">
      <AppSelect
        ref={ref}
        id={id}
        value={showCustom ? OTHER : value}
        disabled={disabled}
        aria-describedby={ariaDescribedBy}
        aria-invalid={ariaInvalid}
        onValueChange={(next) => {
          if (next === OTHER) {
            setCustom(true);
            if (known) onValueChange("");
            return;
          }
          setCustom(false);
          onValueChange(next);
        }}
        options={[
          ...SECRET_CONTENT_TYPES.map((type) => ({ value: type, label: type })),
          { value: OTHER, label: "Other…" },
        ]}
      />
      {showCustom ? (
        <Input
          className="font-mono"
          aria-label="Custom content type"
          value={value}
          disabled={disabled}
          placeholder="application/x-custom"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => onValueChange(event.target.value)}
        />
      ) : null}
    </div>
  );
}
