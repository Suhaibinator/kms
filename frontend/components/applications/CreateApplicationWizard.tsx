import { Check, Plus, Trash2 } from "lucide-react";
import { useEffect, useId, useState } from "react";
import type { CreateApplicationWizardProps } from "@/components/applications/contracts";
import { Modal } from "@/components/Modal";
import { Badge, Checkbox, Field, Input, Spinner, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isConflict } from "@/lib/api";
import {
  type ContractEntry,
  deriveContractFromSchema,
  schemaSha256Hex,
} from "@/lib/contract-derive";
import { useFieldErrors } from "@/lib/hooks";
import { isProductionEnvironment } from "@/lib/readiness";
import type { Application, ConfigurationSchema } from "@/lib/types";
import {
  firstError,
  validateApplicationName,
  validateContract,
  validateEnv,
  validateReleaseName,
} from "@/lib/validation";
import { ContractEditor } from "./ContractEditor";

export type { CreateApplicationWizardProps };

const STEPS = ["Basics", "Schema", "Contract", "Environments"] as const;

const DEFAULT_CONTRACT: ContractEntry[] = [
  { alias: "runtime", kind: "parameter", content_type: "json" },
];

type SchemaMode = "none" | "existing" | "new";

interface PinnedSchema {
  id: string;
  version: number;
  json: string;
}

interface EnvRow {
  env: string;
  description: string;
  token: boolean;
}

type EnvResult = { state: "created" | "attached" } | { state: "failed"; message: string };

const SCHEMA_MODES: Array<{ value: SchemaMode; label: string }> = [
  { value: "none", label: "No schema (validate later)" },
  { value: "existing", label: "Pin a registered schema" },
  { value: "new", label: "Register a new schema" },
];

function WizardProgress({ current }: { current: number }) {
  return (
    <ol className="wizard-steps" aria-label="New application progress">
      {STEPS.map((label, index) => {
        const number = index + 1;
        const state = number < current ? "complete" : number === current ? "current" : "upcoming";
        return (
          <li
            key={label}
            className={`wizard-step wizard-step-${state}`}
            aria-current={number === current ? "step" : undefined}
          >
            <span className="wizard-step-number" aria-hidden>
              {number < current ? <Check size={16} strokeWidth={2.25} /> : number}
            </span>
            <span>{label}</span>
          </li>
        );
      })}
    </ol>
  );
}

function parseSchemaObject(text: string): string | null {
  try {
    const parsed: unknown = JSON.parse(text);
    return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
      ? null
      : "Schema must be a JSON object.";
  } catch (cause) {
    return cause instanceof Error ? `Schema is not valid JSON: ${cause.message}` : "Invalid JSON.";
  }
}

async function safeSha(text: string): Promise<string | null> {
  try {
    return await schemaSha256Hex(text);
  } catch {
    return null;
  }
}

/**
 * Basics → Schema → Contract → Environments. A new schema is registered when
 * leaving the Schema step, so a failed registration leaves nothing behind and
 * the contract can be derived from it. Environments are created one by one
 * after the application; a 409 means the namespace already existed and is
 * simply attached, and failed rows can be retried without redoing the rest.
 */
