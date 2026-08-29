import { Download, Plus, Trash2, Upload } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Badge, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { FileInput } from "@/components/ui/file-input";
import { Input } from "@/components/ui/input";
import {
  CONTRACT_FILE_FORMAT,
  type ContractEntry,
  checkContractAlignment,
  deriveContractFromSchema,
  type ParsedContractFile,
  parseContractFile,
} from "@/lib/contract-derive";
import { PARAMETER_CONTENT_TYPES } from "@/lib/validation";
import { contractProblems } from "./contracts";

export interface ContractEditorProps {
  value: ContractEntry[];
  onChange: (next: ContractEntry[]) => void;
  /** When set, the editor checks alignment live and offers one-click fixes. */
  schemaJson?: string | null;
  /** Called after a successful Import (the wizard captures `schema_sha256`). */
  onImport?: (parsed: ParsedContractFile) => void;
  disabled?: boolean;
}

type Origin = "artifact" | "diverged" | null;

const KIND_OPTIONS = [
  { value: "parameter", label: "parameter" },
  { value: "secret", label: "secret" },
];
const CONTENT_TYPE_OPTIONS = PARAMETER_CONTENT_TYPES.map((type) => ({ value: type, label: type }));

let rowCounter = 0;
const newRowId = () => `row-${++rowCounter}`;

function sameEntry(a: ContractEntry, b: ContractEntry): boolean {
  if (a.alias !== b.alias || a.kind !== b.kind) return false;
  return a.kind === "secret" || (a.content_type ?? "") === (b.content_type ?? "");
}

/** The `kms-config-contract/v1` envelope for the current rows. */
export function exportContract(contract: readonly ContractEntry[]): string {
  return JSON.stringify(
    {
      format: CONTRACT_FILE_FORMAT,
      groups: contract
        .filter((entry) => entry.kind === "parameter")
        .map((entry) => ({
          alias: entry.alias,
          kind: "parameter",
          content_type: entry.content_type,
        })),
      secrets: contract
        .filter((entry) => entry.kind === "secret")
        .map((entry) => ({ alias: entry.alias, kind: "secret" })),
    },
    null,
    2,
  );
}

/**
 * Structured contract rows with Import (envelope or bare array), Export and a
 * live alignment check against a schema. Rows that came from an imported
 * artifact are marked "from artifact" until they are edited ("diverged").
 */
