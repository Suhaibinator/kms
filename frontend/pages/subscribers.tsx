import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "@/components/icons";
import { Badge, EmptyState, PageHeader, TableSkeleton } from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatRelative, formatUnixMs } from "@/lib/format";
import type { NamespaceRef, Subscriber } from "@/lib/types";

const REFRESH_MS = 5000;

function formatNamespace(ns: NamespaceRef): string {
  return `${ns.env}/${ns.app}`;
}

// The namespace a subscriber is grouped under: its first watched namespace. A
// client subscribes namespace-wide, so this is stable.
function groupKey(s: Subscriber): string {
  const first = s.namespaces?.[0];
  return first ? formatNamespace(first) : "unscoped";
}

export default function SubscribersPage() {
  const toast = useToast();
  const [subscribers, setSubscribers] = useState<Subscriber[]>([]);
  const [currentRevision, setCurrentRevision] = useState(0);
  const [initialLoading, setInitialLoading] = useState(true);
  const [lastUpdated, setLastUpdated] = useState(0);

  const mounted = useRef(true);
  const erroredRef = useRef(false);
  const requestRef = useRef<AbortController | null>(null);

  const refresh = useCallback(
    async (background: boolean) => {
      // A manual refresh and the background poll share the same in-flight
      // guard, so slow responses can never pile up.
      if (requestRef.current) return;
      const controller = new AbortController();
      requestRef.current = controller;
      try {
        const res = await api.subscribers({ signal: controller.signal });
        if (!mounted.current) return;
        setSubscribers(res.subscribers ?? []);
        setCurrentRevision(res.current_revision ?? 0);
        setLastUpdated(Date.now());
        erroredRef.current = false;
      } catch (err) {
        if (isAbortError(err)) return;
        // Only surface an error once per failure streak to avoid toast spam on
        // the 5s poll.
        if (!background || !erroredRef.current) {
          toast.error(err, "Failed to load subscribers");
        }
        erroredRef.current = true;
      } finally {
        if (requestRef.current === controller) requestRef.current = null;
        if (mounted.current && !background) setInitialLoading(false);
      }
    },
    [toast],
  );

  useEffect(() => {
    mounted.current = true;
    let stopped = false;
    let pollTimer: number | undefined;

    const schedulePoll = () => {
      if (stopped || document.hidden || pollTimer !== undefined) return;
      pollTimer = window.setTimeout(async () => {
        pollTimer = undefined;
        await refresh(true);
        schedulePoll();
      }, REFRESH_MS);
    };
    const onVisibilityChange = () => {
      if (pollTimer !== undefined) window.clearTimeout(pollTimer);
      pollTimer = undefined;
      if (!document.hidden) {
        void refresh(true).finally(() => {
          schedulePoll();
        });
      }
    };

    void refresh(false).finally(schedulePoll);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      stopped = true;
      mounted.current = false;
      if (pollTimer !== undefined) window.clearTimeout(pollTimer);
      document.removeEventListener("visibilitychange", onVisibilityChange);
      requestRef.current?.abort();
      requestRef.current = null;
    };
  }, [refresh]);

  const staleCount = subscribers.filter((s) => s.last_acked_revision < currentRevision).length;

  const groups = useMemo(() => {
    const map = new Map<string, Subscriber[]>();
    for (const s of subscribers) {
      const key = groupKey(s);
      const list = map.get(key) ?? [];
      list.push(s);
      map.set(key, list);
    }
    return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [subscribers]);

  return (
    <>
      <PageHeader
        title="Subscribers"
        subtitle="Applications currently live-subscribed to configuration."
        actions={
          <>
            <span className="badge badge-success">
              <span className="badge-dot" style={{ background: "var(--success)" }} />
              live · updated {lastUpdated ? formatRelative(lastUpdated) : "—"}
            </span>
            <button className="btn" onClick={() => void refresh(false)}>
              <RefreshCw size={16} aria-hidden />
              Refresh
            </button>
          </>
        }
      />

      <div className="card-grid mb-16">
        <div className="stat">
          <div className="stat-label">Current revision</div>
          <div className="stat-value">{currentRevision}</div>
          <div className="stat-sub">latest configuration</div>
        </div>
        <div className="stat">
          <div className="stat-label">Connected</div>
          <div className="stat-value">{subscribers.length}</div>
          <div className="stat-sub">active subscriptions</div>
        </div>
        <div className="stat">
          <div className="stat-label">Behind latest</div>
          <div className="stat-value">{staleCount}</div>
          <div className="stat-sub">
            {staleCount === 0 ? (
              <span className="text-success">all applied</span>
            ) : (
              <span className="text-warning">need to catch up</span>
            )}
          </div>
        </div>
      </div>

      {initialLoading ? (
        <TableSkeleton
          headers={[
            "Client",
            "Identity",
            "Namespaces",
            "Remote address",
            "Connected",
            "Last heartbeat",
            "Applied revision",
          ]}
          rows={4}
        />
      ) : subscribers.length === 0 ? (
        <EmptyState
          icon={<Icon.subscribers size={20} />}
          title="No applications are currently subscribed"
        >
          Live subscribers appear here once an SDK client connects.
        </EmptyState>
      ) : (
        groups.map(([ns, list]) => (
          <div key={ns} className="ns-group">
            <div className="ns-group-title">
              <span className="ns-group-env">{ns}</span>
              <span className="faint text-sm">
                {list.length} {list.length === 1 ? "subscriber" : "subscribers"}
              </span>
            </div>
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Client</th>
                    <th>Identity</th>
                    <th>Namespaces</th>
                    <th>Remote address</th>
                    <th>Connected</th>
                    <th>Last heartbeat</th>
                    <th>Applied revision</th>
                  </tr>
                </thead>
                <tbody>
                  {list.map((s) => {
                    const behind = currentRevision - s.last_acked_revision;
                    const stale = behind > 0;
                    return (
                      <tr
                        key={s.instance_id || `${s.client_name}-${s.remote_addr}`}
                        style={stale ? { background: "var(--warning-soft)" } : undefined}
                      >
                        <td>
                          {s.client_name}
                          {s.instance_id ? (
                            <div className="faint text-sm mono">{s.instance_id}</div>
                          ) : null}
                        </td>
                        <td className="mono">{s.identity || <span className="faint">—</span>}</td>
                        <td>
                          <div className="row-wrap">
                            {(s.namespaces ?? []).length === 0 ? (
                              <span className="faint">—</span>
                            ) : (
                              s.namespaces.map((ns, i) => (
                                <Badge key={i} kind="neutral">
                                  {formatNamespace(ns)}
                                </Badge>
                              ))
                            )}
                          </div>
                        </td>
                        <td className="mono">
                          {s.remote_addr || <span className="faint">—</span>}
                        </td>
                        <td className="nowrap" title={formatUnixMs(s.connected_at_unix_ms)}>
                          {formatRelative(s.connected_at_unix_ms)}
                        </td>
                        <td className="nowrap" title={formatUnixMs(s.last_heartbeat_unix_ms)}>
                          {formatRelative(s.last_heartbeat_unix_ms)}
                        </td>
                        <td>
                          {stale ? (
                            <Badge kind="warning">
                              v{s.last_acked_revision} · {behind} behind
                            </Badge>
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
          </div>
        ))
      )}
    </>
  );
}
