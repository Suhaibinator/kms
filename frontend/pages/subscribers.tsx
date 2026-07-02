import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Subscriber } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { Badge, EmptyState, Loading, PageHeader } from "@/components/ui";

const REFRESH_MS = 5000;

export default function SubscribersPage() {
  const toast = useToast();
  const [subscribers, setSubscribers] = useState<Subscriber[]>([]);
  const [currentRevision, setCurrentRevision] = useState(0);
  const [initialLoading, setInitialLoading] = useState(true);
  const [lastUpdated, setLastUpdated] = useState(0);
  // Re-render the "updated Ns ago" label on a timer without refetching.
  const [, setTick] = useState(0);

  const mounted = useRef(true);
  const erroredRef = useRef(false);

  const refresh = useCallback(
    async (background: boolean) => {
      try {
        const res = await api.subscribers();
        if (!mounted.current) return;
        setSubscribers(res.subscribers ?? []);
        setCurrentRevision(res.current_revision ?? 0);
        setLastUpdated(Date.now());
        erroredRef.current = false;
      } catch (err) {
        // Only surface an error once per failure streak to avoid toast spam on
        // the 5s poll.
        if (!background || !erroredRef.current) {
          toast.error(err, "Failed to load subscribers");
        }
        erroredRef.current = true;
      } finally {
        if (mounted.current && !background) setInitialLoading(false);
      }
    },
    [toast],
  );

  useEffect(() => {
    mounted.current = true;
    void refresh(false);
    const poll = window.setInterval(() => void refresh(true), REFRESH_MS);
    const ticker = window.setInterval(() => setTick((t) => t + 1), 1000);
    return () => {
      mounted.current = false;
      window.clearInterval(poll);
      window.clearInterval(ticker);
    };
  }, [refresh]);

  const staleCount = subscribers.filter((s) => s.last_acked_revision < currentRevision).length;

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
        <Loading />
      ) : subscribers.length === 0 ? (
        <EmptyState title="No applications are currently subscribed">
          Live subscribers appear here once an SDK client connects.
        </EmptyState>
      ) : (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Client</th>
                <th>Identity</th>
                <th>Paths</th>
                <th>Remote address</th>
                <th>Connected</th>
                <th>Last heartbeat</th>
                <th>Applied revision</th>
              </tr>
            </thead>
            <tbody>
              {subscribers.map((s) => {
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
                        {(s.paths ?? []).length === 0 ? (
                          <span className="faint">—</span>
                        ) : (
                          s.paths.map((p) => (
                            <Badge key={p} kind="neutral">
                              {p}
                            </Badge>
                          ))
                        )}
                      </div>
                    </td>
                    <td className="mono">{s.remote_addr || <span className="faint">—</span>}</td>
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
      )}
    </>
  );
}
