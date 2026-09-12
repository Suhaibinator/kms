import { RefreshCw } from "lucide-react";
import Link from "next/link";
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "@/components/icons";
import { ReleaseDiffSummary } from "@/components/releases/diff/ReleaseDiffSummary";
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
  JsonView,
  PageHeader,
  Pagination,
  Spinner,
  TableSkeleton,
  TableSummary,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError, type ReleaseDiffQuery } from "@/lib/api";
import {
  auditReleasePreviousVersion,
  auditReleaseSchemaVersion,
  auditReleaseVersion,
  auditShipReleaseVersions,
} from "@/lib/audit-release";
import {
  datetimeLocalToUnixMs,
  displayAuditResource,
  formatRelative,
  formatUnixMs,
  isEmptyJson,
  prettyJson,
} from "@/lib/format";
import {
  useCursorPagination,
  useFieldErrors,
  useLatestRequest,
  useNamespaces,
  useQueryParams,
} from "@/lib/hooks";
import { links } from "@/lib/links";
import type { SortColumn } from "@/lib/sort";
import type { AuditEvent, AuditFilters } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { useNow } from "@/lib/useNow";
import { validateKeyPrefix } from "@/lib/validation";

interface FilterForm {
  env: string;
  app: string;
  key_prefix: string;
  actor: string;
  event_type: string;
  from: string;
  to: string;
}

/**
 * The from → to pair an activation event describes, or null when the event
 * did not activate anything, was a first activation, or does not record the
 * selectors needed to address both releases. A rollback's `previous_version`
 * is the newer release it rolled back from, so the pair reads newer → older.
 */
function auditCompareQuery(event: AuditEvent): ReleaseDiffQuery | null {
  if (!event.resource_env || !event.resource_app) return null;
  if (
    event.event_type === "configuration_release.activate" ||
    event.event_type === "configuration_release.rollback"
  ) {
    const schemaVersion = auditReleaseSchemaVersion(event);
    const to = auditReleaseVersion(event);
    const from = auditReleasePreviousVersion(event);
    if (
      schemaVersion === undefined ||
      to === undefined ||
      from === undefined ||
      !event.resource_key
    )
      return null;
    return {
      env: event.resource_env,
      app: event.resource_app,
      name: event.resource_key,
      schemaVersion,
      from,
      to,
    };
  }
  if (event.event_type === "application.ship") {
    const ship = auditShipReleaseVersions(event);
    if (
      !ship ||
      ship.previousVersion === undefined ||
      ship.schemaVersion === undefined ||
      !ship.releaseName
    )
      return null;
    return {
      env: ship.environment ?? event.resource_env,
      app: event.resource_app,
      name: ship.releaseName,
      schemaVersion: ship.schemaVersion,
      from: ship.previousVersion,
      to: ship.releaseVersion,
    };
  }
  return null;
}

const EMPTY_FORM: FilterForm = {
  env: "",
  app: "",
  key_prefix: "",
  actor: "",
  event_type: "",
  from: "",
  to: "",
};

const PAGE_SIZE = 50;

// Module scope so the sort controller's memos stay stable across renders.
const COLUMNS: ReadonlyArray<SortColumn<AuditEvent>> = [
  { id: "time", label: "Time", value: (e) => e.created_at_unix_ms },
  { id: "event", label: "Event", value: (e) => e.event_type },
  { id: "actor", label: "Actor", value: (e) => e.actor_identity },
  { id: "resource", label: "Resource", value: (e) => displayAuditResource(e) ?? e.resource_type },
  { id: "decision", label: "Decision", value: (e) => e.decision },
  { id: "source", label: "Source IP", value: (e) => e.source_ip },
];

const TABLE_HEADERS = headerLabels(COLUMNS);

const PAGE_SORT_HINT = "Sorts the events loaded on this page, not the whole log.";

// The URL is the source of truth for an investigation, so it can be shared,
// reloaded and returned to: the seven filters plus the cursor position.
const QUERY_KEYS = [
  "app",
  "env",
  "key_prefix",
  "actor",
  "event_type",
  "from",
  "to",
  "page_token",
  "page",
] as const;
type QueryValues = Record<(typeof QUERY_KEYS)[number], string | null>;

function decisionKind(decision: string): "success" | "danger" | "neutral" {
  if (decision === "allow") return "success";
  if (decision === "deny") return "danger";
  return "neutral";
}

/** "End must be after start." once both bounds are set the wrong way round. */
function rangeError(from: string, to: string): string | null {
  const fromMs = datetimeLocalToUnixMs(from);
  const toMs = datetimeLocalToUnixMs(to);
  if (fromMs === undefined || toMs === undefined) return null;
  return fromMs > toMs ? "End must be after start." : null;
}

