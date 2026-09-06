import { useId } from "react";
import {
  SensitiveValueField,
  type SensitiveValueFieldProps,
} from "@/components/SensitiveValueField";
import { Checkbox } from "@/components/ui";

export function SecretValueField({
  base64,
  onBase64Change,
  ...props
}: Omit<
  SensitiveValueFieldProps,
  "multiline" | "additionalControls" | "onGenerate" | "controlLabel"
> & {
  base64: boolean;
  onBase64Change: (base64: boolean) => void;
}) {
  const checkboxId = useId();
  return (
    <SensitiveValueField
      {...props}
      multiline
      onGenerate={() => onBase64Change(false)}
      additionalControls={
        <div className="checkbox-row">
          <Checkbox
            id={checkboxId}
            checked={base64}
            disabled={props.disabled}
            onCheckedChange={(checked) => onBase64Change(checked === true)}
          />
          <label htmlFor={checkboxId}>Value is already base64</label>
        </div>
      }
    />
  );
}
