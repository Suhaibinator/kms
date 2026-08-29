/**
 * Schema-driven form model for parameter values.
 *
 * A pinned configuration schema describes every alias as a top-level property.
 * This module turns one alias's sub-schema into a field tree the console can
 * render as inputs, keeps a JSON escape hatch for anything it cannot express,
 * and validates a value against the rendered subset so problems show up before
 * the release dry-run does. It is deliberately not a full JSON Schema
 * implementation: unsupported keywords make a subtree a raw JSON field rather
 * than a wrong form.
 */

export type JsonSchema = { [keyword: string]: unknown };
export type JsonObject = { [key: string]: unknown };

export type FieldKind = "string" | "number" | "boolean" | "object" | "list" | "json";

export interface FormField {
  kind: FieldKind;
  /** Property path from the alias root; `[]` is the root itself. */
  path: string[];
  name: string;
  required: boolean;
  schema: JsonSchema;
  description?: string;
  /** Allowed values for enum-typed strings or numbers. */
  enumValues?: Array<string | number>;
  /** Number fields: integers only. */
  integer?: boolean;
  /** Object fields: child fields in schema order. */
  fields?: FormField[];
  /** Object fields: whether keys outside `fields` are allowed. */
  allowsExtra?: boolean;
  /** List fields: the item kind. Object items render as repeated sub-forms. */
  item?: "string" | "number" | "boolean" | "object";
  /** List fields with object items: the item's form, rooted at a placeholder path (see `itemAt`). */
  itemField?: FormField;
  /** JSON fallback fields: why the subtree is not rendered as inputs. */
  reason?: string;
  /** The schema allows `null` as well (generator wrapper for pointers, slices, maps). */
  nullable?: boolean;
}

export interface ValidationIssue {
  path: string[];
  message: string;
}

export const MAX_FORM_DEPTH = 6;

export function isJsonObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isObject(value: unknown): value is JsonObject {
  return isJsonObject(value);
}

function isSchema(value: unknown): value is JsonSchema {
  return isObject(value);
}

export function pathKey(path: string[]): string {
  return path.join(" ");
}

