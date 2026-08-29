import { ChevronsDownUp, ChevronsUpDown, Upload } from "lucide-react";
import type { ReactNode, Ref } from "react";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { JsonEditor } from "@/components/JsonEditor";
import { SchemaForm } from "@/components/SchemaForm";
import { Input, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { base64ByteLength } from "@/lib/encoding";
import { assignRef } from "@/lib/forms";
import { inferSchema } from "@/lib/json-text";
import { buildForm, type JsonSchema } from "@/lib/schema-form";
import { PARAMETER_CONTENT_TYPES } from "@/lib/types";
import { formatBytes, validateParameterValue } from "@/lib/validation";

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
  /** Whichever control renders for the type, e.g. for a modal's `initialFocus`. */
  inputRef?: Ref<HTMLElement>;
  disabled?: boolean;
  onBlur?: () => void;
  /** Cmd/Ctrl+Enter in a text editor. */
  onSubmit?: () => void;
  /** Minimum rows for the json editor and the binary textarea. */
  rows?: number;
  placeholder?: string;
}

/** A sample value per content type, shown while the control is empty. */
export const VALUE_PLACEHOLDERS: Record<string, string | undefined> = {
  string: "value",
  integer: "100",
  float: "1.5",
  boolean: undefined,
  json: "{}",
  binary: "SGVsbG8sIHdvcmxkIQ==",
};

// Go's strconv.ParseBool spellings, grouped by what they mean.
const TRUE_LITERALS = new Set(["1", "t", "T", "TRUE", "true", "True"]);
const FALSE_LITERALS = new Set(["0", "f", "F", "FALSE", "false", "False"]);

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

/** Reads a picked file as standard base64 (what the binary content type stores). */
async function readFileAsBase64(file: File): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 1) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
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
  inputRef,
  disabled = false,
  onBlur,
  onSubmit,
  rows,
  placeholder,
}: ParameterValueInputProps) {
  const pinned = contentType === "json" && schema !== null && buildForm(schema) !== null;
  const inferred = useInferredSchema(value, contentType === "json" && !pinned);
  // A one-line string opens in a single-line input; the operator can widen it,
  // and a value that already holds a line break has no single-line form.
  const [multiline, setMultiline] = useState(false);
  const fileInputId = useId();
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  // Native radios share a name so the arrow keys move between them.
  const radioName = useId();
  // A `<label for>` cannot name a group, so the boolean switch names itself
  // after the label the wrapping Field points at it.
  const [labelledBy, setLabelledBy] = useState<string | undefined>(undefined);
  useEffect(() => {
    if (!id || contentType !== "boolean") return;
    const selector = `label[for="${id.replace(/["\\]/g, "\\$&")}"]`;
    setLabelledBy(document.querySelector<HTMLLabelElement>(selector)?.id || undefined);
  }, [id, contentType]);
  const aria = {
    id,
    "aria-label": ariaLabel,
    "aria-describedby": ariaDescribedBy,
    "aria-invalid": ariaInvalid,
    "aria-required": ariaRequired,
  };
  const hint = placeholder ?? VALUE_PLACEHOLDERS[contentType];
  switch (contentType) {
    case "boolean": {
      const literal = value.trim();
      // Only the spelled-out forms select a side; a stored `1`/`t`/`F` is
      // shown beside the switch, and picking a side rewrites it canonically.
      const isTrue = literal === "true";
      const isFalse = literal === "false";
      const meaning = TRUE_LITERALS.has(literal)
        ? "true"
        : FALSE_LITERALS.has(literal)
          ? "false"
          : null;
      const choose = (next: "true" | "false") => {
        onChange(next);
      };
      return (
        <div className="value-boolean">
          <div
            id={id}
            role="radiogroup"
            className="value-segment"
            aria-label={ariaLabel}
            aria-labelledby={ariaLabel ? undefined : labelledBy}
            aria-describedby={ariaDescribedBy}
            aria-invalid={ariaInvalid}
            aria-required={ariaRequired}
            data-disabled={disabled ? "true" : undefined}
            onBlur={(event) => {
              // Leaving the whole control, not just moving between its halves.
              if (!event.currentTarget.contains(event.relatedTarget as Node | null)) onBlur?.();
            }}
          >
            <label className="value-segment-button">
              <input
                type="radio"
                ref={(node) => assignRef(inputRef, node)}
                className="sr-only"
                name={radioName}
                value="true"
                checked={isTrue}
                disabled={disabled}
                onChange={() => choose("true")}
              />
              true
            </label>
            <label className="value-segment-button">
              <input
                type="radio"
                className="sr-only"
                name={radioName}
                value="false"
                checked={isFalse}
                disabled={disabled}
                onChange={() => choose("false")}
              />
              false
            </label>
          </div>
          {meaning !== null && !isTrue && !isFalse ? (
            <span className="value-literal faint text-xs" data-testid="value-literal">
              Stored as <code className="mono">{literal}</code> ({meaning}). Pick a side to save it
              as <code className="mono">true</code> or <code className="mono">false</code>.
            </span>
          ) : null}
        </div>
      );
    }
    case "integer":
    case "float":
      return (
        <Input
          {...aria}
          ref={(node) => assignRef(inputRef, node)}
          className="font-mono"
          inputMode={contentType === "integer" ? "numeric" : "decimal"}
          value={value}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          placeholder={hint}
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
            inputRef={inputRef}
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
          inputRef={inputRef}
          value={value}
          disabled={disabled}
          rows={rows}
          placeholder={hint}
          onChange={onChange}
          onBlur={onBlur}
          onSubmit={onSubmit}
        />
      );
    }
    case "binary": {
      const compact = value.replace(/[\r\n]/g, "");
      const decoded = compact === "" ? 0 : base64ByteLength(compact);
      const valid = compact !== "" && validateParameterValue(value, "binary") === null;
      const pickFile = async (file: File | undefined) => {
        if (!file) return;
        try {
          onChange(await readFileAsBase64(file));
        } catch {
          // The browser refused the read; the textarea still accepts a paste.
        }
      };
      return (
        <div className="value-binary">
          <Textarea
            {...aria}
            ref={(node) => assignRef(inputRef, node)}
            className="font-mono"
            rows={rows ?? 6}
            value={value}
            disabled={disabled}
            spellCheck={false}
            placeholder={hint}
            onChange={(event) => onChange(event.target.value)}
            onBlur={onBlur}
          />
          <div className="value-binary-tools">
            <span className="faint text-xs" role="status" data-testid="value-binary-size">
              {compact === ""
                ? "Standard base64; line breaks are ignored."
                : valid
                  ? `Decodes to ${formatBytes(decoded)}.`
                  : "Not valid base64 yet."}
            </span>
            <input
              ref={fileInputRef}
              id={fileInputId}
              type="file"
              className="sr-only"
              tabIndex={-1}
              aria-hidden="true"
              disabled={disabled}
              onChange={(event) => {
                void pickFile(event.target.files?.[0]);
                // Allow picking the same file twice in a row.
                event.target.value = "";
              }}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled}
              onClick={() => fileInputRef.current?.click()}
            >
              <Upload size={14} aria-hidden /> Upload file…
            </Button>
          </div>
        </div>
      );
    }
    default: {
      const needsTextarea = multiline || value.includes("\n");
      const toggle = (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="value-input-toggle"
          aria-label={needsTextarea ? "Edit on one line" : "Edit on several lines"}
          aria-pressed={needsTextarea}
          disabled={disabled || value.includes("\n")}
          onClick={() => setMultiline((current) => !current)}
        >
          {needsTextarea ? (
            <ChevronsDownUp size={14} aria-hidden />
          ) : (
            <ChevronsUpDown size={14} aria-hidden />
          )}
        </Button>
      );
      if (needsTextarea) {
        return (
          <div className="value-input-row" data-multiline="true">
            <Textarea
              {...aria}
              ref={(node) => assignRef(inputRef, node)}
              className="font-mono"
              rows={4}
              value={value}
              disabled={disabled}
              spellCheck={false}
              placeholder={hint}
              onChange={(event) => onChange(event.target.value)}
              onBlur={onBlur}
            />
            {toggle}
          </div>
        );
      }
      return (
        <div className="value-input-row">
          <Input
            {...aria}
            ref={(node) => assignRef(inputRef, node)}
            className="font-mono"
            value={value}
            disabled={disabled}
            autoComplete="off"
            spellCheck={false}
            placeholder={hint}
            onChange={(event) => onChange(event.target.value)}
            onBlur={onBlur}
          />
          {toggle}
        </div>
      );
    }
  }
}

