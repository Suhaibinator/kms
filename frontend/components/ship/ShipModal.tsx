import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { ShipModalProps } from "@/components/applications/contracts";
import { Ident, ReleaseIdent } from "@/components/Ident";
import { Modal } from "@/components/Modal";
import { ViolationTable } from "@/components/releases/ViolationTable";
import { Badge, Button, Checkbox, Field, Input, Spinner } from "@/components/ui";
import { ButtonLink } from "@/components/ui/button";
import { api, isAbortError, isConflict } from "@/lib/api";
import type { ShipStepId } from "@/lib/glossary";
import { useLatestRequest } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { FixAction } from "@/lib/readiness";
import type {
  Finding,
  RollbackResponse,
  ShipChange,
  ShipPreview as ShipPreviewData,
  ShipResult,
} from "@/lib/types";
import { type ShipConflict, ConflictPanel } from "./ConflictPanel";
import {
  buildChanges,
  changesKey,
  defaultEnvironment,
  driftCandidates,
  everActivated,
  initialRows,
  makeRow,
  missingSecrets,
  needsTypedConfirmation,
  PREVIEW_DEBOUNCE_MS,
  readStoredMode,
  reuseWrittenVersions,
  rowsParse,
  type ShipMode,
  type ShipPhase,
  type ShipRow,
  storeMode,
} from "./model";
import RollbackDialog from "./RollbackDialog";
import { RolloutPanel } from "./RolloutPanel";
import { ShipEditor } from "./ShipEditor";
import { ShipPreview } from "./ShipPreview";
import { ShipSteps } from "./ShipSteps";

export type { ShipModalProps };

interface Activation {
  version: number;
  revision: number;
  previousVersion: number;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function newRequestId(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }
}

/**
 * Quick change: edit values, dry-run a preview, ship with a CAS guard, watch
 * the rollout — one modal, guided or express (plan §2.4).
 */
