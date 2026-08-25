import { Cable, FileUp, Plus, RefreshCw, RotateCcw, Send, SlidersHorizontal } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { SetupAction } from "@/components/applications/contracts";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { Modal } from "@/components/Modal";
import ConnectSdkPanel from "@/components/onboarding/ConnectSdkPanel";
import SetupPanel from "@/components/onboarding/SetupPanel";
import { StatusChip } from "@/components/StatusChip";
import RollbackDialog from "@/components/ship/RollbackDialog";
import ShipModal from "@/components/ship/ShipModal";
import { EmptyState, PageHeader } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import type { ContractEntry } from "@/lib/contract-derive";
import { crumbs } from "@/lib/crumbs";
import { utf8ToBase64 } from "@/lib/encoding";
import type { ApplicationConfigurationRow, ApplicationOverview, HealthResponse } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { ActionMenu } from "./ActionMenu";
import { AddEnvironmentModal } from "./AddEnvironmentModal";
import { ApplicationDefinitionModal } from "./ApplicationDefinitionModal";
import { BulkParameterModal } from "./BulkParameterModal";
import CloneEnvironmentModal from "./CloneEnvironmentModal";
import { ConfigurationMatrix } from "./ConfigurationMatrix";
import { DefinitionCard } from "./DefinitionCard";
import { DeriveSchemaDialog } from "./DeriveSchemaDialog";
import { EnvironmentPipeline } from "./EnvironmentPipeline";
import { ImportDefaultsModal } from "./ImportDefaultsModal";
import { QuickSecretModal } from "./QuickSecretModal";
import type { CloneSeed, QuickSecretSeed } from "./shared";

