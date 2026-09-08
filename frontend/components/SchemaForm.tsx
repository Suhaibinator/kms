import { ChevronDown, Plus, Trash2 } from "lucide-react";
import {
  type ReactNode,
  type Ref,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { JsonEditor } from "@/components/JsonEditor";
import { Button, Checkbox, Field, Input, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { assignRef } from "@/lib/forms";
import { checkJson, tokenizeJson } from "@/lib/json-text";
import {
  arrayAllowsEmpty,
  arrayStateLabel,
  buildForm,
  describeConstraints,
  extraKeys,
  type FormField,
  formatIssuePath,
  getAt,
  initialValue,
  isJsonObject,
  itemAt,
  type JsonObject,
  type JsonSchema,
  parseNumberDraft,
  pathKey,
  schemaNeedsExactJson,
  setAt,
  validateValue,
} from "@/lib/schema-form";
import { cn } from "@/lib/utils";

export interface SchemaFormProps {
  /** The alias's sub-schema (`schema.properties[alias]`). */
  schema: JsonSchema;
  /** The value as JSON text — the single source of truth shared with the JSON editor. */
  value: string;
  onChange: (text: string) => void;
  /** False while any visible or retained local draft cannot be committed. */
  onValidityChange?: (valid: boolean) => void;
  disabled?: boolean;
  /** Accessible name for the raw JSON editor and, in form mode, the field group. */
  jsonLabel?: string;
  rows?: number;
  onBlur?: () => void;
  /** Cmd/Ctrl+Enter in the JSON editor. */
  onSubmit?: () => void;
  className?: string;
  /** Forwarded to the JSON editor's textarea so a wrapping `Field` labels the real control. */
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-required"?: boolean;
  /** The JSON textarea, or the first field's control in form mode — for a modal's `initialFocus`. */
  inputRef?: Ref<HTMLElement>;
  /** Where the schema came from; changes the caption copy. */
  captionSource?: "pinned" | "inferred";
  /** Shown beside the mode toggle, e.g. schema and alias chips. */
  schemaLabel?: ReactNode;
  /** Changes when the parent intentionally replaces the draft, e.g. restore or source pin. */
  resetKey?: string;
  preferForm?: boolean;
  /** @deprecated Exact numeric text is always protected. */
  preserveExactNumbers?: boolean;
}

type Mode = "form" | "json";

/** Where the operator's last Form/JSON choice is kept (same pattern as the ship modal's mode). */
export const VALUE_EDITOR_MODE_STORAGE_KEY = "kms-value-editor-mode";

export function readStoredEditorMode(): Mode | null {
  try {
    const raw = window.localStorage.getItem(VALUE_EDITOR_MODE_STORAGE_KEY);
    return raw === "form" || raw === "json" ? raw : null;
  } catch {
    return null;
  }
}

export function storeEditorMode(mode: Mode): void {
  try {
    window.localStorage.setItem(VALUE_EDITOR_MODE_STORAGE_KEY, mode);
  } catch {
    /* storage unavailable; the toggle still works for this open */
  }
}

/** New rows are drafts, so opening or cancelling one never changes the JSON. */
function NewArrayItem({
  field,
  text,
  disabled,
  onChange,
  onAdd,
  onCancel,
}: {
  field: FormField;
  text: string;
  disabled: boolean;
  onChange: (text: string) => void;
  onAdd: (item: unknown) => void;
  onCancel: () => void;
}) {
  const [objectValid, setObjectValid] = useState(false);
  const schema = isJsonObject(field.schema.items) ? field.schema.items : {};
  const label = `New ${field.name} item`;
  const controlId = useId();
  const input = useRef<HTMLFieldSetElement>(null);
  useEffect(() => {
    input.current
      ?.querySelector<HTMLElement>('input, textarea, [role="combobox"], [role="checkbox"]')
      ?.focus();
  }, []);
  const parsed =
    field.item === "object"
      ? parseFormText(text)
      : field.item === "number"
        ? (() => {
            const result = parseFormNumber(text, Boolean(field.integer));
            return result.error || result.value === undefined
              ? { ok: false as const, error: result.error ?? "Enter a number." }
              : { ok: true as const, data: result.value };
          })()
        : { ok: true as const, data: field.item === "boolean" ? text === "true" : text };
  const issues = parsed.ok ? validateValue(schema, parsed.data) : [];
  const blank = field.item === "string" && text === "";
  const canAdd =
    parsed.ok &&
    parsed.data !== undefined &&
    !issues.length &&
    !blank &&
    (field.item !== "object" || objectValid);
  const emptyAllowed = field.item === "string" && validateValue(schema, "").length === 0;
  const unset = enumUnsetValue(field.enumValues ?? []);
  return (
    <fieldset
      ref={input}
      className="grid gap-2 rounded-md border border-input p-3"
      aria-label={label}
    >
      <span className="text-sm font-medium">New item · Not added yet</span>
      {field.item === "object" ? (
        <SchemaForm
          schema={schema}
          value={text}
          onChange={onChange}
          disabled={disabled}
          preferForm
          jsonLabel={label}
          onValidityChange={setObjectValid}
        />
      ) : field.item === "boolean" ? (
        <div className="checkbox-row">
          <Checkbox
            id={controlId}
            aria-label={label}
            checked={text === "true"}
            disabled={disabled}
            onCheckedChange={(checked) => onChange(String(checked === true))}
          />
          <label htmlFor={controlId} className="block">
            {text === "true" ? "True" : "False"}
          </label>
        </div>
      ) : field.enumValues ? (
        <AppSelect
          aria-label={label}
          disabled={disabled}
          value={text === "" ? unset : text}
          options={[
            { value: unset, label: "Choose a value" },
            ...field.enumValues
              .filter((value) => value !== "")
              .map((value) => ({ value: String(value), label: String(value) })),
          ]}
          onValueChange={(next) => onChange(next === unset ? "" : next)}
        />
      ) : (
        <Input
          aria-label={label}
          value={text}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          inputMode={field.item === "number" ? (field.integer ? "numeric" : "decimal") : undefined}
          onChange={(event) => onChange(event.target.value)}
        />
      )}
      {text !== "" && (issues.length || !parsed.ok) ? (
        <span className="field-error" role="alert">
          {!parsed.ok
            ? parsed.error
            : issues.map((issue) => `${formatIssuePath(issue.path)} ${issue.message}`).join("; ")}
        </span>
      ) : null}
      <div className="row-wrap">
        <Button
          type="button"
          size="sm"
          disabled={disabled || !canAdd}
          aria-label={`Add new ${field.name} item`}
          onClick={() => {
            if (parsed.ok && canAdd) onAdd(parsed.data);
          }}
        >
          Add item
        </Button>
        {blank && emptyAllowed ? (
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => onAdd("")}
          >
            Add empty string
          </Button>
        ) : null}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={disabled}
          aria-label={`Cancel new ${field.name} item`}
          onClick={onCancel}
        >
          Cancel
        </Button>
      </div>
    </fieldset>
  );
}

function serialize(value: unknown): string {
  return value === undefined ? "" : JSON.stringify(value, null, 2);
}

function parseText(text: string): { ok: true; data: unknown } | { ok: false; error: string } {
  if (text.trim() === "") return { ok: true, data: undefined };
  const problem = checkJson(text);
  if (problem) {
    return {
      ok: false,
      error: `must be valid JSON (line ${problem.line}, col ${problem.column}: ${problem.message})`,
    };
  }
  try {
    return { ok: true, data: JSON.parse(text) };
  } catch {
    return { ok: false, error: "must be valid JSON" };
  }
}

function needsExactJson(text: string): boolean {
  return tokenizeJson(text).some(
    (token) =>
      token.kind === "number" &&
      JSON.stringify(Number(text.slice(token.start, token.end))) !==
        text.slice(token.start, token.end),
  );
}

function parseFormText(text: string): ReturnType<typeof parseText> {
  const parsed = parseText(text);
  return parsed.ok && needsExactJson(text)
    ? {
        ok: false,
        error: "Use the JSON editor for the whole value to preserve exact numeric text.",
      }
    : parsed;
}

// Compare decimal values without rounding; harmless spellings such as 1.0 and
// 1e2 remain usable, while digits lost by Number are never silently committed.
function decimalKey(text: string): string {
  const [mantissa, exponent = "0"] = text.toLowerCase().replace(/^\+/, "").split("e");
  const fraction = mantissa.split(".")[1]?.length ?? 0;
  const digits = mantissa.replace(".", "").replace(/^(-?)0+/, "$1");
  const trimmed = digits.replace(/0+$/, "");
  if (trimmed === "" || trimmed === "-") return "0";
  return `${trimmed}e${BigInt(exponent) - BigInt(fraction) + BigInt(digits.length - trimmed.length)}`;
}

function parseFormNumber(text: string, integer: boolean): ReturnType<typeof parseNumberDraft> {
  const result = parseNumberDraft(text, integer);
  if (
    !result.error &&
    result.value !== undefined &&
    decimalKey(text.trim()) !== decimalKey(String(result.value))
  ) {
    return { value: undefined, error: "Use JSON to preserve this number's exact precision." };
  }
  return result;
}

function fieldLabel(field: FormField): string {
  return field.name;
}

function sameJson(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

function enumUnsetValue(values: Array<string | number>): string {
  let value = "__kms_unset__";
  while (values.some((option) => String(option) === value)) value += "_";
  return value;
}

/**
 * Renders one alias's value as typed inputs derived from its schema, with a
 * JSON editor as the escape hatch. Both views edit the same JSON text, so the
 * parent keeps validating and submitting exactly what it did before.
 */
export function SchemaForm({
  schema,
  value,
  onChange,
  onValidityChange,
  disabled = false,
  jsonLabel = "Value",
  rows = 7,
  onBlur,
  onSubmit,
  className,
  id,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
  "aria-required": ariaRequired,
  captionSource = "pinned",
  schemaLabel,
  preferForm,
  resetKey,
  inputRef,
}: SchemaFormProps) {
  const baseId = useId();
  // Form mode has no single control; the first field's input stands in for it.
  const firstControlRef = useCallback(
    (node: HTMLElement | null) => {
      const first = node
        ? Array.from(
            node.querySelectorAll<HTMLElement>(
              'input:not([type="hidden"]), textarea, [role="combobox"], [role="checkbox"], button',
            ),
          ).find((candidate) => !candidate.closest(".schema-form-toolbar"))
        : undefined;
      assignRef(inputRef, first ?? null);
    },
    [inputRef],
  );
  const root = useMemo(() => buildForm(schema), [schema]);
  const parsed = useMemo(() => parseText(value), [value]);
  const exactJsonOnly = needsExactJson(value) || schemaNeedsExactJson(schema);
  const formable =
    !exactJsonOnly &&
    root !== null &&
    parsed.ok &&
    (parsed.data === undefined || isJsonObject(parsed.data));
  // The operator's last choice wins; otherwise a pinned schema opens on its
  // fields and an inferred one — a convenience — behind the JSON they know.
  const [mode, setModeState] = useState<Mode>(() => {
    if (!root || !formable) return "json";
    return preferForm
      ? "form"
      : (readStoredEditorMode() ?? (captionSource === "pinned" ? "form" : "json"));
  });
  const setMode = (next: Mode) => {
    setModeState(next);
    storeEditorMode(next);
  };
  const [drafts, setDrafts] = useState<Record<string, { text: string; error: string | null }>>({});
  const hasNewItems = Object.keys(drafts).some((key) => key.endsWith(" \0new"));
  // Keep local incomplete number/JSON text while typing, but discard it on an
  // explicit parent restore/source replacement. Expansion and mode stay intact.
  useEffect(() => {
    void resetKey;
    setDrafts({});
  }, [resetKey]);
  const valid =
    parsed.ok && value.trim() !== "" && !Object.values(drafts).some((draft) => draft.error);
  // Inline parent callbacks often change identity after patching a row. Notify
  // only for validity changes, avoiding callback-driven update loops.
  const validityCallback = useRef(onValidityChange);
  validityCallback.current = onValidityChange;
  useEffect(() => {
    validityCallback.current?.(valid);
  }, [valid]);
  // Object groups the operator folded; a group with a problem inside stays open.
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const effectiveMode: Mode = mode === "form" && formable ? "form" : "json";

  // A brand-new value starts from the schema's shape so required fields are visible.
  useEffect(() => {
    if (!disabled && effectiveMode === "form" && root && value.trim() === "") {
      onChange(serialize(initialValue(root)));
    }
  }, [disabled, effectiveMode, root, value, onChange]);

  const data: JsonObject = parsed.ok && isJsonObject(parsed.data) ? parsed.data : {};
  const issues = useMemo(() => (root ? validateValue(schema, data) : []), [schema, root, data]);
  const issueByPath = useMemo(() => {
    const map = new Map<string, string[]>();
    for (const issue of issues) {
      const key = pathKey(issue.path);
      map.set(key, [...(map.get(key) ?? []), issue.message]);
    }
    return map;
  }, [issues]);
  const issueKeys = useMemo(() => [...issueByPath.keys()], [issueByPath]);

  function commit(path: string[], next: unknown) {
    onChange(serialize(setAt(data, path, next)));
  }
  function setDraft(key: string, text: string | undefined, error: string | null = null) {
    setDrafts((current) => {
      const copy = { ...current };
      if (text === undefined) delete copy[key];
      else copy[key] = { text, error };
      return copy;
    });
  }
  function reindexListDrafts(listKey: string, removedIndex: number) {
    const prefix = `${listKey} `;
    setDrafts((previous) => {
      const next: Record<string, { text: string; error: string | null }> = {};
      for (const [draftKey, draft] of Object.entries(previous)) {
        if (!draftKey.startsWith(prefix)) {
          next[draftKey] = draft;
          continue;
        }
        const path = draftKey.slice(prefix.length).split(" ");
        const index = Number(path[0]);
        if (!Number.isInteger(index) || String(index) !== path[0] || index < removedIndex) {
          next[draftKey] = draft;
        } else if (index > removedIndex) {
          next[`${prefix}${[String(index - 1), ...path.slice(1)].join(" ")}`] = draft;
        }
      }
      return next;
    });
  }
  function errorFor(field: FormField, draftError: string | null): string | null {
    if (draftError) return draftError;
    const messages = issueByPath.get(pathKey(field.path));
    return messages ? messages.join("; ") : null;
  }
  /** Whether any issue sits at or below this group's path. */
  function hasIssueWithin(key: string): boolean {
    return issueKeys.some((issueKey) => issueKey === key || issueKey.startsWith(`${key} `));
  }
  /** Description, default and stated constraints, with a reset when the value left the default. */
  function hintFor(field: FormField, current: unknown): ReactNode {
    const parts: string[] = [];
    if (field.description) parts.push(field.description);
    if (field.schema.format === "go-duration") parts.push("Go duration, e.g. 1m30s.");
    const hasDefault = "default" in field.schema;
    if (hasDefault) parts.push(`Default: ${JSON.stringify(field.schema.default)}`);
    parts.push(...describeConstraints(field.schema));
    const canReset = hasDefault && !disabled && !sameJson(current, field.schema.default);
    if (parts.length === 0 && !canReset) return undefined;
    return (
      <>
        {parts.join(" · ")}
        {canReset ? (
          <Button
            type="button"
            variant="link"
            size="xs"
            // Geometry as utilities, not a .schema-form-reset rule: the size
            // variant's h-6/px-2/text-xs are utilities and beat a component
            // layer rule, so the link rendered as a 24px box on the hint's 17px
            // text line and grew the line the moment a default was departed
            // from. text-sm is the hint's own size.
            className="ml-1 h-auto p-0 align-baseline text-sm"
            onClick={() => {
              const key = pathKey(field.path);
              setDrafts((current) =>
                Object.fromEntries(
                  Object.entries(current).filter(
                    ([draftKey]) => draftKey !== key && !draftKey.startsWith(`${key} `),
                  ),
                ),
              );
              commit(field.path, field.schema.default);
            }}
          >
            Reset to default
          </Button>
        ) : null}
      </>
    );
  }

  const showSummary =
    root !== null && parsed.ok && formable && issues.length > 0 && captionSource !== "inferred";
  const toolbar = (
    <div className="schema-form-toolbar">
      <fieldset className="schema-form-toggle" aria-label="Value editor">
        <button
          type="button"
          className={cn("schema-form-toggle-button", effectiveMode === "form" && "is-active")}
          aria-pressed={effectiveMode === "form"}
          disabled={disabled || !formable}
          onClick={() => setMode("form")}
        >
          Form
        </button>
        <button
          type="button"
          className={cn("schema-form-toggle-button", effectiveMode === "json" && "is-active")}
          aria-pressed={effectiveMode === "json"}
          disabled={disabled || hasNewItems}
          onClick={() => setMode("json")}
        >
          JSON
        </button>
      </fieldset>
      {hasNewItems ? (
        <span role="status">Add or cancel new list items before saving or switching to JSON.</span>
      ) : null}
      {Object.values(drafts).some((draft) => draft.error) && effectiveMode === "json" ? (
        <span role="alert">
          Fix the incomplete Form fields, or edit JSON to replace those drafts.
        </span>
      ) : null}
      {schemaLabel ? <span className="schema-form-label">{schemaLabel}</span> : null}
      <span className="schema-form-caption faint">
        {root === null
          ? "This schema cannot be rendered as fields; edit it as JSON."
          : !parsed.ok
            ? "Fix the JSON to use the form."
            : exactJsonOnly
              ? "Use JSON to preserve exact numeric precision and representation."
              : !formable
                ? "The form needs a JSON object; the value is something else."
                : effectiveMode === "form"
                  ? captionSource === "inferred"
                    ? "Fields inferred from the current value — no schema is pinned for this key."
                    : "Fields from the pinned schema. Editing a field rewrites the JSON with standard formatting."
                  : "Switch to Form to edit by field."}
      </span>
      {showSummary ? (
        <span className="schema-form-summary" data-testid="schema-form-summary" role="status">
          {issues.length} schema issue{issues.length === 1 ? "" : "s"} — checked again at release
          time.
        </span>
      ) : null}
    </div>
  );

  if (effectiveMode === "json") {
    return (
      <div className={cn("schema-form", className)} data-mode="json">
        {toolbar}
        <JsonEditor
          id={id}
          aria-label={jsonLabel}
          aria-describedby={ariaDescribedBy}
          aria-invalid={ariaInvalid || !valid}
          aria-required={ariaRequired}
          inputRef={inputRef}
          rows={rows}
          value={value}
          disabled={disabled}
          onChange={(next) => {
            setDrafts({});
            onChange(next);
          }}
          onBlur={onBlur}
          onSubmit={valid ? onSubmit : undefined}
        />
      </div>
    );
  }

  const rootField = root as FormField;
  const extras = extraKeys(rootField, data);
  // A key no JSON path can produce, so the extras draft never collides with a field.
  const extrasKey = "\0extras";
  const extrasDraft = drafts[extrasKey]?.text;
  const extrasValue = Object.fromEntries(extras.map((key) => [key, data[key]]));
  const extrasText =
    extrasDraft ?? (extras.length === 0 ? "" : JSON.stringify(extrasValue, null, 2));
  const extrasParsed = extrasDraft === undefined ? null : parseFormText(extrasDraft);
  const extrasError =
    extrasParsed && !extrasParsed.ok
      ? extrasParsed.error
      : extrasParsed?.ok &&
          extrasParsed.data !== undefined &&
          !isJsonObject(extrasParsed.data) &&
          extrasDraft?.trim() !== ""
        ? "must be a JSON object"
        : null;

  function renderField(field: FormField): React.ReactNode {
    const key = pathKey(field.path);
    const controlId = `${baseId}-${key.replace(/[\s\0]+/g, "-")}`;
    const current = getAt(data, field.path);
    const label = fieldLabel(field);
    switch (field.kind) {
      case "object": {
        const forcedOpen = hasIssueWithin(key);
        const open = forcedOpen || !collapsed.has(key);
        const count = field.fields?.length ?? 0;
        return (
          <fieldset
            key={key}
            className="schema-form-group"
            data-path={key}
            data-open={open ? "true" : "false"}
          >
            <legend className="schema-form-legend">
              <button
                type="button"
                className="schema-form-group-toggle"
                aria-expanded={open}
                disabled={forcedOpen}
                onClick={() =>
                  setCollapsed((current) => {
                    const next = new Set(current);
                    if (next.has(key)) next.delete(key);
                    else next.add(key);
                    return next;
                  })
                }
              >
                <ChevronDown size={14} aria-hidden />
                {label}
                {field.required ? (
                  <span aria-hidden="true" className="text-danger">
                    {" *"}
                  </span>
                ) : null}
                {!open ? (
                  <span aria-hidden="true" className="schema-form-group-count">
                    · {count} {count === 1 ? "field" : "fields"}
                  </span>
                ) : null}
              </button>
            </legend>
            {open ? (
              <>
                {field.description ? <p className="faint text-sm">{field.description}</p> : null}
                {errorFor(field, null) ? (
                  <p className="field-error" role="alert">
                    {errorFor(field, null)}
                  </p>
                ) : null}
                <div className="schema-form-fields">{(field.fields ?? []).map(renderField)}</div>
              </>
            ) : null}
          </fieldset>
        );
      }
      case "boolean": {
        const checked = current === true;
        const error = errorFor(field, null);
        const hint = hintFor(field, current);
        return (
          <div key={key} className="schema-form-boolean" data-path={key}>
            <div className="checkbox-row">
              <Checkbox
                id={controlId}
                checked={checked}
                disabled={disabled}
                aria-required={field.required || undefined}
                aria-invalid={error ? true : undefined}
                onCheckedChange={(next) => commit(field.path, next === true)}
              />
              <label htmlFor={controlId} className="block">
                {label}
                {field.required ? (
                  <span aria-hidden="true" className="text-danger">
                    {"\u00a0*"}
                  </span>
                ) : null}
              </label>
            </div>
            {hint ? <p className="field-hint">{hint}</p> : null}
            {error ? (
              <p className="field-error" role="alert">
                {error}
              </p>
            ) : null}
          </div>
        );
      }
      case "string": {
        const text = typeof current === "string" ? current : "";
        const error = errorFor(field, null);
        const hint = hintFor(field, current);
        if (field.enumValues) {
          const unsetValue = enumUnsetValue(field.enumValues);
          const emptyValue = enumUnsetValue([...field.enumValues, unsetValue]);
          const options = field.enumValues.map((option) => ({
            value: option === "" ? emptyValue : String(option),
            label: option === "" ? "Empty string" : String(option),
          }));
          return (
            <Field
              key={key}
              label={label}
              htmlFor={controlId}
              required={field.required}
              hint={hint}
              error={error}
            >
              <AppSelect
                id={controlId}
                value={
                  current === undefined
                    ? field.required
                      ? ""
                      : unsetValue
                    : text === ""
                      ? emptyValue
                      : text
                }
                disabled={disabled}
                placeholder="Choose…"
                options={
                  field.required ? options : [{ value: unsetValue, label: "— none —" }, ...options]
                }
                onValueChange={(next) =>
                  commit(
                    field.path,
                    next === unsetValue ? undefined : next === emptyValue ? "" : next,
                  )
                }
                onBlur={onBlur}
                aria-required={field.required || undefined}
              />
            </Field>
          );
        }
        const long =
          (typeof field.schema.maxLength === "number" && field.schema.maxLength > 200) ||
          field.schema.format === "kms-base64" ||
          /[\r\n]/.test(text);
        const maxLength =
          typeof field.schema.maxLength === "number" ? field.schema.maxLength : undefined;
        return (
          <Field key={key} label={label} required={field.required} hint={hint} error={error}>
            {long ? (
              <Textarea
                id={controlId}
                className="font-mono"
                rows={3}
                value={text}
                disabled={disabled}
                spellCheck={false}
                maxLength={maxLength}
                onChange={(event) => commit(field.path, event.target.value)}
                onBlur={onBlur}
              />
            ) : (
              <Input
                id={controlId}
                className="font-mono"
                value={text}
                disabled={disabled}
                autoComplete="off"
                spellCheck={false}
                maxLength={maxLength}
                onChange={(event) => commit(field.path, event.target.value)}
                onBlur={onBlur}
              />
            )}
          </Field>
        );
      }
      case "number": {
        const draft = drafts[key]?.text;
        const text = draft ?? (typeof current === "number" ? String(current) : "");
        const draftProblem =
          draft === undefined ? null : parseFormNumber(draft, Boolean(field.integer)).error;
        const error = errorFor(field, draftProblem);
        const hint = hintFor(field, current);
        if (field.enumValues) {
          const options = field.enumValues.map((option) => ({
            value: String(option),
            label: String(option),
          }));
          return (
            <Field
              key={key}
              label={label}
              htmlFor={controlId}
              required={field.required}
              hint={hint}
              error={error}
            >
              <AppSelect
                id={controlId}
                value={text}
                disabled={disabled}
                placeholder="Choose…"
                options={field.required ? options : [{ value: "", label: "— none —" }, ...options]}
                onValueChange={(next) => commit(field.path, next === "" ? undefined : Number(next))}
                onBlur={onBlur}
                aria-required={field.required || undefined}
              />
            </Field>
          );
        }
        return (
          <Field key={key} label={label} required={field.required} hint={hint} error={error}>
            <Input
              id={controlId}
              className="font-mono"
              inputMode={field.integer ? "numeric" : "decimal"}
              value={text}
              disabled={disabled}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => {
                const next = event.target.value;
                const result = parseFormNumber(next, Boolean(field.integer));
                setDraft(key, next, result.error);
                if (!result.error) commit(field.path, result.value);
              }}
              onBlur={() => {
                if (draft !== undefined && !parseFormNumber(draft, Boolean(field.integer)).error) {
                  setDraft(key, undefined);
                }
                onBlur?.();
              }}
            />
          </Field>
        );
      }
      case "list": {
        const items = Array.isArray(current) ? current : [];
        const error = errorFor(field, null);
        const hint = hintFor(field, current);
        const nullable =
          field.nullable ||
          (Array.isArray(field.schema.type) &&
            field.schema.type.includes("null") &&
            validateValue(field.schema, null).length === 0);
        const state =
          current === undefined
            ? "unset"
            : current === null
              ? "null"
              : Array.isArray(current)
                ? "set"
                : "invalid";
        const newItemKey = `${key} \0new`;
        const newItem = drafts[newItemKey];
        const allowsEmpty = arrayAllowsEmpty(field.schema);
        const atCapacity =
          typeof field.schema.maxItems === "number" && items.length >= field.schema.maxItems;
        const replaceList = (next: unknown) => {
          // Item indices can be reused after a clear, unset or removal. Do not
          // overlay the replacement list with old numeric/JSON input drafts.
          setDrafts((previous) =>
            Object.fromEntries(
              Object.entries(previous).filter(
                ([draftKey]) => draftKey !== key && !draftKey.startsWith(`${key} `),
              ),
            ),
          );
          commit(field.path, next);
        };
        const stateControl = (
          <div className="grid gap-1">
            <span className={cn("text-sm", error ? "text-danger" : "faint")} role="status">
              {state === "unset"
                ? field.required
                  ? "Missing · Required field"
                  : `${arrayStateLabel(field.schema, "omitted")} · Field omitted`
                : state === "null"
                  ? `${arrayStateLabel(field.schema, "null")} · null${nullable ? "" : " (not allowed)"}`
                  : state === "set"
                    ? items.length === 0
                      ? `${arrayStateLabel(field.schema, "empty")} · Empty list []`
                      : `${items.length} item${items.length === 1 ? "" : "s"}`
                    : "Not an array · use JSON to inspect"}
            </span>
            {items.length === 0 && state !== "invalid" ? (
              <span className="faint text-sm">
                {allowsEmpty
                  ? field.required
                    ? "An empty list is allowed; this field must be included."
                    : "An empty list includes the field with no items."
                  : typeof field.schema.minItems === "number" && field.schema.minItems > 0
                    ? `Add at least ${field.schema.minItems} item${field.schema.minItems === 1 ? "" : "s"}.`
                    : "Add items that satisfy the schema."}
              </span>
            ) : null}
          </div>
        );
        const listActions = (
          <>
            {newItem ? (
              <NewArrayItem
                field={field}
                text={newItem.text}
                disabled={disabled || atCapacity}
                onChange={(text) => setDraft(newItemKey, text, "Add or cancel the new list item.")}
                onAdd={(item) => {
                  setDraft(newItemKey, undefined);
                  commit(field.path, [...items, item]);
                }}
                onCancel={() => setDraft(newItemKey, undefined)}
              />
            ) : null}
            <div className="row-wrap">
              {!newItem && state !== "invalid" ? (
                <Button
                  id={`${controlId}-add`}
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={disabled || atCapacity}
                  onClick={() => {
                    const itemSchema = isJsonObject(field.schema.items) ? field.schema.items : {};
                    const initial = field.itemField
                      ? serialize(initialValue(field.itemField) ?? {})
                      : field.item === "boolean"
                        ? String(itemSchema.default ?? false)
                        : "default" in itemSchema
                          ? String(itemSchema.default)
                          : "";
                    setDraft(newItemKey, initial, "Add or cancel the new list item.");
                  }}
                >
                  <Plus size={14} aria-hidden /> Add {label} item
                </Button>
              ) : null}
              {!newItem && state !== "set" && state !== "invalid" && allowsEmpty ? (
                <Button
                  type="button"
                  variant="link"
                  size="sm"
                  disabled={disabled}
                  aria-label={`Use empty list for ${label}`}
                  onClick={() => replaceList([])}
                >
                  Use empty list
                </Button>
              ) : null}
              {!newItem && !field.required && state !== "unset" ? (
                <Button
                  type="button"
                  variant="link"
                  size="sm"
                  disabled={disabled}
                  aria-label={`Omit ${label} field`}
                  onClick={() => replaceList(undefined)}
                >
                  Omit field
                </Button>
              ) : null}
              {!newItem && nullable && state !== "null" ? (
                <details>
                  <summary className="faint text-sm">More options</summary>
                  <Button
                    type="button"
                    variant="link"
                    size="sm"
                    disabled={disabled}
                    aria-label={`Set ${label} to null`}
                    onClick={() => replaceList(null)}
                  >
                    Set to null
                  </Button>
                </details>
              ) : null}
            </div>
          </>
        );
        const removeItem = (index: number) => {
          reindexListDrafts(key, index);
          commit(
            field.path,
            items.filter((_, position) => position !== index),
          );
        };
        if (field.item === "object" && field.itemField) {
          return (
            <Field key={key} label={label} required={field.required} hint={hint} error={error}>
              {stateControl}
              <ul className="schema-form-list" aria-label={`${label} items`}>
                {items.map((_, index) => {
                  const item = itemAt(field, index);
                  if (!item) return null;
                  return (
                    <li key={pathKey(item.path)} className="schema-form-list-item">
                      {renderField(item)}
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Remove ${label} item ${index + 1}`}
                        disabled={disabled}
                        onClick={() => removeItem(index)}
                      >
                        <Trash2 size={14} aria-hidden />
                      </Button>
                    </li>
                  );
                })}
              </ul>
              {listActions}
            </Field>
          );
        }
        return (
          <Field key={key} label={label} required={field.required} hint={hint} error={error}>
            {stateControl}
            <ul className="schema-form-list" aria-label={`${label} items`}>
              {items.map((item, index) => {
                const itemKey = pathKey([...field.path, String(index)]);
                const itemDraft = drafts[itemKey]?.text;
                const itemError =
                  drafts[itemKey]?.error ?? issueByPath.get(itemKey)?.join("; ") ?? null;
                const ItemInput =
                  field.item === "string" && typeof item === "string" && /[\r\n]/.test(item)
                    ? Textarea
                    : Input;
                const setItem = (next: unknown) =>
                  commit(
                    field.path,
                    items.map((existing, position) => (position === index ? next : existing)),
                  );
                return (
                  <li key={itemKey} className="schema-form-list-row">
                    {field.item === "boolean" ? (
                      <div className="checkbox-row">
                        <Checkbox
                          id={`${controlId}-item-${index}`}
                          aria-label={`${label} item ${index + 1}`}
                          checked={item === true}
                          disabled={disabled}
                          aria-invalid={itemError ? true : undefined}
                          onCheckedChange={(next) => setItem(next === true)}
                        />
                        <label htmlFor={`${controlId}-item-${index}`}>
                          <span>Item {index + 1}</span>{" "}
                          <span className="faint">(index {index})</span>
                          {" · "}
                          <span className="font-mono">{String(item)}</span>
                        </label>
                      </div>
                    ) : field.enumValues ? (
                      <AppSelect
                        aria-label={`${label} item ${index + 1}`}
                        value={String(item ?? "")}
                        disabled={disabled}
                        options={field.enumValues.map((option) => ({
                          value: String(option),
                          label: String(option),
                        }))}
                        onValueChange={(next) =>
                          setItem(field.item === "number" ? Number(next) : next)
                        }
                      />
                    ) : (
                      <ItemInput
                        className="font-mono"
                        aria-label={`${label} item ${index + 1}`}
                        aria-invalid={itemError ? true : undefined}
                        inputMode={
                          field.item === "number"
                            ? field.integer
                              ? "numeric"
                              : "decimal"
                            : undefined
                        }
                        value={
                          itemDraft ??
                          (typeof item === "string" || typeof item === "number" ? String(item) : "")
                        }
                        disabled={disabled}
                        autoComplete="off"
                        spellCheck={false}
                        onChange={(event) => {
                          const next = event.target.value;
                          if (field.item === "number") {
                            const result = parseFormNumber(next, Boolean(field.integer));
                            setDraft(
                              itemKey,
                              next,
                              result.error ??
                                (result.value === undefined ? "must be a number" : null),
                            );
                            if (!result.error && result.value !== undefined) setItem(result.value);
                          } else {
                            setItem(next);
                          }
                        }}
                        onBlur={() => {
                          if (itemDraft !== undefined && !drafts[itemKey]?.error) {
                            setDraft(itemKey, undefined);
                          }
                          onBlur?.();
                        }}
                      />
                    )}
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Remove ${label} item ${index + 1}`}
                      disabled={disabled}
                      onClick={() => removeItem(index)}
                    >
                      <Trash2 size={14} aria-hidden />
                    </Button>
                    {itemError ? (
                      <span className="field-error schema-form-list-error">{itemError}</span>
                    ) : null}
                    {field.item === "string" && item === "" ? (
                      <span className="faint text-sm schema-form-list-error">
                        Empty string · Stored item
                      </span>
                    ) : null}
                  </li>
                );
              })}
            </ul>
            {listActions}
          </Field>
        );
      }
      default: {
        const draft = drafts[key]?.text;
        const text = draft ?? (current === undefined ? "" : JSON.stringify(current, null, 2));
        const draftResult = draft === undefined ? null : parseFormText(draft);
        const draftProblem = draftResult && !draftResult.ok ? draftResult.error : null;
        const error = errorFor(field, draftProblem);
        return (
          <Field
            key={key}
            label={label}
            required={field.required}
            hint={
              hintFor(field, current) ??
              `Edited as JSON — this property ${field.reason ?? "cannot be rendered as fields"}.`
            }
            error={error}
          >
            <JsonEditor
              id={controlId}
              toolbar="minimal"
              rows={4}
              maxHeight="40vh"
              value={text}
              disabled={disabled}
              onChange={(next) => {
                const result = parseFormText(next);
                setDraft(key, next, result.ok ? null : result.error);
                if (result.ok) commit(field.path, result.data);
              }}
              onBlur={() => {
                if (draft !== undefined && parseFormText(draft).ok) setDraft(key, undefined);
                onBlur?.();
              }}
            />
          </Field>
        );
      }
    }
  }

  const rootIssues = issueByPath.get("") ?? [];
  const unknownIssues = issues.filter(
    (issue) => issue.path.length > 0 && extras.includes(issue.path[0]),
  );

  // A fieldset, so the group carries the wrapping Field's label as its name
  // (a <label for> cannot point at a div) and the invalid flag lands on the
  // frame the focus-first-invalid helper looks for.
  return (
    <fieldset
      ref={firstControlRef}
      className={cn("schema-form", className)}
      data-mode="form"
      aria-describedby={ariaDescribedBy}
      data-required={ariaRequired ? "true" : undefined}
      data-invalid={ariaInvalid || !valid ? "true" : undefined}
    >
      <legend className="sr-only">{jsonLabel}</legend>
      {toolbar}
      {rootField.description ? <p className="faint text-sm">{rootField.description}</p> : null}
      <div className="schema-form-fields">{(rootField.fields ?? []).map(renderField)}</div>
      {extras.length > 0 || rootField.allowsExtra ? (
        <Field
          label="Other properties"
          hint={
            rootField.allowsExtra
              ? "Properties the schema does not list individually, as a JSON object."
              : "These keys are not declared by the schema; remove them or pin a schema that allows them."
          }
          error={
            extrasError ??
            (unknownIssues.length > 0
              ? unknownIssues
                  .map((issue) => `${formatIssuePath(issue.path)} ${issue.message}`)
                  .join("; ")
              : null)
          }
        >
          <JsonEditor
            toolbar="minimal"
            rows={3}
            maxHeight="40vh"
            value={extrasText}
            disabled={disabled}
            onChange={(next) => {
              const result = parseFormText(next);
              setDraft(
                extrasKey,
                next,
                !result.ok
                  ? result.error
                  : result.data !== undefined && !isJsonObject(result.data)
                    ? "must be a JSON object"
                    : null,
              );
              if (!result.ok) return;
              if (result.data !== undefined && !isJsonObject(result.data)) return;
              const kept: JsonObject = {};
              for (const [key, item] of Object.entries(data)) {
                if (!extras.includes(key)) kept[key] = item;
              }
              onChange(serialize({ ...kept, ...(isJsonObject(result.data) ? result.data : {}) }));
            }}
            onBlur={() => {
              if (extrasDraft !== undefined && !extrasError) setDraft(extrasKey, undefined);
              onBlur?.();
            }}
          />
        </Field>
      ) : null}
      {rootIssues.length > 0 ? (
        <p className="field-error" role="alert">
          {rootIssues.join("; ")}
        </p>
      ) : null}
    </fieldset>
  );
}
