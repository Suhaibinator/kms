import { AlertTriangle, ArrowLeft, ArrowRight, CheckCircle2, Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Modal } from "@/components/Modal";
import { ParameterValueInput } from "@/components/ParameterValueInput";
import { RolloutPanel } from "@/components/ship/RolloutPanel";
import { Badge, Button, Field, Input, Loading } from "@/components/ui";
import { FileInput } from "@/components/ui/file-input";
import { useToast } from "@/context/ToastContext";
import { api, isConflict, isUnreachableError } from "@/lib/api";
import { deriveContractFromSchema } from "@/lib/contract-derive";
import type {
  Application,
  ApplicationContractField,
  ConfigurationReleaseEntry,
  ConfigurationSchema,
  EnvironmentOverview,
  SchemaMigrationChange,
  SchemaMigrationResponse,
} from "@/lib/types";
import { PARAMETER_CONTENT_TYPES } from "@/lib/types";
import { parseUpgradeDefaults, type UpgradeDefaults } from "@/lib/upgrade-defaults";
import { SchemaComparison } from "./SchemaComparison";

type Step = 0 | 1 | 2 | 3 | 4;
type DraftField = ApplicationContractField & {
  id: number;
  fromAlias?: string;
  key: string;
  version?: number;
  versionText: string;
  value?: string;
  originalValue?: string;
  originalContentType?: string;
  loaded?: boolean;
  loading?: boolean;
  loadError?: string;
};

function exactVersionError(field: DraftField): string | undefined {
  if (!field.versionText) {
    return field.kind === "parameter" ? undefined : "Enter an exact version for a secret.";
  }
  if (!/^[1-9]\d*$/.test(field.versionText)) {
    return "Enter a positive whole number without signs, decimals, or exponents.";
  }
  return Number.isSafeInteger(Number(field.versionText)) ? undefined : "Version is too large.";
}

export interface SchemaMigrationModalProps {
  application: Application;
  environments: EnvironmentOverview[];
  initialEnvironment?: string;
  initialSchemaVersion?: number;
  open: boolean;
  onClose: () => void;
  onApplied: (result: SchemaMigrationResponse) => void;
}

const stepLabels = ["Select schema", "Review contract", "Edit values", "Review and activate"];

function validationMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function SchemaMigrationModal({
  application,
  environments,
  initialEnvironment,
  initialSchemaVersion,
  open,
  onClose,
  onApplied,
}: SchemaMigrationModalProps) {
  const toast = useToast();
  const [source, setSource] = useState("active");
  const [defaults, setDefaults] = useState<UpgradeDefaults | null>(null);
  const [fileName, setFileName] = useState("");
  const [artifactError, setArtifactError] = useState("");
  const [readingArtifact, setReadingArtifact] = useState(false);
  const artifactGeneration = useRef(0);
  const [step, setStep] = useState<Step>(0);
  const [environment, setEnvironment] = useState("");
  const [schemas, setSchemas] = useState<ConfigurationSchema[]>([]);
  const [schemaVersion, setSchemaVersion] = useState(0);
  const [fields, setFields] = useState<DraftField[]>([]);
  const [derivationNotes, setDerivationNotes] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [previewing, setPreviewing] = useState(false);
  const [applying, setApplying] = useState(false);
  const [preview, setPreview] = useState<SchemaMigrationResponse | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [uncertain, setUncertain] = useState(false);
  const [sourceConflict, setSourceConflict] = useState(false);
  const [reloadingSource, setReloadingSource] = useState(false);
  const [sourceEntries, setSourceEntries] = useState<ConfigurationReleaseEntry[]>([]);
  const nextID = useRef(1);
  const sessionKey = useRef("");
  const initializedDraft = useRef("");
  const loadGeneration = useRef(0);
  const sourceIdentity = useRef<{ version: number; activationRevision: number } | null>(null);

  const activeEnvironments = useMemo(
    () => environments.filter((item) => item.release.active),
    [environments],
  );
  const selectedEnvironment = activeEnvironments.find((item) => item.namespace.env === environment);
  const newerSchemas = schemas.filter((schema) => schema.version > application.schema_version);
  const selectedSchema = newerSchemas.find((item) => item.version === schemaVersion);
  const production = selectedEnvironment?.production === true;
  const affectedActiveEnvironments =
    preview?.affected_environments.filter((item) => item.active_version > 0) ?? [];
  const mismatchedSchemaEnvironments = affectedActiveEnvironments.filter(
    (item) => item.schema_version !== preview?.schema_version,
  );
  const contractValid =
    fields.length > 0 &&
    fields.every(
      (field, index) =>
        field.alias.trim() &&
        !fields.some(
          (other, otherIndex) => otherIndex < index && other.alias.trim() === field.alias.trim(),
        ) &&
        (field.kind === "secret" || PARAMETER_CONTENT_TYPES.includes(field.content_type ?? "")),
    );
  const valuesValid = fields.every((field) => !exactVersionError(field));

  useEffect(() => {
    if (!open) {
      artifactGeneration.current += 1;
      loadGeneration.current += 1;
      sessionKey.current = "";
      initializedDraft.current = "";
      return;
    }
    if (sessionKey.current === application.name) return;
    sessionKey.current = application.name;
    const generation = ++loadGeneration.current;
    initializedDraft.current = "";
    const initial = activeEnvironments.some((item) => item.namespace.env === initialEnvironment)
      ? (initialEnvironment ?? "")
      : (activeEnvironments[0]?.namespace.env ?? "");
    setSource("active");
    setDefaults(null);
    setFileName("");
    setArtifactError("");
    setReadingArtifact(false);
    artifactGeneration.current += 1;
    setStep(0);
    setEnvironment(initial);
    setSchemaVersion(initialSchemaVersion ?? 0);
    setPreview(null);
    setPreviewing(false);
    setReloadingSource(false);
    setConfirmation("");
    setUncertain(false);
    setSourceConflict(false);
    setLoading(true);
    const loadAll = async () => {
      const available: ConfigurationSchema[] = [];
      let token = "";
      do {
        const result = await api.listSchemas(
          application.name,
          application.release_name,
          token || undefined,
        );
        available.push(...(result.schemas ?? []));
        token = result.next_page_token ?? "";
      } while (token);
      return available;
    };
    void loadAll()
      .then((available) => {
        if (generation !== loadGeneration.current) return;
        const newer = available.filter((schema) => schema.version > application.schema_version);
        setSchemas(available);
        setSchemaVersion((current) =>
          newer.some((schema) => schema.version === current) ? current : newer[0]?.version || 0,
        );
      })
      .catch((error) => {
        if (generation === loadGeneration.current) toast.error(error, "Could not load schemas");
      })
      .finally(() => {
        if (generation === loadGeneration.current) setLoading(false);
      });
  }, [
    open,
    application.name,
    application.release_name,
    application.schema_version,
    initialEnvironment,
    initialSchemaVersion,
    activeEnvironments,
    toast,
  ]);

  useEffect(() => {
    if (!open || !selectedEnvironment?.release.active || !selectedSchema) return;
    const draftKey = `${environment}:${schemaVersion}:${source}:${fileName}`;
    if (initializedDraft.current === draftKey) return;
    initializedDraft.current = draftKey;
    sourceIdentity.current = {
      version: selectedEnvironment.release.active.version,
      activationRevision: selectedEnvironment.release.active.activation_revision,
    };
    const entries = selectedEnvironment.release.active.entries;
    setSourceEntries(entries);
    if (source === "artifact" && (!defaults || defaults.schema_sha256 !== selectedSchema.digest))
      return;
    const derived = deriveContractFromSchema(selectedSchema.schema_json, application.contract);
    setDerivationNotes(source === "artifact" ? [] : derived.notes);
    const suggested = source === "artifact" && defaults ? defaults.contract : derived.contract;
    const mapped: DraftField[] = suggested.map((field) => {
      const entry = entries.find((candidate) => candidate.alias === field.alias);
      const imported =
        source === "artifact"
          ? defaults?.parameters.find((p) => p.alias === field.alias)
          : undefined;
      return {
        ...field,
        id: nextID.current++,
        fromAlias: entry?.alias,
        key: entry?.ref.key ?? field.alias,
        version: entry?.version,
        versionText: entry?.version ? String(entry.version) : "",
        loaded: Boolean(imported) || field.kind === "secret" || !entry,
        value: imported?.value ?? (field.kind === "parameter" && !entry ? "" : undefined),
      };
    });
    setFields(mapped);
  }, [
    open,
    environment,
    application.contract,
    selectedEnvironment,
    selectedSchema,
    source,
    defaults,
    fileName,
    schemaVersion,
  ]);

  useEffect(() => {
    if (!open || step < 2 || !selectedEnvironment) return;
    for (const field of fields) {
      if (
        field.kind !== "parameter" ||
        field.loaded ||
        field.loading ||
        field.loadError ||
        !field.key ||
        !field.version
      )
        continue;
      const id = field.id;
      const requestedKey = field.key;
      const requestedVersion = field.version;
      setFields((current) =>
        current.map((item) =>
          item.id === id ? { ...item, loading: true, loadError: undefined } : item,
        ),
      );
      void api
        .getParameter({ env: environment, app: application.name, key: field.key }, field.version)
        .then(({ parameter }) => {
          setFields((current) =>
            current.map((item) =>
              item.id === id &&
              item.key === requestedKey &&
              item.version === requestedVersion &&
              item.value === undefined
                ? {
                    ...item,
                    value: parameter.value,
                    originalValue: parameter.value,
                    originalContentType: parameter.content_type,
                    loaded: true,
                    loading: false,
                  }
                : item,
            ),
          );
        })
        .catch((error) => {
          setFields((current) =>
            current.map((item) =>
              item.id === id && item.key === requestedKey && item.version === requestedVersion
                ? { ...item, loading: false, loadError: validationMessage(error) }
                : item,
            ),
          );
          toast.error(error, `Could not load ${field.alias} at version ${field.version}`);
        });
    }
  }, [open, step, fields, selectedEnvironment, environment, application.name, toast]);

  function update(id: number, patch: Partial<DraftField>) {
    setPreview(null);
    setFields((current) =>
      current.map((field) => (field.id === id ? { ...field, ...patch } : field)),
    );
  }

  function request(execute: boolean): Parameters<typeof api.migrateApplicationSchema>[1] {
    const changes: SchemaMigrationChange[] = fields.map((field) => {
      const writesValue =
        field.kind === "parameter" &&
        field.value !== undefined &&
        (field.version === undefined ||
          field.value !== field.originalValue ||
          field.content_type !== field.originalContentType);
      const sourceEntry = sourceEntries.find((entry) => entry.alias === field.fromAlias);
      const carriesSource =
        !writesValue && sourceEntry?.ref.key === field.key && sourceEntry.version === field.version;
      return {
        alias: field.alias.trim(),
        ...(field.fromAlias && field.fromAlias !== field.alias.trim()
          ? { from_alias: field.fromAlias }
          : {}),
        ...(!carriesSource && field.key ? { key: field.key } : {}),
        ...(!carriesSource && !writesValue && field.version ? { version: field.version } : {}),
        ...(writesValue ? { value: field.value, content_type: field.content_type } : {}),
      };
    });
    return {
      environment,
      schema_version: schemaVersion,
      contract: fields.map(({ alias, kind, content_type }) => ({
        alias: alias.trim(),
        kind,
        ...(kind === "parameter" ? { content_type } : {}),
      })),
      changes,
      execute,
      ...(sourceIdentity.current
        ? {
            expected_source_version: sourceIdentity.current.version,
            expected_source_activation_revision: sourceIdentity.current.activationRevision,
          }
        : {}),
      ...(execute && preview ? { plan_digest: preview.plan_digest } : {}),
    };
  }

  async function createPreview() {
    if (!valuesValid) return;
    const generation = loadGeneration.current;
    setPreviewing(true);
    try {
      const result = await api.migrateApplicationSchema(application.name, request(false));
      if (generation !== loadGeneration.current || !open) return;
      setPreview(result);
      setStep(3);
    } catch (error) {
      if (generation === loadGeneration.current) {
        if (isConflict(error)) setSourceConflict(true);
        toast.error(error, "Could not preview migration");
      }
    } finally {
      if (generation === loadGeneration.current) setPreviewing(false);
    }
  }

  async function apply() {
    if (!preview) return;
    setApplying(true);
    try {
      const result = await api.migrateApplicationSchema(application.name, request(true));
      if (!result.executed) {
        setPreview(result);
        toast.error(new Error("The migration was not executed."), "Could not apply migration");
        return;
      }
      setPreview(result);
      setStep(4);
      onApplied(result);
    } catch (error) {
      if (isConflict(error)) {
        setPreview(null);
        setSourceConflict(true);
        setStep(2);
        toast.error(
          error,
          "The source changed. Your edits were kept; reload the source before previewing again.",
        );
      } else if (isUnreachableError(error)) {
        toast.error(error, "The result is uncertain. Refreshing active state.");
        setPreview(null);
        setUncertain(true);
        setStep(4);
        onApplied({ ...preview, executed: false });
      } else toast.error(error, "Could not apply migration");
    } finally {
      setApplying(false);
    }
  }

  async function reloadSource() {
    const generation = loadGeneration.current;
    const requestedEnvironment = environment;
    setReloadingSource(true);
    try {
      const active = await api.getActiveRelease(
        { env: environment, app: application.name },
        application.release_name,
      );
      if (generation !== loadGeneration.current || requestedEnvironment !== environment) return;
      sourceIdentity.current = {
        version: active.release.version,
        activationRevision: active.activation_revision,
      };
      setSourceEntries(active.release.entries);
      setSourceConflict(false);
      toast.info("Source refreshed. Review your retained edits before previewing again.");
    } catch (error) {
      if (generation === loadGeneration.current && requestedEnvironment === environment) {
        toast.error(error, "Could not reload source release");
      }
    } finally {
      if (generation === loadGeneration.current && requestedEnvironment === environment) {
        setReloadingSource(false);
      }
    }
  }

  const dirty = step > 0 || fields.some((field) => field.alias !== field.fromAlias);
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Upgrade application schema"
      description={`${application.name} · ${environment || "choose an environment"}`}
      workspace
      wizard
      mobileFullScreen
      dismissible={!applying}
      dirty={dirty && step !== 4}
      footer={(close) =>
        step === 4 ? (
          <Button onClick={close}>Done</Button>
        ) : (
          <>
            <Button
              variant="outline"
              disabled={previewing || applying || reloadingSource}
              onClick={() => {
                if (reloadingSource) return;
                if (step === 0) close();
                else setStep((step - 1) as Step);
              }}
            >
              <ArrowLeft size={15} />
              {step === 0 ? "Cancel" : "Back"}
            </Button>
            {step === 0 ? (
              <Button
                onClick={() => setStep(1)}
                disabled={
                  !environment ||
                  !selectedSchema ||
                  loading ||
                  readingArtifact ||
                  (source === "artifact" &&
                    (!defaults || defaults.schema_sha256 !== selectedSchema?.digest))
                }
              >
                Review contract
                <ArrowRight size={15} />
              </Button>
            ) : null}
            {step === 1 ? (
              <Button onClick={() => setStep(2)} disabled={!contractValid}>
                Edit values
                <ArrowRight size={15} />
              </Button>
            ) : null}
            {step === 2 ? (
              <Button
                onClick={() => void createPreview()}
                loading={previewing}
                disabled={
                  sourceConflict ||
                  reloadingSource ||
                  !contractValid ||
                  !valuesValid ||
                  fields.some(
                    (field) =>
                      field.kind === "parameter" &&
                      (!field.loaded || field.loading || Boolean(field.loadError)),
                  )
                }
              >
                Preview migration
                <ArrowRight size={15} />
              </Button>
            ) : null}
            {step === 3 ? (
              <Button
                variant={production ? "destructive" : "default"}
                onClick={() => void apply()}
                loading={applying}
                disabled={!preview?.valid || (production && confirmation !== environment)}
              >
                Upgrade schema & ship to {environment}
              </Button>
            ) : null}
          </>
        )
      }
    >
      {step < 4 ? (
        <ol className="migration-steps" aria-label="Migration progress">
          {stepLabels.map((label, index) => (
            <li
              key={label}
              aria-current={step === index ? "step" : undefined}
              data-complete={step > index || undefined}
            >
              {index + 1}. {label}
            </li>
          ))}
        </ol>
      ) : null}
      {loading ? <Loading label="Loading registered schemas…" /> : null}
      {!loading && step === 0 ? (
        <div className="migration-form-grid">
          <Field
            label="Destination environment"
            hint="The active release here is the migration baseline and will be replaced. Starting values can come from that release or a defaults file."
          >
            <select
              className="native-select"
              aria-label="Destination environment"
              value={environment}
              onChange={(event) => setEnvironment(event.target.value)}
            >
              {activeEnvironments.map((item) => (
                <option key={item.namespace.env} value={item.namespace.env}>
                  {item.namespace.env}
                  {item.production ? " (production)" : ""} · active v{item.release.active?.version}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label="Target registered schema"
            hint="The exact immutable schema version will be pinned."
          >
            <select
              className="native-select"
              aria-label="Target registered schema"
              value={schemaVersion}
              onChange={(event) => {
                setSchemaVersion(Number(event.target.value));
                setDefaults(null);
                setFileName("");
                setArtifactError("");
                artifactGeneration.current += 1;
                setReadingArtifact(false);
                setPreview(null);
              }}
            >
              {newerSchemas.length === 0 ? (
                <option value={0}>No newer registered schema</option>
              ) : null}
              {newerSchemas.map((schema) => (
                <option key={schema.version} value={schema.version}>
                  v{schema.version} · {schema.digest.slice(0, 16)}…
                </option>
              ))}
            </select>
          </Field>
          <Field label="Starting values">
            <select
              className="native-select"
              aria-label="Starting values"
              value={source}
              onChange={(event) => {
                setSource(event.target.value);
                setPreview(null);
                initializedDraft.current = "";
              }}
            >
              <option value="active">
                Current release {application.release_name}@
                {selectedEnvironment?.release.active?.version}
              </option>
              <option value="artifact">Import defaults file</option>
            </select>
          </Field>
          {source === "artifact" && (
            <Field
              label="Defaults artifact"
              htmlFor="migration-defaults"
              hint="Use a kms-config-defaults/v1 JSON file matching the selected schema. The selected environment is the destination; the artifact profile describes its source."
            >
              <FileInput
                id="migration-defaults"
                fileName={fileName}
                accept=".json,application/json"
                disabled={readingArtifact || !selectedSchema}
                onFile={(file) => {
                  if (!file || !selectedSchema) return;
                  const generation = ++artifactGeneration.current;
                  setDefaults(null);
                  setArtifactError("");
                  setFileName(file.name);
                  setReadingArtifact(true);
                  initializedDraft.current = "";
                  void (async () => {
                    try {
                      if (file.size > 4 * 1024 * 1024) throw new Error("Artifact exceeds 4 MiB.");
                      const parsed = parseUpgradeDefaults(await file.text(), selectedSchema.digest);
                      if (generation === artifactGeneration.current) {
                        initializedDraft.current = "";
                        setDefaults(parsed);
                      }
                    } catch (error) {
                      if (generation === artifactGeneration.current)
                        setArtifactError(validationMessage(error));
                    } finally {
                      if (generation === artifactGeneration.current) setReadingArtifact(false);
                    }
                  })();
                }}
              />
              {defaults && (
                <p>
                  Source profile: {defaults.profile} · Destination: {environment}/{application.name}
                </p>
              )}
              {artifactError && (
                <p className="danger-panel" role="alert">
                  {artifactError}
                </p>
              )}
            </Field>
          )}
          {schemas.length === 0 ? (
            <div className="info-panel">
              No other registered schema is available for this application.
            </div>
          ) : null}
        </div>
      ) : null}
      {step < 4 && (
        <>
          <SchemaComparison
            current={schemas.find((s) => s.version === application.schema_version)}
            target={selectedSchema}
            currentVersion={application.schema_version}
          />
          <section className="info-panel text-sm stack" aria-label="Upgrade scope">
            <p>
              <strong>Application change:</strong> {application.name}’s shared schema pin and
              contract change from v{application.schema_version} to v{schemaVersion || "…"}.
            </p>
            <p>
              <strong>Environment change:</strong> A new release is created and activated in{" "}
              <strong>{environment || "the environment you select"}</strong>, using{" "}
              {source === "active"
                ? `current release ${application.release_name}@${selectedEnvironment?.release.active?.version ?? "…"}`
                : "the selected defaults artifact"}
              . References for retained secret aliases are preserved.
            </p>
            <p>
              Other environments keep their active releases. Their next ship must match the shared
              definition; older-schema releases may no longer be eligible for activation or
              rollback.
            </p>
            <p>
              Validation failure leaves the definition, stored values, and active release unchanged.
            </p>
          </section>
        </>
      )}
      {step === 1 ? (
        <div className="stack">
          <div className="info-panel">
            Every target field is explicit. Rename an alias to carry its active pin; remove a row to
            remove it. New rows require a value or an exact existing resource pin.
          </div>
          {derivationNotes.length ? (
            <div className="warning-panel" role="note">
              <AlertTriangle size={17} />
              <div>
                <strong>Suggested contract changes</strong>
                <ul>
                  {derivationNotes.map((note) => (
                    <li key={note}>{note}</li>
                  ))}
                </ul>
              </div>
            </div>
          ) : null}
          {fields.map((field) => (
            <div
              className="migration-contract-row"
              key={field.id}
              data-testid={`migration-contract-${field.id}`}
            >
              <Field label="Alias">
                <Input
                  aria-label="Alias"
                  value={field.alias}
                  onChange={(event) => update(field.id, { alias: event.target.value })}
                />
              </Field>
              <Field label="Source alias">
                <select
                  className="native-select"
                  aria-label="Source alias"
                  value={field.fromAlias ?? ""}
                  onChange={(event) => {
                    const fromAlias = event.target.value || undefined;
                    const entry = sourceEntries.find((candidate) => candidate.alias === fromAlias);
                    update(field.id, {
                      fromAlias,
                      key: entry?.ref.key ?? field.alias,
                      version: entry?.version,
                      versionText: entry?.version ? String(entry.version) : "",
                      loaded: field.kind === "secret" || !entry,
                      loading: false,
                      loadError: undefined,
                      value: undefined,
                      originalValue: undefined,
                      originalContentType: undefined,
                    });
                  }}
                >
                  <option value="">New field</option>
                  {sourceEntries.map((entry) => (
                    <option key={entry.alias} value={entry.alias}>
                      {entry.alias} · {entry.kind} v{entry.version}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="Kind">
                <select
                  className="native-select"
                  aria-label="Kind"
                  value={field.kind}
                  onChange={(event) => {
                    const kind = event.target.value as "parameter" | "secret";
                    update(field.id, {
                      kind,
                      content_type: kind === "secret" ? undefined : "string",
                      fromAlias: undefined,
                      key: field.alias,
                      version: undefined,
                      versionText: "",
                      loaded: kind === "parameter",
                      loading: false,
                      loadError: undefined,
                      value: kind === "parameter" ? "" : undefined,
                      originalValue: undefined,
                      originalContentType: undefined,
                    });
                  }}
                >
                  <option value="parameter">Parameter</option>
                  <option value="secret">Secret</option>
                </select>
              </Field>
              <Field label="Content type">
                <select
                  className="native-select"
                  aria-label="Content type"
                  value={field.content_type ?? ""}
                  disabled={field.kind === "secret"}
                  onChange={(event) => update(field.id, { content_type: event.target.value })}
                >
                  <option value="">—</option>
                  {PARAMETER_CONTENT_TYPES.map((type) => (
                    <option key={type}>{type}</option>
                  ))}
                </select>
              </Field>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={`Remove ${field.alias}`}
                onClick={() => {
                  setPreview(null);
                  setFields((current) => current.filter((item) => item.id !== field.id));
                }}
              >
                <Trash2 size={15} />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            variant="outline"
            onClick={() =>
              setFields((current) => [
                ...current,
                {
                  id: nextID.current++,
                  alias: "",
                  kind: "parameter",
                  content_type: "string",
                  key: "",
                  versionText: "",
                  loaded: true,
                  value: "",
                },
              ])
            }
          >
            <Plus size={15} />
            Add contract field
          </Button>
        </div>
      ) : null}
      {step === 2 ? (
        <div className="stack">
          {sourceConflict ? (
            <div className="warning-panel">
              <AlertTriangle size={17} />
              <div>
                <strong>The active source changed</strong>
                <div>Your edits are retained. Reload its identity, then review them again.</div>
                <Button
                  variant="outline"
                  loading={reloadingSource}
                  onClick={() => void reloadSource()}
                >
                  Reload source
                </Button>
              </div>
            </div>
          ) : null}
          {fields.map((field) => (
            <ValueDisclosure
              key={field.id}
              expand={Boolean(
                !field.fromAlias ||
                  source === "artifact" ||
                  Boolean(field.loadError) ||
                  application.contract.find((f) => f.alias === field.fromAlias)?.content_type !==
                    field.content_type ||
                  preview?.validation.some((p) => p.alias === field.alias),
              )}
            >
              <summary className="cursor-pointer">
                <span className="mono">{field.alias}</span> · {field.kind} ·{" "}
                {field.value !== undefined &&
                (field.value !== field.originalValue ||
                  field.content_type !== field.originalContentType)
                  ? "edited value"
                  : field.version
                    ? `preserve v${field.version}`
                    : "new value needed"}{" "}
                · Edit
              </summary>
              <div className="migration-value-row">
                <div className="between">
                  <div>
                    <strong className="mono">{field.alias}</strong> <Badge>{field.kind}</Badge>
                    {field.fromAlias && field.fromAlias !== field.alias ? (
                      <span className="faint text-sm">
                        {" "}
                        renamed from <span className="mono">{field.fromAlias}</span>
                      </span>
                    ) : null}
                  </div>
                  <span className="faint text-sm">
                    {field.version ? `source v${field.version}` : "new"}
                  </span>
                </div>
                <div className="migration-form-grid">
                  <Field label="Resource key">
                    <Input
                      aria-label={`${field.alias} resource key`}
                      value={field.key}
                      onChange={(event) =>
                        update(field.id, {
                          key: event.target.value,
                          loaded: field.kind === "secret" || !field.version,
                          loading: false,
                          loadError: undefined,
                          value: field.version ? undefined : "",
                          originalValue: undefined,
                          originalContentType: undefined,
                        })
                      }
                    />
                  </Field>
                  <Field
                    label="Exact version"
                    hint="Leave blank when writing a new parameter value. Secrets require an exact version."
                    error={exactVersionError(field)}
                  >
                    <Input
                      aria-label={`${field.alias} exact version`}
                      inputMode="numeric"
                      value={field.versionText}
                      onChange={(event) => {
                        const versionText = event.target.value;
                        const parsed = /^[1-9]\d*$/.test(versionText)
                          ? Number(versionText)
                          : undefined;
                        const version =
                          parsed !== undefined && Number.isSafeInteger(parsed) ? parsed : undefined;
                        update(field.id, {
                          versionText,
                          version,
                          loaded: field.kind === "secret" || !versionText,
                          loading: false,
                          loadError: undefined,
                          value: versionText ? undefined : "",
                          originalValue: undefined,
                          originalContentType: undefined,
                        });
                      }}
                    />
                  </Field>
                </div>
                {field.loadError ? (
                  <div className="danger-panel" role="alert">
                    {field.loadError}. Change the key or version to retry.
                  </div>
                ) : null}
                {field.kind === "parameter" ? (
                  <Field
                    label="Value"
                    hint="Loaded from the active release's exact pin. Changes create a new version."
                  >
                    <ParameterValueInput
                      aria-label={`${field.alias} value`}
                      contentType={field.content_type ?? "string"}
                      value={field.value ?? ""}
                      onChange={(value) => update(field.id, { value })}
                      disabled={field.loading || Boolean(field.loadError)}
                    />
                  </Field>
                ) : (
                  <div className="info-panel">
                    Secrets are references only. Choose an existing key and exact version in{" "}
                    <span className="mono">
                      {environment}/{application.name}
                    </span>
                    .
                  </div>
                )}
              </div>
            </ValueDisclosure>
          ))}
        </div>
      ) : null}
      {step === 3 && preview ? (
        <div className="stack">
          <div className={preview.valid ? "success-panel" : "danger-panel"}>
            {preview.valid ? "Backend validation passed." : "Backend validation failed."}
          </div>
          <dl className="kv">
            <dt>Schema</dt>
            <dd>v{preview.schema_version}</dd>
            <dt>Source release</dt>
            <dd>
              {preview.release_name}@{preview.source_version}
            </dd>
            <dt>Definition changed</dt>
            <dd>{preview.definition_changed ? "Yes" : "No"}</dd>
          </dl>
          <div className="table-wrap card-table">
            <table className="data">
              <thead>
                <tr>
                  <th>Alias</th>
                  <th>Change</th>
                  <th>Key</th>
                  <th>Version</th>
                </tr>
              </thead>
              <tbody>
                {preview.entries.map((entry) => (
                  <tr key={entry.alias}>
                    <td className="mono" data-label="Alias">
                      {entry.alias}
                    </td>
                    <td data-label="Change">{entry.source}</td>
                    <td className="mono" data-label="Key">
                      {entry.key}
                    </td>
                    <td data-label="Version">
                      {entry.source === "removed" ? (
                        `v${entry.from_version} → removed from release`
                      ) : entry.source === "missing" ? (
                        "Value required"
                      ) : (
                        <>
                          {entry.from_version ? `v${entry.from_version} → ` : ""}v{entry.to_version}
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {preview.validation.length ? (
            <ul>
              {preview.validation.map((problem) => (
                <li key={`${problem.alias}-${problem.code}`}>
                  <strong className="mono">{problem.alias || "Release"}</strong>
                  {problem.schema_pointer ? ` · ${problem.schema_pointer}` : ""}: {problem.message}
                </li>
              ))}
            </ul>
          ) : null}
          {mismatchedSchemaEnvironments.length ||
          (preview.definition_changed && affectedActiveEnvironments.length) ? (
            <div className="warning-panel">
              <AlertTriangle size={17} />
              <div>
                <strong>Global schema pin affects other environments</strong>
                <div>
                  {mismatchedSchemaEnvironments.length
                    ? `Active releases in ${mismatchedSchemaEnvironments
                        .map(
                          (item) =>
                            `${item.environment} (release v${item.active_version}, schema v${item.schema_version})`,
                        )
                        .join(
                          ", ",
                        )} keep their old schema and will not activate or roll back until they are migrated.`
                    : ""}
                  {preview.definition_changed
                    ? " The contract change applies globally. Releases whose entries differ from the updated contract cannot activate or roll back until migrated."
                    : ""}
                </div>
              </div>
            </div>
          ) : null}
          {production ? (
            <Field label={`Type ${environment} to confirm production activation`}>
              <Input
                aria-label="Production confirmation"
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                autoComplete="off"
              />
            </Field>
          ) : null}
        </div>
      ) : null}
      {step === 4 && uncertain ? (
        <div className="danger-panel">
          <AlertTriangle size={18} />
          <div>
            <strong>Activation result is uncertain</strong>
            <div>
              The active state was refreshed. Close this migration and verify the environment before
              starting another one.
            </div>
          </div>
        </div>
      ) : null}
      {step === 4 && preview && !uncertain ? (
        <div className="stack">
          <div className="empty-state">
            <CheckCircle2 size={32} />
            <div className="empty-title">Schema migration activated</div>
            <div>
              Release{" "}
              <span className="mono">
                {preview.release_name}@{preview.release?.version}
              </span>{" "}
              is active in <span className="mono">{environment}</span>.
            </div>
          </div>
          {preview.activation ? (
            <RolloutPanel
              namespace={{ env: environment, app: application.name }}
              releaseName={preview.release_name}
              activationRevision={preview.activation.activation_revision}
              enabled
            />
          ) : null}
        </div>
      ) : null}
    </Modal>
  );
}

function ValueDisclosure({ expand, children }: { expand: boolean; children: React.ReactNode }) {
  const [expanded, setExpanded] = useState(expand);
  useEffect(() => {
    if (expand) setExpanded(true);
  }, [expand]);
  return (
    <details
      className="card p-4"
      open={expanded}
      onToggle={(event) => setExpanded(event.currentTarget.open)}
    >
      {children}
    </details>
  );
}