export default function ShipModal({
  application,
  environments,
  initialEnvironment,
  initialAlias,
  open,
  onClose,
  onShipped,
  onAddSecret,
}: ShipModalProps) {
  const [environment, setEnvironment] = useState("");
  const [rows, setRows] = useState<ShipRow[]>([]);
  const [optIns, setOptIns] = useState<string[]>([]);
  const [phase, setPhase] = useState<ShipPhase>("compose");
  const [mode, setMode] = useState<ShipMode>("guided");
  const [preview, setPreview] = useState<ShipPreviewData | null>(null);
  const [previewChanges, setPreviewChanges] = useState<ShipChange[]>([]);
  const [previewKey, setPreviewKey] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [confirmText, setConfirmText] = useState("");
  const [shipError, setShipError] = useState<string | null>(null);
  const [result, setResult] = useState<ShipResult | null>(null);
  const [conflict, setConflict] = useState<ShipConflict | null>(null);
  const [activation, setActivation] = useState<Activation | null>(null);
  const [retrying, setRetrying] = useState(false);
  const [rollbackOpen, setRollbackOpen] = useState(false);
  const [rolledBack, setRolledBack] = useState<RollbackResponse | null>(null);
  const previewRequest = useLatestRequest();
  // Bumped whenever the environment changes or the modal closes, so a value
  // prefill that lands late is dropped instead of overwriting fresh rows.
  const loadGeneration = useRef(0);
  const loadingAliases = useRef(new Set<string>());
  const confirmId = useId();

  const env = useMemo(
    () => environments.find((candidate) => candidate.namespace.env === environment) ?? null,
    [environments, environment],
  );
  const namespace = useMemo(
    () => ({ env: environment, app: application.name }),
    [environment, application.name],
  );
  const blockers = useMemo(() => missingSecrets(application, env), [application, env]);
  const drift = useMemo(() => driftCandidates(env, rows), [env, rows]);
  const changes = useMemo(() => buildChanges(rows, optIns), [rows, optIns]);
  const key = `${environment}|${changesKey(changes)}`;
  const ready = environment !== "" && rowsParse(rows);
  const stale = preview !== null && previewKey !== key;
  const production = needsTypedConfirmation(environment);
  const hasActive = env?.release.active !== undefined;

  const resetFor = useCallback(
    (nextEnvironment: string) => {
      loadGeneration.current += 1;
      loadingAliases.current.clear();
      previewRequest.abort();
      const nextEnv =
        environments.find((candidate) => candidate.namespace.env === nextEnvironment) ?? null;
      setEnvironment(nextEnvironment);
      setRows(initialRows(application, nextEnv, initialAlias));
      setOptIns([]);
      setPreview(null);
      setPreviewChanges([]);
      setPreviewKey("");
      setPreviewLoading(false);
      setPreviewError(null);
      setConfirmText("");
      setShipError(null);
      setResult(null);
      setConflict(null);
    },
    [application, environments, initialAlias, previewRequest],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: initialise once per open; later prop changes (an overview reload) must not wipe the user's edits.
  useEffect(() => {
    if (!open) {
      loadGeneration.current += 1;
      previewRequest.abort();
      return;
    }
    setPhase("compose");
    setActivation(null);
    setRolledBack(null);
    setRollbackOpen(false);
    setRetrying(false);
    setMode(readStoredMode() ?? (everActivated(environments) ? "express" : "guided"));
    resetFor(defaultEnvironment(environments, initialEnvironment));
  }, [open]);

  // Prefill each new row with the parameter's current value.
  useEffect(() => {
    if (!open || !environment) return;
    const generation = loadGeneration.current;
    for (const row of rows) {
      if (row.loaded || !row.key || loadingAliases.current.has(row.alias)) continue;
      loadingAliases.current.add(row.alias);
      const key = row.key;
      const alias = row.alias;
      void api
        .getParameter({ env: environment, app: application.name, key })
        .then(
          ({ parameter }) => ({ value: parameter.value, loaded: true }) as Partial<ShipRow>,
          (error: unknown) =>
            isAbortError(error)
              ? null
              : ({ loaded: true, loadError: errorMessage(error) } as Partial<ShipRow>),
        )
        .then((patch) => {
          if (generation !== loadGeneration.current || !patch) return;
          loadingAliases.current.delete(alias);
          setRows((current) =>
            current.map((candidate) =>
              candidate.alias === alias && !candidate.loaded
                ? { ...candidate, ...patch }
                : candidate,
            ),
          );
        });
    }
  }, [open, environment, rows, application.name]);

  const runPreview = useCallback(async () => {
    if (!ready) return;
    const run = previewRequest.begin();
    const attempted = changes;
    setPreviewKey(key);
    setPreviewLoading(true);
    setPreviewError(null);
    try {
      const response = await api.ship({
        application: application.name,
        environment,
        changes: attempted,
        dry_run: true,
      });
      if (!run.current) return;
      setPreview(response.preview);
      setPreviewChanges(attempted);
    } catch (error) {
      if (!run.current || isAbortError(error)) return;
      setPreview(null);
      setPreviewError(errorMessage(error));
    } finally {
      if (run.current) setPreviewLoading(false);
    }
  }, [application.name, changes, environment, key, previewRequest, ready]);

  // Auto dry-run: 400 ms after the last edit, once every row parses.
  useEffect(() => {
    if (!open || phase !== "compose" || !ready || previewKey === key) return;
    const timer = window.setTimeout(() => void runPreview(), PREVIEW_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [open, phase, ready, previewKey, key, runPreview]);

  function changeMode(next: ShipMode) {
    setMode(next);
    storeMode(next);
  }

  function patchRow(alias: string, patch: Partial<ShipRow>) {
    setRows((current) => current.map((row) => (row.alias === alias ? { ...row, ...patch } : row)));
  }

  function addRow(alias: string) {
    const row = makeRow(application, env, alias);
    if (!row) return;
    setRows((current) => (current.some((r) => r.alias === alias) ? current : [...current, row]));
    setOptIns((current) => current.filter((candidate) => candidate !== alias));
  }

  function removeRow(alias: string) {
    loadingAliases.current.delete(alias);
    setRows((current) => current.filter((row) => row.alias !== alias));
  }

  function toggleOptIn(alias: string, include: boolean) {
    setOptIns((current) =>
      include
        ? current.includes(alias)
          ? current
          : [...current, alias]
        : current.filter((candidate) => candidate !== alias),
    );
  }

  function handleFix(action: FixAction, finding: Finding) {
    if (action === "create_secret" && finding.scope.alias) {
      onAddSecret(environment, finding.scope.alias);
    }
  }

  const canShip =
    phase === "compose" &&
    ready &&
    preview !== null &&
    !stale &&
    !previewLoading &&
    preview.validation.valid &&
    blockers.length === 0 &&
    (previewChanges.length > 0 || !hasActive) &&
    (!production || confirmText === environment);

  function enterRollout(shipped: ShipResult, next: Activation) {
    setActivation(next);
    setPhase("rollout");
    onShipped(shipped);
  }

  async function ship() {
    if (!canShip || !preview) return;
    setPhase("shipping");
    setShipError(null);
    const expected = preview.base_version;
    let response: ShipResult;
    try {
      response = await api.ship({
        application: application.name,
        environment,
        changes: previewChanges,
        expected_active_version: expected,
        request_id: newRequestId(),
      });
    } catch (error) {
      if (isConflict(error)) {
        // Preflight refused before writing anything.
        setConflict({ message: errorMessage(error) });
        setPhase("conflict");
      } else {
        setShipError(errorMessage(error));
        setPhase("compose");
      }
      return;
    }
    setResult(response);
    switch (response.status) {
      case "activated": {
        enterRollout(response, {
          version: response.release?.version ?? expected + 1,
          revision: response.activation?.activation_revision ?? 0,
          previousVersion: response.activation?.previous_version ?? expected,
        });
        return;
      }
      case "rejected":
        setPhase("rejected");
        return;
      case "release_created_not_activated":
        setRows((current) => reuseWrittenVersions(current, response));
        setPhase("release_created_not_activated");
        onShipped(response);
        return;
      case "conflict":
        setRows((current) => reuseWrittenVersions(current, response));
        setConflict({
          result: response,
          message: response.error?.message ?? "Another activation happened first.",
          currentVersion: response.error?.current_version,
        });
        setPhase("conflict");
        onShipped(response);
        return;
      default:
        // A `preview` status from a non-dry-run call is a server bug; treat it as nothing shipped.
        setShipError("The server previewed instead of shipping. Try again.");
        setPhase("compose");
    }
  }

  /** Back to compose with the rows as they are; the preview re-runs on its own. */
  function backToCompose() {
    setPreview(null);
    setPreviewKey("");
    setPreviewChanges([]);
    setConfirmText("");
    setConflict(null);
    setPhase("compose");
  }

  async function retryActivation() {
    const release = result?.release;
    if (!release || !preview) return;
    setRetrying(true);
    setShipError(null);
    try {
      const response = await api.activateRelease(
        namespace,
        release.name,
        release.version,
        preview.base_version,
      );
      setRetrying(false);
      enterRollout(result, {
        version: release.version,
        revision: response.activation_revision,
        previousVersion: response.previous_version,
      });
    } catch (error) {
      setRetrying(false);
      if (isConflict(error)) {
        setConflict({ result: result ?? undefined, message: errorMessage(error) });
        setPhase("conflict");
      } else {
        setShipError(errorMessage(error));
      }
    }
  }

  function handleClose() {
    if (phase === "shipping") return;
    onClose();
  }

  const step: ShipStepId =
    phase === "rollout"
      ? "rollout"
      : phase === "shipping" || phase === "conflict" || phase === "release_created_not_activated"
        ? "ship"
        : preview
          ? "preview"
          : "change";

  const title =
    phase === "rollout" && activation ? (
      <span className="ship-title">
        Shipped <ReleaseIdent name={application.release_name} version={activation.version} /> to{" "}
        <Ident kind="ns" value={`${environment}/${application.name}`} />
      </span>
    ) : (
      <span className="ship-title">
        Quick change · <Ident kind="app" value={application.name} />
      </span>
    );

  const disabled = phase === "shipping";
  const violations = result?.error?.validation_errors ?? preview?.validation.errors ?? [];
  const releaseHref = result?.release
    ? links.releases({
        app: application.name,
        env: environment,
        name: result.release.name,
        release: `${result.release.name}@${result.release.version}`,
      })
    : null;

  return (
    <>
      <Modal
        open={open}
        wide
        title={title}
        onClose={handleClose}
        dismissible={phase !== "shipping"}
        footer={
          phase === "rollout" ? (
            <>
              {activation && activation.previousVersion > 0 ? (
                <Button
                  type="button"
                  variant="destructive"
                  disabled={rollbackOpen}
                  onClick={() => setRollbackOpen(true)}
                  data-testid="ship-rollback"
                >
                  Roll back
                </Button>
              ) : null}
              <Button type="button" onClick={onClose} data-testid="ship-done">
                Done
              </Button>
            </>
          ) : phase === "compose" || phase === "shipping" ? (
            <>
              <Button type="button" variant="outline" onClick={handleClose} disabled={disabled}>
                Cancel
              </Button>
              <Button
                type="button"
                variant={production ? "destructive-solid" : "default"}
                disabled={!canShip}
                onClick={() => void ship()}
                data-testid="ship-submit"
              >
                {phase === "shipping" ? <Spinner /> : null}
                Ship
              </Button>
            </>
          ) : (
            <Button type="button" variant="outline" onClick={handleClose}>
              Close
            </Button>
          )
        }
      >
        <div className="ship-modal" data-testid="ship-modal" data-phase={phase} data-mode={mode}>
          <div className="ship-mode-row">
            <label className="ship-mode-toggle" htmlFor={`${confirmId}-mode`}>
              <Checkbox
                id={`${confirmId}-mode`}
                checked={mode === "guided"}
                onCheckedChange={(checked) => changeMode(checked ? "guided" : "express")}
              />
              Show steps
            </label>
          </div>
          {mode === "guided" ? <ShipSteps current={step} /> : null}

          {shipError ? (
            <div className="danger-panel mb-4" role="alert">
              {shipError}
            </div>
          ) : null}

          {phase === "compose" || phase === "shipping" ? (
            <>
              <ShipEditor
                application={application}
                environments={environments}
                environment={environment}
                env={env}
                rows={rows}
                blockers={blockers}
                disabled={disabled}
                onEnvironmentChange={resetFor}
                onRowChange={patchRow}
                onAddRow={addRow}
                onRemoveRow={removeRow}
                onAddSecret={onAddSecret}
              />
              <ShipPreview
                preview={preview}
                loading={previewLoading}
                stale={stale}
                error={previewError}
                ready={ready}
                drift={drift}
                optIns={optIns}
                disabled={disabled}
                onToggleOptIn={toggleOptIn}
                onRefresh={() => void runPreview()}
                onFix={handleFix}
              />
              {production ? (
                <Field
                  label={
                    <>
                      Type <span className="mono">{environment}</span> to ship to production
                    </>
                  }
                  htmlFor={confirmId}
                  className="ship-confirm"
                >
                  <Input
                    id={confirmId}
                    className="font-mono"
                    value={confirmText}
                    autoComplete="off"
                    spellCheck={false}
                    disabled={disabled}
                    data-testid="ship-confirm-env"
                    onChange={(event) => setConfirmText(event.target.value)}
                  />
                </Field>
              ) : null}
            </>
          ) : null}

          {phase === "rejected" ? (
            <section className="ship-outcome danger-panel" role="alert" data-testid="ship-rejected">
              <strong>Rejected before writing.</strong>
              <p className="text-sm">
                Validation failed, so no parameter version or release was created. Fix the values
                and ship again.
              </p>
              {violations.length > 0 ? <ViolationTable violations={violations} /> : null}
              <div className="ship-outcome-actions">
                <Button type="button" onClick={backToCompose}>
                  Edit changes
                </Button>
              </div>
            </section>
          ) : null}

          {phase === "release_created_not_activated" && result ? (
            <section
              className="ship-outcome danger-panel"
              role="alert"
              data-testid="ship-not-activated"
            >
              <strong>
                {result.release ? (
                  <>
                    <ReleaseIdent name={result.release.name} version={result.release.version} />{" "}
                    created, not activated.
                  </>
                ) : (
                  "Release created, not activated."
                )}
              </strong>
              <p className="text-sm">
                {result.error?.message ??
                  "The activation re-validated the release and refused it. Nothing clients see has changed."}
              </p>
              {result.parameters.length > 0 ? (
                <p className="text-sm">
                  Written:{" "}
                  {result.parameters.map((entry, index) => (
                    <span key={entry.alias}>
                      {index > 0 ? ", " : ""}
                      <span className="mono">
                        {entry.alias} v{entry.version}
                      </span>
                    </span>
                  ))}
                  . A retry pins these versions instead of writing them again.
                </p>
              ) : null}
              {violations.length > 0 ? <ViolationTable violations={violations} /> : null}
              <div className="ship-outcome-actions">
                <Button type="button" onClick={backToCompose} disabled={retrying}>
                  Fix and retry
                </Button>
                {releaseHref ? (
                  <ButtonLink href={releaseHref} variant="outline">
                    Open {result.release ? `v${result.release.version}` : ""} in Releases
                  </ButtonLink>
                ) : null}
                {!result.error?.validation_errors?.length && result.release ? (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={retrying}
                    onClick={() => void retryActivation()}
                  >
                    {retrying ? <Spinner /> : null}
                    Retry activation
                  </Button>
                ) : null}
              </div>
            </section>
          ) : null}

          {phase === "conflict" && conflict ? (
            <ConflictPanel
              environment={environment}
              releaseName={application.release_name}
              baseVersion={preview?.base_version ?? 0}
              conflict={conflict}
              disabled={false}
              onRepreview={backToCompose}
              onDiscard={onClose}
            />
          ) : null}

          {phase === "rollout" && activation ? (
            <>
              {rolledBack ? (
                <div className="info-panel mb-4" role="status" data-testid="ship-rolled-back">
                  Rolled back to{" "}
                  <ReleaseIdent
                    name={rolledBack.release.name}
                    version={rolledBack.release.version}
                  />{" "}
                  at <Ident kind="revision" value={String(rolledBack.activation_revision)} />.
                </div>
              ) : (
                <div className="ship-activation-line text-sm faint mb-3">
                  {activation.previousVersion > 0 ? (
                    <>
                      <ReleaseIdent
                        name={application.release_name}
                        version={activation.previousVersion}
                      />{" "}
                      →{" "}
                      <ReleaseIdent name={application.release_name} version={activation.version} />
                    </>
                  ) : (
                    <>
                      First activation of{" "}
                      <ReleaseIdent name={application.release_name} version={activation.version} />
                    </>
                  )}
                  {result?.parameters.length ? (
                    <>
                      {" "}
                      · wrote{" "}
                      {result.parameters.map((entry, index) => (
                        <span key={entry.alias} className="mono">
                          {index > 0 ? ", " : ""}
                          {entry.alias} v{entry.version}
                        </span>
                      ))}
                    </>
                  ) : null}
                </div>
              )}
              <RolloutPanel
                namespace={namespace}
                releaseName={application.release_name}
                activationRevision={rolledBack?.activation_revision ?? activation.revision}
                enabled={open && phase === "rollout"}
                onRollback={
                  activation.previousVersion > 0 && !rolledBack
                    ? () => setRollbackOpen(true)
                    : undefined
                }
                rollbackDisabled={rollbackOpen}
                refreshToken={rolledBack?.activation_revision}
              />
              {env?.rollout.total === 0 && env.rollout.connected === 0 ? (
                <p className="faint text-sm mt-3">
                  <Badge>tip</Badge> No SDK is connected to this environment yet.
                </p>
              ) : null}
            </>
          ) : null}
        </div>
      </Modal>

      {activation ? (
        <RollbackDialog
          namespace={namespace}
          name={application.release_name}
          active={{
            name: application.release_name,
            version: activation.version,
            activation_revision: activation.revision,
            previous_version: activation.previousVersion,
            created_by: "",
            created_at_unix_ms: 0,
            is_rolled_back: false,
            schema_id: preview?.schema_id ?? "",
            schema_version: preview?.schema_version ?? 0,
            digest: result?.release?.digest ?? "",
            entries: [],
          }}
          open={rollbackOpen}
          onClose={() => setRollbackOpen(false)}
          onDone={(rollback) => {
            setRollbackOpen(false);
            setRolledBack(rollback);
            // The environment moved again; let the page reload its overview.
            if (result) onShipped(result);
          }}
        />
      ) : null}
    </>
  );
}
