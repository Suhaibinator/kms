import { Badge, Button } from "@/components/ui";
import type { ReleaseValidationError } from "@/lib/types";

export interface ActivationFailure {
  operation: "Activation" | "Rollback" | "Validation";
  target: string;
  violations: ReleaseValidationError[];
}

/** Schema violations from validate/activate/rollback, one row per error. */
export function ViolationTable({ violations }: { violations: ReleaseValidationError[] }) {
  return (
    <div className="table-wrap mt-3">
      <table className="data">
        <thead>
          <tr>
            <th>Alias</th>
            <th>Code</th>
            <th>Schema pointer</th>
            <th>Message</th>
          </tr>
        </thead>
        <tbody>
          {violations.map((violation) => (
            <tr key={JSON.stringify(violation)}>
              <td className="mono">{violation.alias || "release"}</td>
              <td>
                <Badge kind="danger">{violation.code}</Badge>
              </td>
              <td className="mono">{violation.schema_pointer || "—"}</td>
              <td>{violation.message}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The red panel a failed validation/activation/rollback opens, with its violations. */
export function ActivationFailurePanel({
  failure,
  onDismiss,
}: {
  failure: ActivationFailure;
  onDismiss: () => void;
}) {
  return (
    <section className="danger-panel mb-4" role="alert">
      <div className="between">
        <div>
          <strong>
            {failure.operation === "Validation"
              ? `${failure.target} failed validation`
              : `${failure.operation} blocked for ${failure.target}`}
          </strong>
          <div className="text-sm mt-2">
            {failure.operation === "Validation"
              ? "Resolve every violation before activating this release."
              : "The active release and activation revision were not changed."}
          </div>
        </div>
        <Button variant="outline" size="sm" onClick={onDismiss}>
          Dismiss
        </Button>
      </div>
      <ViolationTable violations={failure.violations} />
    </section>
  );
}