export default function CreateApplicationWizard({
  open,
  onClose,
  onCreated,
}: CreateApplicationWizardProps) {
  const toast = useToast();
  const formId = useId();
  const [step, setStep] = useState(1);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [releaseName, setReleaseName] = useState("runtime");
  const basics = useFieldErrors<"name" | "releaseName">();

  const [schemaMode, setSchemaMode] = useState<SchemaMode>("none");
  const [schemas, setSchemas] = useState<ConfigurationSchema[] | null>(null);
  const [existingPick, setExistingPick] = useState("");
  const [newSchemaId, setNewSchemaId] = useState("");
  const [newSchemaJson, setNewSchemaJson] = useState("");
  const [registered, setRegistered] = useState<PinnedSchema | null>(null);
  const [schemaError, setSchemaError] = useState("");
  const [schemaJsonUsed, setSchemaJsonUsed] = useState<string | null>(null);
  const [schemaSha, setSchemaSha] = useState<string | null>(null);

  const [contract, setContract] = useState<ContractEntry[]>([]);
  const [contractSeeded, setContractSeeded] = useState(false);
  const [artifactSha, setArtifactSha] = useState<string | null>(null);

  const [rows, setRows] = useState<EnvRow[]>([{ env: "dev", description: "", token: false }]);
  const [results, setResults] = useState<Record<number, EnvResult>>({});
  const [created, setCreated] = useState<Application | null>(null);
  const [busy, setBusy] = useState(false);

  const resetBasics = basics.reset;
  useEffect(() => {
    if (!open) return;
    setStep(1);
    setName("");
    setDescription("");
    setReleaseName("runtime");
    resetBasics();
    setSchemaMode("none");
    setSchemas(null);
    setExistingPick("");
    setNewSchemaId("");
    setNewSchemaJson("");
    setRegistered(null);
    setSchemaError("");
    setSchemaJsonUsed(null);
    setSchemaSha(null);
    setContract([]);
    setContractSeeded(false);
    setArtifactSha(null);
    setRows([{ env: "dev", description: "", token: false }]);
    setResults({});
    setCreated(null);
    setBusy(false);
  }, [open, resetBasics]);

  // The registry is only needed once the Schema step is reached.
  useEffect(() => {
    if (!open || step !== 2 || schemas !== null) return;
    let cancelled = false;
    api
      .listSchemas()
      .then((response) => {
        if (!cancelled) setSchemas(response.schemas ?? []);
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        setSchemas([]);
        toast.error(error, "Failed to load schemas");
      });
    return () => {
      cancelled = true;
    };
  }, [open, step, schemas, toast]);

  const nameProblem = validateApplicationName(name.trim());
  const releaseNameProblem = validateReleaseName(releaseName.trim());
  const basicsBlocking = firstError(nameProblem, releaseNameProblem);

  const existing = (schemas ?? []).find(
    (schema) => `${schema.id}@${schema.version}` === existingPick,
  );
  const newSchemaProblem =
    schemaMode === "new"
      ? firstError(
          newSchemaId.trim() ? null : "Schema ID is required.",
          parseSchemaObject(newSchemaJson),
        )
      : null;
  const schemaBlocking =
    schemaMode === "existing" && !existing ? "Pick a registered schema." : newSchemaProblem;
  const pinned: PinnedSchema | null =
    schemaMode === "existing" && existing
      ? { id: existing.id, version: existing.version, json: existing.schema_json }
      : schemaMode === "new"
        ? registered
        : null;

  const contractProblem = validateContract(contract);

  const activeRows = rows.filter((row) => row.env.trim() !== "");
  const rowProblems = rows.map((row, index) => {
    if (row.env.trim() === "") return null;
    const problem = validateEnv(row.env.trim());
    if (problem) return problem;
    const duplicate = rows.findIndex((other) => other.env.trim() === row.env.trim());
    return duplicate !== index ? `${row.env.trim()} is listed twice.` : null;
  });
  const rowsBlocking = rowProblems.find((problem) => problem !== null) ?? null;

  function seedContract(json: string | null) {
    if (contractSeeded) return;
    setContract(json ? deriveContractFromSchema(json).contract : DEFAULT_CONTRACT);
    setContractSeeded(true);
  }

  async function leaveSchemaStep() {
    if (schemaBlocking) return;
    let json: string | null = null;
    if (schemaMode === "existing" && existing) {
      json = existing.schema_json;
    } else if (schemaMode === "new") {
      const id = newSchemaId.trim();
      if (!(registered && registered.id === id && registered.json === newSchemaJson)) {
        setBusy(true);
        setSchemaError("");
        try {
          const { schema } = await api.createSchema(id, newSchemaJson);
          setRegistered({ id: schema.id, version: schema.version, json: newSchemaJson });
          toast.success("Schema registered", `${schema.id}@${schema.version}`);
        } catch (error) {
          setSchemaError(error instanceof Error ? error.message : "Failed to register the schema.");
          return;
        } finally {
          setBusy(false);
        }
      }
      json = newSchemaJson;
    }
    setSchemaJsonUsed(json);
    setSchemaSha(json ? await safeSha(json) : null);
    seedContract(json);
    setStep(3);
  }

  function next() {
    if (step === 1) {
      basics.markAllTouched();
      if (basicsBlocking) return;
      if (!newSchemaId) setNewSchemaId(`${name.trim()}-${releaseName.trim()}`);
      setStep(2);
    } else if (step === 2) {
      void leaveSchemaStep();
    } else if (step === 3) {
      if (contractProblem) return;
      setStep(4);
    }
  }

  async function createNamespaces(app: Application, indices: number[]) {
    const next = { ...results };
    for (const index of indices) {
      const row = rows[index];
      if (!row) continue;
      try {
        await api.createNamespace({
          env: row.env.trim(),
          app: app.name,
          description: row.description,
          allowed_auth_methods: row.token ? ["mtls", "token"] : ["mtls"],
        });
        next[index] = { state: "created" };
      } catch (error) {
        next[index] = isConflict(error)
          ? { state: "attached" }
          : {
              state: "failed",
              message: error instanceof Error ? error.message : "Failed to create environment.",
            };
      }
      setResults({ ...next });
    }
    return Object.values(next).every((result) => result.state !== "failed");
  }

  async function create(indices?: number[]) {
    if (busy || rowsBlocking) return;
    setBusy(true);
    try {
      let app = created;
      if (!app) {
        const response = await api.createApplication({
          name: name.trim(),
          description,
          release_name: releaseName.trim(),
          schema_id: pinned?.id ?? "",
          schema_version: pinned?.version ?? 0,
          contract,
        });
        app = response.application;
        setCreated(app);
        toast.success("Application created", `${app.name} is ready for environments.`);
      }
      const pending =
        indices ??
        rows
          .map((_, index) => index)
          .filter((index) => {
            const state = results[index]?.state;
            return rows[index]?.env.trim() !== "" && state !== "created" && state !== "attached";
          });
      const allDone = await createNamespaces(app, pending);
      if (allDone) onCreated(app);
    } catch (error) {
      toast.error(error, "Failed to create application");
    } finally {
      setBusy(false);
    }
  }

  const failed = Object.values(results).some((result) => result.state === "failed");
  const schemaOptions = (schemas ?? []).map((schema) => ({
    value: `${schema.id}@${schema.version}`,
    label: `${schema.id}@${schema.version}`,
  }));

  function updateRow(index: number, patch: Partial<EnvRow>) {
    setRows((current) => current.map((row, at) => (at === index ? { ...row, ...patch } : row)));
  }

  return (
    <Modal
      open={open}
      title={created ? `${created.name} created` : "New application"}
      onClose={onClose}
      dismissible={!busy}
      wide
      footer={
        created ? (
          <>
            {failed ? (
              <Button type="button" variant="outline" onClick={() => void create()} disabled={busy}>
                {busy ? <Spinner /> : null}
                Retry failed
              </Button>
            ) : null}
            <Button type="button" onClick={() => onCreated(created)} disabled={busy}>
              Done
            </Button>
          </>
        ) : (
          <>
            <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            {step > 1 ? (
              <Button
                type="button"
                variant="outline"
                onClick={() => setStep(step - 1)}
                disabled={busy}
              >
                Back
              </Button>
            ) : null}
            {step < 4 ? (
              <Button
                form={formId}
                type="submit"
                disabled={
                  busy ||
                  (step === 1 && basicsBlocking !== null) ||
                  (step === 2 && schemaBlocking !== null) ||
                  (step === 3 && contractProblem !== null)
                }
              >
                {busy ? <Spinner /> : null}
                {step === 2 && schemaMode === "new" ? "Register and continue" : "Next"}
              </Button>
            ) : (
              <Button form={formId} type="submit" disabled={busy || rowsBlocking !== null}>
                {busy ? <Spinner /> : null}
                Create application
              </Button>
            )}
          </>
        )
      }
    >
      <WizardProgress current={created ? 4 : step} />
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          if (step < 4) next();
          else void create();
        }}
      >
        {step === 1 ? (
          <>
            <div className="wizard-intro text-sm">
              An application owns one configuration shape. Environments hold the values.
            </div>
            <div className="form-row">
              <Field
                label="Application name"
                hint="Lowercase letters, digits, and hyphens."
                error={basics.shown("name", nameProblem)}
              >
                <Input
                  className="font-mono"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  onBlur={() => basics.touch("name")}
                  placeholder="payments-api"
                />
              </Field>
              <Field
                label="Release name"
                hint="What clients subscribe to; defaults to runtime."
                error={basics.shown("releaseName", releaseNameProblem)}
              >
                <Input
                  className="font-mono"
                  value={releaseName}
                  onChange={(event) => setReleaseName(event.target.value)}
                  onBlur={() => basics.touch("releaseName")}
                />
              </Field>
            </div>
            <Field label="Description">
              <Input value={description} onChange={(event) => setDescription(event.target.value)} />
            </Field>
          </>
        ) : null}

        {step === 2 ? (
          <>
            <div className="wizard-intro text-sm">
              A pinned schema validates every release before activation. Only parameter aliases are
              validated; secrets never enter the schema.
            </div>
            <Field label="Schema">
              <AppSelect
                value={schemaMode}
                onValueChange={(mode) => setSchemaMode((mode as SchemaMode) || "none")}
                options={SCHEMA_MODES}
              />
            </Field>
            {schemaMode === "existing" ? (
              <Field
                label="Registered schema"
                hint={schemas === null ? "Loading the registry…" : undefined}
                error={existingPick === "" ? null : existing ? null : "Unknown schema."}
              >
                <AppSelect
                  className="font-mono"
                  value={existingPick}
                  onValueChange={setExistingPick}
                  options={schemaOptions}
                  placeholder={
                    schemas?.length === 0 ? "No schemas registered" : "Select id@version…"
                  }
                  disabled={schemas === null || schemas.length === 0}
                />
              </Field>
            ) : null}
            {schemaMode === "new" ? (
              <>
                <Field
                  label="Schema ID"
                  error={newSchemaId.trim() ? null : "Schema ID is required."}
                >
                  <Input
                    className="font-mono"
                    value={newSchemaId}
                    onChange={(event) => setNewSchemaId(event.target.value)}
                  />
                </Field>
                <Field
                  label="Schema JSON"
                  hint="Paste runtime.schema.json or load the file. Registered when you continue."
                  error={newSchemaJson.trim() ? parseSchemaObject(newSchemaJson) : null}
                >
                  <Textarea
                    className="font-mono"
                    rows={10}
                    value={newSchemaJson}
                    spellCheck={false}
                    onChange={(event) => setNewSchemaJson(event.target.value)}
                  />
                </Field>
                <input
                  type="file"
                  accept=".json,application/json"
                  aria-label="Schema file"
                  onChange={(event) => {
                    const file = event.target.files?.[0];
                    if (file) void file.text().then(setNewSchemaJson);
                  }}
                />
                {registered ? (
                  <div className="info-panel mt-4 text-sm">
                    Registered as{" "}
                    <span className="mono">
                      {registered.id}@{registered.version}
                    </span>
                    {registered.json !== newSchemaJson || registered.id !== newSchemaId.trim()
                      ? "; the edited schema will be registered as a new version."
                      : "."}
                  </div>
                ) : null}
                {schemaError ? (
                  <div className="danger-panel mt-4" role="alert">
                    {schemaError}
                  </div>
                ) : null}
              </>
            ) : null}
          </>
        ) : null}

        {step === 3 ? (
          <>
            <div className="wizard-intro text-sm">
              The contract lists every alias the application reads.{" "}
              {pinned ? "It was derived from the schema; adjust it if needed." : ""}
            </div>
            {pinned ? (
              <div className="row-wrap mb-4">
                <span className="text-sm">
                  Schema <span className="mono">{`${pinned.id}@${pinned.version}`}</span>
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() =>
                    setContract(deriveContractFromSchema(pinned.json, contract).contract)
                  }
                >
                  Re-derive from schema
                </Button>
              </div>
            ) : null}
            <ContractEditor
              value={contract}
              onChange={setContract}
              schemaJson={schemaJsonUsed}
              onImport={(parsed) => setArtifactSha(parsed.schema_sha256 ?? null)}
            />
            {artifactSha ? (
              pinned && schemaSha ? (
                artifactSha === schemaSha ? (
                  <div className="info-panel mt-4 text-sm" role="status">
                    The imported contract was generated from this exact schema (sha256 matches).
                  </div>
                ) : (
                  <div className="warn-panel mt-4 text-sm" role="status">
                    The imported contract was generated from a different schema (sha256{" "}
                    <span className="mono">{artifactSha.slice(0, 12)}</span> ≠{" "}
                    <span className="mono">{schemaSha.slice(0, 12)}</span>). Regenerate the artifact
                    or pin the matching schema.
                  </div>
                )
              ) : (
                <div className="info-panel mt-4 text-sm" role="status">
                  The imported contract references schema sha256{" "}
                  <span className="mono">{artifactSha.slice(0, 12)}</span>; no schema is pinned.
                </div>
              )
            ) : null}
          </>
        ) : null}

        {step === 4 ? (
          <>
            <div className="wizard-intro text-sm">
              Each environment is an isolated namespace. Existing namespaces named{" "}
              <span className="mono">{`<env>/${name.trim()}`}</span> are attached, not recreated.
            </div>
            <div className="wizard-env-rows">
              {rows.map((row, index) => {
                const result = results[index];
                const production = isProductionEnvironment(row.env.trim());
                return (
                  <div className="wizard-env-row" key={index}>
                    <Field label="Environment" error={rowProblems[index]}>
                      <Input
                        className="font-mono"
                        value={row.env}
                        disabled={result?.state === "created" || result?.state === "attached"}
                        onChange={(event) => updateRow(index, { env: event.target.value })}
                        placeholder="prod"
                      />
                    </Field>
                    <Field label="Description">
                      <Input
                        value={row.description}
                        onChange={(event) => updateRow(index, { description: event.target.value })}
                      />
                    </Field>
                    <div className="wizard-env-meta">
                      <div className="checkbox-row">
                        <Checkbox
                          id={`${formId}-token-${index}`}
                          checked={row.token}
                          onCheckedChange={(checked) => updateRow(index, { token: checked })}
                        />
                        <label htmlFor={`${formId}-token-${index}`}>Bearer tokens</label>
                      </div>
                      {production ? <Badge kind="warning">production</Badge> : null}
                      {result?.state === "created" ? <Badge kind="success">created</Badge> : null}
                      {result?.state === "attached" ? <Badge kind="accent">attached</Badge> : null}
                      {result?.state === "failed" ? (
                        <>
                          <Badge kind="danger" title={result.message}>
                            failed
                          </Badge>
                          <span className="text-danger text-xs">{result.message}</span>
                          <Button
                            type="button"
                            variant="outline"
                            size="xs"
                            disabled={busy}
                            onClick={() => void create([index])}
                          >
                            Retry
                          </Button>
                        </>
                      ) : null}
                      {!created ? (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Remove environment ${row.env || index + 1}`}
                          onClick={() =>
                            setRows((current) => current.filter((_, at) => at !== index))
                          }
                        >
                          <Trash2 size={14} />
                        </Button>
                      ) : null}
                    </div>
                  </div>
                );
              })}
            </div>
            {!created ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() =>
                  setRows((current) => [...current, { env: "", description: "", token: false }])
                }
              >
                <Plus size={13} />
                Add environment
              </Button>
            ) : null}
            {activeRows.length === 0 && !created ? (
              <div className="faint mt-4 text-sm">No environments yet; you can add them later.</div>
            ) : null}
          </>
        ) : null}
      </form>
    </Modal>
  );
}
