import { checkJson, tokenizeJson } from "./json-text";
import { arrayAllowsEmpty, type JsonSchema, unwrapNullable, validateValue } from "./schema-form";

export interface ArrayMigration {
  id: string;
  from: string;
  to: string;
  /** Undefined means the source needs manual editing, rather than a suggested conversion. */
  value?: string;
  reason: string;
}

export interface PreparedUpgradeValue {
  value: string;
  added: string[];
  removed: string[];
  initializedLists: string[];
  appliedDefaults: string[];
  migrations: ArrayMigration[];
  suggestions: ArrayMigration[];
}
const object = (v: unknown): v is JsonSchema =>
  v !== null && typeof v === "object" && !Array.isArray(v);
/** Prepare a draft without parsing/re-encoding scalar values (including large numbers). */
export function prepareUpgradeValue(
  value: string,
  schema: JsonSchema | string | null,
  alias?: string,
  acceptedMigrations: readonly string[] = [],
): PreparedUpgradeValue {
  const unchanged = (): PreparedUpgradeValue => ({
    value,
    added: [],
    removed: [],
    initializedLists: [],
    appliedDefaults: [],
    migrations: [],
    suggestions: [],
  });
  const result = unchanged();
  if (!schema || checkJson(value)) return result;
  const defaults = new WeakMap<JsonSchema, string>();
  if (typeof schema === "string") {
    const source = schema;
    if (checkJson(source)) return result;
    try {
      const parsed: unknown = JSON.parse(source);
      if (!object(parsed)) return result;
      const sourceTokens = tokenizeJson(source).filter((token) => token.kind !== "ws");
      let position = 0;
      // Associate each schema object's default with its original JSON slice,
      // including nested objects/arrays and numbers JSON.parse cannot represent.
      function collect(node: unknown): void {
        const token = sourceTokens[position++];
        const opening = source.slice(token.start, token.end);
        if (opening === "{") {
          while (source[sourceTokens[position].start] !== "}") {
            const keyToken = sourceTokens[position++];
            const key = JSON.parse(source.slice(keyToken.start, keyToken.end)) as string;
            position++; // colon
            const start = sourceTokens[position].start;
            collect(object(node) ? node[key] : undefined);
            if (key === "default" && object(node))
              defaults.set(node, source.slice(start, sourceTokens[position - 1].end));
            if (source[sourceTokens[position].start] === ",") position++;
          }
          position++;
        } else if (opening === "[") {
          let index = 0;
          while (source[sourceTokens[position].start] !== "]") {
            collect(Array.isArray(node) ? node[index++] : undefined);
            if (source[sourceTokens[position].start] === ",") position++;
          }
          position++;
        }
      }
      collect(parsed);
      schema =
        alias === undefined
          ? parsed
          : object(parsed.properties) && object(parsed.properties[alias])
            ? parsed.properties[alias]
            : null;
    } catch {
      return result;
    }
    if (!schema) return result;
  }
  const tokens = tokenizeJson(value).filter((t) => t.kind !== "ws");
  let cursor = 0;
  function normalized(raw: unknown): JsonSchema | null {
    if (!object(raw)) return null;
    const s = unwrapNullable(raw) ?? raw;
    if (s !== raw && Array.isArray(raw.anyOf)) {
      const inner = raw.anyOf.find((branch) => object(branch) && branch.type !== "null");
      const owner = object(inner) && "default" in inner ? inner : raw;
      const exact = defaults.get(owner);
      if (exact !== undefined) defaults.set(s, exact);
    }
    return ["$ref", "oneOf", "anyOf", "allOf", "if", "then", "else", "not"].some((k) => k in s)
      ? null
      : s;
  }
  function initial(raw: unknown, path: string[]): string | undefined {
    const s = normalized(raw);
    if (!s) return undefined;
    if ("default" in s) {
      const exact = defaults.get(s);
      const safe = (v: unknown): boolean =>
        typeof v === "number"
          ? false // Parsed schema numbers may already be rounded or underflowed; raw tokens are unavailable.
          : Array.isArray(v)
            ? v.every(safe)
            : object(v)
              ? Object.values(v).every(safe)
              : true;
      const candidate = exact ?? (safe(s.default) ? JSON.stringify(s.default) : undefined);
      if (candidate === undefined) return undefined;
      const defaultValue: unknown = JSON.parse(candidate);
      if (
        validateValue(raw as JsonSchema, defaultValue).length > 0 ||
        (Array.isArray(defaultValue) && "contains" in s)
      )
        return undefined;
      result.appliedDefaults.push(path.join("."));
      return candidate;
    }
    if (arrayAllowsEmpty(s)) {
      result.initializedLists.push(path.join("."));
      return "[]";
    }
    if (s.type === "object" && object(s.properties)) {
      const parts: string[] = [];
      for (const key of Array.isArray(s.required) ? s.required : []) {
        if (typeof key !== "string") continue;
        const entry = initial(s.properties[key], [...path, key]);
        if (entry !== undefined) parts.push(`${JSON.stringify(key)}:${entry}`);
      }
      return `{${parts.join(",")}}`;
    }
    return undefined;
  }
  function walk(raw: unknown, path: string[]): string {
    const start = tokens[cursor].start;
    const s = normalized(raw);
    const ch = value.slice(tokens[cursor].start, tokens[cursor].end);
    if (ch === "{") {
      cursor++;
      const properties = s && object(s.properties) ? s.properties : {};
      const seen = new Set<string>();
      const parts: string[] = [];
      const entries: Array<{
        key: string;
        keyText: string;
        child: string;
        original: string;
        forbidden: boolean;
      }> = [];
      let changed = false;
      while (value[tokens[cursor].start] !== "}") {
        const keyToken = tokens[cursor++];
        const key = JSON.parse(value.slice(keyToken.start, keyToken.end)) as string;
        seen.add(key);
        cursor++; // colon
        const childStart = tokens[cursor].start;
        const hasProperty = Object.hasOwn(properties, key);
        // Pattern-based objects are left intact for backend validation.
        const forbidden = s?.additionalProperties === false && !hasProperty && !s.patternProperties;
        const child = walk(
          forbidden ? null : hasProperty ? properties[key] : s?.additionalProperties,
          [...path, key],
        );
        const original = value.slice(childStart, tokens[cursor - 1].end);
        entries.push({
          key,
          keyText: value.slice(keyToken.start, keyToken.end),
          child,
          original,
          forbidden,
        });
        if (value[tokens[cursor].start] === ",") cursor++;
      }
      cursor++;
      const converted = new Map<string, string>();
      const protectedSources = new Set<string>();
      const pendingTargets = new Set<string>();
      for (const [key, rawChild] of Object.entries(properties)) {
        if (seen.has(key)) continue;
        const childSchema = normalized(rawChild);
        if (
          !childSchema ||
          !(
            childSchema.type === "array" ||
            (Array.isArray(childSchema.type) && childSchema.type.includes("array"))
          )
        )
          continue;
        const declared =
          typeof childSchema["x-kms-migrate-from"] === "string"
            ? childSchema["x-kms-migrate-from"]
            : undefined;
        const sourceKey = declared ?? (key.endsWith("s") ? key.slice(0, -1) : undefined);
        const source = entries.find((entry) => entry.key === sourceKey);
        // Name-based suggestions only move fields the target no longer declares.
        if (!source || (!declared && Object.hasOwn(properties, source.key))) continue;
        const sourceValue: unknown = JSON.parse(source.original);
        const itemSchema = object(childSchema.items) ? normalized(childSchema.items) : null;
        if (!declared && itemSchema?.type !== "string") continue;
        const singleton =
          typeof sourceValue === "string" && sourceValue !== ""
            ? `[${source.original}]`
            : undefined;
        const candidate =
          singleton &&
          !["contains", "prefixItems", "unevaluatedItems"].some(
            (keyword) => keyword in childSchema,
          ) &&
          validateValue(childSchema, [sourceValue]).length === 0
            ? singleton
            : sourceValue === "" && arrayAllowsEmpty(childSchema)
              ? "[]"
              : undefined;
        const migration: ArrayMigration = {
          id: JSON.stringify([...path, key]),
          from: [...path, source.key].join("."),
          to: [...path, key].join("."),
          value: candidate,
          reason:
            candidate === "[]"
              ? "The old value is an empty string. Discard it and use an empty list."
              : candidate
                ? "Keep the existing string as one list item."
                : "The old value cannot be converted automatically. Edit the target field before removing the old field.",
        };
        if (
          candidate !== undefined &&
          ((declared && singleton) || acceptedMigrations.includes(migration.id))
        ) {
          converted.set(key, candidate);
          result.migrations.push(migration);
        } else {
          result.suggestions.push(migration);
          protectedSources.add(source.key);
          pendingTargets.add(key);
        }
      }
      for (const entry of entries) {
        if (entry.forbidden && !protectedSources.has(entry.key)) {
          result.removed.push([...path, entry.key].join("."));
          changed = true;
        } else {
          parts.push(`${entry.keyText}:${entry.child}`);
          changed ||= entry.child !== entry.original;
        }
      }
      for (const [key, childSchema] of Object.entries(properties)) {
        if (seen.has(key) || pendingTargets.has(key)) continue;
        const required = Array.isArray(s?.required) && s.required.includes(key);
        if (!converted.has(key) && !required && !(object(childSchema) && "default" in childSchema))
          continue;
        const child = converted.get(key) ?? initial(childSchema, [...path, key]);
        if (child !== undefined) {
          parts.push(`${JSON.stringify(key)}:${child}`);
          result.added.push([...path, key].join("."));
          changed = true;
        }
      }
      return changed ? `{${parts.join(",")}}` : value.slice(start, tokens[cursor - 1].end);
    }
    if (ch === "[") {
      cursor++;
      const parts: string[] = [];
      let changed = false;
      while (value[tokens[cursor].start] !== "]") {
        const childStart = tokens[cursor].start;
        // Tuple prefix positions have their own schemas; `items` applies only after them.
        const index = parts.length;
        const itemSchema =
          Array.isArray(s?.prefixItems) && index < s.prefixItems.length
            ? s.prefixItems[index]
            : s?.items;
        const child = walk(itemSchema, [...path, String(index)]);
        changed ||= child !== value.slice(childStart, tokens[cursor - 1].end);
        parts.push(child);
        if (value[tokens[cursor].start] === ",") cursor++;
      }
      cursor++;
      return changed ? `[${parts.join(",")}]` : value.slice(start, tokens[cursor - 1].end);
    }
    cursor++;
    return value.slice(start, tokens[cursor - 1].end);
  }
  try {
    const prepared = walk(schema, []);
    if (result.added.length || result.removed.length) result.value = prepared;
  } catch {
    return unchanged();
  }
  return result;
}
