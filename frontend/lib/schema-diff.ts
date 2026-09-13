import { tokenizeJson } from "./json-text";
import { type JsonSchema, unwrapNullable } from "./schema-form";

export interface SchemaDifference {
  path: string;
  change: "added" | "removed" | "changed";
}

/** Compare JSON Schema properties recursively; constraints are compared at
 * their owning field, so nested additions do not obscure their parent. */
export interface StructuredSchemaDifference extends SchemaDifference {
  segments: string[];
}

/** A number marker must not also be a user-supplied object key in either schema. */
function unusedNumberKey(documents: string[]): string {
  const keys = new Set<string>();
  for (const document of documents) {
    for (const token of tokenizeJson(document)) {
      if (token.kind === "key") keys.add(JSON.parse(document.slice(token.start, token.end)));
    }
  }
  let key = "\u0000kms.schema-number";
  while (keys.has(key)) key += "_";
  return key;
}

/** Preserve JSON number tokens before JSON.parse can round them to IEEE-754 values. */
function parseSchema(document: string, numberKey: string): unknown {
  let encoded = "";
  for (const token of tokenizeJson(document)) {
    if (token.kind === "error") throw new Error("Invalid schema JSON");
    const raw = document.slice(token.start, token.end);
    encoded += token.kind === "number" ? JSON.stringify({ [numberKey]: raw }) : raw;
  }
  return JSON.parse(encoded);
}

export function structuredSchemaDifferences(
  before: string,
  after: string,
): StructuredSchemaDifference[] {
  const result: StructuredSchemaDifference[] = [];
  function visit(a: unknown, b: unknown, segments: string[]) {
    const path = segments.join(".");
    if (a === undefined) {
      result.push({ path, segments, change: "added" });
      const properties =
        b && typeof b === "object" ? (b as Record<string, unknown>).properties : undefined;
      if (properties && typeof properties === "object" && !Array.isArray(properties))
        for (const [key, value] of Object.entries(properties))
          visit(undefined, value, [...segments, key]);
      return;
    }
    if (b === undefined) {
      result.push({ path, segments, change: "removed" });
      const properties =
        a && typeof a === "object" ? (a as Record<string, unknown>).properties : undefined;
      if (properties && typeof properties === "object" && !Array.isArray(properties))
        for (const [key, value] of Object.entries(properties))
          visit(value, undefined, [...segments, key]);
      return;
    }
    const obj = (v: unknown): Record<string, unknown> =>
      v !== null && typeof v === "object" && !Array.isArray(v)
        ? (v as Record<string, unknown>)
        : {};
    const aa = obj(a),
      bb = obj(b);
    const ap = obj(aa.properties),
      bp = obj(bb.properties);
    const canonical = (v: unknown): string => {
      if (Array.isArray(v)) return `[${v.map(canonical).join(",")}]`;
      if (v !== null && typeof v === "object")
        return `{${Object.entries(v)
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([k, value]) => `${JSON.stringify(k)}:${canonical(value)}`)
          .join(",")}}`;
      return JSON.stringify(v);
    };
    const constraints = (v: Record<string, unknown>) =>
      Object.fromEntries(Object.entries(v).filter(([k]) => k !== "properties"));
    if (
      canonical(constraints(aa)) !== canonical(constraints(bb)) ||
      ((typeof a !== "object" || typeof b !== "object") && a !== b)
    )
      result.push({ path: path || "(root)", segments, change: "changed" });
    const requiredA = new Set(Array.isArray(aa.required) ? aa.required : []);
    const requiredB = new Set(Array.isArray(bb.required) ? bb.required : []);
    for (const key of [...new Set([...Object.keys(ap), ...Object.keys(bp)])].sort()) {
      const child = [...segments, key];
      const start = result.length;
      visit(ap[key], bp[key], child);
      if (requiredA.has(key) !== requiredB.has(key) && result.length === start)
        result.push({ path: child.join("."), segments: child, change: "changed" });
    }
    if (aa.items !== undefined || bb.items !== undefined)
      visit(aa.items, bb.items, [...segments, "[]"]);
    const prefixA = Array.isArray(aa.prefixItems) ? aa.prefixItems : [];
    const prefixB = Array.isArray(bb.prefixItems) ? bb.prefixItems : [];
    for (let index = 0; index < Math.max(prefixA.length, prefixB.length); index++)
      visit(prefixA[index], prefixB[index], [...segments, String(index)]);
  }
  try {
    const numberKey = unusedNumberKey([before || "{}", after]);
    visit(parseSchema(before || "{}", numberKey), parseSchema(after, numberKey), []);
  } catch {
    return [{ path: "Schema document", segments: [], change: "changed" }];
  }
  return result;
}

export function schemaDifferences(before: string, after: string): SchemaDifference[] {
  return structuredSchemaDifferences(before, after).map(({ path, change }) => ({ path, change }));
}

// --- Effect of a difference on values ----------------------------------------------

export type SchemaEffect = {
  kind: "provide" | "optional" | "remove" | "ignored" | "review" | "none";
  text: string;
};

const isSchemaObject = (value: unknown): value is JsonSchema =>
  value !== null && typeof value === "object" && !Array.isArray(value);

