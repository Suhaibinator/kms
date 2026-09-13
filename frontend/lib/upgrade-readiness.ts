/**
 * Readiness of one schema-upgrade draft field against the target schema.
 *
 * The migration wizard's "Needs attention" signal used to know about load
 * errors, bad versions and missing values, but not whether a preserved value
 * still satisfies the schema it is being carried into. This module runs the
 * console's local schema checks (`validateValue`) over the draft value exactly
 * as the server will type it, attributes every issue to the schema difference
 * that caused it, and maps server problems from the preview back to the nested
 * field they concern. Local checks are a subset of JSON Schema: they can miss a
 * problem but never invent one, so they inform without blocking the preview.
 */

import { checkJson } from "./json-text";
import type { StructuredSchemaDifference } from "./schema-diff";
import { aliasSchema, type ValidationIssue, validateValue } from "./schema-form";
import type { ReleaseValidationError } from "./types";
import type { UpgradeDraftField } from "./upgrade-field-changes";
import { validateParameterValue } from "./validation";

export type ReadinessStatus =
  | "ready"
  | "needs_value"
  | "needs_version"
  | "invalid_draft"
  | "fails_schema"
  | "loading"
  | "load_error"
  | "unchecked";

export type IssueCause =
  | "new_required"
  | "now_required"
  | "no_longer_allowed"
  | "type_changed"
  | "constraint_changed"
  | "undeclared"
  | "unchanged_rule";

export interface ReadinessIssue {
  /** Path inside the alias value; `[]` is the value itself. */
  path: string[];
  message: string;
  cause: IssueCause;
}

export interface FieldReadiness {
  status: ReadinessStatus;
  issues: ReadinessIssue[];
  /** Short, value-free description for summaries and jump lists. */
  summary: string;
}

export const READINESS_LABEL: Record<ReadinessStatus, string> = {
  ready: "Passes local checks",
  needs_value: "Value needed",
  needs_version: "Version needed",
  invalid_draft: "Invalid draft",
  fails_schema: "Fails target schema",
  loading: "Loading…",
  load_error: "Load failed",
  unchecked: "Not checked locally",
};

export const READINESS_TONE: Record<ReadinessStatus, "success" | "warning" | "danger" | "neutral"> =
  {
    ready: "success",
    needs_value: "warning",
    needs_version: "warning",
    invalid_draft: "danger",
    fails_schema: "danger",
    loading: "neutral",
    load_error: "danger",
    unchecked: "neutral",
  };

export const CAUSE_COPY: Record<IssueCause, string> = {
  new_required: "new required field",
  now_required: "now required",
  no_longer_allowed: "no longer allowed by the schema",
  type_changed: "type changed",
  constraint_changed: "constraint changed",
  undeclared: "not declared by the target schema",
  unchanged_rule: "existing rule",
};

export type SchemaValue =
  | { ok: true; value: unknown }
  | { ok: false; reason: "unparsable" | "unchecked" };

// Go's strconv.ParseBool true spellings; `validateParameterValue` has already
// rejected anything outside the full literal set.
const TRUE_LITERALS = new Set(["1", "t", "T", "TRUE", "true", "True"]);

/**
 * Coerces a draft string to the JSON value the server validates for its
 * content type (`core.parameterSchemaValue`). `unchecked` marks values the
 * local checker cannot represent faithfully: unsafe integers, Go float
 * spellings JS cannot parse, and binary.
 */
export function schemaValueFor(value: string, contentType: string): SchemaValue {
  if (validateParameterValue(value, contentType) !== null) {
    return { ok: false, reason: "unparsable" };
  }
  const trimmed = value.trim();
  switch (contentType) {
    case "json": {
      if (checkJson(value)) return { ok: false, reason: "unparsable" };
      try {
        return { ok: true, value: JSON.parse(value) };
      } catch {
        return { ok: false, reason: "unparsable" };
      }
    }
    case "integer": {
      const parsed = Number(trimmed);
      return Number.isSafeInteger(parsed)
        ? { ok: true, value: parsed }
        : { ok: false, reason: "unchecked" };
    }
    case "float": {
      const parsed = Number(trimmed);
      return Number.isFinite(parsed)
        ? { ok: true, value: parsed }
        : { ok: false, reason: "unchecked" };
    }
    case "boolean":
      return { ok: true, value: TRUE_LITERALS.has(trimmed) };
    case "string":
      return { ok: true, value };
    default:
      return { ok: false, reason: "unchecked" };
  }
}

