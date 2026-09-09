import {
  Cable,
  Copy,
  FileUp,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  RotateCcw,
  Send,
  Trash2,
  Upload,
} from "lucide-react";
import { useRouter } from "next/router";
import { useEffect, useMemo, useRef, useState } from "react";
import { FindingList } from "@/components/FindingList";
import { Ident } from "@/components/Ident";
import { StatusChip } from "@/components/StatusChip";
import { TransportBadge } from "@/components/TransportBadge";
import { Badge, PageHeader } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button, ButtonLink } from "@/components/ui/button";
import { crumbs } from "@/lib/crumbs";
import { formatUnixMs } from "@/lib/format";
import { useNamespaces } from "@/lib/hooks";
import { links } from "@/lib/links";
import { countOtherKeys } from "@/lib/overview";
import type { ApplicationOverview, EnvironmentOverview, Namespace } from "@/lib/types";
import { ActionMenu, type ActionMenuItem } from "./ActionMenu";
import { DeleteEnvironmentDialog, deleteBlockReason } from "./DeleteEnvironmentDialog";
import { columnFindings } from "./EnvironmentColumn";
import { orderEnvironments } from "./EnvironmentPipeline";
import { EnvironmentValuesTable } from "./EnvironmentValuesTable";
import { AuthMethodBadges, NamespaceSettingsModal } from "./NamespaceSettingsModal";
import { ReleaseSection } from "./ReleaseSection";
import { SubscribersSection } from "./SubscribersSection";
import { useApplicationActions } from "./useApplicationActions";
import type { OverviewFreshness } from "./useApplicationOverview";

/** What the namespace list says this environment still holds. */
type ContentsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; namespace: Namespace };

export interface EnvironmentHomeProps {
  overview: ApplicationOverview;
  /** The environment this page is about; the caller has already found it. */
  environment: EnvironmentOverview;
  loading: boolean;
  reload: () => Promise<void>;
  freshness?: OverviewFreshness;
  onWritingChange?: (writing: boolean) => void;
  /** `?ship=`: `1` opens Ship, an alias also prefills a row. Each new value seeds once. */
  ship?: string | null;
  /** `?rollback=1` opens Roll back for this environment. Each new value seeds once. */
  rollback?: string | null;
  schemaVersion?: number;
}

/**
 * One environment on its own page: its values as a table, its release,
 * subscribers, findings and namespace settings, with the environment-level
 * actions promoted into the header. The application page remains the
 * side-by-side comparison; this is the place to work in one environment.
 */