export interface ApplicationHomeProps {
  overview: ApplicationOverview;
  loading: boolean;
  reload: () => Promise<void>;
  /** `?env=`: the pipeline column to focus and the default Ship environment. */
  env: string | null;
  /** `?ship=`: `1` opens the Ship modal, an alias also prefills a row. Seeded once. */
  ship: string | null;
  /** `?tab=matrix` shows the per-key table instead of the pipeline. */
  tab: string | null;
  /** `?rollback=1` opens Roll back for `?env`, or the environment menu. Seeded once. */
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
      label={`${label} environment`}
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

export function ApplicationHome({
  overview,
  loading,
  reload,
  env,
  ship,
  tab,
  rollback,
}: ApplicationHomeProps) {
  const toast = useToast();
  const replaceQuery = useQueryReplace("/applications");
  const application = overview.application;
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
  const activeNames = activeEnvironments.map((environment) => environment.namespace.env);
  const aliases = application.contract.map((field) => field.alias);

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
  const [defaultsEnv, setDefaultsEnv] = useState<string | null>(null);
  const [secretSaving, setSecretSaving] = useState(false);
  const [writeRow, setWriteRow] = useState<ApplicationConfigurationRow | null>(null);
  const [writeTargets, setWriteTargets] = useState<string[] | null>(null);
  const [retryEnvironments, setRetryEnvironments] = useState<string[] | null>(null);
  const [writeSaving, setWriteSaving] = useState(false);
  const [seeded, setSeeded] = useState(false);

  // Seed the modals from the URL exactly once. Closing them clears the params
  // through replaceQuery (from the handlers), never the other way round.
  useEffect(() => {
    if (seeded) return;
    setSeeded(true);
    if (ship) {
      setShipTarget({ env: focusEnv ?? defaultShipEnv, alias: ship === "1" ? undefined : ship });
    }
    if (rollback === "1") {
      const target =
        focusEnv && activeNames.includes(focusEnv)
          ? focusEnv
          : activeNames.length === 1
            ? activeNames[0]
            : null;
      if (target) setRollbackEnv(target);
      else if (activeNames.length > 1) setRollbackMenuOpen(true);
    }
  }, [seeded, ship, rollback, focusEnv, defaultShipEnv, activeNames]);

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

  function closeShip() {
    setShipTarget(null);
    if (ship) replaceQuery({ ship: "" });
  }

  function closeRollback() {
    setRollbackEnv(null);
    if (rollback) replaceQuery({ rollback: "" });
  }

  function openAddValue(environment: string, alias: string) {
    const value = environments
      .find((candidate) => candidate.namespace.env === environment)
      ?.values.find((candidate) => candidate.alias === alias);
    setRetryEnvironments(null);
    setWriteTargets([environment]);
    setWriteRow({ key: value?.key ?? alias, kind: "parameter", environments: {} });
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

  function openSecret(environment: string, alias: string) {
    setSecretSeed({ environment, key: alias });
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

  const rollbackTarget = activeEnvironments.find(
    (environment) => environment.namespace.env === rollbackEnv,
  );

  return (
    <div className="application-home">
      <PageHeader
        breadcrumbs={crumbs.application(application.name)}
        title={
          <span className="row-wrap">
            <Ident kind="app" value={application.name} tooltip={false} />
            <StatusChip status={overview.status} />
          </span>
        }
        documentTitle={application.name}
        subtitle={application.description || "Application configuration across environments."}
        actions={
          <>
            <Button
              type="button"
              disabled={environments.length === 0}
              onClick={() => setShipTarget({ env: defaultShipEnv })}
            >
              <Send size={15} />
              Quick change
            </Button>
            <EnvironmentAction
              label="Import defaults"
              icon={<FileUp size={15} />}
              environments={environmentNames}
              onPick={setDefaultsEnv}
            />
            <EnvironmentAction
              label="Roll back"
              icon={<RotateCcw size={15} />}
              environments={activeNames}
              onPick={setRollbackEnv}
              open={rollbackMenuOpen}
              onOpenChange={setRollbackMenuOpen}
            />
            <Button type="button" variant="outline" onClick={() => setEnvironmentOpen(true)}>
              <Plus size={15} />
              Add environment
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => setDefinition({ prefill: null })}
            >
              <SlidersHorizontal size={15} />
              Edit definition
            </Button>
            <EnvironmentAction
              label="Connect SDK"
              icon={<Cable size={15} />}
              environments={environmentNames}
              onPick={setConnectEnv}
            />
            <Button
              type="button"
              variant="outline"
              onClick={() => void reload()}
              disabled={loading}
            >
              <RefreshCw size={15} />
              Refresh
            </Button>
          </>
        }
      />
      {overview.status === "setup" ? (
        <SetupPanel overview={overview} onAction={onSetupAction} />
      ) : null}
      <DefinitionCard
        overview={overview}
        onEdit={(prefill) => setDefinition({ prefill: prefill ?? null })}
        onDeriveSchema={() => setDeriveOpen(true)}
      />
      {environments.length === 0 ? (
        <EmptyState
          icon={<Icon.namespace size={20} />}
          title="No environments"
          actions={<Button onClick={() => setEnvironmentOpen(true)}>Add environment</Button>}
        >
          Add dev, staging, production, or a provider-specific environment to begin managing values.
        </EmptyState>
      ) : (
        <Tabs
          value={tab === "matrix" ? "matrix" : "pipeline"}
          onValueChange={(value) => replaceQuery({ tab: value === "matrix" ? "matrix" : "" })}
          className="application-tabs"
        >
          <TabsList variant="line" aria-label="Application views" className="mb-4">
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
                onShip: (environment, alias) => setShipTarget({ env: environment, alias }),
                onRollback: setRollbackEnv,
                onConnect: setConnectEnv,
              }}
            />
          </TabsContent>
          <TabsContent value="matrix">
            <div className="between mb-2">
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
              environments={environments.map((environment) => environment.namespace)}
              rows={overview.rows}
              onAddSecret={openSecret}
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
        open={shipTarget !== null}
        onClose={closeShip}
        onShipped={() => void reload()}
        onAddSecret={openSecret}
      />
      <RollbackDialog
        namespace={{ env: rollbackEnv ?? "", app: application.name }}
        name={application.release_name}
        active={rollbackTarget?.release.active ?? null}
        open={rollbackEnv !== null}
        onClose={closeRollback}
        onDone={() => {
          closeRollback();
          void reload();
        }}
      />
      <Modal
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
        open={environmentOpen}
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
        open={cloneOpen}
        onClose={() => setCloneOpen(false)}
        onCreated={() => {
          setCloneOpen(false);
          void reload();
        }}
        onAddSecret={(environment, alias) => {
          setCloneOpen(false);
          void reload();
          openSecret(environment, alias);
        }}
      />
      <ApplicationDefinitionModal
        open={definition !== null}
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
        open={deriveOpen}
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
              value_base64: utf8ToBase64(request.value),
              content_type: request.contentType,
              metadata_json: "{}",
              client_bound: false,
              generate_access_token: false,
              expires_at_unix_ms: 0,
            });
            toast.success(
              `Secret created (version ${response.version})`,
              `${application.name} · ${request.environment} · ${request.key}`,
            );
            setSecretSeed(null);
            await reload();
          } catch (error) {
            toast.error(error, "Failed to create secret");
          } finally {
            setSecretSaving(false);
          }
        }}
      />
      <ImportDefaultsModal
        application={application.name}
        environment={defaultsEnv ?? ""}
        production={
          environments.find((candidate) => candidate.namespace.env === defaultsEnv)?.production ??
          false
        }
        open={defaultsEnv !== null}
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
                `Created independent versions in ${response.results.length} environment(s).`,
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
