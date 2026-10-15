import { useId } from "react";
import {
  SensitiveValueField,
  type SensitiveValueFieldProps,
} from "@/components/SensitiveValueField";
import { Checkbox } from "@/components/ui";

/**
 * A secret value input with the base64 pass-through and a deliberate "Store an
 * empty value" choice. Ticking it clears the value and disables the field and
 * its tools, so an empty secret is never stored by leaving the box blank.
 */
export function SecretValueField({
  base64,
  onBase64Change,
  storeEmpty,
  onStoreEmptyChange,
  ...props
}: Omit<
  SensitiveValueFieldProps,
  "multiline" | "additionalControls" | "onGenerate" | "controlLabel"
> & {
  base64: boolean;
  onBase64Change: (base64: boolean) => void;
  storeEmpty: boolean;
  onStoreEmptyChange: (storeEmpty: boolean) => void;
}) {
  const base64Id = useId();
  const storeEmptyId = useId();
  const valueDisabled = props.disabled || storeEmpty;
  return (
    <SensitiveValueField
      {...props}
      disabled={valueDisabled}
      placeholder={storeEmpty ? "An empty value will be stored" : props.placeholder}
      multiline
      onGenerate={() => onBase64Change(false)}
      additionalControls={
        <>
          <div className="checkbox-row">
            <Checkbox
              id={base64Id}
              checked={base64}
              disabled={valueDisabled}
              onCheckedChange={(checked) => onBase64Change(checked === true)}
            />
            <label htmlFor={base64Id}>Value is already base64</label>
          </div>
          <div className="checkbox-row">
            <Checkbox
              id={storeEmptyId}
              checked={storeEmpty}
              disabled={props.disabled}
              onCheckedChange={(checked) => {
                const next = checked === true;
                if (next) {
                  // Nothing typed may ride along with an empty write.
                  props.onChange("");
                  onBase64Change(false);
                }
                onStoreEmptyChange(next);
              }}
            />
            <label htmlFor={storeEmptyId}>Store an empty value</label>
          </div>
        </>
      }
    />
  );
}
