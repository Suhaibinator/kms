import { RefreshCw } from "lucide-react";
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
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { useLatestRequest } from "@/lib/hooks";
import type { HealthResponse, KeyMetadata } from "@/lib/types";
import { useNow } from "@/lib/useNow";

function keyStateKind(state: string): "success" | "warning" | "neutral" {
  const s = state.toLowerCase();
  if (s === "active" || s === "enabled" || s === "current") return "success";
  if (s === "rotating" || s === "pending" || s === "disabled") return "warning";
  return "neutral";
}

export default function HealthPage() {
  const toast = useToast();
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [keys, setKeys] = useState<KeyMetadata[]>([]);
  const [loading, setLoading] = useState(true);
  const now = useNow();
  const { begin } = useLatestRequest();

  const load = useCallback(async () => {
    const run = begin();
    setLoading(true);
    const [h, k] = await Promise.allSettled([
      api.health({ signal: run.signal }),
      api.keys({ signal: run.signal }),
    ]);
    if (!run.current) return;
    if (h.status === "fulfilled") setHealth(h.value);
    else {
      // A stale "healthy" from the previous load must not outlive a failed
      // refresh; null renders as "unknown".
      setHealth(null);
      toast.error(h.reason, "Failed to load health");
    }
    if (k.status === "fulfilled") setKeys(k.value.keys ?? []);
    else toast.error(k.reason, "Failed to load keys");
    setLoading(false);
  }, [begin, toast]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <PageHeader
        title="Health & keys"
        subtitle="Service status and key metadata (never key material)."
        actions={
          <Button variant="outline" onClick={() => void load()} disabled={loading}>
            {loading ? <Spinner /> : null}
            {!loading ? <RefreshCw size={16} aria-hidden /> : null}
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
        }
      />

      {loading && health === null ? (
        <>
          <div className="card-grid mb-4">
            <StatSkeleton label="Health" />
            <StatSkeleton label="Readiness" />
            <StatSkeleton label="Version" />
            <StatSkeleton label="Current revision" />
            <StatSkeleton label="Admin client cert" />
          </div>
          <div className="card">
            <h2 className="card-title">Encryption keys</h2>
            <TableSkeleton headers={["ID", "Source", "State", "Created"]} rows={3} />
          </div>
        </>
      ) : (
        <>
          <div className="card-grid mb-4">
            <div className="stat">
              <div className="stat-label">Health</div>
              <div className="stat-badges">
                {health === null ? (
                  <Badge kind="neutral">unknown</Badge>
                ) : (
                  <Badge kind={health.healthy ? "success" : "danger"}>
                    {health.healthy ? "healthy" : "unhealthy"}
                  </Badge>
                )}
              </div>
            </div>
            <div className="stat">
              <div className="stat-label">Readiness</div>
              <div className="stat-badges">
                {health === null ? (
                  <Badge kind="neutral">unknown</Badge>
                ) : (
                  <Badge kind={health.ready ? "success" : "warning"}>
                    {health.ready ? "ready" : "not ready"}
                  </Badge>
                )}
              </div>
            </div>
            <div className="stat">
              <div className="stat-label">Version</div>
              <div className="stat-value-sm mono">{health?.version || "—"}</div>
            </div>
            <div className="stat">
              <div className="stat-label">Current revision</div>
              <div className="stat-value">{health?.current_revision ?? "—"}</div>
            </div>
            {/* Whether admins must present a client certificate on top of their
                token. `relaxed` also covers a server running without TLS, where
                the requirement cannot be enforced. */}
            <div className="stat">
              <div className="stat-label">Admin client cert</div>
              <div className="stat-badges">
                {health === null ? (
                  <Badge kind="neutral">unknown</Badge>
                ) : (
                  <Badge kind={health.admin_client_cert_required ? "success" : "warning"}>
                    {health.admin_client_cert_required ? "required" : "relaxed"}
                  </Badge>
                )}
              </div>
            </div>
          </div>

          <div className="card">
            <h2 className="card-title">Encryption keys</h2>
            {keys.length === 0 ? (
              <EmptyState icon={<Icon.health size={20} />} title="No key metadata available">
                The service exposes key metadata once a master key provider is configured.
              </EmptyState>
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>ID</th>
                      <th>Source</th>
                      <th>State</th>
                      <th>Created</th>
                    </tr>
                  </thead>
                  <tbody>
                    {keys.map((k) => (
                      <tr key={k.id}>
                        <td className="mono">{k.id}</td>
                        <td>{k.source || <span className="faint">—</span>}</td>
                        <td>
                          <Badge kind={keyStateKind(k.state)}>{k.state || "unknown"}</Badge>
                        </td>
                        <td className="nowrap" title={formatUnixMs(k.created_at_unix_ms)}>
                          {formatRelative(k.created_at_unix_ms, now)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="card">
            <h2 className="card-title">Backup &amp; recovery</h2>
            <div className="info-panel">
              <p className="mt-0">
                Back up the SQLite database <strong>and</strong> the master key file together, and
                store them separately from each other. Neither is useful on its own: the database
                holds only ciphertext, and the master key alone decrypts nothing.
              </p>
              <p>
                <strong>Bound secret versions</strong> additionally require their operator-supplied
                binding key. There is no escrow — if the master key or a cohort&apos;s binding key
                is lost, those versions are permanently unrecoverable. Per-secret access tokens
                remain an independent optional gate. Restore procedures should verify that the
                master key provider is reachable before the service reports ready.
              </p>
            </div>
          </div>
        </>
      )}
    </>
  );
}
