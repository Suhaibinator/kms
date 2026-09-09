import { useSchemaRegistry } from "@/lib/useSchemaRegistry";
import { useRouter } from "next/router";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { Ident } from "@/components/Ident";
import { Modal } from "@/components/Modal";
import ConnectSdkPanel from "@/components/onboarding/ConnectSdkPanel";
import { ParameterWorkspace } from "@/components/parameters/ParameterWorkspace";
import { releaseKey } from "@/components/releases/utils";
import { SecretWorkspace } from "@/components/secrets/SecretWorkspace";
import RollbackDialog from "@/components/ship/RollbackDialog";
import ShipModal from "@/components/ship/ShipModal";
import { useToast } from "@/context/ToastContext";
import {
  api,
  isSecretAlreadyExists,
  type ResourceRef,
  SECRET_ALREADY_EXISTS_MESSAGE,
} from "@/lib/api";
import { links } from "@/lib/links";
import { valueFor, valueForKey } from "@/lib/overview";
import type { FixAction } from "@/lib/readiness";
import type {
  ApplicationConfigurationRow,
  ApplicationOverview,
  ConfigurationSchema,
  Finding,
  HealthResponse,
  ReleaseEntryKind,
  ShipResult,
} from "@/lib/types";
import { queryValue, useQueryReplace } from "@/lib/url";
import { AddEnvironmentModal } from "./AddEnvironmentModal";
import { ApplicationDefinitionModal } from "./ApplicationDefinitionModal";
import { BulkParameterModal } from "./BulkParameterModal";
import CloneEnvironmentModal from "./CloneEnvironmentModal";
import { DeriveSchemaDialog } from "./DeriveSchemaDialog";
import type { EnvironmentCallbacks } from "./EnvironmentColumn";
import { ImportDefaultsModal } from "./ImportDefaultsModal";
import { QuickSecretModal } from "./QuickSecretModal";
import { SchemaMigrationModal } from "./SchemaMigrationModal";
import type { CloneSeed, QuickSecretSeed } from "./shared";

interface ShipTarget {
  env?: string;
  alias?: string;
}

/**
 * Everything an application surface can open. The application page and the
 * single-environment page hold different layouts around the same set of
 * modals, so the wiring lives here once: the callers pass a `pathname` (the
 * route the modals clean their query params back onto) and render `modals`.
 */
export interface ApplicationActions {
  /** Ship to `env` (the page default when omitted), optionally prefilling one alias. */
  openShip: (env?: string, alias?: string) => void;
  openRollback: (env: string) => void;
  openConnect: (env: string) => void;
  openImportDefaults: (env: string) => void;
  /** Upgrade schema in `env`; without a version the modal picks the newest. */
  openMigrate: (env: string, schemaVersion?: number) => void;
  /** Add an environment; `copyFrom` preselects "Copy values from <env>". */
  openAddEnvironment: (copyFrom?: string) => void;
  openDefinition: () => void;
  openDerive: () => void;
  /** Quick-add the secret an alias resolves to. */
  openSecret: (
    env: string,
    alias: string,
    then?: QuickSecretSeed["then"],
    physicalKey?: string,
  ) => void;
  /** Quick-add a secret by physical key (a matrix cell, or a bare "New secret"). */
  openSecretSeed: (seed: QuickSecretSeed) => void;
  /** Write the parameter a contract alias resolves to. */
  openAddValue: (env: string, alias: string) => void;
  /** Write one parameter by its physical key. */
  openAddValueForKey: (env: string, key: string) => void;
  /** Open an existing parameter's workspace. */
  openParameter: (ref: ResourceRef) => void;
  /** Open an existing secret's workspace. */
  openSecretWorkspace: (ref: ResourceRef) => void;
  /** Edit one matrix row, optionally limited to some environments. */
  openWriteRow: (row: ApplicationConfigurationRow, targets?: string[]) => void;
  /** Go to the releases page that owns the contract (or add an environment first). */
  manageContract: (env?: string) => void;
  /** A finding's Fix button (lib/readiness.ts FIX_FOR). */
  onFix: (action: FixAction, finding: Finding) => void;
  /** Close every modal, e.g. when the page switches schema track. */
  closeAll: () => void;
}

export interface UseApplicationActionsOptions {
  overview: ApplicationOverview;
  reload: () => Promise<void>;
  /** True while a write modal is open, so the page can pause its background check. */
  onWritingChange?: (writing: boolean) => void;
  /** The route whose query the modals clean up (`/applications`, `/applications/environment`). */
  pathname: string;
  /** The environment an action without one falls back to. */
  defaultEnv?: string | null;
  /** The schema track the page is on; defaults to the overview's. */
  schemaVersion?: number;
  /** `?tab=matrix`, so a workspace's back link returns to the tab it came from. */
  tab?: string | null;
}

