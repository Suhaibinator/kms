import { Pencil, Trash2 } from "lucide-react";
import Link from "next/link";
import { useEffect, useId, useMemo, useState } from "react";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import {
  Badge,
  Checkbox,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useFieldErrors, useNamespaces } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { AuthMethod, Namespace } from "@/lib/types";

const TABLE_HEADERS = [
  "Environment",
  "Description",
  "Auth methods",
  "Parameters",
  "Secrets",
  "Created",
];

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
  error,
}: {
  methods: AuthMethod[];
  onChange: (next: AuthMethod[]) => void;
  error?: string | null;
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
      error={error}
    >
      <div className="checkbox-row">
        <Checkbox
          id="method-mtls"
          checked={methods.includes("mtls")}
          onCheckedChange={(checked) => toggle("mtls", checked)}
        />
        <label htmlFor="method-mtls">
          <strong>mTLS</strong>
          <div className="faint text-sm">Client certificates from the built-in CA.</div>
        </label>
      </div>
      <div className="checkbox-row">
        <Checkbox
          id="method-token"
          checked={methods.includes("token")}
          onCheckedChange={(checked) => toggle("token", checked)}
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
  const editErrors = useFieldErrors<"methods">();
  // The Save button sits in the modal footer, outside the form element; the
  // HTML `form` attribute is what lets Enter in the body submit it.
  const editFormId = useId();

  const [deleteTarget, setDeleteTarget] = useState<Namespace | null>(null);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    if (error) toast.error(error, "Failed to load namespaces");
  }, [error, toast]);

  const grouped = useMemo(() => {
    const map = new Map<string, Namespace[]>();
    for (const ns of namespaces) {
      const list = map.get(ns.app) ?? [];
      list.push(ns);
      map.set(ns.app, list);
    }
    return [...map.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([groupApp, list]) => ({
        app: groupApp,
        list: list.sort((x, y) => x.env.localeCompare(y.env)),
      }));
  }, [namespaces]);

  const methodsError = editMethods.length === 0 ? "Select at least one allowed auth method." : null;
  const shownMethodsError = editErrors.shown("methods", methodsError);

  function openEdit(ns: Namespace) {
    setEditTarget(ns);
    setEditDescription(ns.description);
    setEditMethods(ns.allowed_auth_methods ?? []);
    editErrors.reset();
  }

  async function onEdit(e: React.FormEvent) {
    e.preventDefault();
    if (!editTarget) return;
    editErrors.markAllTouched();
    if (methodsError) return;
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
        title="Application environments"
        subtitle="Deployment environments are isolated application namespaces. Add them from the owning application."
        actions={<ButtonLink href="/applications">Open applications</ButtonLink>}
      />

      {error && !loading ? (
        <EmptyState
          icon={<Icon.namespace size={20} />}
          title="Could not load environments"
          actions={<Button onClick={reload}>Try again</Button>}
        >
          The namespace list is unavailable. Check the connection and try again.
        </EmptyState>
      ) : loading && namespaces.length === 0 ? (
        <TableSkeleton headers={TABLE_HEADERS} />
      ) : namespaces.length === 0 ? (
        <EmptyState
          icon={<Icon.namespace size={20} />}
          title="No application environments yet"
          actions={<ButtonLink href="/applications">Create an application</ButtonLink>}
        >
          Create an application, define its shared contract, then add one or more environments.
        </EmptyState>
      ) : (
        <div aria-busy={loading || undefined}>
          {grouped.map((group) => (
            <div key={group.app} className="ns-group">
              <div className="ns-group-title">
                <span className="ns-group-name">{group.app}</span>
                <span className="faint text-sm">
                  {group.list.length} {group.list.length === 1 ? "environment" : "environments"}
                </span>
              </div>
              <div className="table-wrap card-table">
                <table className="data">
                  <thead>
                    <tr>
                      {TABLE_HEADERS.map((header) => (
                        <th key={header}>{header}</th>
                      ))}
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {group.list.map((ns) => {
                      const total = ns.parameter_count + ns.secret_count;
                      const canDelete = total === 0;
                      const deleteReason = `Namespace holds ${ns.parameter_count} parameter(s) and ${ns.secret_count} secret(s). Empty it before deleting.`;
                      const deleteReasonId = `delete-reason-${ns.env}-${ns.app}`;
                      return (
                        <tr key={`${ns.env}/${ns.app}`}>
                          <td className="mono" data-label="Environment">
                            {ns.env}
                          </td>
                          <td data-label="Description">
                            {ns.description || <span className="faint">—</span>}
                          </td>
                          <td data-label="Auth methods">
                            <AuthMethodBadges methods={ns.allowed_auth_methods} />
                          </td>
                          <td data-label="Parameters">
                            <Link href={links.parameters(ns)}>{ns.parameter_count}</Link>
                          </td>
                          <td data-label="Secrets">
                            <Link href={links.secrets(ns)}>{ns.secret_count}</Link>
                          </td>
                          <td className="nowrap" data-label="Created">
                            {formatUnixMs(ns.created_at_unix_ms)}
                          </td>
                          <td>
                            <div className="row-actions">
                              <Button variant="outline" size="sm" onClick={() => openEdit(ns)}>
                                <Pencil size={14} aria-hidden />
                                Edit
                              </Button>
                              <span
                                className={canDelete ? undefined : "action-unavailable"}
                                title={canDelete ? undefined : deleteReason}
                              >
                                <Button
                                  variant="destructive"
                                  size="sm"
                                  disabled={!canDelete}
                                  aria-describedby={canDelete ? undefined : deleteReasonId}
                                  onClick={() => setDeleteTarget(ns)}
                                >
                                  <Trash2 size={14} aria-hidden />
                                  Delete
                                </Button>
                                {canDelete ? null : (
                                  <span id={deleteReasonId} className="action-unavailable-reason">
                                    {deleteReason}
                                  </span>
                                )}
                              </span>
                            </div>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          ))}
        </div>
      )}

      <Modal
        open={editTarget !== null}
        title={editTarget ? `Edit ${editTarget.env}/${editTarget.app}` : "Edit namespace"}
        onClose={() => setEditTarget(null)}
        dismissible={!editSaving}
        footer={
          <>
            <Button
              type="button"
              variant="outline"
              onClick={() => setEditTarget(null)}
              disabled={editSaving}
            >
              Cancel
            </Button>
            <Button form={editFormId} type="submit" disabled={editSaving || methodsError !== null}>
              {editSaving ? <Spinner /> : null}
              Save changes
            </Button>
          </>
        }
      >
        {editTarget ? (
          <form id={editFormId} onSubmit={onEdit}>
            <Field label="Namespace">
              <Input className="font-mono" value={`${editTarget.env}/${editTarget.app}`} disabled />
            </Field>
            <Field label="Description" hint="Optional.">
              <Input value={editDescription} onChange={(e) => setEditDescription(e.target.value)} />
            </Field>
            <AuthMethodsField
              methods={editMethods}
              onChange={(next) => {
                setEditMethods(next);
                editErrors.touch("methods");
              }}
              error={shownMethodsError}
            />
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