export function parseSchema(schemaJson: string | null | undefined): JsonSchema | null {
  if (!schemaJson) return null;
  try {
    const parsed: unknown = JSON.parse(schemaJson);
    return isSchema(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/** The sub-schema validating one alias, or null when the schema does not describe it. */
export function aliasSchema(
  schemaJson: string | null | undefined,
  alias: string,
): JsonSchema | null {
  const root = parseSchema(schemaJson);
  if (!root || !isObject(root.properties)) return null;
  const sub = root.properties[alias];
  return isSchema(sub) ? sub : null;
}

const UNSUPPORTED = ["oneOf", "anyOf", "allOf", "not", "$ref", "if", "then", "else"] as const;

/**
 * kms-config-gen wraps pointer, slice, and map fields as
 * `{anyOf: [<schema>, {type: "null"}]}`. Returns the inner schema (carrying the
 * wrapper's description and default) when `schema` has exactly that shape.
 */
export function unwrapNullable(schema: JsonSchema): JsonSchema | null {
  if (!Array.isArray(schema.anyOf) || schema.anyOf.length !== 2) return null;
  const branches = schema.anyOf as unknown[];
  const isNull = (branch: unknown) =>
    isObject(branch) && Object.keys(branch).length === 1 && branch.type === "null";
  const inner = branches.find((branch) => !isNull(branch));
  if (!inner || !isSchema(inner) || !branches.some(isNull)) return null;
  const merged: JsonSchema = { ...inner };
  if (typeof schema.description === "string" && merged.description === undefined) {
    merged.description = schema.description;
  }
  if ("default" in schema && !("default" in merged)) merged.default = schema.default;
  return merged;
}

function typeOf(schema: JsonSchema): { type: string | null; reason?: string } {
  for (const keyword of UNSUPPORTED) {
    if (keyword in schema) return { type: null, reason: `uses ${keyword}` };
  }
  const declared = schema.type;
  if (typeof declared === "string") return { type: declared };
  if (Array.isArray(declared)) {
    const kinds = declared.filter((item): item is string => typeof item === "string");
    const nonNull = kinds.filter((item) => item !== "null");
    if (nonNull.length === 1) return { type: nonNull[0] };
    return { type: null, reason: "allows several types" };
  }
  if (isObject(schema.properties)) return { type: "object" };
  if (Array.isArray(schema.enum)) {
    const values = schema.enum as unknown[];
    if (values.every((item) => typeof item === "string")) return { type: "string" };
    if (values.every((item) => typeof item === "number")) return { type: "number" };
  }
  return { type: null, reason: "has no type" };
}

function enumValues(schema: JsonSchema): Array<string | number> | undefined {
  if (!Array.isArray(schema.enum)) return undefined;
  const values = (schema.enum as unknown[]).filter(
    (item): item is string | number => typeof item === "string" || typeof item === "number",
  );
  return values.length === (schema.enum as unknown[]).length ? values : undefined;
}

function scalarItem(schema: JsonSchema): FormField["item"] | null {
  const { type } = typeOf(unwrapNullable(schema) ?? schema);
  if (type === "string") return "string";
  if (type === "integer" || type === "number") return "number";
  if (type === "boolean") return "boolean";
  return null;
}

function buildField(
  name: string,
  path: string[],
  schema: JsonSchema,
  required: boolean,
  depth: number,
): FormField {
  const inner = unwrapNullable(schema);
  if (inner) {
    return { ...buildField(name, path, inner, required, depth), nullable: true };
  }
  const base: FormField = {
    kind: "json",
    path,
    name,
    required,
    schema,
    description: typeof schema.description === "string" ? schema.description : undefined,
  };
  const { type, reason } = typeOf(schema);
  if (!type) return { ...base, reason: reason ?? "is not a simple type" };
  switch (type) {
    case "string":
      return { ...base, kind: "string", enumValues: enumValues(schema) };
    case "integer":
    case "number":
      return {
        ...base,
        kind: "number",
        integer: type === "integer",
        enumValues: enumValues(schema),
      };
    case "boolean":
      return { ...base, kind: "boolean" };
    case "object": {
      if (depth >= MAX_FORM_DEPTH) return { ...base, reason: "is nested too deeply" };
      const properties = isObject(schema.properties) ? schema.properties : null;
      if (!properties || Object.keys(properties).length === 0) {
        return { ...base, reason: "has no declared properties" };
      }
      const requiredKeys = new Set(
        Array.isArray(schema.required)
          ? (schema.required as unknown[]).filter(
              (item): item is string => typeof item === "string",
            )
          : [],
      );
      const fields = Object.entries(properties)
        .filter((entry): entry is [string, JsonSchema] => isSchema(entry[1]))
        .map(([key, sub]) =>
          buildField(key, [...path, key], sub, requiredKeys.has(key), depth + 1),
        );
      return {
        ...base,
        kind: "object",
        fields,
        allowsExtra: schema.additionalProperties !== false,
      };
    }
    case "array": {
      const items = isSchema(schema.items) ? schema.items : null;
      const item = items ? scalarItem(items) : null;
      if (items && !item) {
        // A list of objects: each item is its own sub-form, built once here at
        // a placeholder index and re-rooted per item with `itemAt`.
        const itemField = buildField(name, [...path, ITEM_PLACEHOLDER], items, true, depth + 1);
        if (itemField.kind === "object") {
          return { ...base, kind: "list", item: "object", itemField };
        }
      }
      if (!items || !item) return { ...base, reason: "is a list of non-scalar items" };
      const itemSchema = unwrapNullable(items) ?? items;
      return {
        ...base,
        kind: "list",
        item,
        integer: typeOf(itemSchema).type === "integer",
        enumValues: enumValues(itemSchema),
      };
    }
    default:
      return { ...base, reason: `has type ${type}` };
  }
}

const ITEM_PLACEHOLDER = "\0item";

/** Re-roots a field tree built at `from` so its paths start at `to` instead. */
function rebase(field: FormField, from: string[], to: string[]): FormField {
  const path = [...to, ...field.path.slice(from.length)];
  return {
    ...field,
    path,
    fields: field.fields?.map((child) => rebase(child, from, to)),
    itemField: field.itemField ? rebase(field.itemField, from, to) : undefined,
  };
}

/** The sub-form for item `index` of an object list: `field.itemField` with real paths. */
export function itemAt(field: FormField, index: number): FormField | null {
  if (field.kind !== "list" || !field.itemField) return null;
  const item = rebase(field.itemField, field.itemField.path, [...field.path, String(index)]);
  return { ...item, name: `${field.name} ${index + 1}` };
}

/** The root field for an alias, or null when the alias cannot be rendered as a form at all. */
export function buildForm(schema: JsonSchema | null): FormField | null {
  if (!schema) return null;
  const root = buildField("", [], schema, true, 0);
  return root.kind === "object" ? root : null;
}

/** Depth-first list of every field, root excluded. */
export function flattenFields(root: FormField): FormField[] {
  const out: FormField[] = [];
  const walk = (field: FormField) => {
    for (const child of field.fields ?? []) {
      out.push(child);
      walk(child);
    }
  };
  walk(root);
  return out;
}

/** A starting value: schema defaults, and empty containers for required objects and lists. */
export function initialValue(field: FormField): unknown {
  if ("default" in field.schema) return field.schema.default;
  switch (field.kind) {
    case "object": {
      const out: JsonObject = {};
      for (const child of field.fields ?? []) {
        if (!child.required) continue;
        const value = initialValue(child);
        if (value !== undefined) out[child.name] = value;
      }
      return out;
    }
    case "list":
      return [];
    case "string":
      return field.enumValues ? undefined : "";
    default:
      return undefined;
  }
}

function isIndex(key: string): boolean {
  return /^(?:0|[1-9]\d*)$/.test(key);
}

/** Reads `path` out of `value`; a numeric segment indexes into an array. */
export function getAt(value: unknown, path: string[]): unknown {
  let cursor: unknown = value;
  for (const key of path) {
    if (Array.isArray(cursor) && isIndex(key)) {
      cursor = cursor[Number(key)];
    } else if (isObject(cursor)) {
      cursor = cursor[key];
    } else {
      return undefined;
    }
  }
  return cursor;
}

/**
 * Returns a copy with `path` set to `next`; `undefined` removes an object key
 * (and leaves an array item in place as `undefined` — callers drop array
 * items by writing the filtered array).
 */
export function setAt(value: unknown, path: string[], next: unknown): unknown {
  if (path.length === 0) return next;
  const [head, ...rest] = path;
  if (Array.isArray(value) && isIndex(head)) {
    const copy = [...value];
    copy[Number(head)] = setAt(copy[Number(head)], rest, next);
    return copy;
  }
  const base: JsonObject = isObject(value) ? { ...value } : {};
  const child = setAt(base[head], rest, next);
  if (child === undefined) {
    delete base[head];
  } else {
    base[head] = child;
  }
  return base;
}

/** Keys of `value` the form has no field for (kept verbatim, edited as JSON). */
export function extraKeys(field: FormField, value: unknown): string[] {
  if (!isObject(value)) return [];
  const known = new Set((field.fields ?? []).map((child) => child.name));
  return Object.keys(value).filter((key) => !known.has(key));
}

function describeType(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  return typeof value;
}

function checkType(schema: JsonSchema, value: unknown): string | null {
  const declared = schema.type;
  const allowed =
    typeof declared === "string" ? [declared] : Array.isArray(declared) ? declared : [];
  if (allowed.length === 0) return null;
  const actual = describeType(value);
  const ok = allowed.some((type) => {
    if (type === "integer") return typeof value === "number" && Number.isInteger(value);
    if (type === "number") return typeof value === "number";
    if (type === "object") return isObject(value);
    return type === actual;
  });
  return ok ? null : `must be ${allowed.join(" or ")}, got ${actual}`;
}

/** The range, pattern and length limits a schema states, in the order a hint lists them. */
export function describeConstraints(schema: JsonSchema): string[] {
  const out: string[] = [];
  const { minimum, maximum, exclusiveMinimum, exclusiveMaximum, multipleOf } = schema;
  const low = typeof minimum === "number" ? minimum : undefined;
  const high = typeof maximum === "number" ? maximum : undefined;
  const lowX = typeof exclusiveMinimum === "number" ? exclusiveMinimum : undefined;
  const highX = typeof exclusiveMaximum === "number" ? exclusiveMaximum : undefined;
  if (low !== undefined && high !== undefined) out.push(`Range: ${low}–${high}`);
  else if (low !== undefined) out.push(`Minimum: ${low}`);
  else if (high !== undefined) out.push(`Maximum: ${high}`);
  if (lowX !== undefined) out.push(`Greater than ${lowX}`);
  if (highX !== undefined) out.push(`Less than ${highX}`);
  if (typeof multipleOf === "number" && multipleOf > 0) out.push(`Multiple of ${multipleOf}`);
  const { minLength, maxLength, pattern } = schema;
  if (typeof minLength === "number" && typeof maxLength === "number") {
    out.push(`${minLength}–${maxLength} characters`);
  } else if (typeof minLength === "number" && minLength > 0) {
    out.push(`At least ${minLength} characters`);
  } else if (typeof maxLength === "number") {
    out.push(`At most ${maxLength} characters`);
  }
  if (typeof pattern === "string") out.push(`Pattern: ${pattern}`);
  return out;
}

/**
 * Validates `value` against the subset of JSON Schema the form renders: type,
 * required, enum, const, numeric bounds, string length and pattern, list
 * bounds, `additionalProperties: false`, and the generator's nullable wrapper.
 * Unsupported keywords are ignored; the server re-validates the whole
 * document at release time.
 */
export function validateValue(
  schema: JsonSchema,
  value: unknown,
  path: string[] = [],
  depth = 0,
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const push = (message: string, at: string[] = path) => issues.push({ path: at, message });
  if (depth > MAX_FORM_DEPTH + 2) return issues;
  if (value === undefined) return issues;
  const inner = unwrapNullable(schema);
  if (inner) {
    return value === null ? issues : validateValue(inner, value, path, depth);
  }
  const typeProblem = checkType(schema, value);
  if (typeProblem) {
    push(typeProblem);
    return issues;
  }
  if ("const" in schema && JSON.stringify(schema.const) !== JSON.stringify(value)) {
    push(`must equal ${JSON.stringify(schema.const)}`);
  }
  if (Array.isArray(schema.enum)) {
    const allowed = schema.enum as unknown[];
    if (!allowed.some((item) => JSON.stringify(item) === JSON.stringify(value))) {
      push(`must be one of ${allowed.map((item) => JSON.stringify(item)).join(", ")}`);
    }
  }
  if (typeof value === "number") {
    const { minimum, maximum, exclusiveMinimum, exclusiveMaximum, multipleOf } = schema;
    if (typeof minimum === "number" && value < minimum) push(`must be at least ${minimum}`);
    if (typeof maximum === "number" && value > maximum) push(`must be at most ${maximum}`);
    if (typeof exclusiveMinimum === "number" && value <= exclusiveMinimum) {
      push(`must be greater than ${exclusiveMinimum}`);
    }
    if (typeof exclusiveMaximum === "number" && value >= exclusiveMaximum) {
      push(`must be less than ${exclusiveMaximum}`);
    }
    if (typeof multipleOf === "number" && multipleOf > 0 && value % multipleOf !== 0) {
      push(`must be a multiple of ${multipleOf}`);
    }
  }
  if (typeof value === "string") {
    const { minLength, maxLength, pattern } = schema;
    const length = [...value].length;
    if (typeof minLength === "number" && length < minLength) {
      push(minLength === 1 ? "must not be empty" : `must be at least ${minLength} characters`);
    }
    if (typeof maxLength === "number" && length > maxLength) {
      push(`must be at most ${maxLength} characters`);
    }
    if (typeof pattern === "string") {
      try {
        if (!new RegExp(pattern, "u").test(value)) push(`must match ${pattern}`);
      } catch {
        // An invalid pattern is the schema author's problem; the server reports it.
      }
    }
  }
  if (Array.isArray(value)) {
    const { minItems, maxItems, uniqueItems } = schema;
    if (typeof minItems === "number" && value.length < minItems) {
      push(`must have at least ${minItems} item${minItems === 1 ? "" : "s"}`);
    }
    if (typeof maxItems === "number" && value.length > maxItems) {
      push(`must have at most ${maxItems} item${maxItems === 1 ? "" : "s"}`);
    }
    if (uniqueItems === true) {
      const seen = new Set(value.map((item) => JSON.stringify(item)));
      if (seen.size !== value.length) push("must not contain duplicates");
    }
    if (isSchema(schema.items)) {
      value.forEach((item, index) => {
        issues.push(
          ...validateValue(schema.items as JsonSchema, item, [...path, String(index)], depth + 1),
        );
      });
    }
  }
  if (isObject(value)) {
    const properties = isObject(schema.properties) ? schema.properties : {};
    const required = Array.isArray(schema.required)
      ? (schema.required as unknown[]).filter((item): item is string => typeof item === "string")
      : [];
    for (const key of required) {
      if (!(key in value)) push("is required", [...path, key]);
    }
    for (const [key, child] of Object.entries(value)) {
      const sub = properties[key];
      if (isSchema(sub)) {
        issues.push(...validateValue(sub, child, [...path, key], depth + 1));
      } else if (schema.additionalProperties === false) {
        push("is not a declared property", [...path, key]);
      } else if (isSchema(schema.additionalProperties)) {
        issues.push(
          ...validateValue(schema.additionalProperties, child, [...path, key], depth + 1),
        );
      }
    }
  }
  return issues;
}

/** Parses a number field's text; empty means "unset". */
export function parseNumberDraft(
  text: string,
  integer: boolean,
): { value: number | undefined; error: string | null } {
  const trimmed = text.trim();
  if (trimmed === "") return { value: undefined, error: null };
  const value = Number(trimmed);
  if (!Number.isFinite(value) || !/^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/.test(trimmed)) {
    return { value: undefined, error: "must be a number" };
  }
  if (integer && !Number.isInteger(value)) {
    return { value: undefined, error: "must be a whole number" };
  }
  return { value, error: null };
}

export function formatIssuePath(path: string[]): string {
  return path.length === 0 ? "value" : path.join(".");
}