/**
 * The parameter content-type select. When the chosen type rejects the value
 * already in the editor it offers to clear it, so the operator is never left
 * with `{"a":1}` inside a numeric input and no way out but Cancel.
 */
export function ContentTypeSelect({
  value,
  onValueChange,
  currentValue,
  onClearValue,
  disabled,
  id,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  ref,
}: {
  value: string;
  onValueChange: (contentType: string) => void;
  /** The value in the editor, checked against a newly chosen type. */
  currentValue: string;
  onClearValue: () => void;
  disabled?: boolean;
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  ref?: Ref<HTMLButtonElement>;
}) {
  // Remembered per type change, not derived: the offer means "this type broke
  // the value you already had", not "the value is invalid".
  const [offerFor, setOfferFor] = useState<string | null>(null);
  const mismatch =
    offerFor === value &&
    currentValue.trim() !== "" &&
    validateParameterValue(currentValue, value) !== null;
  return (
    <div className="value-type">
      <AppSelect
        ref={ref}
        id={id}
        value={value}
        disabled={disabled}
        aria-describedby={ariaDescribedBy}
        aria-invalid={ariaInvalid}
        onValueChange={(next) => {
          setOfferFor(
            currentValue.trim() !== "" &&
              validateParameterValue(currentValue, value) === null &&
              validateParameterValue(currentValue, next) !== null
              ? next
              : null,
          );
          onValueChange(next);
        }}
        options={PARAMETER_CONTENT_TYPES.map((contentTypeOption) => ({
          value: contentTypeOption,
          label: contentTypeOption,
        }))}
      />
      {mismatch ? (
        <p className="value-type-offer text-xs" role="status">
          The current value is not valid <span className="mono">{value}</span>.{" "}
          <Button
            type="button"
            variant="link"
            size="xs"
            className="value-type-clear"
            onClick={() => {
              setOfferFor(null);
              onClearValue();
            }}
          >
            Clear value
          </Button>
        </p>
      ) : null}
    </div>
  );
}