export function ContractEditor({
  value,
  onChange,
  schemaJson,
  onImport,
  disabled,
}: ContractEditorProps) {
  const id = useId();
  // One stable id per row, so an artifact snapshot survives an alias edit. A
  // parent that swaps in a contract of another length gets fresh ids for the
  // extra rows; same-length replacements (re-derive) keep theirs.
  const ids = useRef<string[]>([]);
  if (ids.current.length !== value.length) {
    ids.current = value.map((_, index) => ids.current[index] ?? newRowId());
  }
  const [artifact, setArtifact] = useState<Map<string, ContractEntry>>(() => new Map());
  const [panel, setPanel] = useState<"import" | "export" | null>(null);
  const [importText, setImportText] = useState("");
  const [importError, setImportError] = useState("");

  const problems = useMemo(() => contractProblems(value), [value]);
  const problemCount = problems.filter((entry) => entry !== null).length;
  const problem = problems.find((entry) => entry !== null) ?? null;
  const alignment = useMemo(
    () => (schemaJson ? checkContractAlignment(value, schemaJson) : null),
    [value, schemaJson],
  );
  // Expected content types per schema property, for the one-click fixes.
  const expected = useMemo(() => {
    const map = new Map<string, string>();
    if (!schemaJson) return map;
    for (const entry of deriveContractFromSchema(schemaJson).contract) {
      if (entry.content_type) map.set(entry.alias, entry.content_type);
    }
    return map;
  }, [schemaJson]);

  function originOf(index: number, entry: ContractEntry): Origin {
    const snapshot = artifact.get(ids.current[index] ?? "");
    if (!snapshot) return null;
    return sameEntry(snapshot, entry) ? "artifact" : "diverged";
  }

  function commit(nextIds: string[], next: ContractEntry[]) {
    ids.current = nextIds;
    onChange(next);
  }

  function update(index: number, patch: Partial<ContractEntry>) {
    onChange(
      value.map((entry, at) => {
        if (at !== index) return entry;
        const next: ContractEntry = { ...entry, ...patch };
        if (next.kind === "secret") delete next.content_type;
        else if (next.content_type === undefined) next.content_type = "json";
        return next;
      }),
    );
  }

  function remove(index: number) {
    commit(
      ids.current.filter((_, at) => at !== index),
      value.filter((_, at) => at !== index),
    );
  }

  function add(entry: ContractEntry = { alias: "", kind: "parameter", content_type: "json" }) {
    commit([...ids.current, newRowId()], [...value, entry]);
  }

  function applyImport() {
    try {
      const parsed = parseContractFile(importText);
      const nextIds = parsed.contract.map(() => newRowId());
      setArtifact(new Map(nextIds.map((rowId, index) => [rowId, { ...parsed.contract[index] }])));
      commit(nextIds, parsed.contract);
      onImport?.(parsed);
      setImportError("");
      setImportText("");
      setPanel(null);
    } catch (cause) {
      setImportError(cause instanceof Error ? cause.message : "Could not import the contract.");
    }
  }

  async function readFile(file: File | undefined) {
    if (!file) return;
    setImportText(await file.text());
  }

  const headers = ["Alias", "Kind", "Content type"];

  return (
    <div className="contract-editor">
      {value.length === 0 ? (
        <div className="faint text-sm">No aliases yet. Add one or import a contract file.</div>
      ) : (
        <>
          {/* Visually names the three columns; each input already carries its own accessible label. */}
          <div
            className="contract-editor-row contract-editor-head text-xs font-semibold text-muted-foreground"
            aria-hidden="true"
          >
            {headers.map((header) => (
              <span key={header}>{header}</span>
            ))}
            <span />
            <span />
          </div>
          <ul className="contract-editor-rows" aria-label="Contract aliases">
            {value.map((entry, index) => {
              const origin = originOf(index, entry);
              const rowId = ids.current[index] ?? String(index);
              const rowProblem = problems[index] ?? null;
              const problemId = `${id}-problem-${rowId}`;
              return (
                <li className="contract-editor-row" key={rowId}>
                  <Input
                    className="font-mono"
                    aria-label={`Alias ${index + 1}`}
                    aria-invalid={rowProblem ? true : undefined}
                    aria-describedby={rowProblem ? problemId : undefined}
                    value={entry.alias}
                    disabled={disabled}
                    placeholder="alias"
                    onChange={(event) => update(index, { alias: event.target.value })}
                  />
                  <AppSelect
                    id={`${id}-kind-${index}`}
                    value={entry.kind}
                    disabled={disabled}
                    options={KIND_OPTIONS}
                    onValueChange={(kind) =>
                      update(index, { kind: kind === "secret" ? "secret" : "parameter" })
                    }
                  />
                  {entry.kind === "parameter" ? (
                    <AppSelect
                      id={`${id}-type-${index}`}
                      value={entry.content_type ?? ""}
                      disabled={disabled}
                      placeholder="content type"
                      options={CONTENT_TYPE_OPTIONS}
                      onValueChange={(contentType) => update(index, { content_type: contentType })}
                    />
                  ) : (
                    <span className="faint text-sm contract-editor-secret">no content type</span>
                  )}
                  <span className="contract-editor-origin">
                    {origin === "artifact" ? <Badge kind="accent">from artifact</Badge> : null}
                    {origin === "diverged" ? <Badge kind="warning">diverged</Badge> : null}
                  </span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    disabled={disabled}
                    aria-label={`Remove ${entry.alias || `row ${index + 1}`}`}
                    onClick={() => remove(index)}
                  >
                    <Trash2 size={14} />
                  </Button>
                  {rowProblem ? (
                    <span id={problemId} className="col-span-full text-danger text-xs">
                      {rowProblem}
                    </span>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </>
      )}
      {problem ? (
        <div className="text-danger text-sm" role="alert">
          {problem}
          {problemCount > 1 ? ` · ${problemCount} rows need attention` : ""}
        </div>
      ) : null}
      <div className="contract-editor-actions">
        <Button type="button" variant="outline" size="sm" disabled={disabled} onClick={() => add()}>
          <Plus size={13} />
          Add alias
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={disabled}
          aria-expanded={panel === "import"}
          onClick={() => setPanel(panel === "import" ? null : "import")}
        >
          <Upload size={13} />
          Import
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-expanded={panel === "export"}
          onClick={() => setPanel(panel === "export" ? null : "export")}
        >
          <Download size={13} />
          Export
        </Button>
      </div>
      {panel === "import" ? (
        <div className="contract-editor-panel">
          <label className="text-sm" htmlFor={`${id}-import`}>
            Paste a <span className="mono">{CONTRACT_FILE_FORMAT}</span> document or a JSON array of{" "}
            <span className="mono">{"{alias, kind, content_type}"}</span> entries.
          </label>
          <Textarea
            id={`${id}-import`}
            className="font-mono"
            rows={6}
            value={importText}
            spellCheck={false}
            onChange={(event) => setImportText(event.target.value)}
          />
          <FileInput
            accept=".json,application/json"
            aria-label="Contract file"
            buttonLabel="Load file…"
            onFile={(file) => void readFile(file)}
          />
          <div className="row-wrap">
            <Button type="button" size="sm" disabled={!importText.trim()} onClick={applyImport}>
              Apply import
            </Button>
          </div>
          {importError ? (
            <div className="text-danger text-sm" role="alert">
              {importError}
            </div>
          ) : null}
        </div>
      ) : null}
      {panel === "export" ? (
        <div className="contract-editor-panel">
          <div className="between">
            <span className="text-sm">
              <span className="mono">{CONTRACT_FILE_FORMAT}</span> document for this contract.
            </span>
            <CopyButton value={() => exportContract(value)} label="Copy" />
          </div>
          <Textarea
            className="font-mono"
            rows={8}
            readOnly
            aria-label="Exported contract"
            value={exportContract(value)}
          />
        </div>
      ) : null}
      {alignment ? (
        <section className="contract-editor-alignment" aria-label="Schema alignment">
          {alignment.aligned ? (
            <span className="text-sm text-success">Aligned with the schema.</span>
          ) : (
            <ul className="definition-issues">
              {alignment.issues.map((issue) => {
                let fix: { label: string; run: () => void } | null = null;
                if (issue.code === "missing_in_contract" && issue.alias) {
                  const alias = issue.alias;
                  fix = {
                    label: `Add ${alias}`,
                    run: () =>
                      add({
                        alias,
                        kind: "parameter",
                        content_type: expected.get(alias) ?? "json",
                      }),
                  };
                } else if (issue.code === "missing_in_schema" && issue.alias) {
                  const alias = issue.alias;
                  fix = {
                    label: `Remove ${alias}`,
                    run: () => remove(value.findIndex((entry) => entry.alias === alias)),
                  };
                } else if (issue.code === "content_type_mismatch" && issue.alias) {
                  const alias = issue.alias;
                  const type = expected.get(alias);
                  if (type) {
                    fix = {
                      label: `Use ${type}`,
                      run: () =>
                        onChange(
                          value.map((entry) =>
                            entry.alias === alias ? { ...entry, content_type: type } : entry,
                          ),
                        ),
                    };
                  }
                }
                return (
                  <li
                    key={`${issue.code}:${issue.alias ?? ""}`}
                    className={`definition-issue definition-issue-${issue.severity}`}
                  >
                    <Badge kind={issue.severity === "error" ? "danger" : "warning"}>
                      {issue.severity}
                    </Badge>
                    <span className="text-sm">{issue.detail}</span>
                    {fix ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="xs"
                        disabled={disabled}
                        onClick={fix.run}
                      >
                        {fix.label}
                      </Button>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          )}
        </section>
      ) : null}
    </div>
  );
}
