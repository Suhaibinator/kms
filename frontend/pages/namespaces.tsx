import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { Pencil, Trash2 } from "lucide-react";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { Badge, EmptyState, Field, PageHeader, TableSkeleton } from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useNamespaces } from "@/lib/hooks";
import type { AuthMethod, Namespace } from "@/lib/types";

function parametersLink(ns: { env: string; app: string }): string {
  return `/parameters?env=${encodeURIComponent(ns.env)}&app=${encodeURIComponent(ns.app)}`;
}
function secretsLink(ns: { env: string; app: string }): string {
  return `/secrets?env=${encodeURIComponent(ns.env)}&app=${encodeURIComponent(ns.app)}`;
}

function AuthMethodBadges({ methods }: { methods: AuthMethod[] }) {
  if (!methods || methods.length === 0) {
    return <Badge kind="danger">none</Badge>;
  }
  return (
    <div className="row-wrap">
      {methods.includes("mtls") ? <Badge kind="accent">mTLS</Badge> : null}
      {methods.includes("token") ? <Badge kind="neutral">token</Badge> : null}
    </div>
  );
}

function AuthMethodsField({
  methods,
  onChange,
}: {
  methods: AuthMethod[];
  onChange: (next: AuthMethod[]) => void;
}) {
  function toggle(method: AuthMethod, on: boolean) {
    const set = new Set(methods);
    if (on) set.add(method);
    else set.delete(method);
    onChange([...set]);
  }
  return (
    <Field
      label="Allowed auth methods"
      hint="mTLS is the strongest posture. Adding token permits bearer-token clients into this namespace."
    >
      <div className="checkbox-row">
        <input
          id="method-mtls"
          type="checkbox"
          checked={methods.includes("mtls")}
          onChange={(e) => toggle("mtls", e.target.checked)}
        />
        <label htmlFor="method-mtls">
          <strong>mTLS</strong>
          <div className="faint text-sm">Client certificates from the built-in CA.</div>
        </label>
      </div>
      <div className="checkbox-row">
        <input
          id="method-token"
          type="checkbox"
          checked={methods.includes("token")}
          onChange={(e) => toggle("token", e.target.checked)}
        />
        <label htmlFor="method-token">
          <strong>Token</strong>
          <div className="faint text-sm">
            Bearer tokens. Possession-free — anyone holding the string is the app.
          </div>
        </label>
      </div>
    </Field>
  );
}

