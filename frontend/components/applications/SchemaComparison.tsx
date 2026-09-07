import { schemaDifferences } from "@/lib/schema-diff";
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
  if (!target) return null;
  const differences =
    current || currentVersion === 0
      ? schemaDifferences(current?.schema_json ?? "{}", target.schema_json)
      : null;
  return (
    <details className="card p-4">
      <summary className="cursor-pointer">
        Compare current v{currentVersion} → target v{target.version}
      </summary>
      {differences ? (
        differences.length > 0 ? (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Field or schema path</th>
                  <th>Change</th>
                </tr>
              </thead>
              <tbody>
                {differences.map((d) => (
                  <tr key={`${d.path}:${d.change}`}>
                    <td className="mono">{d.path}</td>
                    <td>{d.change}</td>
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
