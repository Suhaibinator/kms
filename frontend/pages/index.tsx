import { ArrowRight, Plus } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import CreateApplicationWizard from "@/components/applications/CreateApplicationWizard";
import { Icon } from "@/components/icons";
import FirstRunChecklist from "@/components/onboarding/FirstRunChecklist";
import FleetGrid from "@/components/overview/FleetGrid";
import ServiceStrip, { type Count } from "@/components/overview/ServiceStrip";
import { StatusChip } from "@/components/StatusChip";
import { RefreshControl } from "@/components/RefreshControl";
import { Badge, EmptyState, PageHeader, TableSkeleton } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { displayAuditResource, formatRelative, formatUnixMs } from "@/lib/format";
import { useLatestRequest, type LoadRun } from "@/lib/hooks";
import { links } from "@/lib/links";
import { STATUS_LABEL } from "@/lib/readiness";
import type {
  ApplicationOverview,
  AppStatus,
  AuditEvent,
  FleetApplication,
  HealthResponse,
  Subscriber,
} from "@/lib/types";
import { useNow } from "@/lib/useNow";

// Per-app overview calls are capped so a large fleet never fans out into
// hundreds of requests; cards past the cap show status without release detail.
export const FLEET_DETAIL_CAP = 25;

/** The independently loaded parts of the dashboard, named so a failure can
 *  say which card is missing instead of "some data". */
export type DashboardSection = "health" | "counts" | "subscribers" | "audit";

const SECTION_LABEL: Record<DashboardSection, string> = {
  health: "service health",
  counts: "namespace counts",
  subscribers: "live subscribers",
  audit: "recent activity",
};

/** "service health, namespace counts and recent activity" */
export function describeSections(sections: DashboardSection[]): string {
  const labels = sections.map((section) => SECTION_LABEL[section]);
  if (labels.length <= 1) return labels[0] ?? "";
  return `${labels.slice(0, -1).join(", ")} and ${labels[labels.length - 1]}`;
}

interface Dashboard {
  health: HealthResponse | null;
  // true when /health itself could not be reached, which is a console-side
  // failure and must not be reported as the service being unhealthy.
  healthFailed: boolean;
  namespaces: Count | null;
  parameters: Count | null;
  secrets: Count | null;
  subscribers: Subscriber[];
  currentRevision: number;
  audit: AuditEvent[];
  /** Sections whose request failed; their cards say so instead of showing zeros. */
  failed: DashboardSection[];
}

interface Fleet {
  // null until the application list has loaded; stays null when it failed, so
  // the page never mistakes a failed load for an empty store.
  applicationCount: number | null;
  applications: FleetApplication[];
  fleetFailed: boolean;
  overviews: Record<string, ApplicationOverview | null>;
  detailLoading: Record<string, boolean>;
}

const EMPTY: Dashboard = {
  health: null,
  healthFailed: false,
  namespaces: null,
  parameters: null,
  secrets: null,
  subscribers: [],
  currentRevision: 0,
  audit: [],
  failed: [],
};

const NO_FLEET: Fleet = {
  applicationCount: null,
  applications: [],
  fleetFailed: false,
  overviews: {},
  detailLoading: {},
};

const FLEET_STATUS_ORDER: AppStatus[] = ["blocked", "attention", "setup", "ready"];

