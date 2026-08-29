import { Eye, Filter, Trash2, X } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Icon } from "@/components/icons";
import { JsonEditor } from "@/components/JsonEditor";
import { ConfirmDialog, Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { ParameterValueInput } from "@/components/ParameterValueInput";
import {
  Badge,
  EmptyState,
  Field,
  Input,
  PageHeader,
  Pagination,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs, labelEntries } from "@/lib/format";
import {
  useCursorPagination,
  useFieldErrors,
  useLatestRequest,
  useNamespaces,
  useQueryParams,
} from "@/lib/hooks";
import { canonicalParameterValue } from "@/lib/json-text";
import { links } from "@/lib/links";
import { PARAMETER_CONTENT_TYPES, type Parameter } from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { useParameterSchema } from "@/lib/useParameterSchema";
import {
  firstError,
  validateContentType,
  validateKey,
  validateKeyPrefix,
  validateMetadataJson,
  validateParameterValue,
  validateValueSize,
} from "@/lib/validation";

const NO_NS: NamespaceSelection = { env: "", app: "" };

/** The fields of the new-parameter form that carry their own validation. */
type CreateField = "key" | "value" | "contentType" | "metadata";

/** Identifies the list a response belongs to, so a stale one cannot mark a
 *  different namespace/prefix/page as loaded. */
function requestScope(selection: NamespaceSelection, prefix: string, token: string): string {
  return JSON.stringify([selection.env, selection.app, prefix, token]);
}

export default function ParametersPage() {
  const toast = useToast();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams(["env", "app", "key_prefix"]);
  const replaceQuery = useQueryReplace("/parameters");

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  const [prefixInput, setPrefixInput] = useState("");
  const [prefixTouched, setPrefixTouched] = useState(false);
  const [prefix, setPrefix] = useState("");

  const [rows, setRows] = useState<Parameter[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedScope, setLoadedScope] = useState("");
  const request = useLatestRequest();

  const paging = useCursorPagination(JSON.stringify([ns.env, ns.app, prefix]));
  const { pageToken, setNextToken } = paging;

  const [createOpen, setCreateOpen] = useState(false);
  const [createNs, setCreateNs] = useState<NamespaceSelection>(NO_NS);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("string");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [saving, setSaving] = useState(false);
  const errors = useFieldErrors<CreateField>();
  // A json value in a namespace with a pinned schema can be edited by field.
  const createSchema = useParameterSchema({
    env: createNs.env,
    app: createNs.app,
    key: key.trim(),
    enabled: createOpen && contentType === "json" && !!createNs.env && !!createNs.app,
  });

  const [deleteTarget, setDeleteTarget] = useState<Parameter | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Seed the selection from deep-link query params exactly once.
  const [seeded, setSeeded] = useState(false);
  useEffect(() => {
    if (!queryReady || seeded) return;
    setSeeded(true);
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    const kp = queryValues.key_prefix ?? "";
    if (env || app) setNs({ env, app });
    if (kp) {
      setPrefixInput(kp);
      setPrefix(kp);
    }
  }, [queryReady, queryValues, seeded]);

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load environments");
  }, [nsError, toast]);

  const hasNs = !!ns.env && !!ns.app;

  // Client-side mirrors of the server's validators (see lib/validation.ts).
  // They fail fast on input the API is certain to reject; the server still has
  // the last word, and its errors keep arriving as toasts.
  const prefixError = validateKeyPrefix(prefixInput.trim());
  const keyError = validateKey(key.trim());
  const contentTypeError = validateContentType(contentType);
  const metadataError = validateMetadataJson(metadataJson);
  // Re-runs when the content type changes — "1.5" is a valid float but not a
  // valid integer. Memoised because a value may run to a megabyte.
  const valueError = useMemo(
    () => firstError(validateValueSize(value), validateParameterValue(value, contentType)),
    [value, contentType],
  );

  // A message stays hidden until the user has left the field or tried to
  // submit, so a freshly opened form is never already covered in errors.
  const shownPrefixError = prefixTouched ? prefixError : null;
  const shownKeyError = errors.shown("key", keyError);
  const shownValueError = errors.shown("value", valueError);
  const shownContentTypeError = errors.shown("contentType", contentTypeError);
  const shownMetadataError = errors.shown("metadata", metadataError);
  // The namespace selects have no blur of their own, so they surface on submit.
  const shownCreateAppError = errors.submitted && !createNs.app ? "Choose an application." : null;
  const shownCreateEnvError = errors.submitted && !createNs.env ? "Choose an environment." : null;
  const createError = firstError(keyError, valueError, contentTypeError, metadataError);
  const shownCreateError = firstError(
    shownKeyError,
    shownValueError,
    shownContentTypeError,
    shownMetadataError,
    shownCreateAppError,
    shownCreateEnvError,
  );

  const load = useCallback(
    async (
      token: string,
      selection: NamespaceSelection,
      activePrefix: string,
    ): Promise<Parameter[] | null> => {
      const run = request.begin();
      const scope = requestScope(selection, activePrefix, token);
      if (!selection.env || !selection.app) {
        setRows([]);
        setNextToken("");
        setLoading(false);
        setLoadedScope(scope);
        return [];
      }
      setLoading(true);
      try {
        const res = await api.listParameters(
          { env: selection.env, app: selection.app },
          activePrefix || undefined,
          100,
          token || undefined,
          { signal: run.signal },
        );
        if (!run.current) return null;
        const loaded = res.parameters ?? [];
        setRows(loaded);
        setNextToken(res.next_page_token ?? "");
        setLoadedScope(scope);
        return loaded;
      } catch (err) {
        if (!run.current || isAbortError(err)) return null;
        setRows([]);
        setNextToken("");
        setLoadedScope(scope);
        toast.error(err, "Failed to load parameters");
        return null;
      } finally {
        if (run.current) setLoading(false);
      }
    },
    [request, setNextToken, toast],
  );

  useEffect(() => {
    void load(pageToken, ns, prefix);
  }, [load, pageToken, ns, prefix]);

  function onSelectNamespace(next: NamespaceSelection) {
    setNs(next);
    setRows([]);
    setDeleteTarget(null);
    replaceQuery({ env: next.env, app: next.app });
  }
  function applyFilter(e: React.FormEvent) {
    e.preventDefault();
    setPrefixTouched(true);
    if (prefixError) return;
    const next = prefixInput.trim();
    setRows([]);
    setDeleteTarget(null);
    setPrefix(next);
    replaceQuery({ key_prefix: next });
  }
  function clearFilter() {
    setPrefixInput("");
    setPrefixTouched(false);
    setRows([]);
    setDeleteTarget(null);
    setPrefix("");
    replaceQuery({ key_prefix: "" });
  }

  function openCreate() {
    setCreateNs(hasNs ? ns : NO_NS);
    setKey("");
    setValue("");
    setContentType("string");
    setMetadataJson("{}");
    errors.reset();
    setCreateOpen(true);
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    errors.markAllTouched();
    // Every problem now has an inline message beside the field that caused it.
    if (!createNs.env || !createNs.app || createError) return;
    const k = key.trim();
    setSaving(true);
    try {
      const res = await api.putParameter({
        env: createNs.env,
        app: createNs.app,
        key: k,
        value: canonicalParameterValue(value, contentType),
        content_type: contentType || "string",
        metadata_json: metadataJson.trim() || "{}",
      });
      toast.success(
        `Parameter saved (version ${res.version})`,
        `${createNs.env}/${createNs.app}/${k}`,
      );
      setCreateOpen(false);
      // If the new parameter lands in the currently viewed namespace, refresh.
      if (createNs.env === ns.env && createNs.app === ns.app) {
        paging.reset();
        await load("", ns, prefix);
      }
    } catch (err) {
      toast.error(err, "Failed to save parameter");
    } finally {
      setSaving(false);
    }
  }

  async function onDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.deleteParameter({
        env: deleteTarget.env,
        app: deleteTarget.app,
        key: deleteTarget.key,
      });
      toast.success("Parameter deleted", deleteTarget.key);
      setDeleteTarget(null);
      // Deleting the last row of page N would otherwise strand the operator on
      // an empty page with no way forward.
      const remaining = await load(pageToken, ns, prefix);
      if (remaining !== null && remaining.length === 0 && paging.hasPrevious) paging.previous();
    } catch (err) {
      toast.error(err, "Failed to delete parameter");
    } finally {
      setDeleting(false);
    }
  }

  // A deep link's env/app land one frame after mount, so "Choose an
  // environment" would flash before the list it asked for.
  const awaitingDeepLink = !seeded && (!queryReady || !!queryValues.env || !!queryValues.app);
  // A response has arrived for exactly this namespace/prefix/page. Gating on
  // this rather than on `loading` keeps the empty state from flashing before
  // the first request has even started.
  const settled = loadedScope === requestScope(ns, prefix, pageToken);

  return (
    <>
      <PageHeader
        title="Parameters"
        subtitle="Non-secret configuration values, isolated by application and environment."
        actions={<Button onClick={openCreate}>New parameter</Button>}
      />

      <form className="filters" onSubmit={applyFilter}>
        {/* The create modal mounts a second picker, so both need their own ids
            or a <label for> resolves to whichever control rendered first. */}
        <NamespacePicker
          namespaces={namespaces}
          value={ns}
          onChange={onSelectNamespace}
          appId="filter-app"
          envId="filter-env"
        />
        <div className="filter-grow">
          {/* Keep this toolbar compact; the prefix rule rides on the placeholder
              and validation message instead of a permanently visible hint. */}
          <Field label="Key prefix" error={shownPrefixError}>
            <Input
              id="key-prefix"
              className="font-mono"
              placeholder="billing"
              value={prefixInput}
              disabled={!hasNs}
              onChange={(e) => setPrefixInput(e.target.value)}
              onBlur={() => setPrefixTouched(true)}
            />
          </Field>
        </div>
        <Button type="submit" variant="outline" disabled={!hasNs || shownPrefixError !== null}>
          <Filter size={15} aria-hidden />
          Filter
        </Button>
        <Button type="button" variant="ghost" onClick={clearFilter} disabled={!hasNs}>
          <X size={15} aria-hidden />
          Clear
        </Button>
      </form>

      {awaitingDeepLink ? (
        <TableSkeleton headers={["Key", "Version", "Type", "Labels", "Created"]} />
      ) : !hasNs ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose an environment">
          Pick an application and environment above to list its parameters.
        </EmptyState>
      ) : !settled || loading ? (
        <TableSkeleton headers={["Key", "Version", "Type", "Labels", "Created"]} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<Icon.parameter size={20} />}
          title="No parameters found"
          actions={
            prefix ? (
              <Button variant="outline" onClick={clearFilter}>
                <X size={15} aria-hidden />
                Clear filter
              </Button>
            ) : (
              <Button onClick={openCreate}>New parameter</Button>
            )
          }
        >
          {prefix
            ? "No parameters match this key prefix."
            : `No parameters in ${ns.env}/${ns.app} yet.`}
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Key</th>
                <th>Version</th>
                <th>Type</th>
                <th>Labels</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.key}>
                  <td data-label="Key">
                    <Link className="cell-path" href={links.parameterDetail(p)}>
                      {p.key}
                    </Link>
                  </td>
                  <td data-label="Version">v{p.version}</td>
                  <td className="nowrap" data-label="Type">
                    {p.content_type || <span className="faint">—</span>}
                  </td>
                  <td data-label="Labels">
                    <div className="row-wrap">
                      {labelEntries(p.labels).map(([k, v]) => (
                        <Badge key={k} kind="accent">
                          {k}: v{v}
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="nowrap" data-label="Created">
                    {formatUnixMs(p.created_at_unix_ms)}
                  </td>
                  <td>
                    <div className="row-actions">
                      <ButtonLink variant="outline" size="sm" href={links.parameterDetail(p)}>
                        <Eye size={14} aria-hidden />
                        Details
                      </ButtonLink>
                      <Button variant="destructive" size="sm" onClick={() => setDeleteTarget(p)}>
                        <Trash2 size={14} aria-hidden />
                        Delete
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Pagination
        hasNext={paging.hasNext}
        onNext={paging.next}
        hasPrevious={paging.hasPrevious}
        onPrevious={paging.previous}
        onReset={paging.reset}
        showReset={paging.hasPrevious}
        page={paging.page}
      />

      <Modal
        open={createOpen}
        title="New parameter"
        onClose={() => setCreateOpen(false)}
        dismissible={!saving}
        footer={
          <>
            <Button variant="outline" onClick={() => setCreateOpen(false)} disabled={saving}>
              Cancel
            </Button>
            <Button onClick={onCreate} disabled={saving || shownCreateError !== null}>
              {saving ? <Spinner /> : null}
              Save parameter
            </Button>
          </>
        }
      >
        <form onSubmit={onCreate}>
          <div className="form-row">
            <NamespacePicker
              namespaces={namespaces}
              value={createNs}
              onChange={setCreateNs}
              appId="create-app"
              envId="create-env"
              appError={shownCreateAppError}
              envError={shownCreateEnvError}
            />
          </div>
          <Field
            label="Key"
            hint="Relative to the selected environment, e.g. rate-limit or billing/timeout"
            error={shownKeyError}
          >
            <Input
              className="font-mono"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              onBlur={() => errors.touch("key")}
              placeholder="rate-limit"
            />
          </Field>
          <Field label="Value" error={shownValueError}>
            <ParameterValueInput
              contentType={contentType}
              value={value}
              schema={createSchema.status === "ready" ? createSchema.schema : null}
              rows={8}
              onChange={setValue}
              onBlur={() => errors.touch("value")}
              placeholder={contentType === "json" ? "{}" : "100"}
            />
          </Field>
          <div className="form-row">
            <Field label="Content type" error={shownContentTypeError}>
              <AppSelect
                value={contentType}
                onValueChange={(nextContentType) => {
                  setContentType(nextContentType);
                  errors.touch("contentType");
                }}
                options={PARAMETER_CONTENT_TYPES.map((contentTypeOption) => ({
                  value: contentTypeOption,
                  label: contentTypeOption,
                }))}
              />
            </Field>
          </div>
          <Field label="Metadata JSON" error={shownMetadataError}>
            <JsonEditor
              toolbar="minimal"
              rows={3}
              maxHeight="30vh"
              value={metadataJson}
              onChange={setMetadataJson}
              onBlur={() => errors.touch("metadata")}
            />
          </Field>
        </form>
      </Modal>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete parameter?"
        danger
        message={
          <>
            Delete <span className="mono">{deleteTarget?.key}</span> from{" "}
            <span className="mono">
              {deleteTarget ? `${deleteTarget.env}/${deleteTarget.app}` : ""}
            </span>{" "}
            and all its versions?
          </>
        }
        confirmLabel="Delete parameter"
        busy={deleting}
        onConfirm={onDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  );
}
