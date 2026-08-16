import { Button } from "@/components/ui/button";
import { ArrowRight, RefreshCw } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { Icon } from "@/components/icons";
import {
  Badge,
  EmptyState,
  PageHeader,
  Spinner,
  StatSkeleton,
  TableSkeleton,
} from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { displayAuditResource, formatRelative } from "@/lib/format";
import type { AuditEvent, HealthResponse, Subscriber } from "@/lib/types";

interface Count {
  value: number;
  // true when the namespace list was truncated, so totals are a lower bound.
  more: boolean;
}

interface Dashboard {
  health: HealthResponse | null;
  namespaces: Count | null;
  parameters: Count | null;
  secrets: Count | null;
  subscribers: Subscriber[];
  currentRevision: number;
  audit: AuditEvent[];
}

const EMPTY: Dashboard = {
  health: null,
  namespaces: null,
  parameters: null,
  secrets: null,
  subscribers: [],
  currentRevision: 0,
  audit: [],
};

function CountText({ c }: { c: Count | null }) {
  if (!c) return <span className="faint">—</span>;
  return (
    <>
      {c.value}
      {c.more ? "+" : ""}
    </>
  );
}

export default function DashboardPage() {
  const toast = useToast();
  const [data, setData] = useState<Dashboard>(EMPTY);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    // Parameter/secret totals are cross-namespace overviews, which the
    // namespace-scoped list APIs can't answer directly — they come from the
    // per-namespace counts on ListNamespaces (plan §10).
    const [health, ns, subs, audit] = await Promise.allSettled([
      api.health(),
      api.listNamespaces(200),
      api.subscribers(),
      api.listAudit({ page_size: 8 }),
    ]);

    const next: Dashboard = { ...EMPTY };
    if (health.status === "fulfilled") next.health = health.value;
    if (ns.status === "fulfilled") {
      const list = ns.value.namespaces ?? [];
      const more = !!ns.value.next_page_token;
      next.namespaces = { value: list.length, more };
      next.parameters = { value: list.reduce((sum, n) => sum + (n.parameter_count ?? 0), 0), more };
      next.secrets = { value: list.reduce((sum, n) => sum + (n.secret_count ?? 0), 0), more };
    }
    if (subs.status === "fulfilled") {
      next.subscribers = subs.value.subscribers ?? [];
      next.currentRevision = subs.value.current_revision ?? 0;
    }
    if (audit.status === "fulfilled") next.audit = audit.value.events ?? [];

    // Surface the first failure (if any) without blocking the rest.
    const firstError = [health, ns, subs, audit].find((r) => r.status === "rejected") as
      | PromiseRejectedResult
      | undefined;
    if (firstError) toast.error(firstError.reason, "Some dashboard data failed to load");

    setData(next);
    setLoading(false);
  }, [toast]);

  useEffect(() => {
    void load();
  }, [load]);

  const staleCount = data.subscribers.filter(
    (s) => s.last_acked_revision < data.currentRevision,
  ).length;

  const h = data.health;

  return (
    <>
      <PageHeader
        title="Overview"
        subtitle="Service status and configuration at a glance."
        actions={
          <Button variant="outline" onClick={() => void load()} disabled={loading}>
            {loading ? <Spinner /> : null}
            {!loading ? <RefreshCw size={16} aria-hidden /> : null}
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
        }
      />

      {/* The page frame stays mounted across refreshes; only the cells swap to
          skeletons, so nothing below them shifts when the data lands. */}
      {loading ? (
        <div className="card-grid">
          <StatSkeleton label="Service" />
          <StatSkeleton label="Current revision" />
          <StatSkeleton label="Namespaces" />
          <StatSkeleton label="Parameters" />
          <StatSkeleton label="Secrets" />
          <StatSkeleton label="Subscribers" />
        </div>
      ) : (
        <div className="card-grid">
          <div className="stat">
            <div className="stat-label">Service</div>
            <div className="stat-badges">
              <Badge kind={h?.healthy ? "success" : "danger"}>
                {h?.healthy ? "healthy" : "unhealthy"}
              </Badge>
              <Badge kind={h?.ready ? "success" : "warning"}>
                {h?.ready ? "ready" : "not ready"}
              </Badge>
            </div>
            <div className="stat-sub">
              {h?.version ? `version ${h.version}` : "version unknown"}
            </div>
          </div>

          <div className="stat">
            <div className="stat-label">Current revision</div>
            <div className="stat-value">{h?.current_revision ?? data.currentRevision}</div>
            <div className="stat-sub">latest applied configuration</div>
          </div>

          <div className="stat">
            <div className="stat-label">Namespaces</div>
            <div className="stat-value">
              <CountText c={data.namespaces} />
            </div>
            <div className="stat-sub">
              <Link href="/namespaces">
                Manage <ArrowRight size={14} aria-hidden />
              </Link>
            </div>
          </div>

          <div className="stat">
            <div className="stat-label">Parameters</div>
            <div className="stat-value">
              <CountText c={data.parameters} />
            </div>
            <div className="stat-sub">
              <Link href="/parameters">
                Manage <ArrowRight size={14} aria-hidden />
              </Link>
            </div>
          </div>

          <div className="stat">
            <div className="stat-label">Secrets</div>
            <div className="stat-value">
              <CountText c={data.secrets} />
            </div>
            <div className="stat-sub">
              <Link href="/secrets">
                Manage <ArrowRight size={14} aria-hidden />
              </Link>
            </div>
          </div>

          <div className="stat">
            <div className="stat-label">Subscribers</div>
            <div className="stat-value">{data.subscribers.length}</div>
            <div className="stat-sub">
              {staleCount > 0 ? (
                <span className="text-warning">{staleCount} behind latest revision</span>
              ) : (
                <span className="text-success">all up to date</span>
              )}
            </div>
          </div>
        </div>
      )}

      <div className="card mt-24">
        <div className="card-title">
          Recent activity
          <Link href="/audit" className="text-sm">
            View audit log <ArrowRight size={14} aria-hidden />
          </Link>
        </div>
        {loading ? (
          <TableSkeleton headers={["When", "Event", "Actor", "Resource", "Decision"]} rows={5} />
        ) : data.audit.length === 0 ? (
          <EmptyState icon={<Icon.audit size={20} />} title="No recent events">
            Administrative actions and policy decisions will appear here.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>When</th>
                  <th>Event</th>
                  <th>Actor</th>
                  <th>Resource</th>
                  <th>Decision</th>
                </tr>
              </thead>
              <tbody>
                {data.audit.map((e) => {
                  const resource = displayAuditResource(e);
                  return (
                    <tr key={e.id}>
                      <td className="nowrap">{formatRelative(e.created_at_unix_ms)}</td>
                      <td className="mono">{e.event_type}</td>
                      <td>
                        {e.actor_identity || <span className="faint">—</span>}
                        {e.actor_type ? (
                          <span className="faint text-sm"> · {e.actor_type}</span>
                        ) : null}
                      </td>
                      <td className="cell-path">{resource || <span className="faint">—</span>}</td>
                      <td>
                        <Badge
                          kind={
                            e.decision === "allow"
                              ? "success"
                              : e.decision === "deny"
                                ? "danger"
                                : "neutral"
                          }
                        >
                          {e.decision || "—"}
                        </Badge>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-title">
          Live subscribers
          <Link href="/subscribers" className="text-sm">
            View all <ArrowRight size={14} aria-hidden />
          </Link>
        </div>
        {loading ? (
          <TableSkeleton headers={["Client", "Last heartbeat", "Applied revision"]} rows={3} />
        ) : data.subscribers.length === 0 ? (
          <EmptyState
            icon={<Icon.subscribers size={20} />}
            title="No applications are currently subscribed"
          >
            Clients appear here once they open a watch stream.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Client</th>
                  <th>Last heartbeat</th>
                  <th>Applied revision</th>
                </tr>
              </thead>
              <tbody>
                {data.subscribers.slice(0, 6).map((s) => {
                  const behind = data.currentRevision - s.last_acked_revision;
                  return (
                    <tr key={s.instance_id || s.client_name}>
                      <td>
                        {s.client_name}
                        {s.instance_id ? (
                          <span className="faint text-sm"> · {s.instance_id}</span>
                        ) : null}
                      </td>
                      <td className="nowrap">{formatRelative(s.last_heartbeat_unix_ms)}</td>
                      <td>
                        {behind > 0 ? (
                          <Badge kind="warning">{behind} behind</Badge>
                        ) : (
                          <Badge kind="success">up to date</Badge>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