export interface UseApplicationActionsResult {
  /** The pipeline column's callback bundle, so a column renders unchanged. */
  callbacks: EnvironmentCallbacks;
  actions: ApplicationActions;
  /** Every modal this hook owns; render it once per page. */
  modals: ReactNode;
  writing: boolean;
  /** The application's registered schemas, newest first. */
  schemas: ConfigurationSchema[];
  latestSchema: ConfigurationSchema | null;
}

export function useApplicationActions({
  overview,
  reload,
  onWritingChange,
  pathname,
  defaultEnv,
  schemaVersion = overview.application.schema_version,
  tab,
}: UseApplicationActionsOptions): UseApplicationActionsResult {
  const toast = useToast();
  const router = useRouter();
  const replaceQuery = useQueryReplace(pathname);
  const application = overview.application;
  const archived = application.archived_at_unix_ms > 0;
  const environments = overview.environments;
  const environmentNames = useMemo(
    () => environments.map((environment) => environment.namespace.env),
    [environments],
  );
  const aliases = application.contract.map((field) => field.alias);
  // The environment an action that needs one falls back to: the page's own
  // (`?env=`, or the environment page's environment), else the first
  // non-production one, else the first.
  const fallbackEnv =
    (defaultEnv && environmentNames.includes(defaultEnv) ? defaultEnv : null) ??
    environments.find((environment) => !environment.production)?.namespace.env ??
    environmentNames[0];

  const [shipTarget, setShipTarget] = useState<ShipTarget | null>(null);
  const [rollbackEnv, setRollbackEnv] = useState<string | null>(null);
  const [environmentOpen, setEnvironmentOpen] = useState(false);
  const [environmentCopyFrom, setEnvironmentCopyFrom] = useState<string | undefined>();
  const [environmentSaving, setEnvironmentSaving] = useState(false);
  const [cloneSeed, setCloneSeed] = useState<CloneSeed | null>(null);
  const [cloneOpen, setCloneOpen] = useState(false);
  const cloneRefresh = useRef<Promise<void> | null>(null);
  const [definitionOpen, setDefinitionOpen] = useState(false);
  const [deriveOpen, setDeriveOpen] = useState(false);
  const [connectEnv, setConnectEnv] = useState<string | null>(null);
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [secretSeed, setSecretSeed] = useState<QuickSecretSeed | null>(null);
  // Ship is waiting for the secret; opening its workspace on top would hide the modal.
  const [parameterTarget, setParameterTarget] = useState<ResourceRef | null>(null);
  const [secretTarget, setSecretTarget] = useState<ResourceRef | null>(null);
  const [defaultsEnv, setDefaultsEnv] = useState<string | null>(null);
  const registry = useSchemaRegistry(application.name, application.release_name);
  const schemas = registry.schemas ?? [];
  const latestSchema = schemas[0] ?? null;

  const [migrationEnv, setMigrationEnv] = useState<string | null>(null);
  const [migrationSchemaVersion, setMigrationSchemaVersion] = useState<number | undefined>();
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
    if (queryValue(router.query.ship) || environment)
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
                schemaVersion,
              }),
            ),
        },
      });
    }
    void reload();
  }

  function closeRollback() {
    setRollbackEnv(null);
    if (queryValue(router.query.rollback)) replaceQuery({ rollback: "" });
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

  function openWriteRow(row: ApplicationConfigurationRow, targets?: string[]) {
    setRetryEnvironments(null);
    setWriteTargets(targets ?? null);
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
  function openSecret(
    environment: string,
    alias: string,
    then?: QuickSecretSeed["then"],
    physicalKey?: string,
  ) {
    const value = valueFor(environments, environment, alias);
    const contentType = environments
      .flatMap((candidate) => candidate.values)
      .find((candidate) => candidate.alias === alias && candidate.content_type)?.content_type;
    setSecretSeed({ environment, key: physicalKey ?? value?.key ?? alias, contentType, then });
  }

  function openExistingSecret(environment: string, key: string) {
    setSecretTarget({ env: environment, app: application.name, key });
  }

  function openExistingParameter(environment: string, key: string) {
    setParameterTarget({ env: environment, app: application.name, key });
  }

  function openAddEnvironment(copyFrom?: string) {
    setEnvironmentCopyFrom(copyFrom);
    setEnvironmentOpen(true);
  }

  function manageContract(environment = fallbackEnv) {
    if (!environment) {
      openAddEnvironment();
      return;
    }
    void router.push(
      links.releases({
        app: application.name,
        env: environment,
        name: application.release_name,
        schemaVersion,
      }),
    );
  }

  /** The key a finding's alias resolves to in its environment (falls back to the alias). */
  function keyFor(finding: Finding): string {
    const alias = finding.scope.alias ?? "";
    return valueFor(environments, finding.scope.env ?? "", alias)?.key ?? alias;
  }

  // Every FixAction in lib/readiness.ts lands somewhere on this page or on the
  // resource the finding names.
  function onFix(action: FixAction, finding: Finding) {
    const scopeEnv = finding.scope.env ?? fallbackEnv;
    const ns = { env: scopeEnv ?? "", app: application.name };
    switch (action) {
      case "add_environment":
        openAddEnvironment();
        break;
      case "edit_contract":
        manageContract(scopeEnv);
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
            release: active ? releaseKey(active) : undefined,
            schemaVersion,
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

  function closeAll() {
    setDefinitionOpen(false);
    setDeriveOpen(false);
    setDefaultsEnv(null);
    setShipTarget(null);
    setRollbackEnv(null);
    setMigrationEnv(null);
  }

  const rollbackTarget = environments.find(
    (environment) => environment.namespace.env === rollbackEnv,
  );

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
            schemaVersion,
            env: target.env,
            tab: tab === "matrix" ? "matrix" : undefined,
          })}
        />
      </span>
    );
  }

  const actions: ApplicationActions = {
    openShip: (environment, alias) =>
      setShipTarget({ env: environment ?? fallbackEnv, ...(alias ? { alias } : null) }),
    openRollback: setRollbackEnv,
    openConnect: setConnectEnv,
    openImportDefaults: setDefaultsEnv,
    openMigrate: (environment, version) => {
      setMigrationSchemaVersion(version);
      setMigrationEnv(environment);
    },
    openAddEnvironment,
    openDefinition: () => setDefinitionOpen(true),
    openDerive: () => setDeriveOpen(true),
    openSecret,
    openSecretSeed: setSecretSeed,
    openAddValue,
    openAddValueForKey,
    openParameter: setParameterTarget,
    openSecretWorkspace: setSecretTarget,
    openWriteRow,
    manageContract,
    onFix,
    closeAll,
  };

  const callbacks: EnvironmentCallbacks = {
    onAddValue: openAddValue,
    onAddSecret: openSecret,
    onOpenSecret: openExistingSecret,
    onOpenParameter: openExistingParameter,
    onShip: (environment, alias) => setShipTarget({ env: environment, alias }),
    onRollback: setRollbackEnv,
    onConnect: setConnectEnv,
    onImportDefaults: setDefaultsEnv,
    onMigrateSchema: setMigrationEnv,
    onEditContract: manageContract,
    onFix,
  };

  const modals = (
    <>
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
      <SchemaMigrationModal
        application={application}
        environments={environments}
        initialEnvironment={migrationEnv ?? undefined}
        initialSchemaVersion={migrationSchemaVersion}
        open={!archived && migrationEnv !== null}
        onClose={() => {
          setMigrationEnv(null);
          setMigrationSchemaVersion(undefined);
          if (queryValue(router.query.migrate)) replaceQuery({ migrate: "" });
        }}
        onApplied={() => void reload()}
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
            schemaVersion={schemaVersion}
            aliases={aliases}
            health={health}
            allowedAuthMethods={
              environments.find((item) => item.namespace.env === connectEnv)?.namespace
                .allowed_auth_methods
            }
          />
        ) : null}
      </Modal>
      <AddEnvironmentModal
        app={application.name}
        environments={environmentNames}
        initialCopyFrom={environmentCopyFrom}
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
          cloneRefresh.current = reload();
        }}
        onAddSecret={async (environment, alias, key) => {
          // Wait until the new environment is reflected in all modal props, so
          // opening recovery cannot reset a value typed during the refresh.
          await cloneRefresh.current;
          openSecret(environment, alias, undefined, key);
        }}
        onAddParameter={async (environment, key) => {
          // The target must be in the overview before opening the environment picker.
          await cloneRefresh.current;
          openAddValueForKey(environment, key);
        }}
      />
      <ApplicationDefinitionModal
        open={!archived && definitionOpen}
        application={application}
        onClose={() => setDefinitionOpen(false)}
        onSaved={() => {
          setDefinitionOpen(false);
          void reload();
        }}
      />
      <DeriveSchemaDialog
        open={!archived && deriveOpen}
        application={application}
        existingSchemaJson={overview.schema_json}
        onClose={() => setDeriveOpen(false)}
        onPinned={() => {
          registry.reload();
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
        schemaVersion={schemaVersion}
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
    </>
  );

  return { callbacks, actions, modals, writing, schemas, latestSchema };
}
