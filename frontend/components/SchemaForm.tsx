import { ChevronDown, Plus, Trash2 } from "lucide-react";
import { type ReactNode, type Ref, useCallback, useEffect, useId, useMemo, useState } from "react";
import { JsonEditor } from "@/components/JsonEditor";
import { Button, Checkbox, Field, Input, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { assignRef } from "@/lib/forms";
import { checkJson } from "@/lib/json-text";
import {
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
  return { ok: true, data: JSON.parse(text) };
}

function fieldLabel(field: FormField): string {
  return field.name;
}

function sameJson(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
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
  inputRef,
}: SchemaFormProps) {
  const baseId = useId();
  // Form mode has no single control; the first field's input stands in for it.
  const firstControlRef = useCallback(
    (node: HTMLElement | null) => {
      const first = node
        ? Array.from(
            node.querySelectorAll<HTMLElement>(
              'input:not([type="hidden"]), textarea, [role="combobox"], [role="checkbox"]',
            ),
          ).find((candidate) => !candidate.closest(".schema-form-toolbar"))
        : undefined;
      assignRef(inputRef, first ?? null);
    },
    [inputRef],
  );
  const root = useMemo(() => buildForm(schema), [schema]);
  const parsed = useMemo(() => parseText(value), [value]);
  const formable =
    root !== null && parsed.ok && (parsed.data === undefined || isJsonObject(parsed.data));
  // The operator's last choice wins; otherwise a pinned schema opens on its
  // fields and an inferred one — a convenience — behind the JSON they know.
  const [mode, setModeState] = useState<Mode>(() => {
    if (!root || !formable) return "json";
    return readStoredEditorMode() ?? (captionSource === "pinned" ? "form" : "json");
  });
  const setMode = (next: Mode) => {
    setModeState(next);
    storeEditorMode(next);
  };
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  // Object groups the operator folded; a group with a problem inside stays open.
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const effectiveMode: Mode = mode === "form" && formable ? "form" : "json";

  // A brand-new value starts from the schema's shape so required fields are visible.
  useEffect(() => {
    if (effectiveMode === "form" && root && value.trim() === "") {
      onChange(serialize(initialValue(root)));
    }
  }, [effectiveMode, root, value, onChange]);

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
  function setDraft(key: string, text: string | undefined) {
    setDrafts((current) => {
      const copy = { ...current };
      if (text === undefined) delete copy[key];
      else copy[key] = text;
      return copy;
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
            className="schema-form-reset"
            onClick={() => commit(field.path, field.schema.default)}
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
          disabled={disabled}
          onClick={() => setMode("json")}
        >
          JSON
        </button>
      </fieldset>
      {schemaLabel ? <span className="schema-form-label">{schemaLabel}</span> : null}
      <span className="schema-form-caption faint">
        {root === null
          ? "This alias has no field-level schema; edit it as JSON."
          : !parsed.ok
            ? "Fix the JSON to use the form."
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
          aria-invalid={ariaInvalid}
          aria-required={ariaRequired}
          inputRef={inputRef}
          rows={rows}
          value={value}
          disabled={disabled}
          onChange={onChange}
          onBlur={onBlur}
          onSubmit={onSubmit}
        />
      </div>
    );
  }

  const rootField = root as FormField;
  const extras = extraKeys(rootField, data);
  // A key no JSON path can produce, so the extras draft never collides with a field.
  const extrasKey = "\0extras";
  const extrasDraft = drafts[extrasKey];
  const extrasValue = Object.fromEntries(extras.map((key) => [key, data[key]]));
  const extrasText =
    extrasDraft ?? (extras.length === 0 ? "" : JSON.stringify(extrasValue, null, 2));
  const extrasParsed = extrasDraft === undefined ? null : parseText(extrasDraft);
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
              <label htmlFor={controlId}>
                {label}
                {field.required ? (
                  <span aria-hidden="true" className="text-danger">
                    {" *"}
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
                onValueChange={(next) => commit(field.path, next === "" ? undefined : next)}
                onBlur={onBlur}
                aria-required={field.required || undefined}
              />
            </Field>
          );
        }
        const long =
          (typeof field.schema.maxLength === "number" && field.schema.maxLength > 200) ||
          field.schema.format === "kms-base64";
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
        const draft = drafts[key];
        const text = draft ?? (typeof current === "number" ? String(current) : "");
        const draftProblem =
          draft === undefined ? null : parseNumberDraft(draft, Boolean(field.integer)).error;
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
                setDraft(key, next);
                const result = parseNumberDraft(next, Boolean(field.integer));
                if (!result.error) commit(field.path, result.value);
              }}
              onBlur={() => {
                if (draft !== undefined && !parseNumberDraft(draft, Boolean(field.integer)).error) {
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
        const removeItem = (index: number) =>
          commit(
            field.path,
            items.filter((_, position) => position !== index),
          );
        if (field.item === "object" && field.itemField) {
          const itemField = field.itemField;
          return (
            <Field key={key} label={label} required={field.required} hint={hint} error={error}>
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
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={disabled}
                onClick={() => commit(field.path, [...items, initialValue(itemField) ?? {}])}
              >
                <Plus size={14} aria-hidden /> Add {label} item
              </Button>
            </Field>
          );
        }
        return (
          <Field key={key} label={label} required={field.required} hint={hint} error={error}>
            <ul className="schema-form-list" aria-label={`${label} items`}>
              {items.map((item, index) => {
                const itemKey = pathKey([...field.path, String(index)]);
                const itemDraft = drafts[itemKey];
                const itemError = issueByPath.get(itemKey)?.join("; ") ?? null;
                const setItem = (next: unknown) =>
                  commit(
                    field.path,
                    items.map((existing, position) => (position === index ? next : existing)),
                  );
                return (
                  <li key={itemKey} className="schema-form-list-row">
                    {field.item === "boolean" ? (
                      <Checkbox
                        aria-label={`${label} item ${index + 1}`}
                        checked={item === true}
                        disabled={disabled}
                        onCheckedChange={(next) => setItem(next === true)}
                      />
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
                      <Input
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
                            setDraft(itemKey, next);
                            const result = parseNumberDraft(next, Boolean(field.integer));
                            if (!result.error && result.value !== undefined) setItem(result.value);
                          } else {
                            setItem(next);
                          }
                        }}
                        onBlur={() => {
                          if (
                            itemDraft !== undefined &&
                            !parseNumberDraft(itemDraft, Boolean(field.integer)).error
                          ) {
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
                  </li>
                );
              })}
            </ul>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled}
              onClick={() =>
                commit(field.path, [
                  ...items,
                  field.item === "boolean"
                    ? false
                    : field.item === "number"
                      ? (field.enumValues?.[0] ?? 0)
                      : (field.enumValues?.[0] ?? ""),
                ])
              }
            >
              <Plus size={14} aria-hidden /> Add {label} item
            </Button>
          </Field>
        );
      }
      default: {
        const draft = drafts[key];
        const text = draft ?? (current === undefined ? "" : JSON.stringify(current, null, 2));
        const draftResult = draft === undefined ? null : parseText(draft);
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
                setDraft(key, next);
                const result = parseText(next);
                if (result.ok) commit(field.path, result.data);
              }}
              onBlur={() => {
                if (draft !== undefined && parseText(draft).ok) setDraft(key, undefined);
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
      data-invalid={ariaInvalid ? "true" : undefined}
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
              setDraft(extrasKey, next);
              const result = parseText(next);
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
