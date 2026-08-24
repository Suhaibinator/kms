import { ArrowRight, Plus, RefreshCw } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useState } from "react";
import CreateApplicationWizard from "@/components/applications/CreateApplicationWizard";
import { Icon } from "@/components/icons";
import FirstRunChecklist from "@/components/onboarding/FirstRunChecklist";
import FleetGrid from "@/components/overview/FleetGrid";
import ServiceStrip, { type Count } from "@/components/overview/ServiceStrip";
import { Badge, EmptyState, PageHeader, Spinner, TableSkeleton } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { displayAuditResource, formatRelative, formatUnixMs } from "@/lib/format";
import { useLatestRequest } from "@/lib/hooks";
import { links } from "@/lib/links";
import type {
  ApplicationOverview,
  AuditEvent,
  FleetApplication,
  HealthResponse,
  Subscriber,
} from "@/lib/types";

// Per-app overview calls are capped so a large fleet never fans out into
// hundreds of requests; cards past the cap show status without release detail.
export const FLEET_DETAIL_CAP = 25;

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
}

interface Fleet {
  // null until the application list has loaded; stays null when it failed, so
  // the page never mistakes a failed load for an empty store.
  applicationCount: number | null;
  applications: FleetApplication[];
  fleetFailed: boolean;
  overviews: Record<string, ApplicationOverview | null>;
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
};

const NO_FLEET: Fleet = {
  applicationCount: null,
  applications: [],
  fleetFailed: false,
  overviews: {},
};