function sameSegments(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((segment, index) => segment === b[index]);
}

/** Attributes a local issue to the schema difference that introduced its rule. */
export function causeFor(
  issue: ValidationIssue,
  alias: string,
  fromAlias: string | undefined,
  differences: readonly StructuredSchemaDifference[],
): IssueCause {
  const at = (segments: string[]) =>
    differences.find((difference) => sameSegments(difference.segments, segments));
  const own =
    at([alias, ...issue.path]) ??
    (fromAlias && fromAlias !== alias ? at([fromAlias, ...issue.path]) : undefined);
  const ancestor = (): StructuredSchemaDifference | undefined => {
    for (let length = issue.path.length - 1; length >= 0; length--) {
      const found = at([alias, ...issue.path.slice(0, length)]);
      if (found) return found;
    }
    return undefined;
  };
  const { message } = issue;
  if (message === "is required") {
    if (own?.change === "added") return "new_required";
    if (own?.change === "changed") return "now_required";
    return "unchanged_rule";
  }
  if (message === "is not a declared property") {
    return own?.change === "removed" ? "no_longer_allowed" : "undeclared";
  }
  if (message.startsWith("must be ") && message.includes(", got ")) {
    return own ? "type_changed" : "unchanged_rule";
  }
  return own || ancestor() ? "constraint_changed" : "unchanged_rule";
}

const VERSION_RE = /^[1-9]\d*$/;

function versionMalformed(text: string): boolean {
  return text !== "" && (!VERSION_RE.test(text) || !Number.isSafeInteger(Number(text)));
}

function plain(status: ReadinessStatus): FieldReadiness {
  return { status, issues: [], summary: READINESS_LABEL[status] };
}

/**
 * The readiness of one draft field. Status precedence, most blocking first:
 * load_error → loading → needs_version → needs_value → invalid_draft →
 * unchecked → fails_schema → ready. Secrets are references only, so they are
 * ready once an exact version is pinned.
 */
export function fieldReadiness(
  field: UpgradeDraftField,
  targetSchemaJson: string | undefined,
  differences: readonly StructuredSchemaDifference[],
  draftValid?: boolean,
): FieldReadiness {
  if (field.kind === "secret") {
    return field.version && !versionMalformed(field.versionText)
      ? plain("ready")
      : plain("needs_version");
  }
  if (field.loadError) return plain("load_error");
  if (field.loading || (!field.loaded && field.version)) return plain("loading");
  if (versionMalformed(field.versionText)) return plain("needs_version");
  if (!field.version && (field.value === undefined || field.value === "")) {
    return plain("needs_value");
  }
  const value = field.value;
  if (value === undefined) return plain("unchecked");
  const contentType = field.content_type ?? "string";
  if (draftValid === false || validateParameterValue(value, contentType) !== null) {
    return plain("invalid_draft");
  }
  const schema = aliasSchema(targetSchemaJson, field.alias.trim());
  if (!schema) return plain("unchecked");
  const coerced = schemaValueFor(value, contentType);
  if (!coerced.ok) return plain(coerced.reason === "unparsable" ? "invalid_draft" : "unchecked");
  const issues = validateValue(schema, coerced.value).map((issue) => ({
    path: issue.path,
    message: issue.message,
    cause: causeFor(issue, field.alias.trim(), field.fromAlias, differences),
  }));
  if (issues.length === 0) return plain("ready");
  const summary = issues.every((issue) => issue.path.length === 0)
    ? "Value fails the target schema"
    : `${issues.length} field${issues.length === 1 ? "" : "s"} fail${issues.length === 1 ? "s" : ""} the target schema`;
  return { status: "fails_schema", issues, summary };
}

/**
 * A `fieldReadiness` that returns the previous result while a field's inputs
 * are unchanged, so row components keep referential equality between renders.
 * Changing the target schema or the difference list discards every entry.
 */
