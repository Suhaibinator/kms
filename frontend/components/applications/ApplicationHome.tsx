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
import { useRouter } from "next/router";
import { useEffect, useMemo, useRef, useState } from "react";
import type { SetupAction } from "@/components/applications/contracts";
import { FindingList } from "@/components/FindingList";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { Modal } from "@/components/Modal";
import ConnectSdkPanel from "@/components/onboarding/ConnectSdkPanel";
import SetupPanel from "@/components/onboarding/SetupPanel";
import { ParameterWorkspace } from "@/components/parameters/ParameterWorkspace";
import { releaseKey } from "@/components/releases/utils";
import { StatusChip } from "@/components/StatusChip";
import { SecretWorkspace } from "@/components/secrets/SecretWorkspace";
import RollbackDialog from "@/components/ship/RollbackDialog";
import ShipModal from "@/components/ship/ShipModal";
import { TransportBadge } from "@/components/TransportBadge";
import { Badge, EmptyState, PageHeader } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import {
  api,
  isSecretAlreadyExists,
  type ResourceRef,
  SECRET_ALREADY_EXISTS_MESSAGE,
} from "@/lib/api";
import type { ContractEntry } from "@/lib/contract-derive";
import { crumbs } from "@/lib/crumbs";
import { links } from "@/lib/links";
import { valueFor, valueForKey } from "@/lib/overview";
import type { FixAction } from "@/lib/readiness";
import type {
  ApplicationConfigurationRow,
  ApplicationOverview,
  Finding,
  HealthResponse,
  ReleaseEntryKind,
  ShipResult,
} from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { ActionMenu, type ActionMenuItem } from "./ActionMenu";
import { AddEnvironmentModal } from "./AddEnvironmentModal";
import { ApplicationDefinitionModal } from "./ApplicationDefinitionModal";
import { BulkParameterModal } from "./BulkParameterModal";
import CloneEnvironmentModal from "./CloneEnvironmentModal";
import { ConfigurationMatrix } from "./ConfigurationMatrix";
import { ALIGNMENT_CODES, DefinitionCard } from "./DefinitionCard";
import { DeriveSchemaDialog } from "./DeriveSchemaDialog";
import { EnvironmentPipeline } from "./EnvironmentPipeline";
import { ImportDefaultsModal } from "./ImportDefaultsModal";
import { QuickSecretModal } from "./QuickSecretModal";
import type { CloneSeed, QuickSecretSeed } from "./shared";
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
}

