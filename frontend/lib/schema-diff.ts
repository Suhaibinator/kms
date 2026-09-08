export interface SchemaDifference {
  path: string;
  change: "added" | "removed" | "changed";
}

/** Compare JSON Schema properties recursively; constraints are compared at
 * their owning field, so nested additions do not obscure their parent. */
export interface StructuredSchemaDifference extends SchemaDifference {
  segments: string[];
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
    visit(JSON.parse(before || "{}"), JSON.parse(after), []);
  } catch {
    return [{ path: "Schema document", segments: [], change: "changed" }];
  }
  return result;
}

export function schemaDifferences(before: string, after: string): SchemaDifference[] {
  return structuredSchemaDifferences(before, after).map(({ path, change }) => ({ path, change }));
}