export function createReadinessCache(): typeof fieldReadiness {
  const entries = new Map<number, { key: string; result: FieldReadiness }>();
  let schema: string | undefined;
  let diffs: readonly StructuredSchemaDifference[] | undefined;
  return (field, targetSchemaJson, differences, draftValid) => {
    if (targetSchemaJson !== schema || differences !== diffs) {
      entries.clear();
      schema = targetSchemaJson;
      diffs = differences;
    }
    const key = JSON.stringify([
      field.alias,
      field.fromAlias,
      field.kind,
      field.content_type,
      field.value,
      field.version,
      field.versionText,
      field.loaded,
      field.loading,
      field.loadError,
      draftValid,
    ]);
    const cached = entries.get(field.id);
    if (cached && cached.key === key) return cached.result;
    const result = fieldReadiness(field, targetSchemaJson, differences, draftValid);
    entries.set(field.id, { key, result });
    return result;
  };
}

function unescapePointer(pointer: string): string[] {
  return pointer
    .split("/")
    .slice(1)
    .map((segment) => segment.replace(/~1/g, "/").replace(/~0/g, "~"));
}

const KEYWORD_MATCHERS: Record<string, (message: string) => boolean> = {
  required: (m) => m === "is required",
  additionalProperties: (m) => m === "is not a declared property",
  type: (m) => m.startsWith("must be ") && m.includes(", got "),
  minimum: (m) => m.startsWith("must be at least ") && !m.endsWith(" characters"),
  maximum: (m) => m.startsWith("must be at most ") && !m.endsWith(" characters"),
  exclusiveMinimum: (m) => m.startsWith("must be greater than "),
  exclusiveMaximum: (m) => m.startsWith("must be less than "),
  multipleOf: (m) => m.startsWith("must be a multiple of "),
  minLength: (m) =>
    m === "must not be empty" || (m.startsWith("must be at least ") && m.endsWith(" characters")),
  maxLength: (m) => m.startsWith("must be at most ") && m.endsWith(" characters"),
  pattern: (m) => m.startsWith("must match "),
  enum: (m) => m.startsWith("must be one of "),
  const: (m) => m.startsWith("must equal "),
  minItems: (m) => m.startsWith("must have at least "),
  maxItems: (m) => m.startsWith("must have at most "),
  uniqueItems: (m) => m === "must not contain duplicates",
};

/**
 * The nested path a server validation problem refers to, relative to the alias
 * value. Uses `instance_pointer` when the server sends it; otherwise matches
 * the keyword in `schema_pointer` (and any field names the message quotes)
 * against the local issues, so a preview problem can focus the same control
 * the local checks flagged. `null` when nothing can be inferred.
 */
export function matchProblemPath(
  problem: ReleaseValidationError,
  issues: readonly ReadinessIssue[],
): string[] | null {
  if (problem.instance_pointer) {
    const segments = unescapePointer(problem.instance_pointer);
    return segments.length ? segments.slice(1) : null;
  }
  const pointer = problem.schema_pointer.split("/").filter(Boolean);
  const keyword = pointer[pointer.length - 1];
  const hint: string[] = [];
  for (let index = 0; index + 1 < pointer.length; index++) {
    if (pointer[index] === "properties") hint.push(pointer[++index]);
  }
  if (hint[0] === problem.alias) hint.shift();
  const quoted = [...problem.message.matchAll(/"([^"]+)"/g)].map((match) => match[1]);
  const matches = keyword ? KEYWORD_MATCHERS[keyword] : undefined;
  const candidates = matches
    ? issues.filter(
        (issue) =>
          matches(issue.message) &&
          (keyword !== "required" ||
            quoted.length === 0 ||
            quoted.includes(issue.path[issue.path.length - 1] ?? "")),
      )
    : [];
  const preferred =
    candidates.find((issue) => hint.every((segment, index) => issue.path[index] === segment)) ??
    candidates[0];
  if (preferred) return preferred.path;
  if (keyword === "required" && quoted.length) return [...hint, quoted[0]];
  return hint.length ? hint : null;
}
