import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import type { AuditEvent, AuditFilters } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { useNamespaces } from "@/lib/hooks";
import { datetimeLocalToUnixMs, formatUnixMs, isEmptyJson, prettyJson } from "@/lib/format";
import { Badge, EmptyState, JsonView, Loading, PageHeader, Pagination } from "@/components/ui";

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

function decisionKind(decision: string): "success" | "danger" | "neutral" {
  if (decision === "allow") return "success";
  if (decision === "deny") return "danger";
  return "neutral";
}

// Render the resource an event touched: full path when a key is present,
// otherwise the namespace, otherwise just the resource type.
function resourceLabel(e: AuditEvent): string | null {
  if (e.resource_env && e.resource_app && e.resource_key) {
    return `/${e.resource_env}/${e.resource_app}/${e.resource_key}`;
  }
  if (e.resource_env && e.resource_app) {
    return `${e.resource_env}/${e.resource_app}`;
  }
  return null;
}

export default function AuditPage() {
  const toast = useToast();
  const { namespaces } = useNamespaces();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  const [form, setForm] = useState<FilterForm>(EMPTY_FORM);
  const [applied, setApplied] = useState<AuditFilters>({});
  const [expanded, setExpanded] = useState<number | null>(null);

  const envs = useMemo(() => {
    const set = new Set<string>();
    for (const ns of namespaces) set.add(ns.env);
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces]);

  const apps = useMemo(() => {
    const set = new Set<string>();
    for (const ns of namespaces) {
      if (!form.env || ns.env === form.env) set.add(ns.app);
    }
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [namespaces, form.env]);

  const load = useCallback(
    async (token: string, filters: AuditFilters) => {
      setLoading(true);
      try {
        const res = await api.listAudit({ ...filters, page_size: 50, page_token: token || undefined });
        setEvents(res.events ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        toast.error(err, "Failed to load audit events");
      } finally {
        setLoading(false);
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken, applied);
  }, [load, pageToken, applied]);

  function apply(e: React.FormEvent) {
    e.preventDefault();
    setPageStack([]);
    setPageToken("");
    setExpanded(null);
    setApplied({
      env: form.env.trim() || undefined,
      app: form.app.trim() || undefined,
      key_prefix: form.key_prefix.trim() || undefined,
      actor: form.actor.trim() || undefined,
      event_type: form.event_type.trim() || undefined,
      from_unix_ms: datetimeLocalToUnixMs(form.from),
      to_unix_ms: datetimeLocalToUnixMs(form.to),
    });
  }
  function clear() {
    setForm(EMPTY_FORM);
    setPageStack([]);
    setPageToken("");
    setExpanded(null);
    setApplied({});
  }
  function goNext() {
    if (!nextToken) return;
    setPageStack((s) => [...s, pageToken]);
    setPageToken(nextToken);
  }
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  function onEnv(env: string) {
    // Clear the app if it no longer belongs to the chosen env.
    const stillValid = !env || namespaces.some((ns) => ns.env === env && ns.app === form.app);
    setForm({ ...form, env, app: stillValid ? form.app : "" });
  }

  return (
    <>
      <PageHeader title="Audit log" subtitle="Authorization decisions and administrative actions." />

      <form className="filters" onSubmit={apply}>
        <div className="field">
          <label className="field-label" htmlFor="f-env">
            Environment
          </label>
          <select
            id="f-env"
            className="select"
            value={form.env}
            onChange={(e) => onEnv(e.target.value)}
          >
            <option value="">All envs</option>
            {envs.map((env) => (
              <option key={env} value={env}>
                {env}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label className="field-label" htmlFor="f-app">
            Application
          </label>
          <select
            id="f-app"
            className="select"
            value={form.app}
            onChange={(e) => setForm({ ...form, app: e.target.value })}
          >
            <option value="">All apps</option>
            {apps.map((app) => (
              <option key={app} value={app}>
                {app}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label className="field-label" htmlFor="f-prefix">
            Key prefix
          </label>
          <input
            id="f-prefix"
            className="input mono"
            value={form.key_prefix}
            onChange={(e) => setForm({ ...form, key_prefix: e.target.value })}
            placeholder="billing/"
          />
        </div>
        <div className="field">
          <label className="field-label" htmlFor="f-actor">
            Actor
          </label>
          <input
            id="f-actor"
            className="input"
            value={form.actor}
            onChange={(e) => setForm({ ...form, actor: e.target.value })}
            placeholder="gradethis-be"
          />
        </div>
        <div className="field">
          <label className="field-label" htmlFor="f-type">
            Event type
          </label>
          <input
            id="f-type"
            className="input"
            value={form.event_type}
            onChange={(e) => setForm({ ...form, event_type: e.target.value })}
            placeholder="secret.read"
          />
        </div>
        <div className="field">
          <label className="field-label" htmlFor="f-from">
            From
          </label>
          <input
            id="f-from"
            className="input"
            type="datetime-local"
            value={form.from}
            onChange={(e) => setForm({ ...form, from: e.target.value })}
          />
        </div>
        <div className="field">
          <label className="field-label" htmlFor="f-to">
            To
          </label>
          <input
            id="f-to"
            className="input"
            type="datetime-local"
            value={form.to}
            onChange={(e) => setForm({ ...form, to: e.target.value })}
          />
        </div>
        <button type="submit" className="btn">
          Apply
        </button>
        <button type="button" className="btn btn-ghost" onClick={clear}>
          Clear
        </button>
      </form>

      {loading ? (
        <Loading />
      ) : events.length === 0 ? (
        <EmptyState title="No audit events">No events match the current filters.</EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Time</th>
                <th>Event</th>
                <th>Actor</th>
                <th>Resource</th>
                <th>Decision</th>
                <th>Source IP</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {events.map((e) => {
                const open = expanded === e.id;
                const hasMeta = !isEmptyJson(e.metadata_json);
                const resource = resourceLabel(e);
                return (
                  <Fragment key={e.id}>
                    <tr>
                      <td className="nowrap" data-label="Time">
                        {formatUnixMs(e.created_at_unix_ms)}
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
                            {resource}
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
                          <button
                            className="btn btn-sm btn-ghost"
                            onClick={() => setExpanded(open ? null : e.id)}
                          >
                            {open ? "Hide" : "Details"}
                          </button>
                        ) : null}
                      </td>
                    </tr>
                    {open && hasMeta ? (
                      <tr>
                        <td colSpan={7}>
                          <JsonView raw={prettyJson(e.metadata_json)} />
                          {e.request_id ? (
                            <div className="faint text-sm mt-8">
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
        hasNext={!!nextToken}
        onNext={goNext}
        onReset={goReset}
        showReset={pageStack.length > 0}
      />
    </>
  );
}