function RecentActivity({ loading, audit }: { loading: boolean; audit: AuditEvent[] }) {
  return (
    <div className="card mt-6">
      <div className="card-title">
        Recent activity
        <Link href="/audit" className="text-sm">
          View audit log <ArrowRight size={14} aria-hidden />
        </Link>
      </div>
      {loading ? (
        <TableSkeleton headers={["When", "Event", "Actor", "Resource", "Decision"]} rows={5} />
      ) : audit.length === 0 ? (
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
              {audit.map((e) => {
                const resource = displayAuditResource(e);
                return (
                  <tr key={e.id}>
                    <td className="nowrap" title={formatUnixMs(e.created_at_unix_ms)}>
                      {formatRelative(e.created_at_unix_ms)}
                    </td>
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
  );
}

function LiveSubscribers({
  loading,
  subscribers,
  currentRevision,
}: {
  loading: boolean;
  subscribers: Subscriber[];
  currentRevision: number;
}) {
  return (
    <div className="card">
      <div className="card-title">
        Live subscribers
        <Link href="/subscribers" className="text-sm">
          View all <ArrowRight size={14} aria-hidden />
        </Link>
      </div>
      {loading ? (
        <TableSkeleton headers={["Client", "Last heartbeat", "Applied revision"]} rows={3} />
      ) : subscribers.length === 0 ? (
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
              {subscribers.slice(0, 6).map((s) => {
                const behind = currentRevision - s.last_acked_revision;
                return (
                  <tr key={s.instance_id || `${s.client_name}-${s.remote_addr}`}>
                    <td>
                      {s.client_name}
                      {s.instance_id ? (
                        <span className="faint text-sm"> · {s.instance_id}</span>
                      ) : null}
                    </td>
                    <td className="nowrap" title={formatUnixMs(s.last_heartbeat_unix_ms)}>
                      {formatRelative(s.last_heartbeat_unix_ms)}
                    </td>
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
  const [data, setData] = useState<Dashboard>(EMPTY);
  const [fleet, setFleet] = useState<Fleet>(NO_FLEET);
  const [loading, setLoading] = useState(true);
  const [fleetLoading, setFleetLoading] = useState(true);
  const [wizardOpen, setWizardOpen] = useState(false);
  const { begin } = useLatestRequest();

  const load = useCallback(async () => {
    const run = begin();
    setLoading(true);
    setFleetLoading(true);
    // Parameter/secret totals are cross-namespace overviews, which the
    // namespace-scoped list APIs can't answer directly — they come from the
    // per-namespace counts on ListNamespaces (plan §10).
    const [health, ns, subs, audit] = await Promise.allSettled([
      api.health({ signal: run.signal }),
      api.listNamespaces(200, undefined, { signal: run.signal }),
      api.subscribers({ signal: run.signal }),
      api.listAudit({ page_size: 8 }, { signal: run.signal }),
    ]);
    if (!run.current) return;

    const next: Dashboard = { ...EMPTY };
    if (health.status === "fulfilled") next.health = health.value;
    else next.healthFailed = true;
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
    if (firstError && !isAbortError(firstError.reason)) {
      toast.error(firstError.reason, "Some dashboard data failed to load");
    }

    setData(next);
    setLoading(false);

    if (!isAdmin) {
      setFleet(NO_FLEET);
      setFleetLoading(false);
      return;
    }

    // The fleet: the application list decides first-run vs grid, the fleet
    // overview carries per-environment status, and the first few per-app
    // overviews add active releases and rollout counts to the cards.
    const [apps, overview] = await Promise.allSettled([
      api.listApplications(200, undefined, { signal: run.signal }),
      api.fleetOverview({ signal: run.signal }),
    ]);
    if (!run.current) return;

    const nextFleet: Fleet = { ...NO_FLEET, overviews: {} };
    if (apps.status === "fulfilled") {
      nextFleet.applicationCount = (apps.value.applications ?? []).length;
    }
    if (overview.status === "fulfilled") {
      nextFleet.applications = overview.value.applications ?? [];
      if (nextFleet.applicationCount === null) {
        nextFleet.applicationCount = nextFleet.applications.length;
      }
    } else {
      nextFleet.fleetFailed = true;
    }
    const fleetError = [apps, overview].find((r) => r.status === "rejected") as
      | PromiseRejectedResult
      | undefined;
    if (fleetError && !isAbortError(fleetError.reason)) {
      toast.error(fleetError.reason, "Failed to load applications");
    }

    if (nextFleet.applications.length > 0) {
      const names = nextFleet.applications
        .slice(0, FLEET_DETAIL_CAP)
        .map((app) => app.application.name);
      const details = await Promise.allSettled(
        names.map((name) => api.applicationOverview(name, undefined, { signal: run.signal })),
      );
      if (!run.current) return;
      names.forEach((name, index) => {
        const result = details[index];
        nextFleet.overviews[name] = result?.status === "fulfilled" ? result.value : null;
      });
    }

    setFleet(nextFleet);
    setFleetLoading(false);
  }, [begin, toast, isAdmin]);

  useEffect(() => {
    void load();
  }, [load]);

  const staleCount = data.subscribers.filter(
    (s) => s.last_acked_revision < data.currentRevision,
  ).length;

  const refresh = (
    <Button variant="outline" onClick={() => void load()} disabled={loading}>
      {loading ? <Spinner /> : null}
      {!loading ? <RefreshCw size={16} aria-hidden /> : null}
      {loading ? "Refreshing…" : "Refresh"}
    </Button>
  );

  const strip = (layout: "grid" | "strip") => (
    <ServiceStrip
      layout={layout}
      loading={loading}
      health={data.health}
      healthFailed={data.healthFailed}
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
        <RecentActivity loading={loading} audit={data.audit} />
        <LiveSubscribers
          loading={loading}
          subscribers={data.subscribers}
          currentRevision={data.currentRevision}
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
            ? "Nothing is configured yet. Work through the steps below."
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
            <div className="fleet-head">
              <h2 className="section-title">
                Applications{" "}
                <span className="faint">{fleet.applicationCount ?? fleet.applications.length}</span>
              </h2>
              <Link href={links.applications()} className="text-sm">
                Manage <ArrowRight size={14} aria-hidden />
              </Link>
            </div>
            <FleetGrid applications={fleet.applications} overviews={fleet.overviews} />
            {(fleet.applicationCount ?? 0) > FLEET_DETAIL_CAP ? (
              <p className="fleet-note faint text-sm">
                Release detail is shown for the first {FLEET_DETAIL_CAP} applications.
              </p>
            ) : null}
          </>
        ) : null}
      </section>

      <RecentActivity loading={loading} audit={data.audit} />

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
