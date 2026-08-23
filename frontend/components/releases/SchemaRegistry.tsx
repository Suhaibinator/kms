import { Plus, RefreshCw } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Modal } from "@/components/Modal";
import {
  Button,
  Checkbox,
  Field,
  Input,
  Pagination,
  Spinner,
  TableSkeleton,
  Textarea,
} from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useCursorPagination } from "@/lib/hooks";
import type { ConfigurationSchema } from "@/lib/types";

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
      title={schema ? `Schema ${schema.id}@${schema.version}` : "Schema"}
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
          <pre className={`schema-code ${wrap ? "schema-code-wrap" : ""}`}>{pretty}</pre>
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
  const [schemaID, setSchemaID] = useState("");
  const [schemaJSON, setSchemaJSON] = useState(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object"
}`);
  const [attempted, setAttempted] = useState(false);
  const [saving, setSaving] = useState(false);
  const schemaError = useMemo(() => schemaJSONError(schemaJSON), [schemaJSON]);

  useEffect(() => {
    if (!open) return;
    setSchemaID("");
    setAttempted(false);
  }, [open]);

  async function register() {
    setAttempted(true);
    if (!schemaID.trim() || schemaError) return;
    setSaving(true);
    try {
      const result = await api.createSchema(schemaID.trim(), schemaJSON);
      toast.success(`Created schema ${result.schema.id}@${result.schema.version}`);
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
      title="Register JSON Schema"
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            disabled={saving || !schemaID.trim() || Boolean(schemaError)}
            onClick={() => void register()}
          >
            {saving ? <Spinner /> : null}
            Register immutable version
          </Button>
        </>
      }
    >
      <Field
        label="Schema ID"
        hint="Each successful registration allocates the next immutable version."
        error={attempted && !schemaID.trim() ? "Schema ID is required." : null}
      >
        <Input
          className="font-mono"
          value={schemaID}
          onChange={(event) => setSchemaID(event.target.value)}
          placeholder="go-common/runtime"
        />
      </Field>
      <Field label="JSON Schema definition" error={attempted || schemaError ? schemaError : null}>
        <Textarea
          className="schema-editor font-mono"
          value={schemaJSON}
          onChange={(event) => setSchemaJSON(event.target.value)}
          spellCheck={false}
        />
      </Field>
    </Modal>
  );
}

export function SchemaRegistry() {
  const toast = useToast();
  const [schemas, setSchemas] = useState<ConfigurationSchema[]>([]);
  const [loading, setLoading] = useState(true);
  const [filterDraft, setFilterDraft] = useState("");
  const [filter, setFilter] = useState("");
  const [registerOpen, setRegisterOpen] = useState(false);
  const [selectedSchema, setSelectedSchema] = useState<ConfigurationSchema | null>(null);
  const paging = useCursorPagination(filter);
  const controllerRef = useRef<AbortController | null>(null);
  const generation = useRef(0);

  const loadSchemas = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    const currentGeneration = ++generation.current;
    setLoading(true);
    try {
      const page = await api.listSchemas(filter || undefined, paging.pageToken || undefined, {
        signal: controller.signal,
      });
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
  }, [filter, paging.pageToken, paging.setNextToken, toast]);

  useEffect(() => {
    void loadSchemas();
    return () => controllerRef.current?.abort();
  }, [loadSchemas]);

  function applyFilter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFilter(filterDraft.trim());
  }

  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Schema registry</h2>
          <p className="text-sm faint">Global, immutable JSON Schema versions used by releases.</p>
        </div>
        <div className="row-wrap">
          <Button variant="outline" disabled={loading} onClick={() => void loadSchemas()}>
            {loading ? <Spinner /> : <RefreshCw size={16} />}
            Refresh
          </Button>
          <Button onClick={() => setRegisterOpen(true)}>
            <Plus size={16} /> Register schema
          </Button>
        </div>
      </div>

      <form className="filters schema-filters" onSubmit={applyFilter}>
        <Field label="Exact schema ID">
          <Input
            className="font-mono"
            value={filterDraft}
            onChange={(event) => setFilterDraft(event.target.value)}
            placeholder="go-common/runtime"
          />
        </Field>
        <Button variant="outline" type="submit" disabled={loading || filterDraft.trim() === filter}>
          Apply filter
        </Button>
        {filter ? (
          <Button
            variant="ghost"
            type="button"
            onClick={() => {
              setFilterDraft("");
              setFilter("");
            }}
          >
            Clear
          </Button>
        ) : null}
      </form>

      {loading ? (
        <TableSkeleton headers={["Schema", "Digest", "Created by", "Created", ""]} rows={4} />
      ) : schemas.length === 0 ? (
        <div className="empty-state card">
          <div className="empty-title">No schemas found</div>
          <div className="text-sm">Register a schema or clear the exact-ID filter.</div>
        </div>
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
                <tr key={`${schema.id}@${schema.version}`}>
                  <td className="mono" data-label="Schema">
                    {schema.id}@{schema.version}
                  </td>
                  <td className="mono" data-label="Digest">
                    {schema.digest.slice(0, 16)}…
                  </td>
                  <td className="mono" data-label="Created by">
                    {schema.created_by || "—"}
                  </td>
                  <td data-label="Created">{formatUnixMs(schema.created_at_unix_ms)}</td>
                  <td className="row-actions" data-label="Actions">
                    <Button variant="outline" size="sm" onClick={() => setSelectedSchema(schema)}>
                      View
                    </Button>
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
          if (paging.page === 1 && (!filter || filter === schema.id)) void loadSchemas();
          else paging.reset();
        }}
      />
      {selectedSchema ? (
        <SchemaViewer schema={selectedSchema} onClose={() => setSelectedSchema(null)} />
      ) : null}
    </>
  );
}
