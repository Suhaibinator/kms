import { ChevronRight, MoreHorizontal, Pencil } from "lucide-react";
import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { ActionMenu } from "@/components/applications/ActionMenu";
import { DeleteEnvironmentDialog } from "@/components/applications/DeleteEnvironmentDialog";
import {
  AuthMethodBadges,
  NamespaceSettingsModal,
} from "@/components/applications/NamespaceSettingsModal";
import { Icon } from "@/components/icons";
import {
  headerLabels,
  MobileListToolbar,
  SortHeaderRow,
  useSort,
} from "@/components/SortableTable";
import { EmptyState, PageHeader, Skeleton, TableSkeleton, TableSummary } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { formatUnixMs } from "@/lib/format";
import { useNamespaces } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { SortColumn } from "@/lib/sort";
import type { Namespace } from "@/lib/types";

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

/** One definition for the loaded table and its skeleton: these widths are what
 *  make the header row wrap to two lines, and a skeleton without them comes up
 *  17.25px short. The last column is the actions gutter, which is content-sized
 *  by `table.data td:has(> .navigable-row-end)` and wants no preference. */
const NAMESPACE_COLGROUP = (
  <colgroup>
    <col className="namespace-col-env" />
    <col />
    <col className="namespace-col-methods" />
    <col className="namespace-col-count" />
    <col className="namespace-col-count" />
    <col className="namespace-col-created" />
    <col />
  </colgroup>
);

export default function NamespacesPage() {
  const toast = useToast();
  const { namespaces, loading, error, reload } = useNamespaces();
  const sort = useSort<Namespace>("/namespaces", COLUMNS);

  const [editTarget, setEditTarget] = useState<Namespace | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Namespace | null>(null);

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
        // The loaded page is one .ns-group per application, each with a heading
        // block above its table; a bare table skeleton left that 51px out and
        // the list jumped on arrival. The class and the colgroup matter too:
        // together they carry the column widths and the header wrapping that
        // make the loaded header row 17.25px taller than a default one.
        <div className="ns-group">
          <div className="ns-group-title">
            <span className="ns-group-name">
              <Skeleton width="8ch" />
            </span>
            <span className="faint text-sm">
              <Skeleton width="12ch" />
            </span>
          </div>
          <TableSkeleton
            headers={TABLE_HEADERS}
            trailing={1}
            tableClassName="namespace-table"
            colgroup={NAMESPACE_COLGROUP}
            toolbar
            summary
          />
        </div>
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
                <MobileListToolbar controller={sort} />
                <table className="data namespace-table">
                  {/* The whole list is loaded, so "of" is the real total. */}
                  <TableSummary shown={group.list.length} noun="environments" />
                  {NAMESPACE_COLGROUP}
                  <thead>
                    <SortHeaderRow controller={sort} after={<th />} />
                  </thead>
                  <tbody>
                    {sort.apply(group.list).map((ns) => {
                      const identityCount = ns.identity_count ?? 0;
                      const total = ns.parameter_count + ns.secret_count + identityCount;
                      const canDelete = total === 0;
                      const deleteReason = `Namespace holds ${ns.parameter_count} parameter(s), ${ns.secret_count} secret(s), and ${identityCount} bound ${identityCount === 1 ? "identity" : "identities"}. Remove these dependencies before deleting.`;
                      const deleteReasonId = `delete-reason-${ns.env}-${ns.app}`;
                      return (
                        <tr key={`${ns.env}/${ns.app}`} className="navigable-row">
                          <td className="mono" data-label="Environment">
                            <Link
                              className="navigable-row-link"
                              href={links.environment(ns.app, ns.env)}
                              aria-label={`Manage ${ns.app}/${ns.env}`}
                            >
                              {ns.env}
                            </Link>
                          </td>
                          <td data-label="Description">
                            {ns.description || <span className="faint">—</span>}
                          </td>
                          <td data-label="Auth methods">
                            <AuthMethodBadges methods={ns.allowed_auth_methods} />
                          </td>
                          <td data-label="Parameters">
                            <Link className="cell-count" href={links.parameters(ns)}>
                              {ns.parameter_count}
                            </Link>
                          </td>
                          <td data-label="Secrets">
                            <Link className="cell-count" href={links.secrets(ns)}>
                              {ns.secret_count}
                            </Link>
                          </td>
                          <td className="nowrap" data-label="Created">
                            {formatUnixMs(ns.created_at_unix_ms)}
                          </td>
                          <td data-label="Actions">
                            <div className="navigable-row-end">
                              <div className="row-actions">
                                <Button
                                  variant="outline"
                                  size="sm"
                                  onClick={() => setEditTarget(ns)}
                                >
                                  <Pencil size={14} aria-hidden />
                                  Edit
                                </Button>
                                <ActionMenu
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
                                    ...(identityCount > 0
                                      ? [
                                          {
                                            key: "identities",
                                            label: "Manage bound identities",
                                            href: links.identities({ env: ns.env, app: ns.app }),
                                          },
                                        ]
                                      : []),
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
                              <span className="navigable-row-chevron" aria-hidden="true">
                                <ChevronRight size={18} />
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

      <NamespaceSettingsModal
        namespace={editTarget}
        onClose={() => setEditTarget(null)}
        onSaved={() => {
          setEditTarget(null);
          reload();
        }}
      />

      <DeleteEnvironmentDialog
        namespace={deleteTarget}
        onCancel={() => setDeleteTarget(null)}
        onDeleted={() => {
          setDeleteTarget(null);
          reload();
        }}
      />
    </>
  );
}
