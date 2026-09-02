import { Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { JsonEditor } from "@/components/JsonEditor";
import { Modal } from "@/components/Modal";
import { Button, Field, Input, Spinner } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors } from "@/lib/hooks";
import type {
  ApplicationDashboard,
  ConfigurationRelease,
  CreateReleaseRequest,
  NamespaceRef,
  ReleaseEntryKind,
} from "@/lib/types";
import { validateAlias, validateMetadataJson, validateReleaseName } from "@/lib/validation";
import { parseReleaseDefinition, releaseDefinitionError } from "./utils";

type SelectorKind = "current" | "label" | "version";

interface BuilderEntry {
  id: number;
  alias: string;
  kind: ReleaseEntryKind;
  contentType: string;
  key: string;
  selector: SelectorKind;
  label: string;
  version: string;
  labels: string[];
  versions: number[];
  metadataLoading: boolean;
  contractOwned: boolean;
}

/** One entry's problems, keyed by the field that shows them. */
export interface EntryProblems {
  alias?: string;
  key?: string;
  label?: string;
  version?: string;
}

let entryID = 0;

function blankEntry(contractOwned = false): BuilderEntry {
  entryID += 1;
  return {
    id: entryID,
    alias: "",
    kind: "parameter",
    contentType: "",
    key: "",
    selector: "current",
    label: "",
    version: "",
    labels: [],
    versions: [],
    metadataLoading: false,
    contractOwned,
  };
}

function prettyDefinition(request: Omit<CreateReleaseRequest, "namespace">): string {
  return JSON.stringify(
    {
      name: request.name,
      ...(request.schema_id
        ? { schema_id: request.schema_id, schema_version: request.schema_version }
        : {}),
      entries: request.entries,
      ...(request.metadata_json && request.metadata_json !== "{}"
        ? { metadata_json: request.metadata_json }
        : {}),
    },
    null,
    2,
  );
}

function requestFromEntries(
  namespace: NamespaceRef,
  name: string,
  schemaID: string,
  schemaVersion: string,
  entries: BuilderEntry[],
  metadataJSON: string,
): CreateReleaseRequest {
  return {
    namespace,
    name: name.trim(),
    schema_id: schemaID.trim() || undefined,
    schema_version: schemaID.trim() ? Number(schemaVersion) : undefined,
    entries: entries.map((entry) => ({
      alias: entry.alias.trim(),
      kind: entry.kind,
      ref: { namespace, key: entry.key.trim() },
      ...(entry.selector === "label"
        ? { label: entry.label }
        : entry.selector === "version"
          ? { version: Number(entry.version) }
          : {}),
    })),
    metadata_json: metadataJSON.trim() || "{}",
  };
}

/**
 * Problems that belong to one entry, so each can be shown on the row that
 * has it. A duplicate alias is reported on the later entry — the first one
 * to claim the name is the one that keeps it.
 */
function entryProblems(entries: readonly BuilderEntry[]): Map<number, EntryProblems> {
  const problems = new Map<number, EntryProblems>();
  const aliases = new Set<string>();
  for (const entry of entries) {
    const mine: EntryProblems = {};
    const alias = entry.alias.trim();
    const aliasError = validateAlias(alias);
    if (aliasError) mine.alias = aliasError;
    else if (aliases.has(alias)) mine.alias = `Duplicate release alias: ${alias}`;
    else aliases.add(alias);
    if (!entry.key.trim()) mine.key = "Choose a resource.";
    if (entry.selector === "label" && !entry.label) mine.label = "Choose a label.";
    if (
      entry.selector === "version" &&
      (!Number.isInteger(Number(entry.version)) || Number(entry.version) < 1)
    ) {
      mine.version = "Choose a valid version.";
    }
    if (Object.keys(mine).length > 0) problems.set(entry.id, mine);
  }
  return problems;
}

