import { useState } from "react";
import { ConfirmDialog } from "@/components/Modal";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { countNoun } from "@/lib/format";
import type { Namespace } from "@/lib/types";

/** What still lives in a namespace, and therefore blocks deleting it. */
export function namespaceDependencies(ns: Namespace): {
  parameters: number;
  secrets: number;
  identities: number;
  total: number;
} {
  const identities = ns.identity_count ?? 0;
  return {
    parameters: ns.parameter_count,
    secrets: ns.secret_count,
    identities,
    total: ns.parameter_count + ns.secret_count + identities,
  };
}

/**
 * Why this environment cannot be deleted yet, or null when it is empty. The
 * server answers a non-empty delete with 412, so the console says which
 * resources have to go first rather than offering a button that fails.
 */
export function deleteBlockReason(ns: Namespace): string | null {
  const { parameters, secrets, identities, total } = namespaceDependencies(ns);
  if (total === 0) return null;
  const parts: string[] = [];
  if (parameters > 0) parts.push(`${parameters} ${countNoun(parameters, "parameters")}`);
  if (secrets > 0) parts.push(`${secrets} ${countNoun(secrets, "secrets")}`);
  if (identities > 0)
    parts.push(`${identities} bound ${identities === 1 ? "identity" : "identities"}`);
  const listed =
    parts.length > 1 ? `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}` : parts[0];
  return `${listed} must be removed first.`;
}

/**
 * The named confirmation for deleting one environment (namespace). Deleting is
 * only offered when nothing is left in it, so the message says so.
 */
export function DeleteEnvironmentDialog({
  namespace,
  onCancel,
  onDeleted,
}: {
  /** The namespace to delete; `null` closes the dialog. */
  namespace: Namespace | null;
  onCancel: () => void;
  /** Called after the server has deleted it: the caller navigates or reloads. */
  onDeleted: (ns: Namespace) => void;
}) {
  const toast = useToast();
  const [deleting, setDeleting] = useState(false);

  async function onDelete() {
    if (!namespace) return;
    setDeleting(true);
    try {
      await api.deleteNamespace({ env: namespace.env, app: namespace.app });
      toast.success("Namespace deleted", `${namespace.env}/${namespace.app}`);
      onDeleted(namespace);
    } catch (err) {
      toast.error(err, "Failed to delete namespace");
    } finally {
      setDeleting(false);
    }
  }

  return (
    <ConfirmDialog
      open={namespace !== null}
      title="Delete namespace?"
      danger
      message={
        <>
          Delete namespace{" "}
          <span className="mono">{namespace ? `${namespace.env}/${namespace.app}` : ""}</span>? This
          is only possible because it holds no parameters or secrets.
        </>
      }
      confirmLabel="Delete namespace"
      busy={deleting}
      onConfirm={onDelete}
      onCancel={onCancel}
    />
  );
}
