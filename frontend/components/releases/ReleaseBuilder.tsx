import { Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Modal } from "@/components/Modal";
import { Button, Field, Input, Spinner, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import type {
  ApplicationDashboard,
  ConfigurationRelease,
  CreateReleaseRequest,
  NamespaceRef,
  ReleaseEntryKind,
} from "@/lib/types";
import { validateAlias, validateMetadataJson, validateReleaseName } from "@/lib/validation";
import { releaseDefinitionError } from "./utils";

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

function builderError(
  name: string,
  schemaID: string,
  schemaVersion: string,
  entries: BuilderEntry[],
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
  const aliases = new Set<string>();
  for (const entry of entries) {
    const aliasError = validateAlias(entry.alias.trim());
    if (aliasError) return aliasError;
    if (aliases.has(entry.alias.trim())) return `Duplicate release alias: ${entry.alias.trim()}`;
    aliases.add(entry.alias.trim());
    if (!entry.key.trim()) return `Choose a resource for ${entry.alias.trim() || "each entry"}.`;
    if (entry.selector === "label" && !entry.label) {
      return `Choose a label for ${entry.alias.trim()}.`;
    }
    if (
      entry.selector === "version" &&
      (!Number.isInteger(Number(entry.version)) || Number(entry.version) < 1)
    ) {
      return `Choose a valid version for ${entry.alias.trim()}.`;
    }
  }
  return null;
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
  const generation = useRef(0);

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    const currentGeneration = ++generation.current;
    setLoading(true);
    setAttempted(false);
    setMode("guided");
    setModeMessage("");
    void api
      .applicationDashboard(namespace.app, { signal: controller.signal })
      .then((result) => {
        if (generation.current !== currentGeneration) return;
        setDashboard(result);
        setName(result.application.release_name || "runtime");
        setSchemaID(result.application.schema_id || "");
        setSchemaVersion(
          result.application.schema_id ? String(result.application.schema_version) : "",
        );
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
        setEntries(nextEntries.length ? nextEntries : [blankEntry(false)]);
        setMetadataJSON("{}");
      })
      .catch((error) => {
        if (!isAbortError(error)) toast.error(error, "Failed to load the release builder");
      })
      .finally(() => {
        if (generation.current === currentGeneration) setLoading(false);
      });
    return () => controller.abort();
  }, [namespace.app, namespace.env, open, toast]);

  const guidedProblem = useMemo(
    () => builderError(name, schemaID, schemaVersion, entries, metadataJSON),
    [entries, metadataJSON, name, schemaID, schemaVersion],
  );
  const jsonProblem = useMemo(() => releaseDefinitionError(jsonText), [jsonText]);

  function guidedRequest(): CreateReleaseRequest {
    return requestFromEntries(namespace, name, schemaID, schemaVersion, entries, metadataJSON);
  }

  function updateEntry(id: number, patch: Partial<BuilderEntry>) {
    setEntries((current) =>
      current.map((entry) => (entry.id === id ? { ...entry, ...patch } : entry)),
    );
  }

  async function loadSelectorMetadata(entry: BuilderEntry) {
    if (!entry.key || entry.metadataLoading) return;
    updateEntry(entry.id, { metadataLoading: true });
    try {
      if (entry.kind === "parameter") {
        const metadata = await api.parameterMetadata({ ...namespace, key: entry.key });
        updateEntry(entry.id, {
          labels: Object.keys(metadata.labels ?? {}).sort(),
          versions: (metadata.versions ?? []).map((version) => version.version),
          metadataLoading: false,
        });
      } else {
        const response = await api.secretMetadata({ ...namespace, key: entry.key });
        updateEntry(entry.id, {
          labels: Object.keys(response.secret.labels ?? {}).sort(),
          versions: (response.secret.versions ?? [])
            .filter((version) => version.state === "enabled")
            .map((version) => version.version),
          metadataLoading: false,
        });
      }
    } catch (error) {
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
          kind: entry.kind,
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
    if (mode === "guided" && guidedProblem) return;
    if (mode === "json" && jsonProblem) return;
    let request: CreateReleaseRequest;
    if (mode === "guided") request = guidedRequest();
    else {
      const parsed = JSON.parse(jsonText) as CreateReleaseRequest;
      request = {
        ...parsed,
        namespace,
        metadata_json: parsed.metadata_json ?? "{}",
      };
    }
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

  const blockingProblem = mode === "guided" ? guidedProblem : jsonProblem;

  return (
    <Modal
      open={open}
      workspace
      dismissible={!saving}
      title={`New release · ${namespace.env}/${namespace.app}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            onClick={() => void createRelease()}
            disabled={saving || Boolean(blockingProblem)}
          >
            {saving ? <Spinner /> : null}
            Create immutable version
          </Button>
        </>
      }
    >
      {loading ? (
        <div className="loading-block" role="status">
          <Spinner /> Loading application contract…
        </div>
      ) : (
        <Tabs value={mode} onValueChange={switchMode}>
          <div className="release-workspace-toolbar">
            <TabsList aria-label="Release definition mode">
              <TabsTrigger value="guided">Guided</TabsTrigger>
              <TabsTrigger value="json">JSON</TabsTrigger>
            </TabsList>
            <div className="text-sm faint">
              {entries.length} {entries.length === 1 ? "entry" : "entries"} · selectors are resolved
              when the release is created
            </div>
          </div>

          {modeMessage ? (
            <div className="warn-panel mb-16" role="alert">
              {modeMessage}
            </div>
          ) : null}

          <TabsContent value="guided">
            <div className="release-builder-basics">
              <Field label="Release name" hint="Owned by the selected application.">
                <Input className="font-mono" value={name} disabled />
              </Field>
              <Field label="Schema ID" hint="Leave both schema fields empty for no schema.">
                <Input
                  className="font-mono"
                  value={schemaID}
                  disabled={Boolean(dashboard?.application.schema_id)}
                  onChange={(event) => setSchemaID(event.target.value)}
                  placeholder="go-common/runtime"
                />
              </Field>
              <Field label="Schema version">
                <Input
                  className="font-mono"
                  inputMode="numeric"
                  value={schemaVersion}
                  disabled={Boolean(dashboard?.application.schema_id)}
                  onChange={(event) => setSchemaVersion(event.target.value)}
                  placeholder="1"
                />
              </Field>
            </div>

            <div className="between mb-12 mt-16">
              <div>
                <h2>Release entries</h2>
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
                  <Plus size={15} /> Add entry
                </Button>
              )}
            </div>

            <div className="release-builder-entries">
              {entries.map((entry) => {
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
                return (
                  <div className="release-builder-entry" key={entry.id}>
                    <Field label="Alias">
                      <Input
                        className="font-mono"
                        value={entry.alias}
                        disabled={entry.contractOwned}
                        onChange={(event) => updateEntry(entry.id, { alias: event.target.value })}
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
                    <Field label="Resource">
                      <AppSelect
                        value={entry.key}
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
                        placeholder={`Choose ${entry.kind}…`}
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
                      <Field label="Label">
                        <AppSelect
                          value={entry.label}
                          onValueChange={(label) => updateEntry(entry.id, { label })}
                          disabled={entry.metadataLoading}
                          placeholder={entry.metadataLoading ? "Loading…" : "Choose label…"}
                          options={entry.labels.map((label) => ({ value: label, label }))}
                        />
                      </Field>
                    ) : entry.selector === "version" ? (
                      <Field label="Version">
                        <AppSelect
                          value={entry.version}
                          onValueChange={(version) => updateEntry(entry.id, { version })}
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
                        size="icon-sm"
                        aria-label={`Remove ${entry.alias || "entry"}`}
                        onClick={() =>
                          setEntries((current) => current.filter((item) => item.id !== entry.id))
                        }
                      >
                        <Trash2 size={15} />
                      </Button>
                    )}
                  </div>
                );
              })}
            </div>

            <details className="advanced-panel mt-16">
              <summary>Advanced release metadata</summary>
              <Field
                label="Metadata JSON"
                error={attempted ? validateMetadataJson(metadataJSON) : null}
              >
                <Textarea
                  className="font-mono"
                  rows={4}
                  value={metadataJSON}
                  onChange={(event) => setMetadataJSON(event.target.value)}
                  spellCheck={false}
                />
              </Field>
            </details>

            {attempted && guidedProblem ? (
              <div className="danger-panel mt-16" role="alert">
                {guidedProblem}
              </div>
            ) : null}

            <div className="release-builder-review mt-16">
              <strong>Ready to create</strong>
              <span className="text-sm faint">
                {name || "Unnamed release"} · {entries.length} immutable pins
                {schemaID ? ` · ${schemaID}@${schemaVersion}` : " · no schema"}
              </span>
            </div>
          </TabsContent>

          <TabsContent value="json">
            <Field
              label="Release definition"
              hint="JSON mode supports the complete API shape, including cross-namespace references."
              error={attempted || jsonProblem ? jsonProblem : null}
            >
              <Textarea
                className="release-json-editor font-mono"
                value={jsonText}
                onChange={(event) => {
                  setJSONText(event.target.value);
                  setModeMessage("");
                }}
                spellCheck={false}
              />
            </Field>
          </TabsContent>
        </Tabs>
      )}
    </Modal>
  );
}
