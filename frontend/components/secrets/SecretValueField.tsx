import { Eye, EyeOff, Sparkles } from "lucide-react";
import { type Ref, useId, useState } from "react";
import { ActionMenu } from "@/components/applications/ActionMenu";
import CopyButton from "@/components/CopyButton";
import { Checkbox, Textarea } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { type GeneratedEncoding, generateSecretValue } from "@/lib/encoding";
import { assignRef } from "@/lib/forms";
import { cn } from "@/lib/utils";

const GENERATORS: Array<{ bytes: 32 | 64; encoding: GeneratedEncoding; label: string }> = [
  { bytes: 32, encoding: "base64url", label: "32 bytes, base64url" },
  { bytes: 64, encoding: "base64url", label: "64 bytes, base64url" },
  { bytes: 32, encoding: "hex", label: "32 bytes, hex" },
  { bytes: 64, encoding: "hex", label: "64 bytes, hex" },
];

/**
 * The plaintext of a secret: masked by default, with a reveal toggle, a
 * generator for fresh random tokens, and a passthrough for values that are
 * already base64 (DER keys, PKCS#12 bundles) so they are stored byte for byte.
 */
export function SecretValueField({
  value,
  onChange,
  base64,
  onBase64Change,
  disabled,
  onBlur,
  placeholder = "secret value…",
  id,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  "aria-required": ariaRequired,
  inputRef,
}: {
  value: string;
  onChange: (value: string) => void;
  /** The value is already base64 and must be sent as-is. */
  base64: boolean;
  onBase64Change: (base64: boolean) => void;
  disabled?: boolean;
  onBlur?: () => void;
  placeholder?: string;
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-required"?: boolean;
  /** The textarea, e.g. for a modal's `initialFocus`. */
  inputRef?: Ref<HTMLElement>;
}) {
  const [visible, setVisible] = useState(false);
  const checkboxId = useId();
  return (
    <div className="value-secret">
      <Textarea
        id={id}
        ref={(node) => assignRef(inputRef, node)}
        className={cn("font-mono", !visible && "value-secret-masked")}
        data-masked={visible ? "false" : "true"}
        value={value}
        disabled={disabled}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        aria-describedby={ariaDescribedBy}
        aria-invalid={ariaInvalid}
        aria-required={ariaRequired}
        onChange={(event) => onChange(event.target.value)}
        onBlur={onBlur}
      />
      <div className="value-secret-tools">
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-pressed={visible}
          disabled={disabled}
          onClick={() => setVisible((current) => !current)}
        >
          {visible ? <EyeOff size={14} aria-hidden /> : <Eye size={14} aria-hidden />}
          {visible ? "Hide value" : "Show value"}
        </Button>
        <ActionMenu
          label="Generate a random value"
          align="start"
          trigger={
            <Button type="button" variant="outline" size="sm" disabled={disabled}>
              <Sparkles size={14} aria-hidden /> Generate…
            </Button>
          }
          items={GENERATORS.map((generator) => ({
            key: `${generator.bytes}-${generator.encoding}`,
            label: generator.label,
            onSelect: () => {
              onChange(generateSecretValue(generator.bytes, generator.encoding));
              // A generated value is only useful if the operator can read it back.
              onBase64Change(false);
              setVisible(true);
            },
          }))}
        />
        {value ? <CopyButton label="Copy" value={() => value} /> : null}
        <div className="checkbox-row">
          <Checkbox
            id={checkboxId}
            checked={base64}
            disabled={disabled}
            onCheckedChange={(checked) => onBase64Change(checked === true)}
          />
          <label htmlFor={checkboxId}>Value is already base64</label>
        </div>
      </div>
    </div>
  );
}