/** The release-level checks: name, schema pin, metadata and the entry count. */
function builderError(
  name: string,
  schemaID: string,
  schemaVersion: string,
  entries: readonly BuilderEntry[],
  metadataJSON: string,
): string | null {
  const nameError = validateReleaseName(name.trim());
  if (nameError) return nameError;
  if (Boolean(schemaID.trim()) !== Boolean(schemaVersion.trim())) {
    return "Schema ID and schema version must be provided together.";
  }
  if (
    schemaVersion.trim() &&
    (!Number.isInteger(Number(schemaVersion)) || Number(schemaVersion) < 1)
  ) {
    return "Schema version must be a positive whole number.";
  }
  const metadataError = validateMetadataJson(metadataJSON.trim() || "{}");
  if (metadataError) return metadataError;
  if (entries.length === 0) return "Add at least one entry.";
  return null;
}

interface GuidedSnapshot {
  name: string;
  schemaID: string;
  schemaVersion: string;
  entries: string;
  metadataJSON: string;
}

function entriesKey(entries: readonly BuilderEntry[]): string {
  return JSON.stringify(
    entries.map((entry) => [
      entry.alias,
      entry.kind,
      entry.key,
      entry.selector,
      entry.label,
      entry.version,
    ]),
  );
}

export function ReleaseBuilder({
  open,
  namespace,
  onClose,
  onCreated,
}: {
  open: boolean;
  namespace: NamespaceRef;
  onClose: () => void;
  onCreated: (release: ConfigurationRelease) => void;
}) {
  const toast = useToast();
  const [dashboard, setDashboard] = useState<ApplicationDashboard | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<unknown>(null);
  // Bumped by Retry so the load effect re-runs without reopening the modal.
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [saving, setSaving] = useState(false);
  const [mode, setMode] = useState<"guided" | "json">("guided");
  const [modeMessage, setModeMessage] = useState("");
  const [name, setName] = useState("runtime");
  const [schemaID, setSchemaID] = useState("");
  const [schemaVersion, setSchemaVersion] = useState("");
  const [entries, setEntries] = useState<BuilderEntry[]>([]);
  const [metadataJSON, setMetadataJSON] = useState("{}");
  const [jsonText, setJSONText] = useState("");
  const [attempted, setAttempted] = useState(false);
  // What the load put on screen; the dirty guard compares against it.
  const [snapshot, setSnapshot] = useState<GuidedSnapshot | null>(null);
  const generation = useRef(0);
  // Per-entry generation + controller so a second resource selection
  // supersedes the first one's metadata response instead of racing it.
  const metadataLoads = useRef(
    new Map<number, { generation: number; controller: AbortController }>(),
  );
  // Keys are composite (`<entry id>-alias`), hence the plain string form.
  const entryErrors = useFieldErrors<string>();
  const resetEntryErrors = entryErrors.reset;
  const { formRef, requestFocus } = useFocusFirstInvalid<HTMLDivElement>();
  const schemaIDRef = useRef<HTMLInputElement>(null);
  const firstResourceRef = useRef<HTMLButtonElement>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: `loadAttempt` exists only to re-run this load from Retry.
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    const currentGeneration = ++generation.current;
    setLoading(true);
    setLoadError(null);
    setAttempted(false);
    resetEntryErrors();
    setMode("guided");
    setModeMessage("");
    setJSONText("");
    setSnapshot(null);
    void api
      .applicationDashboard(namespace.app, { signal: controller.signal })
      .then((result) => {
        if (generation.current !== currentGeneration) return;
        setDashboard(result);
        const nextName = result.application.release_name || "runtime";
        const nextSchemaID = result.application.schema_id || "";
        const nextSchemaVersion = result.application.schema_id
          ? String(result.application.schema_version)
          : "";
        const nextEntries = result.application.contract.map((field) => {
          const candidates = result.rows.filter(
            (row) => row.kind === field.kind && row.environments[namespace.env]?.present,
          );
          const directMatch = candidates.find((row) => row.key === field.alias);
          return {
            ...blankEntry(true),
            alias: field.alias,
            kind: field.kind,
            contentType: field.content_type ?? "",
            key: directMatch?.key ?? "",
          };
        });
        const seeded = nextEntries.length ? nextEntries : [blankEntry(false)];
        setName(nextName);
        setSchemaID(nextSchemaID);
        setSchemaVersion(nextSchemaVersion);
        setEntries(seeded);
        setMetadataJSON("{}");
        setSnapshot({
          name: nextName,
          schemaID: nextSchemaID,
          schemaVersion: nextSchemaVersion,
          entries: entriesKey(seeded),
          metadataJSON: "{}",
        });
      })
      .catch((error: unknown) => {
        if (generation.current !== currentGeneration || isAbortError(error)) return;
        // Never present the previous application's contract as this one's.
        setDashboard(null);
        setName("");
        setSchemaID("");
        setSchemaVersion("");
        const seeded = [blankEntry(false)];
        setEntries(seeded);
        setMetadataJSON("{}");
        setSnapshot({
          name: "",
          schemaID: "",
          schemaVersion: "",
          entries: entriesKey(seeded),
          metadataJSON: "{}",
        });
        setLoadError(error);
        toast.error(error, "Failed to load the release builder");
      })
      .finally(() => {
        if (generation.current === currentGeneration) setLoading(false);
      });
    return () => controller.abort();
  }, [loadAttempt, namespace.app, namespace.env, open, toast]);

  useEffect(() => {
    if (open) return;
    for (const load of metadataLoads.current.values()) load.controller.abort();
    metadataLoads.current.clear();
  }, [open]);

  const problems = useMemo(() => entryProblems(entries), [entries]);
  const releaseProblem = useMemo(
    () => builderError(name, schemaID, schemaVersion, entries, metadataJSON),
    [entries, metadataJSON, name, schemaID, schemaVersion],
  );
  const guidedProblem = releaseProblem ?? (problems.size > 0 ? "entries" : null);
  const jsonProblem = useMemo(() => releaseDefinitionError(jsonText), [jsonText]);

  const dirty =
    snapshot !== null &&
    !saving &&
    (mode === "json"
      ? jsonText !== prettyDefinitionFor(snapshot, namespace)
      : name !== snapshot.name ||
        schemaID !== snapshot.schemaID ||
        schemaVersion !== snapshot.schemaVersion ||
        entriesKey(entries) !== snapshot.entries ||
        metadataJSON !== snapshot.metadataJSON);

  function guidedRequest(): CreateReleaseRequest {
    return requestFromEntries(namespace, name, schemaID, schemaVersion, entries, metadataJSON);
  }

  function updateEntry(id: number, patch: Partial<BuilderEntry>) {
    setEntries((current) =>
      current.map((entry) => (entry.id === id ? { ...entry, ...patch } : entry)),
    );
  }

  async function loadSelectorMetadata(entry: BuilderEntry) {
    if (!entry.key) return;
    const previous = metadataLoads.current.get(entry.id);
    previous?.controller.abort();
    const mine = (previous?.generation ?? 0) + 1;
    const controller = new AbortController();
    metadataLoads.current.set(entry.id, { generation: mine, controller });
    const current = () => metadataLoads.current.get(entry.id)?.generation === mine;
    updateEntry(entry.id, { metadataLoading: true });
    try {
      const ref = { ...namespace, key: entry.key };
      const request = { signal: controller.signal };
      let labels: string[];
      let versions: number[];
      if (entry.kind === "parameter") {
        const metadata = await api.parameterMetadata(ref, request);
        labels = Object.keys(metadata.labels ?? {}).sort();
        versions = (metadata.versions ?? []).map((version) => version.version);
      } else {
        const response = await api.secretMetadata(ref, request);
        labels = Object.keys(response.secret.labels ?? {}).sort();
        versions = (response.secret.versions ?? [])
          .filter((version) => version.state === "enabled")
          .map((version) => version.version);
      }
      if (!current()) return;
      updateEntry(entry.id, { labels, versions, metadataLoading: false });
    } catch (error) {
      if (!current() || isAbortError(error)) return;
      updateEntry(entry.id, { metadataLoading: false });
      toast.error(error, `Failed to load versions for ${entry.key}`);
    }
  }

  function switchMode(next: string | number) {
    if (next === mode) return;
    setModeMessage("");
    if (next === "json") {
      const { namespace: _namespace, ...request } = guidedRequest();
      setJSONText(prettyDefinition(request));
      setMode("json");
      return;
    }
    if (jsonProblem) {
      setModeMessage("Fix the JSON definition before returning to Guided mode.");
      return;
    }
    const parsed = JSON.parse(jsonText) as Partial<CreateReleaseRequest>;
    const parsedEntries = parsed.entries ?? [];
    const crossNamespace = parsedEntries.some(
      (entry) =>
        entry.ref?.namespace &&
        (entry.ref.namespace.env !== namespace.env || entry.ref.namespace.app !== namespace.app),
    );
    if (crossNamespace) {
      setModeMessage("Cross-namespace references are supported in JSON mode only.");
      return;
    }
    const contract = new Map(
      (dashboard?.application.contract ?? []).map((field) => [field.alias, field]),
    );
    setName(parsed.name ?? "");
    setSchemaID(parsed.schema_id ?? "");
    setSchemaVersion(parsed.schema_id ? String(parsed.schema_version ?? "") : "");
    setMetadataJSON(parsed.metadata_json ?? "{}");
    setEntries(
      parsedEntries.map((entry) => {
        const field = contract.get(entry.alias);
        return {
          ...blankEntry(Boolean(field)),
          alias: entry.alias,
          kind: entry.kind === "secret" ? "secret" : "parameter",
          contentType: field?.content_type ?? "",
          key: entry.ref?.key ?? "",
          selector: entry.label ? "label" : entry.version ? "version" : "current",
          label: entry.label ?? "",
          version: entry.version ? String(entry.version) : "",
        };
      }),
    );
    setMode("guided");
  }

  async function createRelease() {
    setAttempted(true);
    entryErrors.markAllTouched();
    if ((mode === "guided" && guidedProblem) || (mode === "json" && jsonProblem)) {
      requestFocus();
      return;
    }
    const request: CreateReleaseRequest =
      mode === "guided" ? guidedRequest() : { ...parseReleaseDefinition(jsonText), namespace };
    setSaving(true);
    try {
      const result = await api.createRelease(request);
      toast.success(`Created ${result.release.name}@${result.release.version}`);
      onCreated(result.release);
      onClose();
    } catch (error) {
      toast.error(error, "Could not create release");
    } finally {
      setSaving(false);
    }
  }

  // Guided mode stays clickable so the click can reveal every problem inline;
  // JSON mode already shows its error unconditionally, so it keeps the disable.
  const createDisabled = loading || Boolean(loadError) || (mode === "json" && Boolean(jsonProblem));
  const schemaEditable = !dashboard?.application.schema_id;
  const entryCount = entries.length;
  const problemCount = problems.size;

  return (
    <Modal
      open={open}
      workspace
      dismissible={!saving}
      dirty={dirty}
      title={`New release · ${namespace.env}/${namespace.app}`}
      description="Releases are immutable: every create allocates the next version."
      onClose={onClose}
      initialFocus={schemaEditable ? schemaIDRef : firstResourceRef}
      footer={(close) => (
        <>
          <Button variant="outline" onClick={close} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={() => void createRelease()} disabled={createDisabled} loading={saving}>
            Create release
          </Button>
        </>
      )}
    >
      {loading ? (
        <div className="loading-block" role="status">
          <Spinner /> Loading application contract…
        </div>
      ) : loadError ? (
        <div className="danger-panel" role="alert">
          <div className="between">
            <div>
              <strong>Could not load the application contract</strong>
              <div className="text-sm mt-2">
                {loadError instanceof Error ? loadError.message : String(loadError)}
              </div>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setLoadAttempt((attempt) => attempt + 1)}
            >
              Retry
            </Button>
          </div>
        </div>
      ) : (
        <Tabs value={mode} onValueChange={switchMode}>
          <div className="release-workspace-toolbar">
            <TabsList aria-label="Release definition mode">
              <TabsTrigger value="guided">Guided</TabsTrigger>
              <TabsTrigger value="json">JSON</TabsTrigger>
            </TabsList>
            <div className="text-sm faint">
              {entryCount} {entryCount === 1 ? "entry" : "entries"} · selectors are resolved when
              the release is created
            </div>
          </div>

          {modeMessage ? (
            <div className="warn-panel mb-4" role="alert">
              {modeMessage}
            </div>
          ) : null}

          <TabsContent value="guided">
            <div ref={formRef}>
              <div className="release-builder-basics">
                <Field label="Release name" hint="Owned by the selected application.">
                  <Input className="font-mono" value={name} disabled />
                </Field>
                <Field label="Schema ID" hint="Leave both schema fields empty for no schema.">
                  <Input
                    ref={schemaIDRef}
                    className="font-mono"
                    value={schemaID}
                    disabled={!schemaEditable}
                    onChange={(event) => setSchemaID(event.target.value)}
                    placeholder="go-common/runtime"
                  />
                </Field>
                <Field label="Schema version">
                  <Input
                    className="font-mono"
                    inputMode="numeric"
                    value={schemaVersion}
                    disabled={!schemaEditable}
                    onChange={(event) => setSchemaVersion(event.target.value)}
                    placeholder="1"
                  />
                </Field>
              </div>

              <div className="between mb-3 mt-4">
                <div>
                  <h2 className="section-title">Release entries</h2>
                  <p className="text-sm faint">
                    Contract fields are fixed; choose which resource version supplies each alias.
                  </p>
                </div>
                {dashboard?.application.contract.length ? null : (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setEntries((current) => [...current, blankEntry(false)])}
                  >
                    <Plus size={15} aria-hidden />
                    Add entry
                  </Button>
                )}
              </div>

              <div className="release-builder-entries">
                {entries.map((entry, index) => {
                  const resources = (dashboard?.rows ?? [])
                    .filter(
                      (row) =>
                        row.kind === entry.kind &&
                        row.environments[namespace.env]?.present &&
                        (!entry.contentType ||
                          row.environments[namespace.env]?.content_type === entry.contentType),
                    )
                    .map((row) => ({ value: row.key, label: row.key }));
                  if (entry.key && !resources.some((resource) => resource.value === entry.key)) {
                    resources.unshift({ value: entry.key, label: entry.key });
                  }
                  const mine = problems.get(entry.id) ?? {};
                  const errorFor = (field: keyof EntryProblems) =>
                    entryErrors.shown(`${entry.id}-${field}`, mine[field]);
                  const touch = (field: keyof EntryProblems) =>
                    entryErrors.touch(`${entry.id}-${field}`);
                  return (
                    <div className="release-builder-entry" key={entry.id}>
                      <Field label="Alias" error={errorFor("alias")}>
                        <Input
                          className="font-mono"
                          value={entry.alias}
                          disabled={entry.contractOwned}
                          onChange={(event) => updateEntry(entry.id, { alias: event.target.value })}
                          onBlur={() => touch("alias")}
                        />
                      </Field>
                      <Field label="Kind">
                        <AppSelect
                          value={entry.kind}
                          disabled={entry.contractOwned}
                          onValueChange={(kind) =>
                            updateEntry(entry.id, {
                              kind: kind as ReleaseEntryKind,
                              key: "",
                              labels: [],
                              versions: [],
                            })
                          }
                          options={[
                            { value: "parameter", label: "Parameter" },
                            { value: "secret", label: "Secret" },
                          ]}
                        />
                      </Field>
                      <Field label="Resource" error={errorFor("key")}>
                        <AppSelect
                          ref={index === 0 ? firstResourceRef : undefined}
                          value={entry.key}
                          disabled={resources.length === 0}
                          onValueChange={(key) => {
                            updateEntry(entry.id, {
                              key,
                              label: "",
                              version: "",
                              labels: [],
                              versions: [],
                            });
                            if (entry.selector !== "current") {
                              void loadSelectorMetadata({
                                ...entry,
                                key,
                                label: "",
                                version: "",
                                labels: [],
                                versions: [],
                              });
                            }
                          }}
                          onBlur={() => touch("key")}
                          placeholder={
                            resources.length === 0
                              ? `No matching ${entry.kind}s`
                              : `Choose ${entry.kind}…`
                          }
                          options={resources}
                        />
                      </Field>
                      <Field label="Selector">
                        <AppSelect
                          value={entry.selector}
                          onValueChange={(selector) => {
                            const next = selector as SelectorKind;
                            updateEntry(entry.id, { selector: next, label: "", version: "" });
                            if (next !== "current") void loadSelectorMetadata(entry);
                          }}
                          options={[
                            { value: "current", label: "Current" },
                            { value: "label", label: "Label" },
                            { value: "version", label: "Exact version" },
                          ]}
                        />
                      </Field>
                      {entry.selector === "label" ? (
                        <Field label="Label" error={errorFor("label")}>
                          <AppSelect
                            value={entry.label}
                            onValueChange={(label) => updateEntry(entry.id, { label })}
                            onBlur={() => touch("label")}
                            disabled={entry.metadataLoading}
                            placeholder={entry.metadataLoading ? "Loading…" : "Choose label…"}
                            options={entry.labels.map((label) => ({ value: label, label }))}
                          />
                        </Field>
                      ) : entry.selector === "version" ? (
                        <Field label="Version" error={errorFor("version")}>
                          <AppSelect
                            value={entry.version}
                            onValueChange={(version) => updateEntry(entry.id, { version })}
                            onBlur={() => touch("version")}
                            disabled={entry.metadataLoading}
                            placeholder={entry.metadataLoading ? "Loading…" : "Choose version…"}
                            options={entry.versions.map((version) => ({
                              value: String(version),
                              label: `v${version}`,
                            }))}
                          />
                        </Field>
                      ) : (
                        <div className="release-builder-current faint text-sm">
                          Resolved at creation
                        </div>
                      )}
                      {entry.contractOwned ? null : (
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={`Remove ${entry.alias || "entry"}`}
                          onClick={() => {
                            metadataLoads.current.get(entry.id)?.controller.abort();
                            metadataLoads.current.delete(entry.id);
                            setEntries((current) => current.filter((item) => item.id !== entry.id));
                          }}
                        >
                          <Trash2 size={15} aria-hidden />
                        </Button>
                      )}
                    </div>
                  );
                })}
              </div>

              <details className="advanced-panel mt-4">
                <summary>Advanced release metadata</summary>
                <Field
                  label="Metadata JSON"
                  error={attempted ? validateMetadataJson(metadataJSON) : null}
                >
                  <JsonEditor
                    value={metadataJSON}
                    onChange={setMetadataJSON}
                    toolbar="minimal"
                    rows={4}
                    maxHeight="30vh"
                    onSubmit={() => void createRelease()}
                  />
                </Field>
              </details>

              {attempted && guidedProblem ? (
                <div className="danger-panel mt-4" role="alert">
                  {releaseProblem ??
                    `${problemCount} ${problemCount === 1 ? "entry needs" : "entries need"} attention.`}
                </div>
              ) : null}

              <div className="release-builder-review mt-4">
                <strong>Ready to create</strong>
                <span className="text-sm faint">
                  {name || "Unnamed release"} · {entryCount} immutable pins
                  {schemaID ? ` · ${schemaID}@${schemaVersion}` : " · no schema"}
                </span>
              </div>
            </div>
          </TabsContent>

          <TabsContent value="json">
            <Field
              label="Release definition"
              hint="JSON mode supports the complete API shape, including cross-namespace references."
              error={attempted || jsonProblem ? jsonProblem : null}
            >
              <JsonEditor
                value={jsonText}
                onChange={(text) => {
                  setJSONText(text);
                  setModeMessage("");
                }}
                rows={24}
                maxHeight="60dvh"
                onSubmit={() => void createRelease()}
              />
            </Field>
          </TabsContent>
        </Tabs>
      )}
    </Modal>
  );
}

/** The JSON tab's text for an untouched guided form, so switching tabs alone is not "dirty". */
function prettyDefinitionFor(snapshot: GuidedSnapshot, namespace: NamespaceRef): string {
  const entries = JSON.parse(snapshot.entries) as Array<
    [string, ReleaseEntryKind, string, SelectorKind, string, string]
  >;
  const { namespace: _namespace, ...request } = requestFromEntries(
    namespace,
    snapshot.name,
    snapshot.schemaID,
    snapshot.schemaVersion,
    entries.map(([alias, kind, key, selector, label, version]) => ({
      id: 0,
      alias,
      kind,
      contentType: "",
      key,
      selector,
      label,
      version,
      labels: [],
      versions: [],
      metadataLoading: false,
      contractOwned: false,
    })),
    snapshot.metadataJSON,
  );
  return prettyDefinition(request);
}
