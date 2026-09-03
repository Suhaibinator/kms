import { Plus, RefreshCw } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Icon } from "@/components/icons";
import { JsonEditor } from "@/components/JsonEditor";
import { JsonHighlight } from "@/components/JsonHighlight";
import { Modal } from "@/components/Modal";
import {
  Button,
  Checkbox,
  EmptyState,
  Field,
  Input,
  Pagination,
  TableSkeleton,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useCursorPagination, useFieldErrors } from "@/lib/hooks";
import type { Application, ConfigurationSchema } from "@/lib/types";

function schemaLabel(schema: ConfigurationSchema): string {
  return `${schema.application}/${schema.release_name}@${schema.version}`;
}

const DEFAULT_SCHEMA_JSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object"
}`;

function schemaJSONError(value: string): string | null {
  try {
    JSON.parse(value);
    return null;
  } catch (error) {
    return `Schema must be valid JSON: ${error instanceof Error ? error.message : String(error)}`;
  }
}

function prettySchema(value: string): string {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}

function SchemaViewer({
  schema,
  onClose,
}: {
  schema: ConfigurationSchema | null;
  onClose: () => void;
}) {
  const [wrap, setWrap] = useState(true);
  const pretty = useMemo(() => (schema ? prettySchema(schema.schema_json) : ""), [schema]);

  return (
    <Modal
      open={Boolean(schema)}
      workspace
      title={schema ? `Schema ${schemaLabel(schema)}` : "Schema"}
      onClose={onClose}
    >
      {schema ? (
        <div className="schema-viewer">
          <div className="schema-viewer-meta">
            <dl className="kv">
              <dt>Created</dt>
              <dd>{formatUnixMs(schema.created_at_unix_ms)}</dd>
              <dt>Created by</dt>
              <dd className="mono">{schema.created_by || "—"}</dd>
              <dt>Digest</dt>
              <dd className="mono">{schema.digest}</dd>
              <dt>Metadata</dt>
              <dd className="mono">{schema.metadata_json || "{}"}</dd>
            </dl>
            <div className="row-wrap">
              <label className="row-wrap schema-wrap-toggle" htmlFor="schema-wrap-lines">
                <Checkbox id="schema-wrap-lines" checked={wrap} onCheckedChange={setWrap} /> Wrap
                lines
              </label>
              <CopyButton value={pretty} label="Copy JSON" />
              <CopyButton value={schema.digest} label="Copy digest" />
            </div>
          </div>
          <pre className={`schema-code ${wrap ? "schema-code-wrap" : ""}`}>
            <JsonHighlight text={pretty} lineNumbers />
          </pre>
        </div>
      ) : null}
    </Modal>
  );
}

function RegisterSchemaDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (schema: ConfigurationSchema) => void;
}) {
  const toast = useToast();
  const [applications, setApplications] = useState<Application[]>([]);
  const [application, setApplication] = useState("");
  const [loadingApplications, setLoadingApplications] = useState(false);
  const [schemaJSON, setSchemaJSON] = useState(DEFAULT_SCHEMA_JSON);
  const [attempted, setAttempted] = useState(false);
  const [saving, setSaving] = useState(false);
  const applicationErrors = useFieldErrors<"application">();
  const { formRef, requestFocus } = useFocusFirstInvalid<HTMLDivElement>();
  const schemaError = useMemo(() => schemaJSONError(schemaJSON), [schemaJSON]);
  const applicationProblem = application ? null : "Application is required.";
  const selectedApplication = applications.find((candidate) => candidate.name === application);
  const dirty = application !== "" || schemaJSON !== DEFAULT_SCHEMA_JSON;

  const resetApplicationErrors = applicationErrors.reset;
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setApplication("");
    setSchemaJSON(DEFAULT_SCHEMA_JSON);
    setAttempted(false);
    resetApplicationErrors();
    setLoadingApplications(true);
    const controller = new AbortController();
    const loadApplications = async () => {
      const loaded: Application[] = [];
      const seenTokens = new Set<string>();
      let pageToken = "";
      do {
        const response = await api.listApplications(200, pageToken || undefined, {
          signal: controller.signal,
        });
        loaded.push(...(response.applications ?? []));
        pageToken = response.next_page_token ?? "";
        if (pageToken && seenTokens.has(pageToken)) {
          throw new Error("Application pagination returned the same page token twice.");
        }
        if (pageToken) seenTokens.add(pageToken);
      } while (pageToken);
      return loaded;
    };
    void loadApplications()
      .then((loaded) => {
        if (!cancelled) setApplications(loaded);
      })
      .catch((error: unknown) => {
        if (cancelled || isAbortError(error)) return;
        setApplications([]);
        toast.error(error, "Could not load applications");
      })
      .finally(() => {
        if (!cancelled) setLoadingApplications(false);
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [open, resetApplicationErrors, toast]);

  async function register() {
    setAttempted(true);
    applicationErrors.markAllTouched();
    if (applicationProblem || schemaError) {
      requestFocus();
      return;
    }
    setSaving(true);
    try {
      const result = await api.createSchema(application, schemaJSON);
      toast.success(`Created schema ${schemaLabel(result.schema)}`);
      onCreated(result.schema);
      onClose();
    } catch (error) {
      toast.error(error, "Could not register schema");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={open}
      workspace
      dismissible={!saving}
      dirty={dirty}
      title="Register JSON Schema"
      description="Each registration allocates the next immutable version in the application's release stream."
      onClose={onClose}
      footer={(close) => (
        <>
          <Button variant="outline" onClick={close} disabled={saving}>
            Cancel
          </Button>
          <Button loading={saving} onClick={() => void register()}>
            Register schema
          </Button>
        </>
      )}
    >
      <div ref={formRef}>
        <Field
          label="Application"
          hint={
            selectedApplication
              ? `Registers under ${selectedApplication.name}/${selectedApplication.release_name}`
              : "The application's canonical release name is used automatically."
          }
          error={applicationErrors.shown("application", applicationProblem)}
        >
          <AppSelect
            className="font-mono"
            value={application}
            onValueChange={setApplication}
            onBlur={() => applicationErrors.touch("application")}
            options={applications.map((candidate) => ({
              value: candidate.name,
              label: `${candidate.name} / ${candidate.release_name}`,
            }))}
            placeholder={loadingApplications ? "Loading applications…" : "Select application…"}
            disabled={loadingApplications || applications.length === 0}
          />
        </Field>
        <Field label="JSON Schema definition" error={attempted || schemaError ? schemaError : null}>
          <JsonEditor
            value={schemaJSON}
            onChange={setSchemaJSON}
            rows={24}
            maxHeight="60dvh"
            onSubmit={() => void register()}
          />
        </Field>
      </div>
    </Modal>
  );
}

export function SchemaRegistry() {
  const toast = useToast();
  const [schemas, setSchemas] = useState<ConfigurationSchema[]>([]);
  const [loading, setLoading] = useState(true);
  const [applicationDraft, setApplicationDraft] = useState("");
  const [releaseDraft, setReleaseDraft] = useState("");
  const [applicationFilter, setApplicationFilter] = useState("");
  const [releaseFilter, setReleaseFilter] = useState("");
  const [registerOpen, setRegisterOpen] = useState(false);
  const [selectedSchema, setSelectedSchema] = useState<ConfigurationSchema | null>(null);
  const paging = useCursorPagination(`${applicationFilter}/${releaseFilter}`);
  const controllerRef = useRef<AbortController | null>(null);
  const generation = useRef(0);

  const loadSchemas = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    const currentGeneration = ++generation.current;
    setLoading(true);
    try {
      const page = await api.listSchemas(
        applicationFilter || undefined,
        applicationFilter ? releaseFilter || undefined : undefined,
        paging.pageToken || undefined,
        { signal: controller.signal },
      );
      if (generation.current !== currentGeneration) return;
      setSchemas(page.schemas ?? []);
      paging.setNextToken(page.next_page_token ?? "");
    } catch (error) {
      if (generation.current === currentGeneration && !isAbortError(error)) {
        toast.error(error, "Failed to load schemas");
      }
    } finally {
      if (generation.current === currentGeneration) {
        controllerRef.current = null;
        setLoading(false);
      }
    }
  }, [applicationFilter, releaseFilter, paging.pageToken, paging.setNextToken, toast]);

  useEffect(() => {
    void loadSchemas();
    return () => controllerRef.current?.abort();
  }, [loadSchemas]);

  function applyFilter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const application = applicationDraft.trim();
    setApplicationFilter(application);
    setReleaseFilter(application ? releaseDraft.trim() : "");
  }

  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2 className="section-title">Schema registry</h2>
          <p className="text-sm faint">
            Immutable JSON Schema versions owned by application release streams.
          </p>
        </div>
        <div className="row-wrap">
          <Button variant="outline" loading={loading} onClick={() => void loadSchemas()}>
            {loading ? null : <RefreshCw size={16} aria-hidden />}
            Refresh
          </Button>
          <Button onClick={() => setRegisterOpen(true)}>
            <Plus size={16} aria-hidden />
            Register schema
          </Button>
        </div>
      </div>

      <form className="filters schema-filters" onSubmit={applyFilter}>
        <Field label="Application">
          <Input
            className="font-mono"
            value={applicationDraft}
            onChange={(event) => {
              const value = event.target.value;
              setApplicationDraft(value);
              if (!value.trim()) setReleaseDraft("");
            }}
            placeholder="go-common"
          />
        </Field>
        <Field
          label="Release name"
          hint={applicationDraft.trim() ? undefined : "Choose an application first."}
        >
          <Input
            className="font-mono"
            value={releaseDraft}
            onChange={(event) => setReleaseDraft(event.target.value)}
            placeholder="runtime"
            disabled={!applicationDraft.trim()}
          />
        </Field>
        <Button
          variant="outline"
          type="submit"
          disabled={
            loading ||
            (applicationDraft.trim() === applicationFilter && releaseDraft.trim() === releaseFilter)
          }
        >
          Apply filter
        </Button>
        {applicationFilter || releaseFilter ? (
          <Button
            variant="ghost"
            type="button"
            onClick={() => {
              setApplicationDraft("");
              setReleaseDraft("");
              setApplicationFilter("");
              setReleaseFilter("");
            }}
          >
            Clear
          </Button>
        ) : null}
      </form>

      {loading ? (
        <TableSkeleton headers={["Schema", "Digest", "Created by", "Created", ""]} rows={4} />
      ) : schemas.length === 0 ? (
        <EmptyState
          icon={<Icon.release size={20} />}
          title="No schemas found"
          actions={<Button onClick={() => setRegisterOpen(true)}>Register schema</Button>}
        >
          Register a schema or clear the application/release filters.
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Schema</th>
                <th>Digest</th>
                <th>Created by</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {schemas.map((schema) => (
                <tr key={schemaLabel(schema)}>
                  <td className="mono" data-label="Schema">
                    {schemaLabel(schema)}
                  </td>
                  <td className="mono" data-label="Digest">
                    {schema.digest.slice(0, 16)}…
                  </td>
                  <td className="mono" data-label="Created by">
                    {schema.created_by || "—"}
                  </td>
                  <td data-label="Created">{formatUnixMs(schema.created_at_unix_ms)}</td>
                  <td data-label="Actions">
                    <div className="row-actions">
                      <Button variant="outline" size="sm" onClick={() => setSelectedSchema(schema)}>
                        View
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {!loading ? (
        <Pagination
          hasNext={paging.hasNext}
          onNext={paging.next}
          hasPrevious={paging.hasPrevious}
          onPrevious={paging.previous}
          onReset={paging.reset}
          showReset={paging.hasPrevious}
          page={paging.page}
        />
      ) : null}

      <RegisterSchemaDialog
        open={registerOpen}
        onClose={() => setRegisterOpen(false)}
        onCreated={(schema) => {
          setSelectedSchema(schema);
          // Retarget filters rather than leave the new registration invisible.
          if (
            (applicationFilter && applicationFilter !== schema.application) ||
            (releaseFilter && releaseFilter !== schema.release_name)
          ) {
            setApplicationDraft(schema.application);
            setReleaseDraft(schema.release_name);
            setApplicationFilter(schema.application);
            setReleaseFilter(schema.release_name);
          } else if (paging.page === 1) void loadSchemas();
          else paging.reset();
        }}
      />
      <SchemaViewer schema={selectedSchema} onClose={() => setSelectedSchema(null)} />
    </>
  );
}
