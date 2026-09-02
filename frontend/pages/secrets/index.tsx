import { Filter, X } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import {
  BulkActionBar,
  BulkDeleteDialog,
  SelectAllCell,
  SelectRowCell,
  useBulkSelection,
} from "@/components/BulkSelection";
import { Icon } from "@/components/icons";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { headerLabels, SortHeaderRow, useSort } from "@/components/SortableTable";
import {
  Badge,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  TableSkeleton,
  TableSummary,
} from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { bulkSummary, runBulk } from "@/lib/bulk";
import { crumbs } from "@/lib/crumbs";
import { formatUnixMs } from "@/lib/format";
import { useCursorPagination, useLatestRequest, useNamespaces, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import { rememberNamespace } from "@/lib/namespace-memory";
import { isProductionEnvironment } from "@/lib/readiness";
import type { SortColumn } from "@/lib/sort";
import type { SecretMetadata } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { validateKeyPrefix } from "@/lib/validation";

function currentVersion(s: SecretMetadata): number | null {
  const c = s.labels?.current;
  return typeof c === "number" ? c : null;
}

const NO_NS: NamespaceSelection = { env: "", app: "" };

/** Identifies the list a response belongs to, so a stale one cannot mark a
 *  different namespace/prefix/page as loaded. */
function requestScope(selection: NamespaceSelection, prefix: string, token: string): string {
  return JSON.stringify([selection.env, selection.app, prefix, token]);
}

// Module scope so the sort controller's memos stay stable across renders.
const COLUMNS: ReadonlyArray<SortColumn<SecretMetadata>> = [
  { id: "key", label: "Key", value: (s) => s.key },
  { id: "type", label: "Type", value: (s) => s.content_type },
  { id: "current", label: "Current", value: (s) => currentVersion(s) },
  { id: "versions", label: "Versions", value: (s) => s.versions?.length ?? 0 },
  // Client-bound is the mode that changes how a value is read, so it leads.
  { id: "mode", label: "Mode", value: (s) => Boolean(s.client_bound) },
  { id: "updated", label: "Updated", value: (s) => s.updated_at_unix_ms },
];

const PAGE_SORT_HINT = "Sorts the rows loaded on this page, not the whole namespace.";

export default function SecretsPage() {
  const toast = useToast();
  const { identity } = useAuth();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams(["env", "app", "key_prefix"]);
  const replaceQuery = useQueryReplace("/secrets");
  const sort = useSort<SecretMetadata>("/secrets", COLUMNS);

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  const [prefixInput, setPrefixInput] = useState("");
  const [prefix, setPrefix] = useState("");
  const [prefixTouched, setPrefixTouched] = useState(false);

  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedScope, setLoadedScope] = useState("");
  const request = useLatestRequest();

  const [bulkOpen, setBulkOpen] = useState(false);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkDone, setBulkDone] = useState(0);

  const paging = useCursorPagination(JSON.stringify([ns.env, ns.app, prefix]));
  const { pageToken, setNextToken } = paging;

  const [seeded, setSeeded] = useState(false);
  useEffect(() => {
    if (!queryReady || seeded) return;
    setSeeded(true);
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    const kp = queryValues.key_prefix ?? "";
    if (env || app) setNs({ env, app });
    if (kp) {
      setPrefixInput(kp);
      setPrefix(kp);
    }
  }, [queryReady, queryValues, seeded]);

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load environments");
  }, [nsError, toast]);

  const hasNs = !!ns.env && !!ns.app;

  // The sidebar carries the settled namespace to the other list pages.
  useEffect(() => {
    if (hasNs) rememberNamespace({ env: ns.env, app: ns.app });
  }, [hasNs, ns.env, ns.app]);

  // An empty prefix lists the whole namespace; anything else must be a legal key.
  const prefixError = validateKeyPrefix(prefixInput.trim());
  const shownPrefixError = prefixTouched ? prefixError : null;

  const load = useCallback(
    async (token: string, selection: NamespaceSelection, activePrefix: string) => {
      const run = request.begin();
      const scope = requestScope(selection, activePrefix, token);
      if (!selection.env || !selection.app) {
        setSecrets([]);
        setNextToken("");
        setLoading(false);
        setLoadedScope(scope);
        return;
      }
      setLoading(true);
      try {
        const res = await api.listSecrets(
          { env: selection.env, app: selection.app },
          activePrefix || undefined,
          100,
          token || undefined,
          { signal: run.signal },
        );
        if (!run.current) return;
        setSecrets(res.secrets ?? []);
        setNextToken(res.next_page_token ?? "");
        setLoadedScope(scope);
      } catch (err) {
        if (!run.current || isAbortError(err)) return;
        // Leaving the previous namespace's rows on screen under the new header
        // is worse than an empty table: they look like this namespace's secrets.
        setSecrets([]);
        setNextToken("");
        setLoadedScope(scope);
        toast.error(err, "Failed to load secrets");
      } finally {
        if (run.current) setLoading(false);
      }
    },
    [request, setNextToken, toast],
  );

  useEffect(() => {
    void load(pageToken, ns, prefix);
  }, [load, pageToken, ns, prefix]);

  function onSelectNamespace(next: NamespaceSelection) {
    setNs(next);
    setSecrets([]);
    replaceQuery({ env: next.env, app: next.app });
  }
  function applyFilter(e: React.FormEvent) {
    e.preventDefault();
    setPrefixTouched(true);
    if (prefixError) return;
    const next = prefixInput.trim();
    setSecrets([]);
    setPrefix(next);
    replaceQuery({ key_prefix: next });
  }
  function clearFilter() {
    setPrefixInput("");
    setPrefixTouched(false);
    setSecrets([]);
    setPrefix("");
    replaceQuery({ key_prefix: "" });
  }

  const newSecretLink = hasNs ? links.newSecret(ns) : links.newSecret();

  // A deep link's env/app land one frame after mount, so "Choose an
  // environment" would flash before the list it asked for.
  const awaitingDeepLink = !seeded && (!queryReady || !!queryValues.env || !!queryValues.app);
  // A response has arrived for exactly this namespace/prefix/page. Gating on
  // this rather than on `loading` keeps the empty state from flashing before
  // the first request has even started.
  const scope = requestScope(ns, prefix, pageToken);
  const settled = loadedScope === scope;

  const sortedSecrets = sort.apply(secrets);
  // Bulk delete is an admin convenience; the console's only role distinction is
  // admin vs client identity, and a client's writes depend on a policy the
  // console cannot see.
  const canBulkDelete = identity?.kind === "admin";
  const selection = useBulkSelection(
    canBulkDelete ? secrets.map((secret) => secret.key) : [],
    scope,
  );

  async function onBulkDelete() {
    const targets = selection.selected;
    if (targets.length === 0) return;
    setBulkBusy(true);
    setBulkDone(0);
    try {
      // No bulk endpoint exists: this is the detail page's delete, once per key.
      const result = await runBulk(
        targets,
        (key) => api.deleteSecret({ env: ns.env, app: ns.app, key }),
        setBulkDone,
      );
      const summary = bulkSummary(result, "Deleted", "secrets");
      if (summary.ok) toast.success(summary.title, summary.detail);
      else toast.error(new Error(summary.detail), summary.title);
      setBulkOpen(false);
      selection.clear();
      await load(pageToken, ns, prefix);
    } finally {
      setBulkBusy(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Secrets"
        subtitle="Encrypted values, isolated by application and environment. Values are revealed only on the detail page."
        breadcrumbs={hasNs ? crumbs.environment(ns) : undefined}
        actions={<ButtonLink href={newSecretLink}>New secret</ButtonLink>}
      />

      <form className="filters" onSubmit={applyFilter}>
        <NamespacePicker namespaces={namespaces} value={ns} onChange={onSelectNamespace} />
        <div className="filter-grow">
          <Field label="Key prefix" error={shownPrefixError}>
            <Input
              id="key-prefix"
              className="font-mono"
              placeholder="billing"
              value={prefixInput}
              disabled={!hasNs}
              onChange={(e) => setPrefixInput(e.target.value)}
              onBlur={() => setPrefixTouched(true)}
            />
          </Field>
        </div>
        <Button type="submit" variant="outline" disabled={!hasNs || !!shownPrefixError}>
          <Filter size={15} aria-hidden />
          Filter
        </Button>
        <Button type="button" variant="ghost" onClick={clearFilter} disabled={!hasNs}>
          <X size={15} aria-hidden />
          Clear
        </Button>
      </form>

      {awaitingDeepLink ? (
        <TableSkeleton headers={headerLabels(COLUMNS)} />
      ) : !hasNs ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose an environment">
          Pick an application and environment above to list its secrets.
        </EmptyState>
      ) : !settled || loading ? (
        <TableSkeleton headers={headerLabels(COLUMNS)} />
      ) : secrets.length === 0 ? (
        <EmptyState
          icon={<Icon.secret size={20} />}
          title="No secrets found"
          actions={
            prefix ? (
              <Button variant="outline" onClick={clearFilter}>
                <X size={15} aria-hidden />
                Clear filter
              </Button>
            ) : (
              <ButtonLink href={newSecretLink}>New secret</ButtonLink>
            )
          }
        >
          {prefix ? "No secrets match this key prefix." : `No secrets in ${ns.env}/${ns.app} yet.`}
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <TableSummary
              shown={sortedSecrets.length}
              noun="secrets"
              filters={prefix ? 1 : 0}
              hint={sort.sort ? PAGE_SORT_HINT : undefined}
            />
            <thead>
              <SortHeaderRow
                controller={sort}
                hint={PAGE_SORT_HINT}
                before={
                  canBulkDelete ? (
                    <SelectAllCell selection={selection} label="Select all secrets on this page" />
                  ) : null
                }
              />
            </thead>
            <tbody>
              {sortedSecrets.map((s) => {
                const cur = currentVersion(s);
                return (
                  <tr key={s.key} data-state={selection.has(s.key) ? "selected" : undefined}>
                    {canBulkDelete ? (
                      <SelectRowCell selection={selection} id={s.key} label={`Select ${s.key}`} />
                    ) : null}
                    <td data-label="Key">
                      <Link className="cell-path" href={links.secretDetail(s)}>
                        {s.key}
                      </Link>
                    </td>
                    <td className="nowrap" data-label="Type">
                      {s.content_type || <span className="faint">—</span>}
                    </td>
                    <td data-label="Current">
                      {cur !== null ? `v${cur}` : <span className="faint">—</span>}
                    </td>
                    <td data-label="Versions">{s.versions?.length ?? 0}</td>
                    <td data-label="Mode">
                      <div className="row-wrap">
                        {s.client_bound ? (
                          <Badge kind="warning">client-bound</Badge>
                        ) : (
                          <Badge kind="neutral">standard</Badge>
                        )}
                        {s.has_access_token ? <Badge kind="accent">access token</Badge> : null}
                      </div>
                    </td>
                    <td className="nowrap" data-label="Updated">
                      {formatUnixMs(s.updated_at_unix_ms)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {canBulkDelete ? (
        <BulkActionBar
          selection={selection}
          noun="secrets"
          actionLabel="Delete selected"
          busy={bulkBusy}
          onAction={() => setBulkOpen(true)}
        />
      ) : null}

      <Pagination
        hasNext={paging.hasNext}
        onNext={paging.next}
        hasPrevious={paging.hasPrevious}
        onPrevious={paging.previous}
        onReset={paging.reset}
        showReset={paging.hasPrevious}
        page={paging.page}
        count={secrets.length}
        loading={!settled}
        noun="secrets"
      />

      <BulkDeleteDialog
        open={bulkOpen}
        names={selection.selected}
        noun="secrets"
        verb="Delete"
        verbing="Deleting"
        scope={hasNs ? `${ns.env}/${ns.app}` : undefined}
        production={isProductionEnvironment(ns.env)}
        consequence="Every version of each one is destroyed, and encrypted values cannot be recovered."
        busy={bulkBusy}
        completed={bulkDone}
        onConfirm={() => void onBulkDelete()}
        onCancel={() => setBulkOpen(false)}
      />
    </>
  );
}