function RecentActivity({
  loading,
  failed,
  audit,
  now,
  onRetry,
}: {
  loading: boolean;
  failed: boolean;
  audit: AuditEvent[];
  now: number;
  onRetry: () => void;
}) {
  return (
    <div className="card mt-6">
      <h2 className="card-title">
        Recent activity
        <Link href={links.audit()} className="inline-flex items-center gap-1 text-sm">
          View audit log <ArrowRight size={14} aria-hidden />
        </Link>
      </h2>
      {loading ? (
        // Matches the loaded table: page_size 8, and rows with no action
        // buttons are 44px rather than the skeleton's default 54px.
        <TableSkeleton
          headers={["When", "Event", "Actor", "Resource", "Decision"]}
          rows={8}
          rowHeight={44}
        />
      ) : failed ? (
        <EmptyState
          icon={<Icon.audit size={20} />}
          title="Could not load recent activity"
          actions={
            <Button variant="outline" onClick={onRetry}>
              Try again
            </Button>
          }
        >
          The audit log did not respond.
        </EmptyState>
      ) : audit.length === 0 ? (
        <EmptyState icon={<Icon.audit size={20} />} title="No recent events">
          Administrative actions and policy decisions will appear here.
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
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
              {audit.map((e) => {
                const resource = displayAuditResource(e);
                const href = links.auditResource(e);
                return (
                  <tr key={e.id}>
                    <td
                      data-label="When"
                      className="nowrap"
                      title={formatUnixMs(e.created_at_unix_ms)}
                    >
                      {formatRelative(e.created_at_unix_ms, now)}
                    </td>
                    <td data-label="Event" className="mono">
                      {e.event_type}
                    </td>
                    <td data-label="Actor">
                      {e.actor_identity || <span className="faint">—</span>}
                      {e.actor_type ? (
                        <span className="faint text-sm"> · {e.actor_type}</span>
                      ) : null}
                    </td>
                    <td data-label="Resource" className="cell-path">
                      {resource && href ? (
                        <Link href={href} title={`Open ${e.resource_type}`}>
                          {resource}
                        </Link>
                      ) : (
                        resource || <span className="faint">—</span>
                      )}
                    </td>
                    <td data-label="Decision">
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
  );
}

function LiveSubscribers({
  loading,
  failed,
  subscribers,
  currentRevision,
  now,
  onRetry,
}: {
  loading: boolean;
  failed: boolean;
  subscribers: Subscriber[];
  currentRevision: number;
  now: number;
  onRetry: () => void;
}) {
  return (
    <div className="card">
      <h2 className="card-title">
        Live subscribers
        <Link href={links.subscribers()} className="inline-flex items-center gap-1 text-sm">
          View all <ArrowRight size={14} aria-hidden />
        </Link>
      </h2>
      {loading ? (
        // The loaded table renders subscribers.slice(0, 6) at 44px a row.
        <TableSkeleton
          headers={["Client", "Last heartbeat", "Applied revision"]}
          rows={6}
          rowHeight={44}
        />
      ) : failed ? (
        <EmptyState
          icon={<Icon.subscribers size={20} />}
          title="Could not load subscribers"
          actions={
            <Button variant="outline" onClick={onRetry}>
              Try again
            </Button>
          }
        >
          The subscriber list did not respond.
        </EmptyState>
      ) : subscribers.length === 0 ? (
        <EmptyState
          icon={<Icon.subscribers size={20} />}
          title="No applications are currently subscribed"
        >
          Clients appear here once they open a watch stream.
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Client</th>
                <th>Last heartbeat</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {subscribers.slice(0, 6).map((s) => {
                const behind = currentRevision - s.last_acked_revision;
                return (
                  <tr key={s.instance_id || `${s.client_name}-${s.remote_addr}`}>
                    <td data-label="Client">
                      {s.client_name}
                      {s.instance_id ? (
                        <span className="faint text-sm"> · {s.instance_id}</span>
                      ) : null}
                    </td>
                    <td
                      data-label="Last heartbeat"
                      className="nowrap"
                      title={
                        s.release_name
                          ? "Release streams report lifecycle status instead of transport heartbeats"
                          : formatUnixMs(s.last_heartbeat_unix_ms)
                      }
                    >
                      {s.release_name ? "—" : formatRelative(s.last_heartbeat_unix_ms, now)}
                    </td>
                    <td data-label="Status">
                      {s.release_name ? (
                        <Badge
                          kind={
                            s.release_state === "applied"
                              ? "success"
                              : s.release_state === "rejected"
                                ? "danger"
                                : "neutral"
                          }
                        >
                          {s.release_state
                            ? [
                                s.release_name,
                                s.release_state,
                                s.release_version === undefined ? null : `v${s.release_version}`,
                                s.release_revision === undefined
                                  ? null
                                  : `revision ${s.release_revision}`,
                              ]
                                .filter(Boolean)
                                .join(" · ")
                            : `${s.release_name} · awaiting lifecycle report`}
                        </Badge>
                      ) : behind > 0 ? (
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
  );
}

function FleetSkeleton() {
  return (
    <div className="fleet-grid" aria-busy="true">
      <span className="sr-only">Loading applications…</span>
      {[0, 1, 2].map((n) => (
        <div key={n} className="fleet-card fleet-card-skeleton" />
      ))}
    </div>
  );
}

export default function DashboardPage() {
  const toast = useToast();
  const router = useRouter();
  const { identity } = useAuth();
  const isAdmin = identity?.kind === "admin";
  const now = useNow();
  const [data, setData] = useState<Dashboard>(EMPTY);
  const [fleet, setFleet] = useState<Fleet>(NO_FLEET);
  const [loading, setLoading] = useState(true);
  const [fleetLoading, setFleetLoading] = useState(true);
  const [lastLoadedAt, setLastLoadedAt] = useState<number | null>(null);
  const [wizardOpen, setWizardOpen] = useState(false);
  const [statusFilter, setStatusFilter] = useState<AppStatus | "all">("all");
  const { begin } = useLatestRequest();

  const [pendingSections, setPendingSections] = useState<DashboardSection[]>([
    "health",
    "counts",
    "subscribers",
    "audit",
  ]);
  const activeRun = useRef<LoadRun | null>(null);

  const loadDetail = useCallback(async (name: string, run: LoadRun) => {
    if (!run.current) return;
    setFleet((current) => ({
      ...current,
      detailLoading: { ...current.detailLoading, [name]: true },
    }));
    try {
      const overview = await api.applicationOverview(name, undefined, { signal: run.signal });
      if (!run.current) return;
      setFleet((current) => ({
        ...current,
        overviews: { ...current.overviews, [name]: overview },
      }));
    } catch (error) {
      if (!run.current || isAbortError(error)) return;
      setFleet((current) => ({ ...current, overviews: { ...current.overviews, [name]: null } }));
    } finally {
      if (run.current)
        setFleet((current) => ({
          ...current,
          detailLoading: { ...current.detailLoading, [name]: false },
        }));
    }
  }, []);

  const load = useCallback(async () => {
    const run = begin();
    activeRun.current = run;
    setLoading(true);
    setFleetLoading(isAdmin);
    setPendingSections(["health", "counts", "subscribers", "audit"]);
    setData(EMPTY);
    const errors: { section: DashboardSection; error: unknown }[] = [];
    async function section<T>(
      name: DashboardSection,
      request: Promise<T>,
      apply: (value: T) => Partial<Dashboard>,
    ) {
      try {
        const value = await request;
        if (run.current) setData((current) => ({ ...current, ...apply(value) }));
      } catch (error) {
        if (!run.current || isAbortError(error)) return;
        errors.push({ section: name, error });
        setData((current) => ({
          ...current,
          ...(name === "health" ? { healthFailed: true } : {}),
          failed: [...current.failed, name],
        }));
      } finally {
        if (run.current) setPendingSections((current) => current.filter((item) => item !== name));
      }
    }
    const summary = Promise.all([
      section("health", api.health({ signal: run.signal }), (health) => ({ health })),
      section("counts", api.listNamespaces(200, undefined, { signal: run.signal }), (response) => {
        const list = response.namespaces ?? [];
        const more = !!response.next_page_token;
        return {
          namespaces: { value: list.length, more },
          parameters: { value: list.reduce((sum, ns) => sum + (ns.parameter_count ?? 0), 0), more },
          secrets: { value: list.reduce((sum, ns) => sum + (ns.secret_count ?? 0), 0), more },
        };
      }),
      section("subscribers", api.subscribers({ signal: run.signal }), (response) => ({
        subscribers: response.subscribers ?? [],
        currentRevision: response.current_revision ?? 0,
      })),
      section("audit", api.listAudit({ page_size: 8 }, { signal: run.signal }), (response) => ({
        audit: response.events ?? [],
      })),
    ]).then(() => {
      if (!run.current || errors.length === 0) return;
      const order: DashboardSection[] = ["health", "counts", "subscribers", "audit"];
      toast.error(
        errors[0]?.error,
        `Could not load ${describeSections(order.filter((name) => errors.some((item) => item.section === name)))}`,
      );
    });
    const loadFleet = async () => {
      if (!isAdmin) {
        setFleet(NO_FLEET);
        return;
      }
      try {
        const overview = await api.fleetOverview({ signal: run.signal });
        if (!run.current) return;
        const applications = overview.applications ?? [];
        const names = applications.slice(0, FLEET_DETAIL_CAP).map((app) => app.application.name);
        setFleet({
          applications,
          applicationCount: applications.length,
          fleetFailed: false,
          overviews: {},
          detailLoading: Object.fromEntries(names.map((name) => [name, true])),
        });
        setFleetLoading(false);
        await Promise.all(names.map((name) => loadDetail(name, run)));
      } catch (error) {
        if (!run.current || isAbortError(error)) return;
        setFleet({ ...NO_FLEET, fleetFailed: true });
        setFleetLoading(false);
        toast.error(error, "Failed to load applications");
      }
    };
    await Promise.all([summary, loadFleet()]);
    if (!run.current) return;
    setLoading(false);
    setLastLoadedAt(Date.now());
  }, [begin, toast, isAdmin, loadDetail]);

  useEffect(() => {
    void load();
  }, [load]);

  const staleCount = data.subscribers.filter(
    (s) => !s.release_name && s.last_acked_revision < data.currentRevision,
  ).length;

  const statusCounts = useMemo(() => {
    const counts: Record<AppStatus, number> = { blocked: 0, attention: 0, setup: 0, ready: 0 };
    for (const app of fleet.applications) counts[app.status] += 1;
    return counts;
  }, [fleet.applications]);
  const visibleApplications =
    statusFilter === "all"
      ? fleet.applications
      : fleet.applications.filter((app) => app.status === statusFilter);
  const presentStatuses = FLEET_STATUS_ORDER.filter((status) => statusCounts[status] > 0);

  const refresh = (
    <RefreshControl
      loading={loading || Object.values(fleet.detailLoading).some(Boolean)}
      onRefresh={() => void load()}
      freshness={
        lastLoadedAt === null
          ? undefined
          : {
              transport: "manual",
              stale:
                data.failed.length > 0 ||
                fleet.fleetFailed ||
                Object.values(fleet.overviews).some((overview) => overview === null),
              lastUpdatedAt: lastLoadedAt,
              staleTitle: "Part of the last refresh failed; the cards say which.",
            }
      }
    />
  );

  const strip = (layout: "grid" | "strip") => (
    <ServiceStrip
      layout={layout}
      loading={false}
      healthLoading={pendingSections.includes("health")}
      countsLoading={pendingSections.includes("counts")}
      subscribersLoading={pendingSections.includes("subscribers")}
      subscribersFailed={data.failed.includes("subscribers")}
      health={data.health}
      healthFailed={data.healthFailed}
      countsFailed={data.failed.includes("counts")}
      currentRevision={data.currentRevision}
      namespaces={data.namespaces}
      parameters={data.parameters}
      secrets={data.secrets}
      subscriberCount={data.subscribers.length}
      staleCount={staleCount}
    />
  );

  if (!isAdmin) {
    return (
      <>
        <PageHeader
          title="Overview"
          subtitle="Service status and configuration at a glance."
          actions={refresh}
        />
        {strip("grid")}
        <RecentActivity
          loading={pendingSections.includes("audit")}
          failed={data.failed.includes("audit")}
          audit={data.audit}
          now={now}
          onRetry={() => void load()}
        />
        <LiveSubscribers
          loading={pendingSections.includes("subscribers")}
          failed={data.failed.includes("subscribers")}
          subscribers={data.subscribers}
          currentRevision={data.currentRevision}
          now={now}
          onRetry={() => void load()}
        />
      </>
    );
  }

  const namespaceCount = data.namespaces?.value ?? 0;
  const firstRun = !fleetLoading && fleet.applicationCount === 0;
  const showGrid = !fleetLoading && !firstRun && !fleet.fleetFailed;

  return (
    <>
      <PageHeader
        title="Overview"
        subtitle={
          firstRun
            ? "Create your first application or adopt existing environments."
            : "Every application, every environment, and whether clients have caught up."
        }
        actions={
          <>
            {refresh}
            {!firstRun ? (
              <Button onClick={() => setWizardOpen(true)}>
                <Plus size={16} aria-hidden />
                New application
              </Button>
            ) : null}
          </>
        }
      />

      {strip("strip")}

      <section className="fleet-section" aria-label="Applications">
        {fleetLoading ? (
          <FleetSkeleton />
        ) : firstRun && pendingSections.includes("counts") ? (
          <div className="card" role="status">
            Checking existing environments…
          </div>
        ) : firstRun && data.failed.includes("counts") ? (
          <EmptyState
            title="Could not check existing environments"
            actions={
              <Button variant="outline" onClick={() => void load()}>
                Try again
              </Button>
            }
          >
            Environment counts are unavailable. Retry to see whether to adopt existing environments
            or start a new application.
          </EmptyState>
        ) : firstRun ? (
          <FirstRunChecklist
            namespaceCount={namespaceCount}
            onCreateApplication={() => setWizardOpen(true)}
          />
        ) : fleet.fleetFailed ? (
          <EmptyState
            icon={<Icon.application size={20} />}
            title="Could not load application status"
            actions={
              <Button variant="outline" onClick={() => void load()}>
                Try again
              </Button>
            }
          >
            The fleet overview did not respond. Applications are still listed under{" "}
            <Link href={links.applications()}>Applications</Link>.
          </EmptyState>
        ) : showGrid ? (
          <>
            {presentStatuses.length > 1 ? (
              <fieldset className="fleet-filter">
                <legend className="sr-only">Filter applications by status</legend>
                <Button
                  type="button"
                  size="sm"
                  variant={statusFilter === "all" ? "secondary" : "ghost"}
                  aria-pressed={statusFilter === "all"}
                  onClick={() => setStatusFilter("all")}
                >
                  All <span className="faint">{fleet.applications.length}</span>
                </Button>
                {presentStatuses.map((status) => (
                  <Button
                    key={status}
                    type="button"
                    size="sm"
                    variant={statusFilter === status ? "secondary" : "ghost"}
                    aria-pressed={statusFilter === status}
                    onClick={() => setStatusFilter(statusFilter === status ? "all" : status)}
                  >
                    <StatusChip status={status} size="dot" />
                    {STATUS_LABEL[status]} <span className="faint">{statusCounts[status]}</span>
                  </Button>
                ))}
              </fieldset>
            ) : null}
            <div className="fleet-head">
              <h2 className="section-title">
                Applications{" "}
                <span className="faint">{fleet.applicationCount ?? fleet.applications.length}</span>
              </h2>
              <Link href={links.applications()} className="inline-flex items-center gap-1 text-sm">
                Manage <ArrowRight size={14} aria-hidden />
              </Link>
            </div>
            <FleetGrid
              applications={visibleApplications}
              overviews={fleet.overviews}
              detailLoading={fleet.detailLoading}
              onLoadDetail={(name) => {
                if (activeRun.current) void loadDetail(name, activeRun.current);
              }}
              now={now}
            />
            {(fleet.applicationCount ?? 0) > FLEET_DETAIL_CAP ? (
              <p className="fleet-note faint text-sm">
                Release detail loads automatically for the first {FLEET_DETAIL_CAP} applications.
                Load other cards individually.
              </p>
            ) : null}
          </>
        ) : null}
      </section>

      <RecentActivity
        loading={pendingSections.includes("audit")}
        failed={data.failed.includes("audit")}
        audit={data.audit}
        now={now}
        onRetry={() => void load()}
      />

      <CreateApplicationWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        onCreated={(application) => {
          setWizardOpen(false);
          void router.push(links.application(application.name));
        }}
      />
    </>
  );
}
