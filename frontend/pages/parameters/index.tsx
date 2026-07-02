import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Parameter } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { formatUnixMs, labelEntries } from "@/lib/format";
import { Badge, EmptyState, Field, Loading, PageHeader, Pagination, Spinner } from "@/components/ui";
import { ConfirmDialog, Modal } from "@/components/Modal";

function detailLink(path: string): string {
  return `/parameters/detail?path=${encodeURIComponent(path)}`;
}

export default function ParametersPage() {
  const toast = useToast();
  const [rows, setRows] = useState<Parameter[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  const [prefixInput, setPrefixInput] = useState("");
  const [prefix, setPrefix] = useState("");

  const [createOpen, setCreateOpen] = useState(false);
  const [path, setPath] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [saving, setSaving] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(
    async (token: string, activePrefix: string) => {
      setLoading(true);
      try {
        const res = await api.listParameters(activePrefix || undefined, 100, token || undefined);
        setRows(res.parameters ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        toast.error(err, "Failed to load parameters");
      } finally {
        setLoading(false);
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken, prefix);
  }, [load, pageToken, prefix]);

  function applyFilter(e: React.FormEvent) {
    e.preventDefault();
    setPageStack([]);
    setPageToken("");
    setPrefix(prefixInput.trim());
  }
  function clearFilter() {
    setPrefixInput("");
    setPageStack([]);
    setPageToken("");
    setPrefix("");
  }
  function goNext() {
    if (!nextToken) return;
    setPageStack((s) => [...s, pageToken]);
    setPageToken(nextToken);
  }
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    const p = path.trim();
    if (!p) {
      toast.error(new Error("A parameter path is required."), "Missing path");
      return;
    }
    setSaving(true);
    try {
      const res = await api.putParameter({
        path: p,
        value,
        content_type: contentType.trim() || "text/plain",
        metadata_json: metadataJson.trim() || "{}",
      });
      toast.success(`Parameter saved (version ${res.version})`, p);
      setCreateOpen(false);
      setPath("");
      setValue("");
      setContentType("text/plain");
      setMetadataJson("{}");
      goReset();
      await load("", prefix);
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
      await api.deleteParameter(deleteTarget);
      toast.success("Parameter deleted", deleteTarget);
      setDeleteTarget(null);
      await load(pageToken, prefix);
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
        subtitle="Non-secret configuration values, versioned like secrets."
        actions={
          <button className="btn btn-primary" onClick={() => setCreateOpen(true)}>
            New parameter
          </button>
        }
      />

      <form className="filters" onSubmit={applyFilter}>
        <div className="field filter-grow">
          <label className="field-label" htmlFor="prefix">
            Path prefix
          </label>
          <input
            id="prefix"
            className="input mono"
            placeholder="/prod/api"
            value={prefixInput}
            onChange={(e) => setPrefixInput(e.target.value)}
          />
        </div>
        <button type="submit" className="btn">
          Filter
        </button>
        <button type="button" className="btn btn-ghost" onClick={clearFilter}>
          Clear
        </button>
      </form>

      {loading ? (
        <Loading />
      ) : rows.length === 0 ? (
        <EmptyState title="No parameters found">
          {prefix ? "No parameters match this prefix." : "Create a parameter to get started."}
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Path</th>
                <th>Version</th>
                <th>Type</th>
                <th>Labels</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.path}>
                  <td data-label="Path">
                    <Link className="cell-path" href={detailLink(p.path)}>
                      {p.path}
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
                      <Link className="btn btn-sm" href={detailLink(p.path)}>
                        Details
                      </Link>
                      <button
                        className="btn btn-sm btn-danger"
                        onClick={() => setDeleteTarget(p.path)}
                      >
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
          <Field label="Path" hint="Absolute path, e.g. /prod/api/rate-limit">
            <input
              className="input mono"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/prod/api/rate-limit"
              autoFocus
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
              <input
                className="input"
                value={contentType}
                onChange={(e) => setContentType(e.target.value)}
              />
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
            Delete <span className="mono">{deleteTarget}</span> and all its versions?
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