interface ShipTarget {
  env?: string;
  alias?: string;
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
        {label}
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
    return { key, label, disabled: !only, onSelect: () => only && onPick(only) };
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
}: ApplicationHomeProps) {
  const toast = useToast();
  const router = useRouter();
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
  const aliases = application.contract.map((field) => field.alias);
  const findings = useMemo(() => applicationFindings(overview), [overview]);

  const [shipTarget, setShipTarget] = useState<ShipTarget | null>(null);
  const [rollbackEnv, setRollbackEnv] = useState<string | null>(null);
  const [rollbackMenuOpen, setRollbackMenuOpen] = useState(false);
  const [environmentOpen, setEnvironmentOpen] = useState(false);
  const [environmentSaving, setEnvironmentSaving] = useState(false);
  const [cloneSeed, setCloneSeed] = useState<CloneSeed | null>(null);
  const [cloneOpen, setCloneOpen] = useState(false);
  const [definition, setDefinition] = useState<{ prefill: ContractEntry[] | null } | null>(null);
  const [deriveOpen, setDeriveOpen] = useState(false);
  const [connectEnv, setConnectEnv] = useState<string | null>(null);
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [secretSeed, setSecretSeed] = useState<QuickSecretSeed | null>(null);
  // Ship is waiting for the secret; opening its workspace on top would hide the modal.
  const [parameterTarget, setParameterTarget] = useState<ResourceRef | null>(null);
  const [secretTarget, setSecretTarget] = useState<ResourceRef | null>(null);
  const [defaultsEnv, setDefaultsEnv] = useState<string | null>(null);
  const [secretSaving, setSecretSaving] = useState(false);
  const [writeRow, setWriteRow] = useState<ApplicationConfigurationRow | null>(null);
  const [writeTargets, setWriteTargets] = useState<string[] | null>(null);
  const [retryEnvironments, setRetryEnvironments] = useState<string[] | null>(null);
  const writing = writeRow !== null;
  useEffect(() => {
    onWritingChange?.(writing);
    return () => onWritingChange?.(false);
  }, [writing, onWritingChange]);
  const [writeSaving, setWriteSaving] = useState(false);
  const [lifecycleSaving, setLifecycleSaving] = useState(false);

  // Seed the modals from the URL once per value: a palette action that sets
  // `?ship=` while already on this page must open the modal, and closing it
  // (which clears the param through replaceQuery) must not reopen it. The
  // refs remember which value was consumed, so a reload between the close and
  // the router update cannot re-seed from the still-present param.
  const seededShip = useRef<string | null>(null);
  const seededRollback = useRef<string | null>(null);
  useEffect(() => {
    if (!ship) {
      seededShip.current = null;
    } else if (seededShip.current !== ship) {
      seededShip.current = ship;
      setShipTarget({ env: focusEnv ?? defaultShipEnv, alias: ship === "1" ? undefined : ship });
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
      if (target) setRollbackEnv(target);
      else if (activeNames.length > 1) setRollbackMenuOpen(true);
    }
  }, [ship, rollback, focusEnv, defaultShipEnv, activeNames]);

  // Health only matters to the Connect SDK panel (endpoint + TLS warning).
  useEffect(() => {
    if (!connectEnv || health) return;
    let cancelled = false;
    api
      .health()
      .then((response) => {
        if (!cancelled) setHealth(response);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [connectEnv, health]);

  /** Land on the column that was open in Ship, and drop `?ship=` so it cannot reopen. */
  function closeShip(environment?: string) {
    setShipTarget(null);
    if (ship || environment)
      replaceQuery({ ship: "", ...(environment ? { env: environment } : {}) });
  }

  function onShipped(result: ShipResult, environment: string) {
    const release = result.release;
    if (result.status === "activated" && release) {
      toast.success(`Shipped ${release.name}@${release.version} to ${environment}`, undefined, {
        action: {
          label: "Open release",
          onClick: () =>
            void router.push(
              links.releases({
                app: application.name,
                env: environment,
                name: release.name,
                release: releaseKey(release),
              }),
            ),
        },
      });
    }
    void reload();
  }

  function closeRollback() {
    setRollbackEnv(null);
    if (rollback) replaceQuery({ rollback: "" });
  }

  /** Write one parameter by its physical key (a matrix cell). */
  function openAddValueForKey(environment: string, key: string) {
    setRetryEnvironments(null);
    setWriteTargets([environment]);
    setWriteRow({ key, kind: "parameter", environments: {} });
  }

  /** Write the parameter a contract alias resolves to (a pipeline row or a finding). */
  function openAddValue(environment: string, alias: string) {
    openAddValueForKey(environment, valueFor(environments, environment, alias)?.key ?? alias);
  }

  function openWriteRow(row: ApplicationConfigurationRow) {
    setRetryEnvironments(null);
    setWriteTargets(null);
    setWriteRow(row);
  }

  function closeWrite() {
    setWriteRow(null);
    // A partial failure still wrote the environments that succeeded; the
    // overview is refreshed once the user is done retrying, not underneath them.
    if (retryEnvironments) {
      setRetryEnvironments(null);
      void reload();
    }
  }

  /** Quick-add a secret for an alias: the key the alias resolves to, typed like a sibling environment's value. */
  function openSecret(environment: string, alias: string, then?: QuickSecretSeed["then"]) {
    const value = valueFor(environments, environment, alias);
    const contentType = environments
      .flatMap((candidate) => candidate.values)
      .find((candidate) => candidate.alias === alias && candidate.content_type)?.content_type;
    setSecretSeed({ environment, key: value?.key ?? alias, contentType, then });
  }

  function openExistingSecret(environment: string, key: string) {
    setSecretTarget({ env: environment, app: application.name, key });
  }

  function openExistingParameter(environment: string, key: string) {
    setParameterTarget({ env: environment, app: application.name, key });
  }

  function onSetupAction(action: SetupAction) {
    switch (action.kind) {
      case "edit-definition":
        setDefinition({ prefill: null });
        break;
      case "register-schema":
        setDeriveOpen(true);
        break;
      case "add-environment":
        setEnvironmentOpen(true);
        break;
      case "fill-values": {
        const field = application.contract.find((entry) => entry.alias === action.alias);
        if (action.alias && field?.kind === "secret") openSecret(action.env, action.alias);
        else if (action.alias) openAddValue(action.env, action.alias);
        else setShipTarget({ env: action.env });
        break;
      }
      case "ship":
        setShipTarget({ env: action.env });
        break;
      case "connect":
        setConnectEnv(action.env ?? defaultShipEnv ?? null);
        break;
      case "create-app":
        break;
    }
  }

  /** The key a finding's alias resolves to in its environment (falls back to the alias). */
  function keyFor(finding: Finding): string {
    const alias = finding.scope.alias ?? "";
    return valueFor(environments, finding.scope.env ?? "", alias)?.key ?? alias;
  }

  // Every FixAction in lib/readiness.ts lands somewhere on this page or on the
  // resource the finding names.
  function onFix(action: FixAction, finding: Finding) {
    const scopeEnv = finding.scope.env ?? defaultShipEnv;
    const ns = { env: scopeEnv ?? "", app: application.name };
    switch (action) {
      case "add_environment":
        setEnvironmentOpen(true);
        break;
      case "edit_contract":
        setDefinition({ prefill: null });
        break;
      case "pin_schema":
        setDeriveOpen(true);
        break;
      case "ship":
        setShipTarget({ env: scopeEnv, alias: finding.scope.alias });
        break;
      case "create_parameter":
        if (scopeEnv && finding.scope.alias) openAddValue(scopeEnv, finding.scope.alias);
        else setShipTarget({ env: scopeEnv });
        break;
      case "create_secret":
        openSecret(scopeEnv ?? "", finding.scope.alias ?? "");
        break;
      case "open_resource":
        openExistingParameter(ns.env, keyFor(finding));
        break;
      case "open_secret":
        openExistingSecret(ns.env, keyFor(finding));
        break;
      case "open_release": {
        const active = environments.find((candidate) => candidate.namespace.env === scopeEnv)
          ?.release.active;
        void router.push(
          links.releases({
            app: application.name,
            env: scopeEnv,
            name: application.release_name,
            release: active ? `${active.name}@${active.version}` : undefined,
          }),
        );
        break;
      }
      case "connect_sdk":
        setConnectEnv(scopeEnv ?? null);
        break;
      case "open_subscribers":
        void router.push(links.subscribers());
        break;
      case "open_health":
        void router.push(links.health());
        break;
    }
  }

  const rollbackTarget = activeEnvironments.find(
    (environment) => environment.namespace.env === rollbackEnv,
  );
  // The alias the open secret serves, read from the overview's resolved values.
  /** The alias behind an opened resource plus a way back to its column, shown under the workspace title. */
  function workspaceContext(target: ResourceRef, kind: ReleaseEntryKind) {
    const alias = valueForKey(
      environments.find((candidate) => candidate.namespace.env === target.env),
      kind,
      target.key,
    )?.alias;
    return (
      <span className="row-wrap">
        {alias ? <Ident kind="alias" value={alias} tooltip={false} /> : null}
        <Ident
          kind="app"
          value={application.name}
          tooltip={false}
          href={links.application(application.name, {
            env: target.env,
            tab: tab === "matrix" ? "matrix" : undefined,
          })}
        />
      </span>
    );
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
      setDefaultsEnv,
    ),
    {
      key: "add-environment",
      label: (
        <>
          <Plus size={15} aria-hidden />
          Add environment
        </>
      ),
      onSelect: () => setEnvironmentOpen(true),
    },
    {
      key: "edit-definition",
      label: (
        <>
          <SlidersHorizontal size={15} aria-hidden />
          Edit definition
        </>
      ),
      onSelect: () => setDefinition({ prefill: null }),
    },
    environmentItem(
      "connect-sdk",
      <>
        <Cable size={15} aria-hidden />
        Connect SDK
      </>,
      environmentNames,
      setConnectEnv,
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
        breadcrumbs={crumbs.application(application.name)}
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
            <Button
              type="button"
              disabled={archived || environments.length === 0}
              onClick={() => setShipTarget({ env: defaultShipEnv })}
            >
              <Send size={15} />
              Ship
            </Button>
            <EnvironmentAction
              label="Roll back"
              icon={<RotateCcw size={15} />}
              environments={activeNames}
              onPick={setRollbackEnv}
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
        <FindingList findings={findings} onFix={onFix} className="application-findings" />
      )}
      <DefinitionCard
        overview={overview}
        onEdit={(prefill) => setDefinition({ prefill: prefill ?? null })}
        onDeriveSchema={() => setDeriveOpen(true)}
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
              <Button onClick={() => setEnvironmentOpen(true)}>Add environment</Button>
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
          <TabsList variant="line" aria-label="Application views" className="mb-2">
            <TabsTrigger value="pipeline">Environments</TabsTrigger>
            <TabsTrigger value="matrix">Matrix</TabsTrigger>
          </TabsList>
          <TabsContent value="pipeline">
            <EnvironmentPipeline
              application={application}
              environments={environments}
              rows={overview.rows}
              focusEnv={focusEnv}
              callbacks={{
                onAddValue: openAddValue,
                onAddSecret: openSecret,
                onOpenSecret: openExistingSecret,
                onOpenParameter: openExistingParameter,
                onShip: (environment, alias) => setShipTarget({ env: environment, alias }),
                onRollback: setRollbackEnv,
                onConnect: setConnectEnv,
                onImportDefaults: setDefaultsEnv,
                onEditContract: () => setDefinition({ prefill: null }),
                onFix,
              }}
            />
          </TabsContent>
          <TabsContent value="matrix">
            {/* items-start: .between centres, which floats the two buttons
                ~12px below the heading they belong to against the two-line
                description beside them. */}
            <div className="between mb-2 items-start">
              <div>
                <h2 className="section-title">Configuration matrix</h2>
                <div className="faint text-sm">
                  Parameters show current values; secrets show metadata only. A bulk parameter
                  update creates an independent version in every selected environment.
                </div>
              </div>
              <div className="row-wrap">
                <Button
                  variant="outline"
                  onClick={() => setSecretSeed({ environment: "", key: "" })}
                >
                  <Plus size={15} />
                  New secret
                </Button>
                <Button
                  variant="outline"
                  onClick={() => openWriteRow({ key: "", kind: "parameter", environments: {} })}
                >
                  <Plus size={15} />
                  New parameter
                </Button>
              </div>
            </div>
            <ConfigurationMatrix
              app={application.name}
              environments={environments.map((environment) => ({
                env: environment.namespace.env,
                production: environment.production,
              }))}
              overview={environments}
              rows={overview.rows}
              onAddSecret={(environment, key) => setSecretSeed({ environment, key })}
              onAddValue={openAddValueForKey}
              onOpenSecret={openExistingSecret}
              onOpenParameter={openExistingParameter}
              onEdit={openWriteRow}
            />
          </TabsContent>
        </Tabs>
      )}

      <ShipModal
        application={application}
        environments={environments}
        schemaJson={overview.schema_json}
        initialEnvironment={shipTarget?.env}
        initialAlias={shipTarget?.alias}
        open={!archived && shipTarget !== null}
        onClose={closeShip}
        onShipped={onShipped}
        onAddSecret={(environment, alias) => openSecret(environment, alias, "stay")}
        onOpenSecret={openExistingSecret}
        onRolledBack={() => void reload()}
      />
      <RollbackDialog
        namespace={{ env: rollbackEnv ?? "", app: application.name }}
        name={application.release_name}
        active={rollbackTarget?.release.active ?? null}
        open={!archived && rollbackEnv !== null}
        onClose={closeRollback}
        onDone={() => {
          closeRollback();
          void reload();
        }}
      />
      <Modal
        mobileFullScreen
        open={connectEnv !== null}
        title="Connect SDK"
        onClose={() => setConnectEnv(null)}
        wide
      >
        {connectEnv ? (
          <ConnectSdkPanel
            namespace={{ env: connectEnv, app: application.name }}
            releaseName={application.release_name}
            aliases={aliases}
            health={health}
          />
        ) : null}
      </Modal>
      <AddEnvironmentModal
        app={application.name}
        environments={environmentNames}
        open={!archived && environmentOpen}
        saving={environmentSaving}
        onClose={() => setEnvironmentOpen(false)}
        onClone={(seed) => {
          setEnvironmentOpen(false);
          setCloneSeed(seed);
          setCloneOpen(true);
        }}
        onSave={async (environment, description, methods) => {
          setEnvironmentSaving(true);
          try {
            await api.createNamespace({
              env: environment,
              app: application.name,
              description,
              allowed_auth_methods: methods,
            });
            toast.success("Environment added", `${environment}/${application.name} is ready.`);
            setEnvironmentOpen(false);
            await reload();
          } catch (error) {
            toast.error(error, "Failed to add environment");
          } finally {
            setEnvironmentSaving(false);
          }
        }}
      />
      <CloneEnvironmentModal
        application={application}
        environments={environments}
        seed={cloneSeed}
        open={!archived && cloneOpen}
        onClose={() => setCloneOpen(false)}
        onCreated={(result) => {
          setCloneOpen(false);
          replaceQuery({ env: result.namespace.env });
          void reload();
        }}
        onAddSecret={(environment, alias) => {
          setCloneOpen(false);
          replaceQuery({ env: environment });
          void reload();
          openSecret(environment, alias);
        }}
      />
      <ApplicationDefinitionModal
        open={!archived && definition !== null}
        application={application}
        schemaJson={overview.schema_json}
        environments={environments}
        prefillContract={definition?.prefill ?? null}
        onClose={() => setDefinition(null)}
        onSaved={() => {
          setDefinition(null);
          void reload();
        }}
      />
      <DeriveSchemaDialog
        open={!archived && deriveOpen}
        application={application}
        existingSchemaJson={overview.schema_json}
        onClose={() => setDeriveOpen(false)}
        onPinned={() => {
          setDeriveOpen(false);
          void reload();
        }}
      />
      <QuickSecretModal
        app={application.name}
        environments={environmentNames}
        seed={secretSeed}
        saving={secretSaving}
        onClose={() => setSecretSeed(null)}
        onSave={async (request) => {
          setSecretSaving(true);
          try {
            const response = await api.createSecret({
              env: request.environment,
              app: application.name,
              key: request.key,
              value_base64: request.valueBase64,
              content_type: request.contentType,
              metadata_json: request.metadataJson,
              ...(request.bindingKey !== undefined ? { binding_key: request.bindingKey } : null),
              create_only: true,
              expires_at_unix_ms: request.expiresAtUnixMs,
            });
            toast.success(
              `Secret created (version ${response.version})`,
              `${application.name} · ${request.environment} · ${request.key}`,
            );
            return response;
          } catch (error) {
            if (isSecretAlreadyExists(error)) {
              toast.error(SECRET_ALREADY_EXISTS_MESSAGE, "Secret already exists");
            } else {
              toast.error(error, "Failed to create secret");
            }
            throw error;
          } finally {
            setSecretSaving(false);
          }
        }}
        onCreated={(ref) => {
          // A secret added for Ship returns to the modal that asked for it.
          if (secretSeed?.then !== "stay") setSecretTarget(ref);
          setSecretSeed(null);
          void reload();
        }}
      />
      <ParameterWorkspace
        parameterRef={parameterTarget}
        context={parameterTarget ? workspaceContext(parameterTarget, "parameter") : undefined}
        onClose={() => setParameterTarget(null)}
        onChanged={() => void reload()}
        onDeleted={() => void reload()}
      />
      <SecretWorkspace
        secretRef={secretTarget}
        context={secretTarget ? workspaceContext(secretTarget, "secret") : undefined}
        onClose={() => setSecretTarget(null)}
        onChanged={() => void reload()}
        onDeleted={() => void reload()}
      />
      <ImportDefaultsModal
        application={application.name}
        environment={defaultsEnv ?? ""}
        production={
          environments.find((candidate) => candidate.namespace.env === defaultsEnv)?.production ??
          false
        }
        open={!archived && defaultsEnv !== null}
        onClose={() => setDefaultsEnv(null)}
        onImported={reload}
      />
      <BulkParameterModal
        app={application.name}
        environments={environmentNames}
        schemaJson={overview.schema_json}
        row={writeRow}
        initialEnvironments={writeTargets}
        retryEnvironments={retryEnvironments}
        saving={writeSaving}
        onClose={closeWrite}
        onSave={async (request) => {
          setWriteSaving(true);
          try {
            const response = await api.putApplicationParameter(request);
            const failures = response.results.filter((result) => result.error);
            if (failures.length === 0) {
              toast.success(
                "Values updated",
                `Created independent versions in ${response.results.length} ${response.results.length === 1 ? "environment" : "environments"}.`,
              );
              setWriteRow(null);
              setRetryEnvironments(null);
              await reload();
              return;
            }
            toast.error(
              new Error(
                failures.map((result) => `${result.environment}: ${result.error}`).join("; "),
              ),
              "Some environments failed",
            );
            // Keep the modal and its edits; narrow the targets to what failed.
            setRetryEnvironments(failures.map((result) => result.environment));
          } catch (error) {
            toast.error(error, "Failed to update values");
          } finally {
            setWriteSaving(false);
          }
        }}
      />
    </div>
  );
}