export default function NamespacesPage() {
  const toast = useToast();
  const { namespaces, loading, error, reload } = useNamespaces();

  const [editTarget, setEditTarget] = useState<Namespace | null>(null);
  const [editDescription, setEditDescription] = useState("");
  const [editMethods, setEditMethods] = useState<AuthMethod[]>([]);
  const [editSaving, setEditSaving] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<Namespace | null>(null);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    if (error) toast.error(error, "Failed to load namespaces");
  }, [error, toast]);

  const grouped = useMemo(() => {
    const map = new Map<string, Namespace[]>();
    for (const ns of namespaces) {
      const list = map.get(ns.env) ?? [];
      list.push(ns);
      map.set(ns.env, list);
    }
    return [...map.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([groupEnv, list]) => ({
        env: groupEnv,
        list: list.sort((x, y) => x.app.localeCompare(y.app)),
      }));
  }, [namespaces]);

  function openEdit(ns: Namespace) {
    setEditTarget(ns);
    setEditDescription(ns.description);
    setEditMethods(ns.allowed_auth_methods ?? []);
  }

  async function onEdit(e: React.FormEvent) {
    e.preventDefault();
    if (!editTarget) return;
    if (editMethods.length === 0) {
      toast.error(new Error("Select at least one allowed auth method."), "No auth method");
      return;
    }
    setEditSaving(true);
    try {
      await api.updateNamespace({
        env: editTarget.env,
        app: editTarget.app,
        description: editDescription.trim(),
        allowed_auth_methods: editMethods,
      });
      toast.success("Namespace updated", `${editTarget.env}/${editTarget.app}`);
      setEditTarget(null);
      reload();
    } catch (err) {
      toast.error(err, "Failed to update namespace");
    } finally {
      setEditSaving(false);
    }
  }

  async function onDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.deleteNamespace({ env: deleteTarget.env, app: deleteTarget.app });
      toast.success("Namespace deleted", `${deleteTarget.env}/${deleteTarget.app}`);
      setDeleteTarget(null);
      reload();
    } catch (err) {
      toast.error(err, "Failed to delete namespace");
    } finally {
      setDeleting(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Environments"
        subtitle="Deployment environments are isolated application namespaces. Add them from the owning application."
        actions={
          <Link className="btn btn-primary" href="/applications">
            Open applications
          </Link>
        }
      />

      {loading ? (
        <TableSkeleton
          headers={[
            "Application",
            "Description",
            "Auth methods",
            "Parameters",
            "Secrets",
            "Created",
          ]}
        />
      ) : namespaces.length === 0 ? (
        <EmptyState
          icon={<Icon.namespace size={20} />}
          title="No environments yet"
          actions={
            <Link className="btn btn-primary" href="/applications">
              Create an application
            </Link>
          }
        >
          Create an application, define its shared contract, then add one or more environments.
        </EmptyState>
      ) : (
        grouped.map((group) => (
          <div key={group.env} className="ns-group">
            <div className="ns-group-title">
              <span className="ns-group-env">{group.env}</span>
              <span className="faint text-sm">
                {group.list.length} {group.list.length === 1 ? "application" : "applications"}
              </span>
            </div>
            <div className="table-wrap card-table">
              <table className="data">
                <thead>
                  <tr>
                    <th>Application</th>
                    <th>Description</th>
                    <th>Auth methods</th>
                    <th>Parameters</th>
                    <th>Secrets</th>
                    <th>Created</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {group.list.map((ns) => {
                    const total = ns.parameter_count + ns.secret_count;
                    const canDelete = total === 0;
                    return (
                      <tr key={`${ns.env}/${ns.app}`}>
                        <td className="mono" data-label="Application">
                          {ns.app}
                        </td>
                        <td data-label="Description">
                          {ns.description || <span className="faint">—</span>}
                        </td>
                        <td data-label="Auth methods">
                          <AuthMethodBadges methods={ns.allowed_auth_methods} />
                        </td>
                        <td data-label="Parameters">
                          <Link href={parametersLink(ns)}>{ns.parameter_count}</Link>
                        </td>
                        <td data-label="Secrets">
                          <Link href={secretsLink(ns)}>{ns.secret_count}</Link>
                        </td>
                        <td className="nowrap" data-label="Created">
                          {formatUnixMs(ns.created_at_unix_ms)}
                        </td>
                        <td>
                          <div className="row-actions">
                            <button className="btn btn-sm" onClick={() => openEdit(ns)}>
                              <Pencil size={14} aria-hidden />
                              Edit
                            </button>
                            <button
                              className="btn btn-sm btn-danger"
                              disabled={!canDelete}
                              title={
                                canDelete
                                  ? undefined
                                  : `Namespace holds ${ns.parameter_count} parameter(s) and ${ns.secret_count} secret(s). Empty it before deleting.`
                              }
                              onClick={() => setDeleteTarget(ns)}
                            >
                              <Trash2 size={14} aria-hidden />
                              Delete
                            </button>
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        ))
      )}

      <Modal
        open={editTarget !== null}
        title={editTarget ? `Edit ${editTarget.env}/${editTarget.app}` : "Edit namespace"}
        onClose={() => setEditTarget(null)}
        footer={
          <>
            <button className="btn" onClick={() => setEditTarget(null)} disabled={editSaving}>
              Cancel
            </button>
            <button className="btn btn-primary" onClick={onEdit} disabled={editSaving}>
              {editSaving ? "Saving…" : "Save changes"}
            </button>
          </>
        }
      >
        {editTarget ? (
          <form onSubmit={onEdit}>
            <Field label="Namespace">
              <input
                className="input mono"
                value={`${editTarget.env}/${editTarget.app}`}
                disabled
              />
            </Field>
            <Field label="Description" hint="Optional.">
              <input
                className="input"
                value={editDescription}
                onChange={(e) => setEditDescription(e.target.value)}
              />
            </Field>
            <AuthMethodsField methods={editMethods} onChange={setEditMethods} />
          </form>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete namespace?"
        danger
        message={
          <>
            Delete namespace{" "}
            <span className="mono">
              {deleteTarget ? `${deleteTarget.env}/${deleteTarget.app}` : ""}
            </span>
            ? This is only possible because it holds no parameters or secrets.
          </>
        }
        confirmLabel="Delete namespace"
        busy={deleting}
        onConfirm={onDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  );
}
