import { checkJson, tokenizeJson } from "./json-text";
import { type JsonSchema, unwrapNullable } from "./schema-form";

export interface PreparedUpgradeValue {
  value: string;
  added: string[];
  removed: string[];
}
const object = (v: unknown): v is JsonSchema =>
  v !== null && typeof v === "object" && !Array.isArray(v);
/** Prepare a draft without parsing/re-encoding scalar values (including large numbers). */
export function prepareUpgradeValue(
  value: string,
  schema: JsonSchema | null,
): PreparedUpgradeValue {
  const result: PreparedUpgradeValue = { value, added: [], removed: [] };
  if (!schema || checkJson(value)) return result;
  const tokens = tokenizeJson(value).filter((t) => t.kind !== "ws");
  let cursor = 0;
  function normalized(raw: unknown): JsonSchema | null {
    if (!object(raw)) return null;
    const s = unwrapNullable(raw) ?? raw;
    return ["$ref", "oneOf", "anyOf", "allOf", "if", "then", "else", "not"].some((k) => k in s)
      ? null
      : s;
  }
  function initial(raw: unknown): string | undefined {
    const s = normalized(raw);
    if (!s) return undefined;
    if ("default" in s) {
      const safe = (v: unknown): boolean =>
        typeof v === "number"
          ? Number.isFinite(v) && (!Number.isInteger(v) || Number.isSafeInteger(v))
          : Array.isArray(v)
            ? v.every(safe)
            : object(v)
              ? Object.values(v).every(safe)
              : true;
      return safe(s.default) ? JSON.stringify(s.default) : undefined;
    }
    if (s.type === "array") return "[]";
    if (s.type === "object" && object(s.properties)) {
      const parts: string[] = [];
      for (const key of Array.isArray(s.required) ? s.required : []) {
        if (typeof key !== "string") continue;
        const entry = initial(s.properties[key]);
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
        if (forbidden) {
          result.removed.push([...path, key].join("."));
          changed = true;
        } else {
          parts.push(`${value.slice(keyToken.start, keyToken.end)}:${child}`);
          changed ||= child !== original;
        }
        if (value[tokens[cursor].start] === ",") cursor++;
      }
      cursor++;
      for (const [key, childSchema] of Object.entries(properties)) {
        if (seen.has(key)) continue;
        const required = Array.isArray(s?.required) && s.required.includes(key);
        if (!required && !(object(childSchema) && "default" in childSchema)) continue;
        const child = initial(childSchema);
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
    return { value, added: [], removed: [] };
  }
  return result;
}
