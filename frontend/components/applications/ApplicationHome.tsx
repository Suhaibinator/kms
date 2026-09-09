import {
  Archive,
  ArchiveRestore,
  Cable,
  FileUp,
  MoreHorizontal,
  Plus,
  RefreshCw,
  RotateCcw,
  Send,
  SlidersHorizontal,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { SetupAction } from "@/components/applications/contracts";
import { FindingList } from "@/components/FindingList";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import SetupPanel from "@/components/onboarding/SetupPanel";
import { SearchField } from "@/components/SearchField";
import { StatusChip } from "@/components/StatusChip";
import { TransportBadge } from "@/components/TransportBadge";
import { Badge, EmptyState, PageHeader } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { crumbs } from "@/lib/crumbs";
import type { ApplicationOverview, Finding } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { ActionMenu, type ActionMenuItem } from "./ActionMenu";
import { ConfigurationMatrix } from "./ConfigurationMatrix";
import { ALIGNMENT_CODES, DefinitionCard } from "./DefinitionCard";
import { EnvironmentPipeline } from "./EnvironmentPipeline";
import { useApplicationActions } from "./useApplicationActions";
import type { OverviewFreshness } from "./useApplicationOverview";

export interface ApplicationHomeProps {
  overview: ApplicationOverview;
  loading: boolean;
  reload: () => Promise<void>;
  /** When the overview was loaded and whether it is known to be behind. */
  freshness?: OverviewFreshness;
  /** True while a write modal is open, so the page can pause its background change check. */
  onWritingChange?: (writing: boolean) => void;
  /** `?env=`: the pipeline column to focus and the default Ship environment. */
  env: string | null;
  /** `?ship=`: `1` opens the Ship modal, an alias also prefills a row. Each new value seeds once. */
  ship: string | null;
  /** `?tab=matrix` shows the per-key table instead of the pipeline. */
  tab: string | null;
  /** `?rollback=1` opens Roll back for `?env`, or the environment menu. Each new value seeds once. */
  rollback: string | null;
  /** `?migrate=<schema version>` opens schema migration from the registry. */
  migrate?: string | null;
  schemaVersion?: number;
}

/** A button that acts directly with one environment or offers a menu of them. */
function EnvironmentAction({
  label,
  icon,
  environments,
  onPick,
  open,
  onOpenChange,
  variant = "outline",
}: {
  label: string;
  icon: React.ReactNode;
  environments: string[];
  onPick: (env: string) => void;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  variant?: "outline" | "default";
}) {
  if (environments.length <= 1) {
    const only = environments[0];
    return (
      <Button type="button" variant={variant} disabled={!only} onClick={() => only && onPick(only)}>
        {icon}
        {only ? `${label} ${label === "Ship" ? "to" : "in"} ${only}…` : label}
      </Button>
    );
  }
  return (
    <ActionMenu
      open={open}
      onOpenChange={onOpenChange}
      trigger={
        <Button type="button" variant={variant}>
          {icon}
          {label}
        </Button>
      }
      items={environments.map((env) => ({
        key: env,
        label: <Ident kind="env" value={env} tooltip={false} />,
        onSelect: () => onPick(env),
      }))}
    />
  );
}

/** A "More" menu entry that needs an environment: a submenu when there are
 *  several, a direct item for one, disabled for none. */
function environmentItem(
  key: string,
  label: React.ReactNode,
  environments: string[],
  onPick: (env: string) => void,
): ActionMenuItem {
  if (environments.length <= 1) {
    const only = environments[0];
    return {
      key,
      label: (
        <>
          {label}
          {only ? ` ${key === "import-defaults" ? "to" : "in"} ${only}…` : ""}
        </>
      ),
      disabled: !only,
      onSelect: () => only && onPick(only),
    };
  }
  return {
    key,
    label,
    children: environments.map((env) => ({
      key: `${key}:${env}`,
      label: <Ident kind="env" value={env} tooltip={false} />,
      onSelect: () => onPick(env),
    })),
  };
}

/** App-level findings that are not the Definition card's alignment row. */
export function applicationFindings(overview: ApplicationOverview): Finding[] {
  return overview.findings.filter(
    (finding) => !finding.scope.env && !ALIGNMENT_CODES.has(finding.code),
  );
}

export function ApplicationHome({
  overview,
  loading,
  reload,
  freshness,
  onWritingChange,
  env,
  ship,
  tab,
  rollback,
  migrate,
  schemaVersion = overview.application.schema_version,
}: ApplicationHomeProps) {
  const toast = useToast();
  const replaceQuery = useQueryReplace("/applications");
  const application = overview.application;
  const archived = application.archived_at_unix_ms > 0;
  const environments = overview.environments;
  const environmentNames = useMemo(
    () => environments.map((environment) => environment.namespace.env),
    [environments],
  );
  const focusEnv = env && environmentNames.includes(env) ? env : null;
  const defaultShipEnv =
    focusEnv ??
    environments.find((environment) => !environment.production)?.namespace.env ??
    environmentNames[0];
  const activeEnvironments = useMemo(
    () => environments.filter((environment) => environment.release.active),
    [environments],
  );
  const activeNames = useMemo(
    () => activeEnvironments.map((environment) => environment.namespace.env),
    [activeEnvironments],
  );
  const findings = useMemo(() => applicationFindings(overview), [overview]);

  const { callbacks, actions, modals, schemas, latestSchema } = useApplicationActions({
    overview,
    reload,
    onWritingChange,
    pathname: "/applications",
    defaultEnv: defaultShipEnv,
    schemaVersion,
    tab,
  });

  const [rollbackMenuOpen, setRollbackMenuOpen] = useState(false);
  // Narrows both tabs, and survives switching between them. Local state, not
  // the URL: `?app/env/tab/ship/rollback/migrate/new` is already a lot to carry.
  const [valueFilter, setValueFilter] = useState("");
  const [lifecycleSaving, setLifecycleSaving] = useState(false);

  // Seed the modals from the URL once per value: a palette action that sets
  // `?ship=` while already on this page must open the modal, and closing it
  // (which clears the param through replaceQuery) must not reopen it. The
  // refs remember which value was consumed, so a reload between the close and
  // the router update cannot re-seed from the still-present param.
  const seededShip = useRef<string | null>(null);
  const seededRollback = useRef<string | null>(null);
  const seededMigration = useRef<string | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `actions` is rebuilt every render; the seeded refs, not the dependency list, decide when a param opens a modal.
  useEffect(() => {
    if (!ship) {
      seededShip.current = null;
    } else if (seededShip.current !== ship) {
      seededShip.current = ship;
      actions.openShip(focusEnv ?? defaultShipEnv, ship === "1" ? undefined : ship);
    }
    if (rollback !== "1") {
      seededRollback.current = null;
    } else if (seededRollback.current !== rollback) {
      seededRollback.current = rollback;
      const target =
        focusEnv && activeNames.includes(focusEnv)
          ? focusEnv
          : activeNames.length === 1
            ? activeNames[0]
            : null;
      if (target) actions.openRollback(target);
      else if (activeNames.length > 1) setRollbackMenuOpen(true);
    }
    if (!migrate) {
      seededMigration.current = null;
    } else if (seededMigration.current !== migrate) {
      seededMigration.current = migrate;
      const version = Number(migrate);
      if (Number.isSafeInteger(version) && version > 0 && activeNames.length) {
        actions.openMigrate(
          focusEnv && activeNames.includes(focusEnv) ? focusEnv : activeNames[0],
          version,
        );
      }
    }
  }, [ship, rollback, migrate, focusEnv, defaultShipEnv, activeNames]);

  function onSetupAction(action: SetupAction) {
    switch (action.kind) {
      case "manage-contract":
        actions.manageContract();
        break;
      case "register-schema":
        actions.openDerive();
        break;
      case "add-environment":
        actions.openAddEnvironment();
        break;
      case "fill-values": {
        const field = application.contract.find((entry) => entry.alias === action.alias);
        if (action.alias && field?.kind === "secret") actions.openSecret(action.env, action.alias);
        else if (action.alias) actions.openAddValue(action.env, action.alias);
        else actions.openShip(action.env);
        break;
      }
      case "ship":
        actions.openShip(action.env);
        break;
      case "connect": {
        const target = action.env ?? defaultShipEnv;
        if (target) actions.openConnect(target);
        break;
      }
      case "create-app":
        break;
    }
  }

  async function setArchived(next: boolean) {
    if (lifecycleSaving) return;
    setLifecycleSaving(true);
    try {
      if (next) await api.archiveApplication(application.name);
      else await api.unarchiveApplication(application.name);
      toast.success(next ? "Application archived" : "Application restored");
      await reload();
    } catch (error) {
      toast.error(error, next ? "Failed to archive application" : "Failed to restore application");
    } finally {
      setLifecycleSaving(false);
    }
  }

  const activeMoreItems: ActionMenuItem[] = [
    environmentItem(
      "import-defaults",
      <>
        <FileUp size={15} aria-hidden />
        Import defaults
      </>,
      environmentNames,
      actions.openImportDefaults,
    ),
    {
      key: "add-environment",
      label: (
        <>
          <Plus size={15} aria-hidden />
          Add environment
        </>
      ),
      onSelect: () => actions.openAddEnvironment(),
    },
    {
      key: "edit-definition",
      label: (
        <>
          <SlidersHorizontal size={15} aria-hidden />
          Edit definition
        </>
      ),
      onSelect: () => actions.openDefinition(),
    },
    environmentItem(
      "connect-sdk",
      <>
        <Cable size={15} aria-hidden />
        Connect SDK
      </>,
      environmentNames,
      actions.openConnect,
    ),
    {
      key: "archive",
      label: (
        <>
          <Archive size={15} aria-hidden />
          {environments.length ? "Archive (remove environments first)" : "Archive application"}
        </>
      ),
      disabled: environments.length > 0 || lifecycleSaving,
      onSelect: () => void setArchived(true),
    },
  ];
  const moreItems: ActionMenuItem[] = archived
    ? [
        {
          key: "unarchive",
          label: (
            <>
              <ArchiveRestore size={15} aria-hidden />
              Unarchive application
            </>
          ),
          disabled: lifecycleSaving,
          onSelect: () => void setArchived(false),
        },
      ]
    : activeMoreItems;

  return (
    <div className="application-home">
      <PageHeader
        breadcrumbs={crumbs.application(application.name, schemaVersion)}
        title={
          <span className="row-wrap">
            <Ident kind="app" value={application.name} tooltip={false} />
            <StatusChip status={overview.status} />
            {archived ? <Badge>archived</Badge> : null}
          </span>
        }
        documentTitle={application.name}
        subtitle={application.description || "Application configuration across environments."}
        actions={
          <>
            <label className="row-wrap" htmlFor="application-schema-track">
              <span className="muted">Schema</span>
              <select
                id="application-schema-track"
                aria-label="Schema version"
                value={schemaVersion}
                onChange={(event) => {
                  actions.closeAll();
                  void replaceQuery({
                    schema_version: event.target.value,
                    ship: "",
                    rollback: "",
                    migrate: migrate ?? "",
                  });
                }}
              >
                {schemas.map((schema) => (
                  <option key={schema.version} value={schema.version}>
                    v{schema.version}
                  </option>
                ))}
                {!schemas.some((schema) => schema.version === 0) ? (
                  <option value={0}>v0 · schema-free</option>
                ) : null}
              </select>
            </label>
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
            <EnvironmentAction
              label="Ship"
              icon={<Send size={15} />}
              variant="default"
              environments={archived ? [] : focusEnv ? [focusEnv] : environmentNames}
              onPick={(environment) => actions.openShip(environment)}
            />
            <EnvironmentAction
              label="Roll back"
              icon={<RotateCcw size={15} />}
              environments={activeNames}
              onPick={actions.openRollback}
              open={rollbackMenuOpen}
              onOpenChange={setRollbackMenuOpen}
            />
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
      {archived ? (
        <div className="info-panel mb-4" role="status">
          This application is archived and read-only. Its schema history remains available.
        </div>
      ) : overview.status === "setup" ? (
        <SetupPanel overview={overview} onAction={onSetupAction} />
      ) : (
        <FindingList findings={findings} onFix={actions.onFix} className="application-findings" />
      )}
      <DefinitionCard
        overview={overview}
        onManageReleases={() => actions.manageContract()}
        onDeriveSchema={() => actions.openDerive()}
        latestSchemaVersion={latestSchema?.version}
        onUpgrade={
          activeNames.length
            ? () =>
                actions.openMigrate(
                  focusEnv && activeNames.includes(focusEnv)
                    ? focusEnv
                    : activeNames.length === 1
                      ? activeNames[0]
                      : "",
                  latestSchema?.version,
                )
            : undefined
        }
      />
      {environments.length === 0 ? (
        <EmptyState
          icon={<Icon.namespace size={20} />}
          title="No environments"
          actions={
            archived ? (
              <Button onClick={() => void setArchived(false)} loading={lifecycleSaving}>
                Unarchive application
              </Button>
            ) : (
              <Button onClick={() => actions.openAddEnvironment()}>Add environment</Button>
            )
          }
        >
          {archived
            ? "Restore it before changing its definition or adding environments."
            : "Add dev, staging, production, or a provider-specific environment to begin managing values."}
        </EmptyState>
      ) : (
        <Tabs
          value={tab === "matrix" ? "matrix" : "pipeline"}
          onValueChange={(value) => replaceQuery({ tab: value === "matrix" ? "matrix" : "" })}
          className="application-tabs"
        >
          {/* mb-2, not mb-4: the Tabs root is a flex column with gap-2, so the
              margin stacks on top of it and mb-4 spent 24px against the page's
              16px rhythm. */}
          {/* One box beside the tabs, so a filter typed on either tab is still
              applied after switching to the other. */}
          <div className="between mb-2 items-end">
            <TabsList variant="line" aria-label="Application views">
              <TabsTrigger value="pipeline">Environments</TabsTrigger>
              <TabsTrigger value="matrix">Matrix</TabsTrigger>
            </TabsList>
            <SearchField
              className="w-full max-w-[280px]"
              label="Filter values"
              placeholder="Filter by alias, key or value"
              value={valueFilter}
              onChange={setValueFilter}
              onClear={() => setValueFilter("")}
            />
          </div>
          <TabsContent value="pipeline">
            <EnvironmentPipeline
              application={application}
              environments={environments}
              rows={overview.rows}
              focusEnv={focusEnv}
              filter={valueFilter}
              callbacks={callbacks}
            />
          </TabsContent>
          <TabsContent value="matrix">
            {/* items-start: .between centres, which floats the two buttons
                ~12px below the heading they belong to against the two-line
                description beside them. */}
            <div className="between mb-2 items-start">
              {/* .between wraps on hypothetical main size, which min-width: 0
                  does not change: the description's 891px max-content left 89px
                  for a 273px button pair, so the buttons dropped to a second
                  line at the left edge at every width. A 320px basis wraps only
                  when the row really cannot hold both. */}
              <div className="grow basis-80">
                <h2 className="section-title">Configuration matrix</h2>
                <div className="faint text-sm">
                  Parameters show current values; secrets show metadata only. A bulk parameter
                  update creates an independent version in every selected environment.
                </div>
              </div>
              <div className="row-wrap">
                <Button
                  variant="outline"
                  onClick={() => actions.openSecretSeed({ environment: "", key: "" })}
                >
                  <Plus size={15} />
                  New secret
                </Button>
                <Button
                  variant="outline"
                  onClick={() =>
                    actions.openWriteRow({ key: "", kind: "parameter", environments: {} })
                  }
                >
                  <Plus size={15} />
                  New parameter
                </Button>
              </div>
            </div>
            <ConfigurationMatrix
              app={application.name}
              schemaVersion={schemaVersion}
              environments={environments.map((environment) => ({
                env: environment.namespace.env,
                production: environment.production,
              }))}
              overview={environments}
              rows={overview.rows}
              filter={valueFilter}
              onAddSecret={(environment, key) => actions.openSecretSeed({ environment, key })}
              onAddValue={actions.openAddValueForKey}
              onOpenSecret={(environment, key) =>
                actions.openSecretWorkspace({ env: environment, app: application.name, key })
              }
              onOpenParameter={(environment, key) =>
                actions.openParameter({ env: environment, app: application.name, key })
              }
              onEdit={actions.openWriteRow}
            />
          </TabsContent>
        </Tabs>
      )}
      {modals}
    </div>
  );
}