/** Unix ms → the `datetime-local` value it round-trips through. */
function unixMsToDatetimeLocal(ms: number | undefined): string {
  if (!ms) return "";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function formFromQuery(query: QueryValues): FilterForm {
  return {
    env: query.env ?? "",
    app: query.app ?? "",
    key_prefix: query.key_prefix ?? "",
    actor: query.actor ?? "",
    event_type: query.event_type ?? "",
    from: query.from ?? "",
    to: query.to ?? "",
  };
}

function filtersFromForm(form: FilterForm): AuditFilters {
  return {
    env: form.env.trim() || undefined,
    app: form.app.trim() || undefined,
    key_prefix: form.key_prefix.trim() || undefined,
    actor: form.actor.trim() || undefined,
    event_type: form.event_type.trim() || undefined,
    from_unix_ms: datetimeLocalToUnixMs(form.from),
    to_unix_ms: datetimeLocalToUnixMs(form.to),
  };
}

/** The query patch that describes a filter set (empty strings delete keys). */
function queryFromFilters(filters: AuditFilters): Record<string, string> {
  return {
    env: filters.env ?? "",
    app: filters.app ?? "",
    key_prefix: filters.key_prefix ?? "",
    actor: filters.actor ?? "",
    event_type: filters.event_type ?? "",
    from: unixMsToDatetimeLocal(filters.from_unix_ms),
    to: unixMsToDatetimeLocal(filters.to_unix_ms),
    page_token: "",
    page: "",
  };
}

let lastRowCount = PAGE_SIZE;

export default function AuditPage() {
  const { values, ready } = useQueryParams(QUERY_KEYS);
  const querySignature = JSON.stringify(values);
  const expectedInternalQueries = useRef(new Set<string>());
  const lastQuerySignature = useRef(querySignature);
  const [externalNavigation, setExternalNavigation] = useState(0);

  // A shallow replace made by this page acknowledges local state already in
  // memory. Browser Back/Forward (including a jump between two audit entries)
  // is different: rebuild from that URL so filters and a restored cursor agree.
  // Keeping this distinction here prevents a pagination acknowledgement from
  // discarding the cursor stack or text that is still only a form draft.
  useEffect(() => {
    if (!ready) {
      lastQuerySignature.current = querySignature;
      return;
    }
    if (lastQuerySignature.current === querySignature) return;
    lastQuerySignature.current = querySignature;
    if (expectedInternalQueries.current.delete(querySignature)) return;
    expectedInternalQueries.current.clear();
    setExternalNavigation((version) => version + 1);
  }, [querySignature, ready]);

  const rememberInternalNavigation = useCallback((next: QueryValues) => {
    const signature = JSON.stringify(next);
    expectedInternalQueries.current.add(signature);
    return () => expectedInternalQueries.current.delete(signature);
  }, []);

  // On a static export the query is empty until the client router hydrates;
  // rendering the list before that would fetch page 1 unfiltered for nothing.
  if (!ready)
    return (
      <TableSkeleton
        headers={TABLE_HEADERS}
        rows={lastRowCount}
        trailing={1}
        toolbar
        toolbarHint={PAGE_SORT_HINT}
        summary
      />
    );
  return (
    <AuditLog
      key={externalNavigation}
      initial={values}
      onInternalNavigation={rememberInternalNavigation}
    />
  );
}

function AuditLog({
  initial,
  onInternalNavigation,
}: {
  initial: QueryValues;
  onInternalNavigation: (next: QueryValues) => () => void;
}) {
  const toast = useToast();
  const { namespaces, loading: namespacesLoading, error: namespacesError } = useNamespaces();
  const replaceQuery = useQueryReplace("/audit");
  const currentQuery = useRef(initial);
  const acknowledgedQuery = useRef(initial);
  useEffect(() => {
    // Keep the last URL the router has actually published separately from an
    // optimistic target. A late Page 2 acknowledgement must not overwrite a
    // Page 3 action that was already issued from the same component.
    acknowledgedQuery.current = initial;
  }, [initial]);
  const replaceAuditQuery = useCallback(
    (patch: Record<string, string>) => {
      const next = { ...currentQuery.current };
      for (const key of QUERY_KEYS) {
        if (key in patch) next[key] = patch[key] || null;
      }
      const nextSignature = JSON.stringify(next);
      if (nextSignature === JSON.stringify(currentQuery.current)) return;
      // Update before router.replace resolves: a quick Page 2 → Page 3
      // sequence must build the second URL from Page 2, not stale props.
      currentQuery.current = next;
      const forget = onInternalNavigation(next);
      void replaceQuery(patch)
        .then((changed) => {
          // A false result means Next cancelled the transition and will never
          // publish the expected query. Successful replacements are consumed
          // by AuditPage's query effect, which runs with the router update.
          if (!changed) {
            forget();
            if (JSON.stringify(currentQuery.current) === nextSignature) {
              currentQuery.current = acknowledgedQuery.current;
            }
          }
        })
        .catch(() => {
          forget();
          if (JSON.stringify(currentQuery.current) === nextSignature) {
            currentQuery.current = acknowledgedQuery.current;
          }
        });
    },
    [onInternalNavigation, replaceQuery],
  );
  const sort = useSort<AuditEvent>("/audit", COLUMNS);
  const now = useNow();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadedScope, setLoadedScope] = useState("");
  const [loadError, setLoadError] = useState<string | null>(null);
  const { begin } = useLatestRequest();

  const [form, setForm] = useState<FilterForm>(() => formFromQuery(initial));
  const [applied, setApplied] = useState<AuditFilters>(() =>
    filtersFromForm(formFromQuery(initial)),
  );
  const [expanded, setExpanded] = useState<number | null>(null);
  const filterErrors = useFieldErrors<"key_prefix">();
  // Scoping the cursor on the applied filters resets it to page 1 whenever they change.
  const paging = useCursorPagination(JSON.stringify(applied), {
    pageToken: initial.page_token,
    page: initial.page ? Number(initial.page) : undefined,
  });

  const apps = useMemo(() => {
    const set = new Set<string>();
    for (const ns of namespaces) set.add(ns.app);
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces]);

  const envs = useMemo(() => {
    const set = new Set<string>();
    for (const ns of namespaces) {
      if (!form.app || ns.app === form.app) set.add(ns.env);
    }
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces, form.app]);

  // Preserve deep-linked filters that no longer exist so the query remains
  // legible, while still preventing an empty menu when no choices exist.
  const appOptions = useMemo(() => {
    const options = apps.map((app) => ({ value: app, label: app }));
    if (form.app && !apps.includes(form.app)) {
      options.unshift({
        value: form.app,
        label: namespacesLoading ? form.app : `${form.app} (not found)`,
      });
    }
    return options;
  }, [apps, form.app, namespacesLoading]);

  const envOptions = useMemo(() => {
    const options = envs.map((env) => ({ value: env, label: env }));
    if (form.env && !envs.includes(form.env)) {
      options.unshift({
        value: form.env,
        label: namespacesLoading ? form.env : `${form.env} (not found)`,
      });
    }
    return options;
  }, [envs, form.env, namespacesLoading]);

  const loadingEmptyNamespaces = namespacesLoading && namespaces.length === 0;
  const appPlaceholder = loadingEmptyNamespaces
    ? "Loading applications…"
    : namespacesError && appOptions.length === 0
      ? "Applications unavailable"
      : appOptions.length === 0
        ? "No applications available"
        : "All applications";
  const envPlaceholder = loadingEmptyNamespaces
    ? "Loading environments…"
    : !form.app && namespaces.length > 0
      ? "Select application first"
      : namespacesError && envOptions.length === 0
        ? "Environments unavailable"
        : envOptions.length === 0
          ? "No environments available"
          : "All environments";

  const prefixProblem = validateKeyPrefix(form.key_prefix.trim());
  const rangeProblem = rangeError(form.from, form.to);
  const appliedFilters = Object.values(applied).filter((v) => v !== undefined && v !== "").length;
  const hasFilters = appliedFilters > 0;

  const { setNextToken } = paging;
  const load = useCallback(
    async (token: string, filters: AuditFilters) => {
      const run = begin();
      const scope = JSON.stringify([token, filters]);
      setLoading(true);
      setLoadError(null);
      try {
        const res = await api.listAudit(
          { ...filters, page_size: PAGE_SIZE, page_token: token || undefined },
          { signal: run.signal },
        );
        if (!run.current) return;
        const list = res.events ?? [];
        setEvents(list);
        setLoadedScope(scope);
        lastRowCount = Math.max(5, list.length);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        if (run.current && !isAbortError(err)) {
          setLoadError(err instanceof Error ? err.message : "The audit log did not respond.");
          toast.error(err, "Failed to load audit events");
        }
      } finally {
        if (run.current) setLoading(false);
      }
    },
    [begin, setNextToken, toast],
  );

  const requestedScope = JSON.stringify([paging.pageToken, applied]);
  const eventsMatchScope = loadedScope === requestedScope;

  useEffect(() => {
    void load(paging.pageToken, applied);
  }, [load, paging.pageToken, applied]);

  function apply(e: React.FormEvent) {
    e.preventDefault();
    filterErrors.markAllTouched();
    if (prefixProblem || rangeProblem) return;
    setExpanded(null);
    const next = filtersFromForm(form);
    setApplied(next);
    replaceAuditQuery(queryFromFilters(next));
  }
  function clear() {
    setForm(EMPTY_FORM);
    filterErrors.reset();
    setExpanded(null);
    setApplied({});
    replaceAuditQuery(queryFromFilters({}));
  }

  // The cursor moves from event handlers, so the URL follows it here rather
  // than from an effect (see useQueryReplace).
  function nextPage() {
    replaceAuditQuery({ page_token: paging.nextToken, page: String(paging.page + 1) });
    paging.next();
  }
  function previousPage() {
    const page = paging.previousToken ? paging.page - 1 : 1;
    replaceAuditQuery({ page_token: paging.previousToken, page: page > 1 ? String(page) : "" });
    paging.previous();
  }
  function firstPage() {
    replaceAuditQuery({ page_token: "", page: "" });
    paging.reset();
  }

  function onApp(app: string) {
    // An environment is application-owned; clear it when the new application
    // does not define that environment.
    const stillValid = !app || namespaces.some((ns) => ns.app === app && ns.env === form.env);
    setForm({ ...form, app, env: stillValid ? form.env : "" });
  }

  return (
    <>
      <PageHeader
        title="Audit log"
        subtitle="Authorization decisions and administrative actions."
        actions={
          <Button
            variant="outline"
            onClick={() => void load(paging.pageToken, applied)}
            disabled={loading}
          >
            {loading ? <Spinner /> : <RefreshCw size={16} aria-hidden />}
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
        }
      />

      <form className="filters" onSubmit={apply}>
        <Field label="Application" htmlFor="f-app">
          <AppSelect
            id="f-app"
            value={form.app}
            disabled={loadingEmptyNamespaces || appOptions.length === 0}
            onValueChange={onApp}
            placeholder={appPlaceholder}
            options={appOptions}
          />
        </Field>
        <Field label="Environment" htmlFor="f-env">
          <AppSelect
            id="f-env"
            value={form.env}
            disabled={loadingEmptyNamespaces || !form.app || envOptions.length === 0}
            onValueChange={(env) => setForm({ ...form, env })}
            placeholder={envPlaceholder}
            options={envOptions}
          />
        </Field>
        <Field
          label="Key prefix"
          htmlFor="f-prefix"
          error={filterErrors.shown("key_prefix", prefixProblem)}
        >
          <Input
            id="f-prefix"
            className="font-mono"
            value={form.key_prefix}
            onChange={(e) => setForm({ ...form, key_prefix: e.target.value })}
            onBlur={() => filterErrors.touch("key_prefix")}
            placeholder="billing"
          />
        </Field>
        <Field label="Actor" htmlFor="f-actor">
          <Input
            id="f-actor"
            value={form.actor}
            onChange={(e) => setForm({ ...form, actor: e.target.value })}
            placeholder="gradethis-be"
          />
        </Field>
        <Field label="Event type" htmlFor="f-type">
          <Input
            id="f-type"
            value={form.event_type}
            onChange={(e) => setForm({ ...form, event_type: e.target.value })}
            placeholder="secret.read"
          />
        </Field>
        <Field label="From" htmlFor="f-from">
          <Input
            id="f-from"
            type="datetime-local"
            value={form.from}
            onChange={(e) => setForm({ ...form, from: e.target.value })}
          />
        </Field>
        <Field label="To" htmlFor="f-to" hint="End is exclusive" error={rangeProblem}>
          <Input
            id="f-to"
            type="datetime-local"
            value={form.to}
            onChange={(e) => setForm({ ...form, to: e.target.value })}
          />
        </Field>
        <Button
          type="submit"
          variant="outline"
          disabled={prefixProblem !== null || rangeProblem !== null}
        >
          Apply
        </Button>
        <Button type="button" variant="ghost" onClick={clear}>
          Clear
        </Button>
      </form>

      {loadError ? (
        <div className="danger-panel mb-4" role="alert">
          <strong>Could not load audit events.</strong> {loadError}
          {eventsMatchScope && events.length > 0
            ? " Showing the last successful results for this query."
            : " No results are available for the current query."}
        </div>
      ) : null}

      {loading && !eventsMatchScope ? (
        <TableSkeleton
          headers={TABLE_HEADERS}
          rows={lastRowCount}
          trailing={1}
          toolbar
          toolbarHint={PAGE_SORT_HINT}
          summary
        />
      ) : !eventsMatchScope ? null : events.length === 0 ? (
        <EmptyState
          icon={<Icon.audit size={20} />}
          title="No audit events"
          actions={
            hasFilters ? (
              <Button variant="outline" onClick={clear}>
                Clear filters
              </Button>
            ) : undefined
          }
        >
          {hasFilters
            ? "No events match the current filters."
            : "No audit events have been recorded yet."}
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <MobileListToolbar controller={sort} hint={PAGE_SORT_HINT} />
          <table className="data">
            <TableSummary
              shown={events.length}
              noun="events"
              filters={appliedFilters}
              hint={sort.sort ? PAGE_SORT_HINT : undefined}
            />
            <thead>
              <SortHeaderRow controller={sort} hint={PAGE_SORT_HINT} after={<th />} />
            </thead>
            <tbody>
              {sort.apply(events).map((e) => {
                const open = expanded === e.id;
                const hasMeta = !isEmptyJson(e.metadata_json);
                const resource = displayAuditResource(e);
                const resourceHref = links.auditResource(e);
                const compareQuery = auditCompareQuery(e);
                const compareHref = compareQuery ? links.releaseCompare(compareQuery) : null;
                const metaId = `audit-meta-${e.id}`;
                return (
                  <Fragment key={e.id}>
                    <tr>
                      <td
                        className="nowrap"
                        data-label="Time"
                        title={formatUnixMs(e.created_at_unix_ms)}
                      >
                        {formatRelative(e.created_at_unix_ms, now)}
                      </td>
                      <td className="mono" data-label="Event">
                        {e.event_type}
                      </td>
                      <td data-label="Actor">
                        {e.actor_identity || <span className="faint">—</span>}
                        {e.actor_type ? (
                          <span className="faint text-sm"> · {e.actor_type}</span>
                        ) : null}
                      </td>
                      <td data-label="Resource">
                        {resource ? (
                          <span className="cell-path">
                            {resourceHref ? (
                              <Link href={resourceHref} title={`Open ${e.resource_type}`}>
                                {resource}
                              </Link>
                            ) : (
                              resource
                            )}
                            {e.resource_type !== "configuration_release" &&
                            e.resource_version > 0 ? (
                              <span className="faint"> · v{e.resource_version}</span>
                            ) : null}
                            {compareHref && compareQuery ? (
                              <>
                                {" "}
                                <Link
                                  href={compareHref}
                                  className="text-sm"
                                  title="Compare the two releases this activation swapped"
                                >
                                  What changed (v{compareQuery.from} → v{compareQuery.to})
                                </Link>
                              </>
                            ) : null}
                          </span>
                        ) : (
                          <span className="faint">{e.resource_type || "—"}</span>
                        )}
                      </td>
                      <td data-label="Decision">
                        <Badge kind={decisionKind(e.decision)}>{e.decision || "—"}</Badge>
                      </td>
                      <td className="mono" data-label="Source IP">
                        {e.source_ip || <span className="faint">—</span>}
                      </td>
                      <td data-label="Actions">
                        {hasMeta ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-expanded={open}
                            aria-controls={metaId}
                            onClick={() => setExpanded(open ? null : e.id)}
                          >
                            {open ? "Hide" : "Details"}
                          </Button>
                        ) : null}
                      </td>
                    </tr>
                    {open && hasMeta ? (
                      <tr id={metaId}>
                        <td data-label="Metadata" colSpan={7}>
                          <JsonView raw={prettyJson(e.metadata_json)} />
                          {compareQuery ? (
                            <div className="mt-2">
                              <ReleaseDiffSummary
                                query={compareQuery}
                                href={compareHref ?? undefined}
                              />
                            </div>
                          ) : null}
                          {e.request_id ? (
                            <div className="faint text-sm mt-2">
                              request id: <span className="mono">{e.request_id}</span>
                            </div>
                          ) : null}
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <Pagination
        hasNext={eventsMatchScope && paging.hasNext}
        onNext={nextPage}
        hasPrevious={paging.hasPrevious}
        onPrevious={previousPage}
        onReset={firstPage}
        showReset={paging.page > 1}
        page={paging.page}
        count={loading || !eventsMatchScope ? undefined : events.length}
        loading={loading}
        noun="events"
      />
    </>
  );
}