export function EnvironmentHome({
  overview,
  environment,
  loading,
  reload,
  freshness,
  onWritingChange,
  ship,
  rollback,
  schemaVersion = overview.application.schema_version,
}: EnvironmentHomeProps) {
  const router = useRouter();
  const application = overview.application;
  const archived = application.archived_at_unix_ms > 0;
  const ns = environment.namespace;
  const env = ns.env;
  const active = environment.release.active;
  const findings = useMemo(() => columnFindings(environment), [environment]);
  const staleFindings = findings.filter((finding) => finding.code === "instance_stale");
  const otherKeys = useMemo(
    () => countOtherKeys(environment, overview.rows),
    [environment, overview.rows],
  );

  const { callbacks, actions, modals } = useApplicationActions({
    overview,
    reload,
    onWritingChange,
    pathname: "/applications/environment",
    defaultEnv: env,
    schemaVersion,
  });

  // The namespace is copied into state when the editor opens, so a background
  // refresh of the overview cannot reset what is being typed.
  const [settingsTarget, setSettingsTarget] = useState<Namespace | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Namespace | null>(null);

  // Seeded once per value, like the application page: a link that carries
  // `?ship=` opens the modal, and closing it must not reopen it.
  const seededShip = useRef<string | null>(null);
  const seededRollback = useRef<string | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `actions` is rebuilt every render; the seeded refs, not the dependency list, decide when a param opens a modal.
  useEffect(() => {
    if (!ship) {
      seededShip.current = null;
    } else if (seededShip.current !== ship) {
      seededShip.current = ship;
      actions.openShip(env, ship === "1" ? undefined : ship);
    }
    if (rollback !== "1") {
      seededRollback.current = null;
    } else if (seededRollback.current !== rollback) {
      seededRollback.current = rollback;
      if (active) actions.openRollback(env);
    }
  }, [ship, rollback, env, active]);

  // What the environment still holds, and therefore whether it can be deleted.
  // The overview's namespace carries parameter and secret counts only — the
  // server fills identity_count in the namespace list query alone — so a
  // namespace whose last contents are bound identities would otherwise look
  // empty here and be refused with 412 on delete. The namespace list is the
  // one place that answers all three; useNamespaces pages it once and caches
  // it for every page in this session.
  const {
    namespaces,
    loading: namespacesLoading,
    error: namespacesError,
    reload: reloadNamespaces,
  } = useNamespaces();
  const listed =
    namespaces.find((candidate) => candidate.env === env && candidate.app === ns.app) ?? null;
  const contents: ContentsState = listed
    ? { status: "ready", namespace: listed }
    : namespacesLoading
      ? { status: "loading" }
      : {
          status: "error",
          message: namespacesError
            ? "Could not check what this environment still holds."
            : "This environment is not in the namespace list.",
        };
  const blockReason =
    contents.status === "ready"
      ? deleteBlockReason(contents.namespace)
      : contents.status === "loading"
        ? "Checking what this environment still holds…"
        : contents.message;
  const canRollback = Boolean(active && active.previous_version > 0);
  const releaseHref = links.releases({
    app: ns.app,
    env,
    name: application.release_name,
    schemaVersion,
  });

  const moreItems: ActionMenuItem[] = [
    {
      key: "import-defaults",
      label: (
        <>
          <FileUp size={15} aria-hidden />
          Import defaults…
        </>
      ),
      disabled: archived,
      onSelect: () => actions.openImportDefaults(env),
    },
    ...(active
      ? [
          {
            key: "migrate-schema",
            label: (
              <>
                <Upload size={15} aria-hidden />
                Upgrade schema…
              </>
            ),
            disabled: archived,
            onSelect: () => actions.openMigrate(env),
          },
        ]
      : []),
    {
      key: "clone",
      label: (
        <>
          <Copy size={15} aria-hidden />
          Clone to a new environment…
        </>
      ),
      disabled: archived,
      onSelect: () => actions.openAddEnvironment(env),
    },
    { key: "parameters", label: "Parameters", href: links.parameters(ns) },
    { key: "secrets", label: "Secrets", href: links.secrets(ns) },
    { key: "releases", label: "Releases", href: releaseHref },
    {
      key: "edit-settings",
      label: (
        <>
          <Pencil size={15} aria-hidden />
          Edit settings…
        </>
      ),
      onSelect: () => setSettingsTarget(ns),
    },
    {
      key: "delete",
      label: blockReason ? (
        <>
          <span>
            <Trash2 size={15} aria-hidden />
            Delete environment…
          </span>
          <span className="faint text-xs">{blockReason}</span>
        </>
      ) : (
        <>
          <Trash2 size={15} aria-hidden />
          Delete environment…
        </>
      ),
      disabled: blockReason !== null,
      onSelect: () => setDeleteTarget(contents.status === "ready" ? contents.namespace : ns),
    },
  ];

  const trail = crumbs.environment({ env, app: ns.app }, schemaVersion);
  const breadcrumbs = trail.map((crumb, index) =>
    index === trail.length - 1 ? { ...crumb, href: undefined } : crumb,
  );

  return (
    <div className="application-home environment-home">
      <PageHeader
        breadcrumbs={breadcrumbs}
        documentTitle={`${env} · ${ns.app}`}
        title={
          <span className="row-wrap">
            <Ident kind="env" value={env} production={environment.production} tooltip={false} />
            <StatusChip status={environment.status} production={environment.production} />
            {environment.production ? <Badge kind="warning">production</Badge> : null}
          </span>
        }
        subtitle={ns.description || `One environment of ${ns.app}.`}
        actions={
          <>
            {freshness ? (
              <TransportBadge
                transport="poll"
                stale={freshness.staleReason !== null}
                lastUpdatedAt={freshness.lastLoadedAt}
                title="Checked every 30 seconds while this tab is visible; changes are announced, not applied."
                staleTitle={
                  freshness.staleReason === "changed"
                    ? "A release was activated since this loaded. Refresh to see it."
                    : "The last refresh failed; what is shown may be behind."
                }
              />
            ) : null}
            <Button
              type="button"
              variant="outline"
              size="icon"
              aria-label="Refresh"
              title="Refresh"
              onClick={() => void reload()}
              disabled={loading}
            >
              <RefreshCw size={15} aria-hidden />
            </Button>
            <Button type="button" disabled={archived} onClick={() => actions.openShip(env)}>
              <Send size={15} aria-hidden />
              Ship to {env}…
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={archived || !canRollback}
              onClick={() => actions.openRollback(env)}
            >
              <RotateCcw size={15} aria-hidden />
              {active?.is_rolled_back ? `Re-activate v${active.previous_version}` : "Roll back"}
            </Button>
            <Button type="button" variant="outline" onClick={() => actions.openConnect(env)}>
              <Cable size={15} aria-hidden />
              Connect SDK
            </Button>
            <ActionMenu
              items={moreItems}
              trigger={
                <Button type="button" variant="outline" aria-label="More actions">
                  <MoreHorizontal size={15} aria-hidden />
                  More
                </Button>
              }
            />
          </>
        }
      />

      <div className="environment-switcher">
        <label className="row-wrap" htmlFor="environment-switcher-select">
          <span className="muted">Environment</span>
          <AppSelect
            id="environment-switcher-select"
            className="min-w-[200px]"
            value={env}
            onValueChange={(next) => {
              if (next && next !== env) void router.push(links.environment(ns.app, next));
            }}
            options={orderEnvironments(overview.environments).map((candidate) => ({
              value: candidate.namespace.env,
              label: candidate.production
                ? `${candidate.namespace.env} · production`
                : candidate.namespace.env,
            }))}
          />
        </label>
        <ButtonLink variant="outline" href={links.application(ns.app, { schemaVersion, env })}>
          All environments
        </ButtonLink>
      </div>

      {archived ? (
        <div className="info-panel mb-4" role="status">
          This application is archived and read-only. Its schema history remains available.
        </div>
      ) : null}

      <FindingList
        findings={findings.filter((finding) => finding.code !== "instance_stale")}
        onFix={actions.onFix}
        className="application-findings"
      />
      {staleFindings.length > 0 ? (
        <details className="info-panel text-sm">
          <summary className="cursor-pointer">
            {staleFindings.length} stale instances · View details
          </summary>
          <FindingList findings={staleFindings} onFix={actions.onFix} />
        </details>
      ) : null}

      <EnvironmentValuesTable
        environment={environment}
        otherKeys={otherKeys}
        onAddValue={callbacks.onAddValue}
        onAddSecret={callbacks.onAddSecret}
        onOpenSecret={callbacks.onOpenSecret}
        onOpenParameter={callbacks.onOpenParameter}
        onShip={callbacks.onShip}
        onEditContract={() => actions.manageContract(env)}
      />

      <div className="environment-panels">
        <div className="card environment-panel">
          <ReleaseSection
            environment={environment}
            onShip={callbacks.onShip}
            onRollback={callbacks.onRollback}
          />
        </div>
        <div className="card environment-panel">
          <SubscribersSection
            environment={environment}
            releaseName={application.release_name}
            onConnect={callbacks.onConnect}
          />
        </div>
      </div>

      <section className="card environment-settings" aria-label={`Settings for ${env}`}>
        <div className="card-title">
          <h2 className="section-title">Environment settings</h2>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setSettingsTarget(ns)}
            aria-label="Edit environment settings"
          >
            <Pencil size={14} aria-hidden />
            Edit
          </Button>
        </div>
        <dl className="environment-settings-grid">
          <div>
            <dt className="faint text-sm">Description</dt>
            <dd>{ns.description || <span className="faint">No description</span>}</dd>
          </div>
          <div>
            <dt className="faint text-sm">Allowed auth methods</dt>
            <dd>
              <AuthMethodBadges methods={ns.allowed_auth_methods} />
            </dd>
          </div>
          <div>
            <dt className="faint text-sm">Created</dt>
            <dd>
              {formatUnixMs(ns.created_at_unix_ms)}
              {ns.created_by ? ` by ${ns.created_by}` : ""}
            </dd>
          </div>
          <div>
            <dt className="faint text-sm">Contents</dt>
            <dd>
              {contents.status === "ready" ? (
                <>
                  {contents.namespace.parameter_count} parameters ·{" "}
                  {contents.namespace.secret_count} secrets ·{" "}
                  {contents.namespace.identity_count ?? 0} bound identities
                </>
              ) : (
                <span className="faint">…</span>
              )}
            </dd>
          </div>
        </dl>
        <div className="row-wrap">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={blockReason !== null}
            aria-describedby={blockReason ? "environment-delete-reason" : undefined}
            onClick={() => setDeleteTarget(contents.status === "ready" ? contents.namespace : ns)}
          >
            <Trash2 size={14} aria-hidden />
            Delete environment
          </Button>
          {blockReason ? (
            <span id="environment-delete-reason" className="faint text-sm">
              {blockReason}
            </span>
          ) : null}
          {contents.status === "error" ? (
            <Button type="button" variant="outline" size="sm" onClick={reloadNamespaces}>
              Retry
            </Button>
          ) : null}
        </div>
      </section>

      {modals}
      <NamespaceSettingsModal
        namespace={settingsTarget}
        onClose={() => setSettingsTarget(null)}
        onSaved={() => {
          setSettingsTarget(null);
          void reload();
        }}
      />
      <DeleteEnvironmentDialog
        namespace={deleteTarget}
        noun="environment"
        onCancel={() => setDeleteTarget(null)}
        onDeleted={() => {
          setDeleteTarget(null);
          void router.replace(links.application(ns.app));
        }}
      />
    </div>
  );
}
