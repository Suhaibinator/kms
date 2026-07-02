import { Fragment, useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { AuditEvent, AuditFilters } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { datetimeLocalToUnixMs, formatUnixMs, isEmptyJson, prettyJson } from "@/lib/format";
import { Badge, EmptyState, JsonView, Loading, PageHeader, Pagination } from "@/components/ui";

interface FilterForm {
  path_prefix: string;
  actor: string;
  event_type: string;
  from: string;
  to: string;
}

const EMPTY_FORM: FilterForm = {
  path_prefix: "",
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

export default function AuditPage() {
  const toast = useToast();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  const [form, setForm] = useState<FilterForm>(EMPTY_FORM);
  const [applied, setApplied] = useState<AuditFilters>({});
  const [expanded, setExpanded] = useState<number | null>(null);

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
      path_prefix: form.path_prefix.trim() || undefined,
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

  return (
    <>
      <PageHeader title="Audit log" subtitle="Authorization decisions and administrative actions." />

      <form className="filters" onSubmit={apply}>
        <div className="field">
          <label className="field-label" htmlFor="f-prefix">
            Path prefix
          </label>
          <input
            id="f-prefix"
            className="input mono"
            value={form.path_prefix}
            onChange={(e) => setForm({ ...form, path_prefix: e.target.value })}
            placeholder="/prod"
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
                        {e.resource_path ? (
                          <span className="cell-path">
                            {e.resource_path}
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
