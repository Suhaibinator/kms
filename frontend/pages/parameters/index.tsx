import { ChevronDown, Eye, MoreHorizontal, RefreshCw, X } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActionMenu } from "@/components/applications/ActionMenu";
import {
  BulkActionBar,
  BulkDeleteDialog,
  SelectAllCell,
  SelectRowCell,
  useBulkSelection,
} from "@/components/BulkSelection";
import { Highlight, Snippet } from "@/components/Highlight";
import { Icon } from "@/components/icons";
import { JsonEditor } from "@/components/JsonEditor";
import { ConfirmDialog, Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { ContentTypeSelect, ParameterValueInput } from "@/components/ParameterValueInput";
import { ParameterWorkspace } from "@/components/parameters/ParameterWorkspace";
import { SearchField } from "@/components/SearchField";
import {
  headerLabels,
  MobileListToolbar,
  SortHeaderRow,
  useSort,
} from "@/components/SortableTable";
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
import { formatUnixMs, isEmptyJson, labelEntries } from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import {
  useCursorPagination,
  useFieldErrors,
  useLatestRequest,
  useNamespaces,
  useQueryParams,
} from "@/lib/hooks";
import { canonicalParameterValue } from "@/lib/json-text";
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
import type { Parameter } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { useNamespaceIndex } from "@/lib/useNamespaceIndex";
import { useParameterSchema } from "@/lib/useParameterSchema";
import {
  firstError,
  MAX_KEY_LENGTH,
  validateContentType,
  validateKey,
  validateMetadataJson,
  validateParameterValue,
  validateValueSize,
} from "@/lib/validation";
import { shouldOpenWorkspace } from "@/lib/workspace";

const NO_NS: NamespaceSelection = { env: "", app: "" };

/** The key-length counter appears once a key is within this many characters of the cap. */
const KEY_COUNTER_FROM = MAX_KEY_LENGTH - 56;

/** The fields of the new-parameter form that carry their own validation. */
type CreateField = "key" | "value" | "contentType" | "metadata";

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
const COLUMNS: ReadonlyArray<SortColumn<Parameter>> = [
  { id: "key", label: "Key", value: (p) => p.key },
  { id: "version", label: "Version", value: (p) => p.version },
  { id: "type", label: "Type", value: (p) => p.content_type },
  // A stack of label badges has no single value to order by.
  { id: "labels", label: "Labels" },
  { id: "created", label: "Created", value: (p) => p.created_at_unix_ms },
];

const PAGE_SORT_HINT = "Sorts the rows loaded on this page, not the whole namespace.";
const SEARCH_SORT_HINT = "Sorts the matches, not the whole namespace.";

/** Named once: the header checkbox, the mobile toolbar and the skeleton that
 *  reserves that toolbar's row all have to say the same thing, and the
 *  skeleton's placeholder only wraps to the loaded number of lines if it does. */
const SELECT_ALL_LABEL = "Select all parameters on this page";

export default function ParametersPage() {
  const toast = useToast();
  const { identity } = useAuth();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams([
    "env",
    "app",
    "q",
    "key_prefix",
  ]);
  const replaceQuery = useQueryReplace("/parameters");
  const sort = useSort<Parameter>("/parameters", COLUMNS);

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  // What the box holds right now, and what the ranker has been told about —
  // the second lands one debounce after the last keystroke.
  const [searchInput, setSearchInput] = useState("");
  const [query, setQuery] = useState("");
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const [parameterTarget, setParameterTarget] = useState<Parameter | null>(null);
  const [rows, setRows] = useState<Parameter[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedScope, setLoadedScope] = useState("");
  const request = useLatestRequest();

  // No query here: clearing a search returns to the page you were browsing.
  const paging = useCursorPagination(JSON.stringify([ns.env, ns.app]));
  const { pageToken, setNextToken } = paging;

  const [createOpen, setCreateOpen] = useState(false);
  const [createNs, setCreateNs] = useState<NamespaceSelection>(NO_NS);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [valueValid, setValueValid] = useState(true);
  const [contentType, setContentType] = useState("string");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [metadataOpen, setMetadataOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const errors = useFieldErrors<CreateField>();
  const keyRef = useRef<HTMLInputElement | null>(null);
  const { formRef, requestFocus } = useFocusFirstInvalid();
  // A json value in a namespace with a pinned schema can be edited by field.
  const createSchema = useParameterSchema({
    env: createNs.env,
    app: createNs.app,
    key: key.trim(),
    enabled: createOpen && contentType === "json" && !!createNs.env && !!createNs.app,
  });

  const [deleteTarget, setDeleteTarget] = useState<Parameter | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Bulk delete is an admin convenience; the console's only role distinction is
  // admin vs client identity, and a client's writes depend on a policy the
  // console cannot see, so it keeps the per-row action only.
  const canBulkDelete = identity?.kind === "admin";
  const [bulkOpen, setBulkOpen] = useState(false);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkDone, setBulkDone] = useState(0);

  // Follow deep links and same-page/history navigation. Query values are stable
  // until the relevant URL fields change; local drafts do not write from effects.
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

  // Client-side mirrors of the server's validators (see lib/validation.ts).
  // They fail fast on input the API is certain to reject; the server still has
  // the last word, and its errors keep arriving as toasts.
  const keyError = validateKey(key.trim());
  const contentTypeError = validateContentType(contentType);
  const metadataError = validateMetadataJson(metadataJson);
  // Re-runs when the content type changes — "1.5" is a valid float but not a
  // valid integer. Memoised because a value may run to a megabyte.
  const valueError = useMemo(
    () => firstError(validateValueSize(value), validateParameterValue(value, contentType)),
    [value, contentType],
  );

  // A message stays hidden until the user has left the field or tried to
  // submit, so a freshly opened form is never already covered in errors.
  const invalidFormDraft = !valueValid && valueError === null;
  const shownKeyError = errors.shown("key", keyError);
  const shownValueError = errors.shown("value", valueError);
  const shownContentTypeError = errors.shown("contentType", contentTypeError);
  const shownMetadataError = errors.shown("metadata", metadataError);
  // The namespace selects have no blur of their own, so they surface on submit.
  const shownCreateAppError = errors.submitted && !createNs.app ? "Choose an application." : null;
  const shownCreateEnvError = errors.submitted && !createNs.env ? "Choose an environment." : null;
  const createError = firstError(keyError, valueError, contentTypeError, metadataError);
  const shownCreateError = firstError(
    shownKeyError,
    shownValueError,
    shownContentTypeError,
    shownMetadataError,
    shownCreateAppError,
    shownCreateEnvError,
  );
  const createDirty =
    key !== "" || value !== "" || contentType !== "string" || !isEmptyJson(metadataJson);

  const load = useCallback(
    async (token: string, selection: NamespaceSelection): Promise<Parameter[] | null> => {
      const run = request.begin();
      const scope = requestScope(selection, token);
      if (!selection.env || !selection.app) {
        setRows([]);
        setNextToken("");
        setLoading(false);
        setLoadedScope(scope);
        return [];
      }
      setLoading(true);
      try {
        const res = await api.listParameters(
          { env: selection.env, app: selection.app },
          undefined,
          100,
          token || undefined,
          { signal: run.signal },
        );
        if (!run.current) return null;
        const loaded = res.parameters ?? [];
        setRows(loaded);
        setNextToken(res.next_page_token ?? "");
        setLoadedScope(scope);
        return loaded;
      } catch (err) {
        if (!run.current || isAbortError(err)) return null;
        setRows([]);
        setNextToken("");
        setLoadedScope(scope);
        toast.error(err, "Failed to load parameters");
        return null;
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
      const res = await api.listParameters(
        { env: ns.env, app: ns.app },
        undefined,
        INDEX_PAGE_SIZE,
        token || undefined,
        { signal },
      );
      return { items: res.parameters ?? [], next: res.next_page_token ?? "" };
    },
    [ns.env, ns.app],
  );
  const onIndexError = useCallback(
    (error: unknown) => toast.error(error, "Failed to search parameters"),
    [toast],
  );
  const index = useNamespaceIndex<Parameter>(
    JSON.stringify([ns.env, ns.app]),
    searchMode && hasNs,
    fetchIndexPage,
    onIndexError,
  );
  const { invalidate: invalidateIndex } = index;
  const searchable = useMemo(
    () => buildSearchIndex(index.rows, (p: Parameter) => ({ key: p.key, text: p.value })),
    [index.rows],
  );
  const { matches, total: matchTotal } = useMemo(
    () =>
      searchMode ? searchIndex(searchable, query, SEARCH_RESULT_LIMIT) : { matches: [], total: 0 },
    [searchable, query, searchMode],
  );

  /** Re-reads whatever the current mode is showing after a write. The other
   *  mode is only marked stale: its rows are refetched when it comes back. */
  const refresh = useCallback(async (): Promise<Parameter[] | null> => {
    invalidateIndex();
    if (searchMode) {
      requestedScope.current = null;
      return null;
    }
    return load(pageToken, ns);
  }, [invalidateIndex, load, ns, pageToken, searchMode]);

  function onSelectNamespace(next: NamespaceSelection) {
    appliedScope.current = seedScope(next, query);
    setNs(next);
    setDeleteTarget(null);
    replaceQuery({ env: next.env, app: next.app });
  }
  // Called from the input's own handler, never an effect: the URL may only be
  // rewritten in response to something the operator did (see lib/url.ts).
  function commitSearch(next: string) {
    appliedScope.current = seedScope(ns, next);
    setDeleteTarget(null);
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

  function openCreate() {
    setCreateNs(hasNs ? ns : NO_NS);
    setKey("");
    setValue("");
    setValueValid(true);
    setContentType("string");
    setMetadataJson("{}");
    setMetadataOpen(false);
    errors.reset();
    setCreateOpen(true);
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    errors.markAllTouched();
    // Every problem now has an inline message beside the field that caused it;
    // move focus there so the button never looks dead.
    if (!createNs.env || !createNs.app || createError || !valueValid) {
      if (metadataError) setMetadataOpen(true);
      requestFocus();
      return;
    }
    const k = key.trim();
    setSaving(true);
    try {
      const res = await api.putParameter({
        env: createNs.env,
        app: createNs.app,
        key: k,
        create_only: true,
        value: canonicalParameterValue(value, contentType),
        content_type: contentType || "string",
        metadata_json: metadataJson.trim() || "{}",
      });
      toast.success(
        `Parameter saved (version ${res.version})`,
        `${createNs.env}/${createNs.app}/${k}`,
      );
      setCreateOpen(false);
      // The index is dropped whichever namespace the parameter landed in: it
      // may be the one a search is about to be run against.
      invalidateIndex();
      // If the new parameter lands in the currently viewed namespace, refresh.
      if (createNs.env === ns.env && createNs.app === ns.app) {
        if (searchMode) {
          requestedScope.current = null;
        } else {
          paging.reset();
          requestedScope.current = requestScope(ns, "");
          await load("", ns);
        }
      }
    } catch (err) {
      toast.error(err, "Failed to save parameter");
    } finally {
      setSaving(false);
    }
  }

  async function onDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.deleteParameter({
        env: deleteTarget.env,
        app: deleteTarget.app,
        key: deleteTarget.key,
      });
      toast.success("Parameter deleted", deleteTarget.key);
      setDeleteTarget(null);
      // Deleting the last row of page N would otherwise strand the operator on
      // an empty page with no way forward.
      const remaining = await refresh();
      if (remaining !== null && remaining.length === 0 && paging.hasPrevious) paging.previous();
    } catch (err) {
      toast.error(err, "Failed to delete parameter");
    } finally {
      setDeleting(false);
    }
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
  const keyLength = [...key].length;

  // Rank first, cut to the result limit, and only then order by column: the
  // whole index is never sorted, and with no column chosen `sortRows` copies,
  // so relevance order survives.
  const matchByKey = useMemo(
    () => new Map(matches.map((match) => [match.item.key, match])),
    [matches],
  );
  const visibleRows = sort.apply(searchMode ? matches.map((match) => match.item) : rows);
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
  // Scoped to this exact namespace, query and page: any of them changing is a
  // different list, and the ticks must not travel to it.
  const listScope = JSON.stringify([ns.env, ns.app, query, searchMode ? "" : pageToken]);
  const selection = useBulkSelection(
    canBulkDelete ? visibleRows.map((row) => row.key) : [],
    listScope,
  );

  async function onBulkDelete() {
    const targets = selection.selected;
    if (targets.length === 0) return;
    setBulkBusy(true);
    setBulkDone(0);
    try {
      // No bulk endpoint exists: this is the row action, run once per key.
      const result = await runBulk(
        targets,
        (key) => api.deleteParameter({ env: ns.env, app: ns.app, key }),
        setBulkDone,
      );
      const summary = bulkSummary(result, "Deleted", "parameters");
      if (summary.ok) toast.success(summary.title, summary.detail);
      else toast.error(new Error(summary.detail), summary.title);
      setBulkOpen(false);
      selection.clear();
      const remaining = await refresh();
      if (remaining !== null && remaining.length === 0 && paging.hasPrevious) paging.previous();
    } finally {
      setBulkBusy(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Parameters"
        subtitle="Non-secret configuration values, isolated by application and environment."
        breadcrumbs={hasNs ? crumbs.environment(ns) : undefined}
        actions={<Button onClick={openCreate}>New parameter</Button>}
      />

      <div className="filters">
        {/* The create modal mounts a second picker, so both need their own ids
            or a <label for> resolves to whichever control rendered first. */}
        <NamespacePicker
          namespaces={namespaces}
          value={ns}
          onChange={onSelectNamespace}
          appId="filter-app"
          envId="filter-env"
        />
        <div className="filter-grow">
          <SearchField
            label="Find parameter"
            placeholder="Search keys and values"
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
          trailing={1}
          toolbar
          toolbarHint={sortHint}
          toolbarSelection={canBulkDelete && SELECT_ALL_LABEL}
          summary
        />
      ) : !hasNs ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose an environment">
          Pick an application and environment above to list its parameters.
        </EmptyState>
      ) : searchMode && index.error ? (
        <EmptyState
          icon={<Icon.parameter size={20} />}
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
          trailing={1}
          toolbar
          toolbarHint={sortHint}
          toolbarSelection={canBulkDelete && SELECT_ALL_LABEL}
          summary
        />
      ) : visibleRows.length === 0 ? (
        <EmptyState
          icon={<Icon.parameter size={20} />}
          title="No parameters found"
          actions={
            searchMode ? (
              <Button variant="outline" onClick={clearSearch}>
                <X size={15} aria-hidden />
                Clear search
              </Button>
            ) : (
              <Button onClick={openCreate}>New parameter</Button>
            )
          }
        >
          {searchMode
            ? `No parameters match \u201c${query}\u201d.`
            : `No parameters in ${ns.env}/${ns.app} yet.`}
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
              shown={visibleRows.length}
              total={searchMode ? matchTotal : undefined}
              noun="parameters"
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
                after={<th />}
              />
            </thead>
            <tbody>
              {visibleRows.map((p) => {
                const match = matchByKey.get(p.key);
                return (
                  <tr key={p.key} data-state={selection.has(p.key) ? "selected" : undefined}>
                    {canBulkDelete ? (
                      <SelectRowCell selection={selection} id={p.key} label={`Select ${p.key}`} />
                    ) : null}
                    <td data-label="Key">
                      <Link
                        className="cell-path"
                        href={links.parameterDetail(p)}
                        onClick={(event) => {
                          if (shouldOpenWorkspace(event)) setParameterTarget(p);
                        }}
                      >
                        <Highlight text={p.key} ranges={match?.keyRanges} />
                      </Link>
                      {/* Only the value matched: show the operator why the row is
                        here, rather than a key with nothing highlighted. */}
                      {match && match.textRanges.length > 0 ? (
                        <Snippet text={p.value} ranges={match.textRanges} />
                      ) : null}
                    </td>
                    <td data-label="Version">v{p.version}</td>
                    <td className="nowrap" data-label="Type">
                      {p.content_type || <span className="faint">—</span>}
                    </td>
                    <td data-label="Labels">
                      <div className="row-wrap">
                        {labelEntries(p.labels).map(([k, v]) => (
                          <Badge key={k} kind="accent">
                            {k}: v{v}
                          </Badge>
                        ))}
                      </div>
                    </td>
                    <td className="nowrap" data-label="Created">
                      {formatUnixMs(p.created_at_unix_ms)}
                    </td>
                    <td data-label="Actions">
                      <div className="row-actions">
                        <ButtonLink
                          variant="outline"
                          size="sm"
                          href={links.parameterDetail(p)}
                          onClick={(event) => {
                            if (shouldOpenWorkspace(event)) setParameterTarget(p);
                          }}
                        >
                          <Eye size={14} aria-hidden />
                          Details
                        </ButtonLink>
                        {/* Delete is the only destructive row action; it sits
                          behind a menu so a stray click cannot reach it. */}
                        <ActionMenu
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={`More actions for ${p.key}`}
                            >
                              <MoreHorizontal size={15} aria-hidden />
                            </Button>
                          }
                          items={[
                            {
                              key: "delete",
                              label: "Delete",
                              onSelect: () => setDeleteTarget(p),
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
      )}

      {canBulkDelete ? (
        <BulkActionBar
          selection={selection}
          noun="parameters"
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
          count={rows.length}
          loading={!settled}
          noun="parameters"
        />
      )}

      <Modal
        mobileFullScreen
        open={createOpen}
        title="New parameter"
        description="Saving creates the parameter's first version and makes it current."
        onClose={() => setCreateOpen(false)}
        dismissible={!saving}
        dirty={createDirty}
        initialFocus={keyRef}
        footer={(close) => (
          <>
            <Button variant="outline" onClick={close} disabled={saving}>
              Cancel
            </Button>
            <Button
              onClick={onCreate}
              loading={saving}
              disabled={shownCreateError !== null || invalidFormDraft}
            >
              Save parameter
            </Button>
          </>
        )}
      >
        <form ref={formRef} onSubmit={onCreate}>
          <div className="form-row">
            <NamespacePicker
              namespaces={namespaces}
              value={createNs}
              onChange={setCreateNs}
              appId="create-app"
              envId="create-env"
              appError={shownCreateAppError}
              envError={shownCreateEnvError}
            />
          </div>
          <div className="form-row">
            <Field
              label="Key"
              hint={
                <>
                  Relative to the selected environment, e.g. rate-limit or billing/timeout
                  {keyLength >= KEY_COUNTER_FROM ? (
                    <span className="mono" data-testid="key-counter">
                      {" "}
                      · {keyLength}/{MAX_KEY_LENGTH}
                    </span>
                  ) : null}
                </>
              }
              error={shownKeyError}
            >
              <Input
                ref={keyRef}
                className="font-mono"
                value={key}
                maxLength={MAX_KEY_LENGTH}
                onChange={(e) => setKey(e.target.value)}
                onBlur={() => errors.touch("key")}
                placeholder="rate-limit"
              />
            </Field>
            <Field label="Content type" error={shownContentTypeError}>
              <ContentTypeSelect
                value={contentType}
                currentValue={value}
                onValueChange={(nextContentType) => {
                  setContentType(nextContentType);
                  errors.touch("contentType");
                }}
                onClearValue={() => setValue("")}
              />
            </Field>
          </div>
          <Field label="Value" error={shownValueError}>
            <ParameterValueInput
              contentType={contentType}
              value={value}
              schema={createSchema.status === "ready" ? createSchema.schema : null}
              rows={8}
              onChange={setValue}
              onValidityChange={setValueValid}
              onBlur={() => errors.touch("value")}
            />
          </Field>
          <div className="value-disclosure">
            <button
              type="button"
              className="value-disclosure-toggle"
              aria-expanded={metadataOpen || shownMetadataError !== null}
              aria-controls="create-metadata"
              onClick={() => setMetadataOpen((open) => !open)}
            >
              <ChevronDown size={14} aria-hidden />
              Metadata JSON
              {!metadataOpen && !isEmptyJson(metadataJson) ? (
                <span className="faint">(set)</span>
              ) : null}
            </button>
            {metadataOpen || shownMetadataError !== null ? (
              <div id="create-metadata" className="value-disclosure-body">
                <Field label="Metadata JSON" error={shownMetadataError}>
                  <JsonEditor
                    toolbar="minimal"
                    rows={3}
                    maxHeight="30vh"
                    value={metadataJson}
                    onChange={setMetadataJson}
                    onBlur={() => errors.touch("metadata")}
                  />
                </Field>
              </div>
            ) : null}
          </div>
        </form>
      </Modal>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete parameter?"
        danger
        message={
          <>
            Delete <span className="mono">{deleteTarget?.key}</span> from{" "}
            <span className="mono">
              {deleteTarget ? `${deleteTarget.env}/${deleteTarget.app}` : ""}
            </span>{" "}
            and all its versions?
          </>
        }
        confirmLabel="Delete parameter"
        busy={deleting}
        onConfirm={onDelete}
        onCancel={() => setDeleteTarget(null)}
      />

      <BulkDeleteDialog
        open={bulkOpen}
        names={selection.selected}
        noun="parameters"
        verb="Delete"
        verbing="Deleting"
        scope={hasNs ? `${ns.env}/${ns.app}` : undefined}
        production={isProductionEnvironment(ns.env)}
        consequence="Every version of each one goes with it, and this cannot be undone."
        busy={bulkBusy}
        completed={bulkDone}
        onConfirm={() => void onBulkDelete()}
        onCancel={() => setBulkOpen(false)}
      />
      <ParameterWorkspace
        parameterRef={parameterTarget}
        onClose={() => setParameterTarget(null)}
        onChanged={() => void refresh()}
        onDeleted={() => void refresh()}
      />
    </>
  );
}
