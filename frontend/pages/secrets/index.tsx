import { RefreshCw, X } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { QuickSecretModal } from "@/components/applications/QuickSecretModal";
import {
  BulkActionBar,
  BulkDeleteDialog,
  SelectAllCell,
  SelectRowCell,
  useBulkSelection,
} from "@/components/BulkSelection";
import { Highlight } from "@/components/Highlight";
import { Icon } from "@/components/icons";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { SearchField } from "@/components/SearchField";
import {
  headerLabels,
  MobileListToolbar,
  SortHeaderRow,
  useSort,
} from "@/components/SortableTable";
import { BindingModeBadge } from "@/components/secrets/SecretBadges";
import { SecretWorkspace } from "@/components/secrets/SecretWorkspace";
import { EmptyState, PageHeader, Pagination, TableSkeleton, TableSummary } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import {
  api,
  isAbortError,
  isSecretAlreadyExists,
  type ResourceRef,
  SECRET_ALREADY_EXISTS_MESSAGE,
} from "@/lib/api";
import { bulkSummary, runBulk } from "@/lib/bulk";
import { crumbs } from "@/lib/crumbs";
import { formatUnixMs } from "@/lib/format";
import { useCursorPagination, useLatestRequest, useNamespaces, useQueryParams } from "@/lib/hooks";
import {
  buildSearchIndex,
  INDEX_MAX_PAGES,
  INDEX_PAGE_SIZE,
  SEARCH_DEBOUNCE_MS,
  SEARCH_RESULT_LIMIT,
  searchIndex,
} from "@/lib/key-search";
import { links } from "@/lib/links";
import { rememberNamespace } from "@/lib/namespace-memory";
import { isProductionEnvironment } from "@/lib/readiness";
import type { SortColumn } from "@/lib/sort";
import type { SecretMetadata } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { useNamespaceIndex } from "@/lib/useNamespaceIndex";
import { shouldOpenWorkspace } from "@/lib/workspace";

function currentVersion(s: SecretMetadata): number | null {
  const c = s.labels?.current;
  return typeof c === "number" ? c : null;
}

const NO_NS: NamespaceSelection = { env: "", app: "" };

/** Identifies the browse list a response belongs to, so a stale one cannot
 *  mark a different namespace/page as loaded. The search query is deliberately
 *  absent: searching does not refetch this list. */
function requestScope(selection: NamespaceSelection, token: string): string {
  return JSON.stringify([selection.env, selection.app, token]);
}

/** What the URL seeding effect compares against. It carries the query so an
 *  internal `?q=` replacement is recognised as this page's own work, and the
 *  namespace so an external navigation still resets the box. */
function seedScope(selection: NamespaceSelection, query: string): string {
  return JSON.stringify([selection.env, selection.app, query]);
}

/** How much of the namespace the index can hold, for the truncation note. */
const INDEX_MAX_KEYS = (INDEX_PAGE_SIZE * INDEX_MAX_PAGES).toLocaleString("en-US");

// Module scope so the sort controller's memos stay stable across renders.
const COLUMNS: ReadonlyArray<SortColumn<SecretMetadata>> = [
  { id: "key", label: "Key", value: (s) => s.key },
  { id: "type", label: "Type", value: (s) => s.content_type },
  { id: "current", label: "Current", value: (s) => currentVersion(s) },
  { id: "versions", label: "Versions", value: (s) => s.versions?.length ?? 0 },
  // Binding-key protection changes how the current version is read, so it leads.
  { id: "mode", label: "Mode", value: (s) => Boolean(s.bound) },
  { id: "updated", label: "Updated", value: (s) => s.updated_at_unix_ms },
];

const PAGE_SORT_HINT = "Sorts the rows loaded on this page, not the whole namespace.";
const SEARCH_SORT_HINT = "Sorts the matches, not the whole namespace.";

/** Named once: the header checkbox, the mobile toolbar and the skeleton that
 *  reserves that toolbar's row all have to say the same thing, and the
 *  skeleton's placeholder only wraps to the loaded number of lines if it does. */
const SELECT_ALL_LABEL = "Select all secrets on this page";

