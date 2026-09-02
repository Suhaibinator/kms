import { MoreHorizontal, Pencil } from "lucide-react";
import Link from "next/link";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { ActionMenu } from "@/components/applications/ActionMenu";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { headerLabels, SortHeaderRow, useSort } from "@/components/SortableTable";
import {
  Badge,
  Checkbox,
  EmptyState,
  Field,
  Input,
  PageHeader,
  TableSkeleton,
  TableSummary,
} from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useFieldErrors, useNamespaces } from "@/lib/hooks";
import { identitiesRelyingOn, methodLabel } from "@/lib/identity-methods";
import { links } from "@/lib/links";
import type { SortColumn } from "@/lib/sort";
import type { AuthMethod, Identity, Namespace } from "@/lib/types";

const MAX_IDENTITY_PAGES = 10;

function sameMethods(a: readonly AuthMethod[], b: readonly AuthMethod[]): boolean {
  return a.length === b.length && a.every((method) => b.includes(method));
}

// Module scope so the sort controller's memos stay stable across renders. The
// order chosen here applies inside every application's table at once.
const COLUMNS: ReadonlyArray<SortColumn<Namespace>> = [
  { id: "env", label: "Environment", value: (ns) => ns.env },
  { id: "description", label: "Description", value: (ns) => ns.description },
  {
    id: "methods",
    label: "Auth methods",
    value: (ns) => [...(ns.allowed_auth_methods ?? [])].sort().join(","),
  },
  { id: "parameters", label: "Parameters", value: (ns) => ns.parameter_count },
  { id: "secrets", label: "Secrets", value: (ns) => ns.secret_count },
  { id: "created", label: "Created", value: (ns) => ns.created_at_unix_ms },
];

