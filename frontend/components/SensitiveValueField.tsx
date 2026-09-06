import { Eye, EyeOff, Sparkles } from "lucide-react";
import { type ReactNode, type Ref, useState } from "react";
import { ActionMenu } from "@/components/applications/ActionMenu";
import CopyButton from "@/components/CopyButton";
import { Input, Textarea } from "@/components/ui";
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

/** An ephemeral credential input with shared reveal, generation, and copy controls. */
export function SensitiveValueField({
  value,
  onChange,
  multiline = false,
  controlLabel = "value",
  additionalControls,
  onGenerate,
  disabled,
  required,
  onBlur,
  placeholder = "secret value…",
  id,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  "aria-required": ariaRequired,
  inputRef,
}: SensitiveValueFieldProps) {
  const [visible, setVisible] = useState(false);
  const Control = multiline ? Textarea : Input;
  return (
    <div className="value-secret">
      <Control
        type={multiline ? undefined : visible ? "text" : "password"}
        id={id}
        ref={(node: HTMLInputElement | HTMLTextAreaElement | null) => assignRef(inputRef, node)}
        className={cn("font-mono", multiline && !visible && "value-secret-masked")}
        data-masked={visible ? "false" : "true"}
        value={value}
        required={required}
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
          {`${visible ? "Hide" : "Show"} ${controlLabel}`}
        </Button>
        <ActionMenu
          align="start"
          trigger={
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled}
              aria-label={controlLabel === "value" ? undefined : `Generate ${controlLabel}`}
            >
              <Sparkles size={14} aria-hidden /> Generate…
            </Button>
          }
          items={GENERATORS.map((generator) => ({
            key: `${generator.bytes}-${generator.encoding}`,
            label: generator.label,
            disabled,
            onSelect: () => {
              if (disabled) return;
              onChange(generateSecretValue(generator.bytes, generator.encoding));
              // A generated value is only useful if the operator can read it back.
              onGenerate?.();
              setVisible(true);
            },
          }))}
        />
        {value ? (
          <CopyButton
            label={controlLabel === "value" ? "Copy" : `Copy ${controlLabel}`}
            value={() => value}
            disabled={disabled}
          />
        ) : null}
        {additionalControls}
      </div>
    </div>
  );
}

export interface SensitiveValueFieldProps {
  value: string;
  onChange: (value: string) => void;
  multiline?: boolean;
  controlLabel?: string;
  additionalControls?: ReactNode;
  onGenerate?: () => void;
  disabled?: boolean;
  required?: boolean;
  onBlur?: () => void;
  placeholder?: string;
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-required"?: boolean;
  /** The input or textarea, e.g. for a modal's `initialFocus`. */
  inputRef?: Ref<HTMLElement>;
}