export default function SecretsPage() {
  const toast = useToast();
  const { identity } = useAuth();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams([
    "env",
    "app",
    "q",
    "key_prefix",
  ]);
  const replaceQuery = useQueryReplace("/secrets");
  const sort = useSort<SecretMetadata>("/secrets", COLUMNS);

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  // What the box holds right now, and what the ranker has been told about —
  // the second lands one debounce after the last keystroke.
  const [searchInput, setSearchInput] = useState("");
  const [query, setQuery] = useState("");
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedScope, setLoadedScope] = useState("");
  const request = useLatestRequest();

  const [bulkOpen, setBulkOpen] = useState(false);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkDone, setBulkDone] = useState(0);
  const [newSecretOpen, setNewSecretOpen] = useState(false);
  const [secretSaving, setSecretSaving] = useState(false);
  const [secretTarget, setSecretTarget] = useState<ResourceRef | null>(null);

  // No query here: clearing a search returns to the page you were browsing.
  const paging = useCursorPagination(JSON.stringify([ns.env, ns.app]));
  const { pageToken, setNextToken } = paging;

  const [seeded, setSeeded] = useState(false);
  const appliedScope = useRef<string | null>(null);
  useEffect(() => {
    if (!queryReady) return;
    setSeeded(true);
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    // `key_prefix` is the old filter's parameter: a bookmarked link still
    // opens, now as a search for the same text.
    const q = queryValues.q ?? queryValues.key_prefix ?? "";
    const scope = seedScope({ env, app }, q);
    // An internal replace can acknowledge an applied search after the user
    // has started typing the next draft. Only external scope changes reset it.
    if (appliedScope.current === scope) return;
    appliedScope.current = scope;
    setNs((current) => (current.env === env && current.app === app ? current : { env, app }));
    setSearchInput(q);
    setQuery(q);
  }, [queryReady, queryValues]);

  // A pending keystroke must not commit after the page is gone.
  useEffect(
    () => () => {
      if (searchTimer.current) clearTimeout(searchTimer.current);
    },
    [],
  );

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load environments");
  }, [nsError, toast]);

  const hasNs = !!ns.env && !!ns.app;

  // The sidebar carries the settled namespace to the other list pages.
  useEffect(() => {
    if (hasNs) rememberNamespace({ env: ns.env, app: ns.app });
  }, [hasNs, ns.env, ns.app]);

  const load = useCallback(
    async (token: string, selection: NamespaceSelection) => {
      const run = request.begin();
      const scope = requestScope(selection, token);
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
          undefined,
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

  const searchMode = query !== "";
  const browseScope = requestScope(ns, pageToken);
  // The browse list is fetched once per namespace/page. Searching does not
  // touch it, so clearing a search puts the same page back without a refetch.
  const requestedScope = useRef<string | null>(null);
  useEffect(() => {
    if (searchMode) return;
    const scope = requestScope(ns, pageToken);
    if (requestedScope.current === scope) return;
    requestedScope.current = scope;
    void load(pageToken, ns);
  }, [load, pageToken, ns, searchMode]);

  // The whole namespace, walked only while a search is running.
  const fetchIndexPage = useCallback(
    async (token: string, signal: AbortSignal) => {
      const res = await api.listSecrets(
        { env: ns.env, app: ns.app },
        undefined,
        INDEX_PAGE_SIZE,
        token || undefined,
        { signal },
      );
      return { items: res.secrets ?? [], next: res.next_page_token ?? "" };
    },
    [ns.env, ns.app],
  );
  const onIndexError = useCallback(
    (error: unknown) => toast.error(error, "Failed to search secrets"),
    [toast],
  );
  const index = useNamespaceIndex<SecretMetadata>(
    JSON.stringify([ns.env, ns.app]),
    searchMode && hasNs,
    fetchIndexPage,
    onIndexError,
  );
  const { invalidate: invalidateIndex } = index;
  const searchable = useMemo(
    () => buildSearchIndex(index.rows, (secret: SecretMetadata) => ({ key: secret.key })),
    [index.rows],
  );
  const { matches, total: matchTotal } = useMemo(
    () =>
      searchMode ? searchIndex(searchable, query, SEARCH_RESULT_LIMIT) : { matches: [], total: 0 },
    [searchable, query, searchMode],
  );

  /** Re-reads whatever the current mode is showing after a write. The other
   *  mode is only marked stale: its rows are refetched when it comes back. */
  const refresh = useCallback(() => {
    invalidateIndex();
    if (searchMode) requestedScope.current = null;
    else void load(pageToken, ns);
  }, [invalidateIndex, load, ns, pageToken, searchMode]);

  function onSelectNamespace(next: NamespaceSelection) {
    appliedScope.current = seedScope(next, query);
    setNs(next);
    replaceQuery({ env: next.env, app: next.app });
  }
  // Called from the input's own handler, never an effect: the URL may only be
  // rewritten in response to something the operator did (see lib/url.ts).
  function commitSearch(next: string) {
    appliedScope.current = seedScope(ns, next);
    setQuery(next);
    // `key_prefix` goes with it: the seeding effect falls back to that legacy
    // key, so leaving it behind would refill the box the moment it is cleared.
    void replaceQuery({ q: next, key_prefix: "" });
  }
  function onSearchChange(value: string) {
    setSearchInput(value);
    if (searchTimer.current) clearTimeout(searchTimer.current);
    searchTimer.current = setTimeout(() => {
      searchTimer.current = null;
      commitSearch(value.trim());
    }, SEARCH_DEBOUNCE_MS);
  }
  function clearSearch() {
    if (searchTimer.current) {
      clearTimeout(searchTimer.current);
      searchTimer.current = null;
    }
    setSearchInput("");
    commitSearch("");
  }

  const newSecretLink = hasNs ? links.newSecret(ns) : links.newSecret();
  const secretEnvironments = useMemo(
    () =>
      namespaces.filter((namespace) => namespace.app === ns.app).map((namespace) => namespace.env),
    [namespaces, ns.app],
  );

  function openNewSecret(event: React.MouseEvent<HTMLElement>) {
    if (hasNs && shouldOpenWorkspace(event)) setNewSecretOpen(true);
  }

  // A deep link's env/app land one frame after mount, so "Choose an
  // environment" would flash before the list it asked for.
  const awaitingDeepLink =
    !seeded &&
    (!queryReady ||
      !!queryValues.env ||
      !!queryValues.app ||
      !!queryValues.q ||
      !!queryValues.key_prefix);
  // A response has arrived for exactly this namespace/page — or, in search
  // mode, the index has finished loading. Gating on this rather than on
  // `loading` keeps the empty state from flashing before the first request has
  // even started.
  const settled = searchMode ? index.ready : loadedScope === browseScope;
  const busy = searchMode ? index.loading : loading;

  // Rank first, cut to the result limit, and only then order by column: the
  // whole index is never sorted, and with no column chosen `sortRows` copies,
  // so relevance order survives.
  const matchByKey = useMemo(
    () => new Map(matches.map((match) => [match.item.key, match])),
    [matches],
  );
  const visibleSecrets = sort.apply(searchMode ? matches.map((match) => match.item) : secrets);
  const sortHint = searchMode ? SEARCH_SORT_HINT : PAGE_SORT_HINT;
  const summaryHint = [
    sort.sort ? sortHint : null,
    // Only once the cut actually dropped something: at exactly the limit
    // nothing was hidden, and saying otherwise would be a lie.
    searchMode && matchTotal > SEARCH_RESULT_LIMIT
      ? `Showing the best ${SEARCH_RESULT_LIMIT} matches — keep typing`
      : null,
  ]
    .filter(Boolean)
    .join(" · ");
  // Bulk delete is an admin convenience; the console's only role distinction is
  // admin vs client identity, and a client's writes depend on a policy the
  // console cannot see.
  const canBulkDelete = identity?.kind === "admin";
  // Scoped to this exact namespace, query and page: any of them changing is a
  // different list, and the ticks must not travel to it.
  const listScope = JSON.stringify([ns.env, ns.app, query, searchMode ? "" : pageToken]);
  const selection = useBulkSelection(
    canBulkDelete ? visibleSecrets.map((secret) => secret.key) : [],
    listScope,
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
      refresh();
    } finally {
      setBulkBusy(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Secrets"
        subtitle="Encrypted values, isolated by application and environment. Open a secret to manage it without losing this list."
        breadcrumbs={hasNs ? crumbs.environment(ns) : undefined}
        actions={
          <ButtonLink href={newSecretLink} onClick={openNewSecret}>
            New secret
          </ButtonLink>
        }
      />

      <div className="filters">
        <NamespacePicker namespaces={namespaces} value={ns} onChange={onSelectNamespace} />
        <div className="filter-grow">
          <SearchField
            label="Find secret"
            placeholder="Search keys"
            value={searchInput}
            disabled={!hasNs}
            onChange={onSearchChange}
            onClear={clearSearch}
            hint={
              searchMode && settled && !index.complete
                ? `Searched the first ${INDEX_MAX_KEYS} keys in this namespace.`
                : undefined
            }
          />
        </div>
      </div>

      {awaitingDeepLink ? (
        <TableSkeleton
          headers={headerLabels(COLUMNS)}
          leading={canBulkDelete ? 1 : 0}
          toolbar
          toolbarHint={sortHint}
          toolbarSelection={canBulkDelete && SELECT_ALL_LABEL}
          summary
        />
      ) : !hasNs ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose an environment">
          Pick an application and environment above to list its secrets.
        </EmptyState>
      ) : searchMode && index.error ? (
        <EmptyState
          icon={<Icon.secret size={20} />}
          title="Search failed"
          actions={
            <Button variant="outline" onClick={() => invalidateIndex()}>
              <RefreshCw size={15} aria-hidden />
              Retry
            </Button>
          }
        >
          This namespace could not be loaded, so there is nothing to search yet.
        </EmptyState>
      ) : !settled || busy ? (
        <TableSkeleton
          headers={headerLabels(COLUMNS)}
          leading={canBulkDelete ? 1 : 0}
          toolbar
          toolbarHint={sortHint}
          toolbarSelection={canBulkDelete && SELECT_ALL_LABEL}
          summary
        />
      ) : visibleSecrets.length === 0 ? (
        <EmptyState
          icon={<Icon.secret size={20} />}
          title="No secrets found"
          actions={
            searchMode ? (
              <Button variant="outline" onClick={clearSearch}>
                <X size={15} aria-hidden />
                Clear search
              </Button>
            ) : (
              <ButtonLink href={newSecretLink} onClick={openNewSecret}>
                New secret
              </ButtonLink>
            )
          }
        >
          {searchMode
            ? `No secrets match \u201c${query}\u201d.`
            : `No secrets in ${ns.env}/${ns.app} yet.`}
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <MobileListToolbar
            controller={sort}
            selection={canBulkDelete ? selection : undefined}
            selectionLabel={SELECT_ALL_LABEL}
            hint={sortHint}
          />
          <table className="data">
            <TableSummary
              shown={visibleSecrets.length}
              total={searchMode ? matchTotal : undefined}
              noun="secrets"
              filters={searchMode ? 1 : 0}
              hint={summaryHint || undefined}
            />
            <thead>
              <SortHeaderRow
                controller={sort}
                hint={sortHint}
                before={
                  canBulkDelete ? (
                    <SelectAllCell selection={selection} label={SELECT_ALL_LABEL} />
                  ) : null
                }
              />
            </thead>
            <tbody>
              {visibleSecrets.map((s) => {
                const cur = currentVersion(s);
                const match = matchByKey.get(s.key);
                return (
                  <tr key={s.key} data-state={selection.has(s.key) ? "selected" : undefined}>
                    {canBulkDelete ? (
                      <SelectRowCell selection={selection} id={s.key} label={`Select ${s.key}`} />
                    ) : null}
                    <td data-label="Key">
                      <Link
                        className="cell-path"
                        href={links.secretDetail(s)}
                        onClick={(event) => {
                          if (shouldOpenWorkspace(event)) setSecretTarget(s);
                        }}
                      >
                        <Highlight text={s.key} ranges={match?.keyRanges} />
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
                      <BindingModeBadge bound={s.bound} />
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

      {searchMode ? null : (
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
      )}

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

      <QuickSecretModal
        app={ns.app}
        environments={secretEnvironments}
        seed={newSecretOpen ? { environment: ns.env, key: "" } : null}
        saving={secretSaving}
        onClose={() => setNewSecretOpen(false)}
        onSave={async (request) => {
          setSecretSaving(true);
          try {
            const response = await api.createSecret({
              env: request.environment,
              app: ns.app,
              key: request.key,
              value_base64: request.valueBase64,
              content_type: request.contentType,
              metadata_json: request.metadataJson,
              ...(request.bindingKey !== undefined ? { binding_key: request.bindingKey } : null),
              create_only: true,
              expires_at_unix_ms: request.expiresAtUnixMs,
            });
            toast.success(
              `Secret created (version ${response.version})`,
              `${request.environment}/${ns.app}/${request.key}`,
            );
            return response;
          } catch (error) {
            if (isSecretAlreadyExists(error)) {
              toast.error(SECRET_ALREADY_EXISTS_MESSAGE, "Secret already exists");
            } else {
              toast.error(error, "Failed to create secret");
            }
            throw error;
          } finally {
            setSecretSaving(false);
          }
        }}
        onCreated={(ref) => {
          setNewSecretOpen(false);
          setSecretTarget(ref);
          refresh();
        }}
      />
      <SecretWorkspace
        secretRef={secretTarget}
        onClose={() => setSecretTarget(null)}
        onChanged={refresh}
        onDeleted={refresh}
      />
    </>
  );
}
