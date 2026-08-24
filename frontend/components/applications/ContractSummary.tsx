import { Badge } from "@/components/ui";
import type { Application } from "@/lib/types";

export function ContractSummary({ application }: { application: Application }) {
  return (
    <div className="card mb-4 application-contract">
      <div>
        <span className="faint text-sm">Canonical release</span>
        <strong className="mono">{application.release_name}</strong>
      </div>
      <div>
        <span className="faint text-sm">Schema</span>
        <strong className="mono">
          {application.schema_id
            ? `${application.schema_id}@${application.schema_version}`
            : "Not pinned"}
        </strong>
      </div>
      <div>
        <span className="faint text-sm">Shared shape</span>
        <div className="row-wrap">
          {application.contract.length ? (
            application.contract.map((field) => (
              <Badge key={field.alias} kind={field.kind === "secret" ? "warning" : "neutral"}>
                {field.alias}: {field.kind}
                {field.content_type ? `/${field.content_type}` : ""}
              </Badge>
            ))
          ) : (
            <span className="faint">No enforced aliases</span>
          )}
        </div>
      </div>
    </div>
  );
}
