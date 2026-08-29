import { RefreshCw } from "lucide-react";
import Link from "next/link";
import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import { Icon } from "@/components/icons";
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
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
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
const TABLE_HEADERS = ["Time", "Event", "Actor", "Resource", "Decision", "Source IP"];

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
  // On a static export the query is empty until the client router hydrates;
  // rendering the list before that would fetch page 1 unfiltered for nothing.
  if (!ready) return <TableSkeleton headers={TABLE_HEADERS} rows={lastRowCount} />;
  return <AuditLog initial={values} />;
}

function AuditLog({ initial }: { initial: QueryValues }) {
  const toast = useToast();
  const { namespaces } = useNamespaces();
  const replaceQuery = useQueryReplace("/audit");
  const now = useNow();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
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

  const prefixProblem = validateKeyPrefix(form.key_prefix.trim());
  const rangeProblem = rangeError(form.from, form.to);
  const hasFilters = Object.values(applied).some((v) => v !== undefined && v !== "");

  const { setNextToken } = paging;
  const load = useCallback(
    async (token: string, filters: AuditFilters) => {
      const run = begin();
      setLoading(true);
      try {
        const res = await api.listAudit(
          { ...filters, page_size: PAGE_SIZE, page_token: token || undefined },
          { signal: run.signal },
        );
        if (!run.current) return;
        const list = res.events ?? [];
        setEvents(list);
        lastRowCount = Math.max(5, list.length);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        if (run.current && !isAbortError(err)) toast.error(err, "Failed to load audit events");
      } finally {
        if (run.current) setLoading(false);
      }
    },
    [begin, setNextToken, toast],
  );

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
    replaceQuery(queryFromFilters(next));
  }
  function clear() {
    setForm(EMPTY_FORM);
    filterErrors.reset();
    setExpanded(null);
    setApplied({});
    replaceQuery(queryFromFilters({}));
  }

  // The cursor moves from event handlers, so the URL follows it here rather
  // than from an effect (see useQueryReplace).
  function nextPage() {
    replaceQuery({ page_token: paging.nextToken, page: String(paging.page + 1) });
    paging.next();
  }
  function previousPage() {
    const page = paging.previousToken ? paging.page - 1 : 1;
    replaceQuery({ page_token: paging.previousToken, page: page > 1 ? String(page) : "" });
    paging.previous();
  }
  function firstPage() {
    replaceQuery({ page_token: "", page: "" });
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
            onValueChange={onApp}
            placeholder="All applications"
            options={apps.map((app) => ({ value: app, label: app }))}
          />
        </Field>
        <Field label="Environment" htmlFor="f-env">
          <AppSelect
            id="f-env"
            value={form.env}
            disabled={!form.app}
            onValueChange={(env) => setForm({ ...form, env })}
            placeholder={form.app ? "All environments" : "Select application first"}
            options={envs.map((env) => ({ value: env, label: env }))}
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

      {loading ? (
        <TableSkeleton headers={TABLE_HEADERS} rows={lastRowCount} />
      ) : events.length === 0 ? (
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
              {events.map((e) => {
                const open = expanded === e.id;
                const hasMeta = !isEmptyJson(e.metadata_json);
                const resource = displayAuditResource(e);
                const resourceHref = links.auditResource(e);
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
                            {e.resource_version > 0 ? (
                              <span className="faint"> · v{e.resource_version}</span>
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
                      <td>
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
                        <td colSpan={7}>
                          <JsonView raw={prettyJson(e.metadata_json)} />
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
        hasNext={paging.hasNext}
        onNext={nextPage}
        hasPrevious={paging.hasPrevious}
        onPrevious={previousPage}
        onReset={firstPage}
        showReset={paging.page > 1}
        page={paging.page}
        count={loading ? undefined : events.length}
        loading={loading}
        noun="events"
      />
    </>
  );
}
