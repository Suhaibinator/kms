import { Check, Plus, Trash2 } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import type { CreateApplicationWizardProps } from "@/components/applications/contracts";
import { JsonEditor } from "@/components/JsonEditor";
import { Modal } from "@/components/Modal";
import { Badge, Checkbox, Field, Input } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { FileInput } from "@/components/ui/file-input";
import { useToast } from "@/context/ToastContext";
import { api, isConflict } from "@/lib/api";
import {
  type ContractEntry,
  deriveContractFromSchema,
  schemaSha256Hex,
} from "@/lib/contract-derive";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors } from "@/lib/hooks";
import { isProductionEnvironment } from "@/lib/readiness";
import type { Application } from "@/lib/types";
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

type SchemaMode = "none" | "new";

interface PinnedSchema {
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
  { value: "new", label: "Create with a schema" },
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
 * Basics → Schema → Contract → Environments. A new schema stays local
 * while the contract is derived, then the application, schema version one,
 * contract and pin are created atomically. Environments are created one by one
 * afterward; a 409 means the namespace already existed and is simply attached.
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
  const schemaFields = useFieldErrors<"schemaJson">();
  const { formRef, requestFocus } = useFocusFirstInvalid();
  const nameRef = useRef<HTMLInputElement>(null);

  const [schemaMode, setSchemaMode] = useState<SchemaMode>("none");
  const [newSchemaJson, setNewSchemaJson] = useState("");
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
  const resetSchemaFields = schemaFields.reset;
  useEffect(() => {
    if (!open) return;
    setStep(1);
    setName("");
    setDescription("");
    setReleaseName("runtime");
    resetBasics();
    resetSchemaFields();
    setSchemaMode("none");
    setNewSchemaJson("");
    setSchemaJsonUsed(null);
    setSchemaSha(null);
    setContract([]);
    setContractSeeded(false);
    setArtifactSha(null);
    setRows([{ env: "dev", description: "", token: false }]);
    setResults({});
    setCreated(null);
    setBusy(false);
  }, [open, resetBasics, resetSchemaFields]);

  const nameProblem = validateApplicationName(name.trim());
  const releaseNameProblem = validateReleaseName(releaseName.trim());
  const basicsBlocking = firstError(nameProblem, releaseNameProblem);

  const newSchemaJsonProblem = schemaMode === "new" ? parseSchemaObject(newSchemaJson) : null;
  const schemaBlocking = newSchemaJsonProblem;
  const pinned: PinnedSchema | null =
    schemaMode === "new" && !newSchemaJsonProblem ? { json: newSchemaJson } : null;

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
    schemaFields.markAllTouched();
    if (schemaBlocking) {
      requestFocus();
      return;
    }
    const json = schemaMode === "new" ? newSchemaJson : null;
    setSchemaJsonUsed(json);
    setSchemaSha(json ? await safeSha(json) : null);
    seedContract(json);
    setStep(3);
  }

  function next() {
    if (busy) return;
    if (step === 1) {
      basics.markAllTouched();
      if (basicsBlocking) {
        requestFocus();
        return;
      }
      setStep(2);
    } else if (step === 2) {
      void leaveSchemaStep();
    } else if (step === 3) {
      if (contractProblem) {
        requestFocus();
        return;
      }
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
    if (busy) return;
    if (rowsBlocking) {
      requestFocus();
      return;
    }
    setBusy(true);
    try {
      let app = created;
      if (!app) {
        const response = await api.createApplication({
          name: name.trim(),
          description,
          release_name: releaseName.trim(),
          schema_version: 0,
          contract,
          ...(schemaMode === "new"
            ? { schema: { schema_json: newSchemaJson, metadata_json: "{}" } }
            : {}),
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
  function updateRow(index: number, patch: Partial<EnvRow>) {
    setRows((current) => current.map((row, at) => (at === index ? { ...row, ...patch } : row)));
  }

  // Once the application exists, every dismissal hands it to the parent so
  // the page can show it: nothing typed here is lost any more.
  const dirty =
    !created && (step > 1 || name !== "" || description !== "" || releaseName !== "runtime");
  function dismiss() {
    if (created) onCreated(created);
    else onClose();
  }

  return (
    <Modal
      open={open}
      title={created ? `${created.name} created` : "New application"}
      onClose={dismiss}
      dismissible={!busy}
      dirty={dirty && !busy}
      initialFocus={nameRef}
      wide
      footer={(close) =>
        created ? (
          <>
            {failed ? (
              <Button type="button" variant="outline" onClick={() => void create()} loading={busy}>
                Retry failed
              </Button>
            ) : null}
            <Button type="button" onClick={() => onCreated(created)} disabled={busy}>
              Done
            </Button>
          </>
        ) : (
          <>
            <Button type="button" variant="outline" onClick={close} disabled={busy}>
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
              <Button form={formId} type="submit" loading={busy}>
                Next
              </Button>
            ) : (
              <Button form={formId} type="submit" loading={busy}>
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
        ref={formRef}
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
                  ref={nameRef}
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
            {schemaMode === "new" ? (
              <>
                <div className="info-panel mb-4 text-sm">
                  This becomes{" "}
                  <span className="mono">
                    {name.trim()}/{releaseName.trim()}@1
                  </span>
                  . KMS owns the name and creates it with the application in one transaction.
                </div>
                <Field
                  label="Schema JSON"
                  hint="Paste runtime.schema.json or load the file. It is registered when the application is created."
                  error={schemaFields.shown("schemaJson", newSchemaJsonProblem)}
                >
                  <JsonEditor
                    value={newSchemaJson}
                    onChange={setNewSchemaJson}
                    rows={12}
                    maxHeight="45vh"
                    onBlur={() => schemaFields.touch("schemaJson")}
                  />
                </Field>
                <Field label="Schema file" hint="…or drop a .json file here">
                  <FileInput
                    accept=".json,application/json"
                    buttonLabel="Load schema file…"
                    onFile={(file) => {
                      if (file) void file.text().then(setNewSchemaJson);
                    }}
                  />
                </Field>
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
                  Schema <span className="mono">{`${name.trim()}/${releaseName.trim()}@1`}</span>
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
                          size="sm"
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
