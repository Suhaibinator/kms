import { ArrowRight } from "lucide-react";
import Link from "next/link";
import { Badge, StatSkeleton } from "@/components/ui";
import type { HealthResponse } from "@/lib/types";

export interface Count {
  value: number;
  /** true when the namespace list was truncated, so totals are a lower bound. */
  more: boolean;
}

export interface ServiceStripProps {
  loading: boolean;
  health: HealthResponse | null;
  /** true when /health itself could not be reached — a console-side failure,
   *  never reported as the service being unhealthy. */
  healthFailed: boolean;
  /** true when the namespace list failed, so the three totals are unknown
   *  rather than zero. */
  countsFailed?: boolean;
  currentRevision: number;
  namespaces: Count | null;
  parameters: Count | null;
  secrets: Count | null;
  subscriberCount: number;
  staleCount: number;
  /** `grid` is the classic six-card grid; `strip` packs the same six into one row. */
  layout?: "grid" | "strip";
}

export function CountText({ c }: { c: Count | null }) {
  if (!c) return <span className="faint">—</span>;
  return (
    <>
      {c.value}
      {c.more ? "+" : ""}
    </>
  );
}

const LABELS = [
  "Service",
  "Current revision",
  "Namespaces",
  "Parameters",
  "Secrets",
  "Subscribers",
];

/** The six service stats. The frame stays mounted across refreshes; only the
 *  cells swap to skeletons, so nothing below them shifts when the data lands. */
export default function ServiceStrip({
  loading,
  health,
  healthFailed,
  countsFailed = false,
  currentRevision,
  namespaces,
  parameters,
  secrets,
  subscriberCount,
  staleCount,
  layout = "grid",
}: ServiceStripProps) {
  const className = layout === "strip" ? "stat-strip" : "card-grid";
  if (loading) {
    return (
      <div className={className}>
        {LABELS.map((label) => (
          <StatSkeleton key={label} label={label} />
        ))}
      </div>
    );
  }
  const h = health;
  return (
    <div className={className}>
      <div className="stat">
        <div className="stat-label">Service</div>
        {healthFailed ? (
          <>
            <div className="stat-badges">
              <Badge kind="neutral">unknown</Badge>
            </div>
            <div className="stat-sub">could not reach the API</div>
          </>
        ) : (
          <>
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
          </>
        )}
      </div>

      <div className="stat">
        <div className="stat-label">Current revision</div>
        <div className="stat-value">{h?.current_revision ?? currentRevision}</div>
        <div className="stat-sub">latest applied configuration</div>
      </div>

      <div className="stat">
        <div className="stat-label">Namespaces</div>
        <div className="stat-value">
          <CountText c={namespaces} />
        </div>
        <div className="stat-sub">
          {countsFailed ? <span className="text-danger">not loaded · </span> : null}
          <Link href="/namespaces">
            Manage <ArrowRight size={14} aria-hidden />
          </Link>
        </div>
      </div>

      <div className="stat">
        <div className="stat-label">Parameters</div>
        <div className="stat-value">
          <CountText c={parameters} />
        </div>
        <div className="stat-sub">
          {countsFailed ? <span className="text-danger">not loaded · </span> : null}
          <Link href="/parameters">
            Manage <ArrowRight size={14} aria-hidden />
          </Link>
        </div>
      </div>

      <div className="stat">
        <div className="stat-label">Secrets</div>
        <div className="stat-value">
          <CountText c={secrets} />
        </div>
        <div className="stat-sub">
          {countsFailed ? <span className="text-danger">not loaded · </span> : null}
          <Link href="/secrets">
            Manage <ArrowRight size={14} aria-hidden />
          </Link>
        </div>
      </div>

      <div className="stat">
        <div className="stat-label">Subscribers</div>
        <div className="stat-value">{subscriberCount}</div>
        <div className="stat-sub">
          {staleCount > 0 ? (
            <span className="text-warning">{staleCount} behind latest revision</span>
          ) : (
            <span className="text-success">all up to date</span>
          )}
        </div>
      </div>
    </div>
  );
}
