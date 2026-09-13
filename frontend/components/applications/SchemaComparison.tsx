import { useMemo } from "react";
import { describeSchemaEffect, structuredSchemaDifferences } from "@/lib/schema-diff";
import { parseSchema } from "@/lib/schema-form";
import type { ConfigurationSchema } from "@/lib/types";

export function SchemaComparison({
  current,
  target,
  currentVersion,
}: {
  current?: ConfigurationSchema;
  target?: ConfigurationSchema;
  currentVersion: number;
}) {
  const currentJson = current?.schema_json;
  const targetJson = target?.schema_json;
  const comparable = Boolean(current) || currentVersion === 0;
  const rows = useMemo(() => {
    if (targetJson === undefined || !comparable) return null;
    const before = parseSchema(currentJson ?? "{}");
    const after = parseSchema(targetJson);
    return structuredSchemaDifferences(currentJson ?? "{}", targetJson).map((difference) => ({
      ...difference,
      effect: describeSchemaEffect(difference, before, after).text,
    }));
  }, [currentJson, targetJson, comparable]);
  if (!target) return null;
  return (
    <details className="card p-4">
      <summary className="cursor-pointer">
        Compare current v{currentVersion} → target v{target.version}
      </summary>
      {rows ? (
        rows.length > 0 ? (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Field or schema path</th>
                  <th>Change</th>
                  <th>Effect on values</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((d) => (
                  <tr key={`${d.path}:${d.change}`}>
                    <td className="mono" data-label="Field or schema path">
                      {d.path}
                    </td>
                    <td data-label="Change">{d.change}</td>
                    <td data-label="Effect on values">{d.effect}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p>No schema document changes.</p>
        )
      ) : (
        <p>Current schema document unavailable. Target schema shown below.</p>
      )}
      <p className="faint text-sm">
        Schema differences describe fields and constraints. The preview validates your actual
        values.
      </p>
      <details>
        <summary className="cursor-pointer">View target schema</summary>
        <pre className="overflow-auto text-xs">{target.schema_json}</pre>
      </details>
    </details>
  );
}
