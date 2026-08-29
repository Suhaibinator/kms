import Link from "next/link";
import { Badge, Button } from "@/components/ui";
import type { ReleaseValidationError } from "@/lib/types";

export interface ActivationFailure {
  operation: "Activation" | "Rollback" | "Validation";
  target: string;
  violations: ReleaseValidationError[];
}

export interface ViolationTableProps {
  violations: ReleaseValidationError[];
  /**
   * Resolves an alias to the console page for its resource, so a row is a
   * way in rather than a dead end. Return null for an alias that has none
   * (a release-level violation, an unknown alias).
   */
  resolveHref?: (alias: string) => string | null;
  /** Inside the ship modal: jump to the alias's value editor. */
  onEdit?: (alias: string) => void;
}

/** Schema violations from validate/activate/rollback, one row per error. */
export function ViolationTable({ violations, resolveHref, onEdit }: ViolationTableProps) {
  const actionable = Boolean(resolveHref || onEdit);
  return (
    <div className="table-wrap mt-3">
      <table className="data">
        <thead>
          <tr>
            <th>Alias</th>
            <th>Code</th>
            <th>Schema pointer</th>
            <th>Message</th>
            {actionable ? <th /> : null}
          </tr>
        </thead>
        <tbody>
          {violations.map((violation) => {
            const href = violation.alias && resolveHref ? resolveHref(violation.alias) : null;
            return (
              <tr key={JSON.stringify(violation)} data-alias={violation.alias || undefined}>
                <td className="mono">{violation.alias || "release"}</td>
                <td>
                  <Badge kind="danger">{violation.code}</Badge>
                </td>
                <td className="mono">{violation.schema_pointer || "—"}</td>
                <td>{violation.message}</td>
                {actionable ? (
                  <td>
                    <div className="row-actions">
                      {onEdit && violation.alias ? (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => onEdit(violation.alias)}
                        >
                          Edit this value
                        </Button>
                      ) : null}
                      {href ? (
                        <Link href={href} className="ship-link text-sm">
                          Open {violation.alias}
                        </Link>
                      ) : null}
                    </div>
                  </td>
                ) : null}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** The red panel a failed validation/activation/rollback opens, with its violations. */
export function ActivationFailurePanel({
  failure,
  onDismiss,
  resolveHref,
}: {
  failure: ActivationFailure;
  onDismiss: () => void;
  resolveHref?: ViolationTableProps["resolveHref"];
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
      <ViolationTable violations={failure.violations} resolveHref={resolveHref} />
    </section>
  );
}

/**
 * A `resolveHref` for release entries: each alias links to the parameter or
 * secret it pins. Shared by the workspace, the rollback dialog and the ship
 * preview, which all know their entries' keys.
 */
export function entryHrefResolver(
  entries: ReadonlyArray<{
    alias: string;
    kind: string;
    key?: string;
    ref?: { namespace: { env: string; app: string }; key: string };
  }>,
  fallbackNamespace: { env: string; app: string },
  links: {
    parameterDetail: (ref: { env: string; app: string; key: string }) => string;
    secretDetail: (ref: { env: string; app: string; key: string }) => string;
  },
): (alias: string) => string | null {
  return (alias) => {
    const entry = entries.find((candidate) => candidate.alias === alias);
    if (!entry) return null;
    const key = entry.ref?.key ?? entry.key;
    if (!key) return null;
    const namespace = entry.ref?.namespace ?? fallbackNamespace;
    const ref = { env: namespace.env, app: namespace.app, key };
    return entry.kind === "secret" ? links.secretDetail(ref) : links.parameterDetail(ref);
  };
}