const unwrap = (schema: JsonSchema): JsonSchema => unwrapNullable(schema) ?? schema;

/**
 * Resolves a structured difference path (`properties` names, `[]` for `items`,
 * an index for `prefixItems`) inside a parsed schema, unwrapping the
 * generator's nullable wrapper on the way down.
 */
export function schemaNodeAt(
  root: JsonSchema | null,
  segments: readonly string[],
): { node: JsonSchema | null; parent: JsonSchema | null; key: string | null } {
  let node: JsonSchema | null = root ? unwrap(root) : null;
  let parent: JsonSchema | null = null;
  let key: string | null = null;
  for (const segment of segments) {
    if (!node) return { node: null, parent: null, key: null };
    parent = node;
    key = segment;
    const properties = isSchemaObject(node.properties) ? node.properties : {};
    let next: unknown;
    if (segment in properties) next = properties[segment];
    else if (segment === "[]") next = node.items;
    else if (/^\d+$/.test(segment) && Array.isArray(node.prefixItems))
      next = node.prefixItems[Number(segment)];
    node = isSchemaObject(next) ? unwrap(next) : null;
  }
  return { node, parent, key };
}

const requiredIn = (parent: JsonSchema | null, key: string | null): boolean =>
  Boolean(
    parent && key !== null && Array.isArray(parent.required) && parent.required.includes(key),
  );

const DOCUMENTATION_KEYWORDS = new Set(["description", "title", "examples", "$comment"]);

const canonical = (value: unknown): string => {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value !== null && typeof value === "object")
    return `{${Object.entries(value)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => `${JSON.stringify(k)}:${canonical(v)}`)
      .join(",")}}`;
  return JSON.stringify(value);
};

function changedKeywords(a: JsonSchema | null, b: JsonSchema | null): string[] {
  const keys = new Set([...Object.keys(a ?? {}), ...Object.keys(b ?? {})]);
  keys.delete("properties");
  return [...keys].filter((key) => canonical(a?.[key]) !== canonical(b?.[key])).sort();
}

function describeRequired(a: JsonSchema | null, b: JsonSchema | null): string {
  const names = (schema: JsonSchema | null) =>
    new Set(
      Array.isArray(schema?.required)
        ? schema.required.filter((item): item is string => typeof item === "string")
        : [],
    );
  const before = names(a);
  const after = names(b);
  const added = [...after].filter((name) => !before.has(name));
  const removed = [...before].filter((name) => !after.has(name));
  const parts: string[] = [];
  if (added.length) parts.push(`now requires ${added.join(", ")}`);
  if (removed.length) parts.push(`no longer requires ${removed.join(", ")}`);
  return parts.join("; ") || "required changed";
}

/**
 * What one schema difference means for a stored value: whether the operator
 * must provide, remove or review something, or nothing at all.
 */
export function describeSchemaEffect(
  diff: StructuredSchemaDifference,
  before: JsonSchema | null,
  after: JsonSchema | null,
): SchemaEffect {
  if (!diff.segments.length) return { kind: "review", text: "Root constraints changed" };
  const top = diff.segments.length === 1;
  const a = schemaNodeAt(before, diff.segments);
  const b = schemaNodeAt(after, diff.segments);
  if (diff.change === "added") {
    if (requiredIn(b.parent, b.key)) {
      return {
        kind: "provide",
        text: top ? "New contract field — give it a value in Edit values" : "Provide a value",
      };
    }
    const fallback =
      b.node && "default" in b.node ? ` · default: ${JSON.stringify(b.node.default)}` : "";
    return { kind: "optional", text: `Optional${fallback}` };
  }
  if (diff.change === "removed") {
    if (top) return { kind: "ignored", text: "Leaves the release" };
    if (b.parent?.additionalProperties === false) {
      return { kind: "remove", text: "Remove it from the value" };
    }
    return { kind: "ignored", text: "Ignored by the target schema" };
  }
  const wasRequired = requiredIn(a.parent, a.key);
  const isRequired = requiredIn(b.parent, b.key);
  const keywords = changedKeywords(a.node, b.node);
  const meaningful = keywords.filter((keyword) => !DOCUMENTATION_KEYWORDS.has(keyword));
  const detail = meaningful
    .map((keyword) => (keyword === "required" ? describeRequired(a.node, b.node) : keyword))
    .join(", ");
  if (isRequired && !wasRequired) {
    return {
      kind: "provide",
      text: detail
        ? `Now required — provide a value · ${detail}`
        : "Now required — provide a value",
    };
  }
  if (wasRequired && !isRequired) {
    return { kind: "optional", text: detail ? `Now optional · ${detail}` : "Now optional" };
  }
  if (!keywords.length) return { kind: "review", text: "Constraints changed" };
  if (!meaningful.length) return { kind: "none", text: "Documentation only" };
  return {
    kind: "review",
    text: meaningful.every((keyword) => keyword === "required")
      ? detail
      : `${meaningful.filter((k) => k !== "required").join(", ")} changed${meaningful.includes("required") ? ` · ${describeRequired(a.node, b.node)}` : ""}`,
  };
}
