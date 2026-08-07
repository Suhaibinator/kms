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
import { formatUnixMs } from "@/lib/format";
import type { HealthResponse, KeyMetadata } from "@/lib/types";

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

  const load = useCallback(async () => {
    setLoading(true);
    const [h, k] = await Promise.allSettled([api.health(), api.keys()]);
    if (h.status === "fulfilled") setHealth(h.value);
    else toast.error(h.reason, "Failed to load health");
    if (k.status === "fulfilled") setKeys(k.value.keys ?? []);
    else toast.error(k.reason, "Failed to load keys");
    setLoading(false);
  }, [toast]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <PageHeader
        title="Health & keys"
        subtitle="Service status and key metadata (never key material)."
        actions={
          <button className="btn" onClick={() => void load()} disabled={loading}>
            {loading ? <Spinner /> : null}
            {loading ? "Refreshing…" : "Refresh"}
          </button>
        }
      />

      {loading ? (
        <>
          <div className="card-grid mb-16">
            <StatSkeleton label="Health" />
            <StatSkeleton label="Readiness" />
            <StatSkeleton label="Version" />
            <StatSkeleton label="Current revision" />
          </div>
          <div className="card">
            <div className="card-title">Encryption keys</div>
            <TableSkeleton headers={["ID", "Source", "State", "Created"]} rows={3} />
          </div>
        </>
      ) : (
        <>
          <div className="card-grid mb-16">
            <div className="stat">
              <div className="stat-label">Health</div>
              <div className="stat-badges">
                <Badge kind={health?.healthy ? "success" : "danger"}>
                  {health?.healthy ? "healthy" : "unhealthy"}
                </Badge>
              </div>
            </div>
            <div className="stat">
              <div className="stat-label">Readiness</div>
              <div className="stat-badges">
                <Badge kind={health?.ready ? "success" : "warning"}>
                  {health?.ready ? "ready" : "not ready"}
                </Badge>
              </div>
            </div>
            <div className="stat">
              <div className="stat-label">Version</div>
              <div className="stat-value mono" style={{ fontSize: 18 }}>
                {health?.version || "—"}
              </div>
            </div>
            <div className="stat">
              <div className="stat-label">Current revision</div>
              <div className="stat-value">{health?.current_revision ?? "—"}</div>
            </div>
          </div>

          <div className="card">
            <div className="card-title">Encryption keys</div>
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
                        <td className="nowrap">{formatUnixMs(k.created_at_unix_ms)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="card">
            <div className="card-title">Backup &amp; recovery</div>
            <div className="info-panel">
              <p className="mt-0">
                Back up the SQLite database <strong>and</strong> the master key file together, and
                store them separately from each other. Neither is useful on its own: the database
                holds only ciphertext, and the master key alone decrypts nothing.
              </p>
              <p>
                <strong>Client-bound secrets</strong> additionally require each application&apos;s
                per-secret access token. There is no escrow — if the master key or a client token is
                lost, those secrets are permanently unrecoverable. Restore procedures should verify
                that the master key provider is reachable before the service reports ready.
              </p>
            </div>
          </div>
        </>
      )}
    </>
  );
}
