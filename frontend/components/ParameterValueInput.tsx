import type { ReactNode } from "react";
import { useMemo, useRef } from "react";
import { JsonEditor } from "@/components/JsonEditor";
import { SchemaForm } from "@/components/SchemaForm";
import { Input, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { inferSchema } from "@/lib/json-text";
import { buildForm, type JsonSchema } from "@/lib/schema-form";

export interface ParameterValueInputProps {
  /** string | integer | float | boolean | json | binary */
  contentType: string;
  value: string;
  onChange: (value: string) => void;
  /** The alias's pinned sub-schema; only consulted for json values. */
  schema?: JsonSchema | null;
  /** Chips shown beside the Form/JSON toggle when a pinned schema applies. */
  schemaLabel?: ReactNode;
  /** Forwarded to the real control so a wrapping `Field` labels it. */
  id?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-required"?: boolean;
  disabled?: boolean;
  onBlur?: () => void;
  /** Cmd/Ctrl+Enter in a text editor. */
  onSubmit?: () => void;
  /** Minimum rows for the json editor and textareas. */
  rows?: number;
  placeholder?: string;
}

/**
 * Keeps a schema inferred from `value` stable across keystrokes: it only
 * changes identity when the inferred shape changes, and the last good shape
 * survives a moment of invalid JSON.
 */
function useInferredSchema(value: string, enabled: boolean): JsonSchema | null {
  const last = useRef<{ key: string; schema: JsonSchema | null }>({ key: "", schema: null });
  return useMemo(() => {
    if (!enabled) return null;
    let parsed: unknown;
    try {
      parsed = JSON.parse(value);
    } catch {
      return last.current.schema;
    }
    const next = inferSchema(parsed);
    const key = next ? JSON.stringify(next) : "";
    if (key === last.current.key) return last.current.schema;
    last.current = { key, schema: next };
    return next;
  }, [value, enabled]);
}

/** The right editor for a parameter's content type: typed inputs for scalars, a schema form or JSON editor for json. */
export function ParameterValueInput({
  contentType,
  value,
  onChange,
  schema = null,
  schemaLabel,
  id,
  "aria-label": ariaLabel,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  "aria-required": ariaRequired,
  disabled = false,
  onBlur,
  onSubmit,
  rows,
  placeholder,
}: ParameterValueInputProps) {
  const pinned = contentType === "json" && schema !== null && buildForm(schema) !== null;
  const inferred = useInferredSchema(value, contentType === "json" && !pinned);
  const aria = {
    id,
    "aria-label": ariaLabel,
    "aria-describedby": ariaDescribedBy,
    "aria-invalid": ariaInvalid,
    "aria-required": ariaRequired,
  };
  switch (contentType) {
    case "boolean":
      return (
        <AppSelect
          {...aria}
          className="font-mono"
          value={value.trim()}
          disabled={disabled}
          placeholder="Choose…"
          options={[
            { value: "true", label: "true" },
            { value: "false", label: "false" },
          ]}
          onValueChange={onChange}
          onBlur={onBlur}
        />
      );
    case "integer":
    case "float":
      return (
        <Input
          {...aria}
          className="font-mono"
          inputMode={contentType === "integer" ? "numeric" : "decimal"}
          value={value}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          placeholder={placeholder}
          onChange={(event) => onChange(event.target.value)}
          onBlur={onBlur}
        />
      );
    case "json": {
      const effective = pinned ? schema : inferred;
      if (effective && buildForm(effective)) {
        return (
          <SchemaForm
            // A pinned schema arriving after an inferred one restarts the
            // editor so it opens on its fields.
            key={pinned ? "pinned" : "inferred"}
            {...aria}
            schema={effective}
            captionSource={pinned ? "pinned" : "inferred"}
            schemaLabel={pinned ? schemaLabel : undefined}
            jsonLabel={ariaLabel}
            value={value}
            disabled={disabled}
            rows={rows}
            onChange={onChange}
            onBlur={onBlur}
            onSubmit={onSubmit}
          />
        );
      }
      return (
        <JsonEditor
          {...aria}
          value={value}
          disabled={disabled}
          rows={rows}
          placeholder={placeholder}
          onChange={onChange}
          onBlur={onBlur}
          onSubmit={onSubmit}
        />
      );
    }
    default:
      return (
        <Textarea
          {...aria}
          className="font-mono"
          rows={rows ?? (contentType === "binary" ? 6 : 3)}
          value={value}
          disabled={disabled}
          spellCheck={false}
          placeholder={placeholder}
          onChange={(event) => onChange(event.target.value)}
          onBlur={onBlur}
        />
      );
  }
}