const TABLE_HEADERS = headerLabels(COLUMNS);

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
  const sort = useSort<Namespace>("/namespaces", COLUMNS);

  const [editTarget, setEditTarget] = useState<Namespace | null>(null);
  const [editDescription, setEditDescription] = useState("");
  const [editMethods, setEditMethods] = useState<AuthMethod[]>([]);
  const [editSaving, setEditSaving] = useState(false);
  const editErrors = useFieldErrors<"methods">();
  // The Save button sits in the modal footer, outside the form element; the
  // HTML `form` attribute is what lets Enter in the body submit it.
  const editFormId = useId();
  const descriptionRef = useRef<HTMLInputElement>(null);
  // Identities bound anywhere, loaded when the editor opens so removing an
  // auth method can say which of them it breaks. null = could not be checked.
  const [identities, setIdentities] = useState<Identity[] | null>(null);
  const [identitiesLoading, setIdentitiesLoading] = useState(false);
  const identitiesRun = useRef(0);
  const [confirmRemoval, setConfirmRemoval] = useState(false);

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
  const editDirty =
    editTarget !== null &&
    (editDescription !== editTarget.description ||
      !sameMethods(editMethods, editTarget.allowed_auth_methods ?? []));

  useEffect(() => {
    if (!editTarget) return;
    const controller = new AbortController();
    const run = ++identitiesRun.current;
    setIdentities(null);
    setIdentitiesLoading(true);
    void (async () => {
      try {
        const all: Identity[] = [];
        let token: string | undefined;
        for (let page = 0; page < MAX_IDENTITY_PAGES; page += 1) {
          const res = await api.listIdentities(200, token, { signal: controller.signal });
          all.push(...(res.identities ?? []));
          token = res.next_page_token || undefined;
          if (!token) break;
        }
        if (run !== identitiesRun.current) return;
        setIdentities(all);
      } catch {
        // Unknown is shown as such; the save is still allowed.
        if (run === identitiesRun.current) setIdentities(null);
      } finally {
        if (run === identitiesRun.current) setIdentitiesLoading(false);
      }
    })();
    return () => {
      identitiesRun.current += 1;
      controller.abort();
    };
  }, [editTarget]);

  const removedMethods = editTarget
    ? (editTarget.allowed_auth_methods ?? []).filter((method) => !editMethods.includes(method))
    : [];
  const affected =
    editTarget && identities
      ? removedMethods
          .map((method) => ({
            method,
            identities: identitiesRelyingOn(identities, editTarget, method),
          }))
          .filter((entry) => entry.identities.length > 0)
      : [];
  const affectedNames = [
    ...new Set(affected.flatMap((entry) => entry.identities.map((i) => i.name))),
  ];
  const affectedCount = affectedNames.length;

  function openEdit(ns: Namespace) {
    setEditTarget(ns);
    setEditDescription(ns.description);
    setEditMethods(ns.allowed_auth_methods ?? []);
    editErrors.reset();
  }

  function onEdit(e: React.FormEvent) {
    e.preventDefault();
    if (!editTarget) return;
    editErrors.markAllTouched();
    if (methodsError) return;
    // Removing a method identities rely on is a fleet-affecting change: confirm first.
    if (affectedCount > 0) {
      setConfirmRemoval(true);
      return;
    }
    void saveEdit();
  }

  async function saveEdit() {
    if (!editTarget) return;
    setEditSaving(true);
    try {
      await api.updateNamespace({
        env: editTarget.env,
        app: editTarget.app,
        description: editDescription.trim(),
        allowed_auth_methods: editMethods,
      });
      toast.success("Namespace updated", `${editTarget.env}/${editTarget.app}`);
      setConfirmRemoval(false);
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
                  {/* The whole list is loaded, so "of" is the real total. */}
                  <TableSummary shown={group.list.length} noun="environments" />
                  <thead>
                    <SortHeaderRow controller={sort} after={<th />} />
                  </thead>
                  <tbody>
                    {sort.apply(group.list).map((ns) => {
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
                              <ActionMenu
                                label={`More for ${ns.env}/${ns.app}`}
                                trigger={
                                  <Button
                                    type="button"
                                    variant="ghost"
                                    size="icon-sm"
                                    aria-label={`More for ${ns.env}/${ns.app}`}
                                  >
                                    <MoreHorizontal size={16} />
                                  </Button>
                                }
                                items={[
                                  {
                                    key: "delete",
                                    label: canDelete ? (
                                      "Delete environment"
                                    ) : (
                                      <>
                                        <span>Delete environment</span>
                                        <span id={deleteReasonId} className="faint text-xs">
                                          {deleteReason}
                                        </span>
                                      </>
                                    ),
                                    disabled: !canDelete,
                                    onSelect: () => setDeleteTarget(ns),
                                  },
                                ]}
                              />
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
        dirty={editDirty}
        initialFocus={descriptionRef}
        footer={(close) => (
          <>
            {methodsError ? (
              <p className="footer-note" role="status">
                {methodsError}
              </p>
            ) : null}
            <Button type="button" variant="outline" onClick={close} disabled={editSaving}>
              Cancel
            </Button>
            <Button
              form={editFormId}
              type="submit"
              disabled={methodsError !== null}
              loading={editSaving}
            >
              Save changes
            </Button>
          </>
        )}
      >
        {editTarget ? (
          <form id={editFormId} onSubmit={onEdit}>
            <Field label="Namespace">
              <Input className="font-mono" value={`${editTarget.env}/${editTarget.app}`} disabled />
            </Field>
            <Field label="Description" hint="Optional.">
              <Input
                ref={descriptionRef}
                value={editDescription}
                onChange={(e) => setEditDescription(e.target.value)}
              />
            </Field>
            <AuthMethodsField
              methods={editMethods}
              onChange={(next) => {
                setEditMethods(next);
                editErrors.touch("methods");
              }}
              error={shownMethodsError}
            />
            {removedMethods.length > 0 && identitiesLoading ? (
              <p className="faint text-sm" role="status">
                Checking identities…
              </p>
            ) : null}
            {removedMethods.length > 0 && !identitiesLoading && identities === null ? (
              <p className="faint text-sm" role="status">
                Could not check which identities rely on the removed method.
              </p>
            ) : null}
            {affected.map((entry) => (
              <div key={entry.method} className="warn-panel text-sm" role="status">
                <strong>
                  Removing {methodLabel(entry.method)} authentication breaks{" "}
                  {entry.identities.length}{" "}
                  {entry.identities.length === 1 ? "identity" : "identities"}:
                </strong>{" "}
                <span className="mono">{entry.identities.map((i) => i.name).join(", ")}</span>.{" "}
                {entry.identities.length === 1 ? "It stops" : "They stop"} authenticating on the
                next RPC.
              </div>
            ))}
          </form>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={confirmRemoval}
        title="Remove authentication method?"
        danger
        message={
          <>
            Saving removes {removedMethods.map(methodLabel).join(" and ")} authentication from{" "}
            <span className="mono">{editTarget ? `${editTarget.env}/${editTarget.app}` : ""}</span>.{" "}
            {affectedCount} {affectedCount === 1 ? "identity stops" : "identities stop"}{" "}
            authenticating on the next RPC: <span className="mono">{affectedNames.join(", ")}</span>
            .
          </>
        }
        confirmLabel={`Save and break ${affectedCount} ${affectedCount === 1 ? "identity" : "identities"}`}
        busy={editSaving}
        onConfirm={() => void saveEdit()}
        onCancel={() => setConfirmRemoval(false)}
      />

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
