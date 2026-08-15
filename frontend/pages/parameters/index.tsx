import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { Eye, Filter, Trash2, X } from "lucide-react";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import {
  Badge,
  EmptyState,
  Field,
  PageHeader,
  Pagination,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs, labelEntries } from "@/lib/format";
import { useNamespaces, useQueryParams } from "@/lib/hooks";
import { PARAMETER_CONTENT_TYPES, type Parameter } from "@/lib/types";

function detailLink(ref: { env: string; app: string; key: string }): string {
  return `/parameters/detail?env=${encodeURIComponent(ref.env)}&app=${encodeURIComponent(
    ref.app,
  )}&key=${encodeURIComponent(ref.key)}`;
}

const NO_NS: NamespaceSelection = { env: "", app: "" };

export default function ParametersPage() {
  const toast = useToast();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams(["env", "app", "key_prefix"]);

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  const [prefixInput, setPrefixInput] = useState("");
  const [prefix, setPrefix] = useState("");

  const [rows, setRows] = useState<Parameter[]>([]);
  const [loading, setLoading] = useState(false);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");
  const loadController = useRef<AbortController | null>(null);

  const [createOpen, setCreateOpen] = useState(false);
  const [createNs, setCreateNs] = useState<NamespaceSelection>(NO_NS);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("string");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [saving, setSaving] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<Parameter | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Seed the selection from deep-link query params exactly once.
  const seeded = useRef(false);
  useEffect(() => {
    if (!queryReady || seeded.current) return;
    seeded.current = true;
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    const kp = queryValues.key_prefix ?? "";
    if (env || app) setNs({ env, app });
    if (kp) {
      setPrefixInput(kp);
      setPrefix(kp);
    }
  }, [queryReady, queryValues]);

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load namespaces");
  }, [nsError, toast]);

  const hasNs = !!ns.env && !!ns.app;

  const load = useCallback(
    async (token: string, selection: NamespaceSelection, activePrefix: string) => {
      loadController.current?.abort();
      if (!selection.env || !selection.app) {
        setRows([]);
        setNextToken("");
        setLoading(false);
        return;
      }
      const controller = new AbortController();
      loadController.current = controller;
      setLoading(true);
      try {
        const res = await api.listParameters(
          { env: selection.env, app: selection.app },
          activePrefix || undefined,
          100,
          token || undefined,
          { signal: controller.signal },
        );
        if (loadController.current === controller) {
          setRows(res.parameters ?? []);
          setNextToken(res.next_page_token ?? "");
        }
      } catch (err) {
        if (!isAbortError(err) && loadController.current === controller) {
          setRows([]);
          setNextToken("");
          toast.error(err, "Failed to load parameters");
        }
      } finally {
        if (loadController.current === controller) {
          loadController.current = null;
          setLoading(false);
        }
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken, ns, prefix);
    return () => loadController.current?.abort();
  }, [load, pageToken, ns, prefix]);

  function onSelectNamespace(next: NamespaceSelection) {
    setNs(next);
    setRows([]);
    setNextToken("");
    setDeleteTarget(null);
    setPageStack([]);
    setPageToken("");
  }
  function applyFilter(e: React.FormEvent) {
    e.preventDefault();
    setRows([]);
    setNextToken("");
    setDeleteTarget(null);
    setPageStack([]);
    setPageToken("");
    setPrefix(prefixInput.trim());
  }
  function clearFilter() {
    setPrefixInput("");
    setRows([]);
    setNextToken("");
    setDeleteTarget(null);
    setPageStack([]);
    setPageToken("");
    setPrefix("");
  }
  function goNext() {
    if (!nextToken) return;
    setPageStack((s) => [...s, pageToken]);
    setPageToken(nextToken);
  }
  function goPrevious() {
    const previous = pageStack[pageStack.length - 1];
    if (previous === undefined) return;
    setPageStack((s) => s.slice(0, -1));
    setPageToken(previous);
  }
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  function openCreate() {
    setCreateNs(hasNs ? ns : NO_NS);
    setKey("");
    setValue("");
    setContentType("string");
    setMetadataJson("{}");
    setCreateOpen(true);
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!createNs.env || !createNs.app) {
      toast.error(new Error("Choose a namespace for the parameter."), "Missing namespace");
      return;
    }
    const k = key.trim();
    if (!k) {
      toast.error(new Error("A key is required."), "Missing key");
      return;
    }
    setSaving(true);
    try {
      const res = await api.putParameter({
        env: createNs.env,
        app: createNs.app,
        key: k,
        value,
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
        goReset();
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
      await load(pageToken, ns, prefix);
    } catch (err) {
      toast.error(err, "Failed to delete parameter");
    } finally {
      setDeleting(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Parameters"
        subtitle="Non-secret configuration values, scoped to a namespace and versioned like secrets."
        actions={
          <button className="btn btn-primary" onClick={openCreate}>
            New parameter
          </button>
        }
      />

      <form className="filters" onSubmit={applyFilter}>
        <NamespacePicker namespaces={namespaces} value={ns} onChange={onSelectNamespace} />
        <div className="field filter-grow">
          <label className="field-label" htmlFor="key-prefix">
            Key prefix
          </label>
          <input
            id="key-prefix"
            className="input mono"
            placeholder="billing/"
            value={prefixInput}
            disabled={!hasNs}
            onChange={(e) => setPrefixInput(e.target.value)}
          />
        </div>
        <button type="submit" className="btn" disabled={!hasNs}>
          <Filter size={15} aria-hidden />
          Filter
        </button>
        <button type="button" className="btn btn-ghost" onClick={clearFilter} disabled={!hasNs}>
          <X size={15} aria-hidden />
          Clear
        </button>
      </form>

      {!hasNs ? (
        <EmptyState icon={<Icon.namespace size={20} />} title="Choose a namespace">
          Pick an environment and application above to list its parameters.
        </EmptyState>
      ) : loading ? (
        <TableSkeleton headers={["Key", "Version", "Type", "Labels", "Created"]} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<Icon.parameter size={20} />}
          title="No parameters found"
          actions={
            prefix ? (
              <button className="btn" onClick={clearFilter}>
                <X size={15} aria-hidden />
                Clear filter
              </button>
            ) : (
              <button className="btn btn-primary" onClick={openCreate}>
                New parameter
              </button>
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
                    <Link className="cell-path" href={detailLink(p)}>
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
                      <Link className="btn btn-sm" href={detailLink(p)}>
                        <Eye size={14} aria-hidden />
                        Details
                      </Link>
                      <button className="btn btn-sm btn-danger" onClick={() => setDeleteTarget(p)}>
                        <Trash2 size={14} aria-hidden />
                        Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Pagination
        hasNext={!!nextToken}
        onNext={goNext}
        hasPrevious={pageStack.length > 0}
        onPrevious={goPrevious}
        onReset={goReset}
        showReset={pageStack.length > 0}
      />

      <Modal
        open={createOpen}
        title="New parameter"
        onClose={() => setCreateOpen(false)}
        footer={
          <>
            <button className="btn" onClick={() => setCreateOpen(false)} disabled={saving}>
              Cancel
            </button>
            <button className="btn btn-primary" onClick={onCreate} disabled={saving}>
              {saving ? <Spinner /> : null}
              Save parameter
            </button>
          </>
        }
      >
        <form onSubmit={onCreate}>
          <div className="form-row">
            <NamespacePicker namespaces={namespaces} value={createNs} onChange={setCreateNs} />
          </div>
          <Field label="Key" hint="Relative to the namespace, e.g. rate-limit or billing/timeout">
            <input
              className="input mono"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder="rate-limit"
            />
          </Field>
          <Field label="Value">
            <textarea
              className="textarea mono"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="100"
            />
          </Field>
          <div className="form-row">
            <Field label="Content type">
              <select
                className="select"
                value={contentType}
                onChange={(e) => setContentType(e.target.value)}
              >
                {PARAMETER_CONTENT_TYPES.map((ct) => (
                  <option key={ct} value={ct}>
                    {ct}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Metadata JSON">
              <input
                className="input mono"
                value={metadataJson}
                onChange={(e) => setMetadataJson(e.target.value)}
              />
            </Field>
          </div>
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
