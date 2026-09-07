export interface SchemaDifference {
  path: string;
  change: "added" | "removed" | "changed";
}

/** Compare JSON Schema properties recursively; constraints are compared at
 * their owning field, so nested additions do not obscure their parent. */
export function schemaDifferences(before: string, after: string): SchemaDifference[] {
  const result: SchemaDifference[] = [];
  function visit(a: unknown, b: unknown, path: string) {
    if (a === undefined) {
      result.push({ path, change: "added" });
      return;
    }
    if (b === undefined) {
      result.push({ path, change: "removed" });
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
      result.push({ path: path || "(root)", change: "changed" });
    for (const key of [...new Set([...Object.keys(ap), ...Object.keys(bp)])].sort())
      visit(ap[key], bp[key], path ? `${path}.${key}` : key);
  }
  try {
    visit(JSON.parse(before || "{}"), JSON.parse(after), "");
  } catch {
    return [{ path: "Schema document", change: "changed" }];
  }
  return result;
}
