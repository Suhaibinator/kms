import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import type { Parameter, ParameterMetadata } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { useQueryParam } from "@/lib/hooks";
import { formatUnixMs, isEmptyJson, labelEntries, prettyJson } from "@/lib/format";
import {
  Badge,
  EmptyState,
  Field,
  JsonView,
  KeyValue,
  Loading,
  PageHeader,
  Spinner,
} from "@/components/ui";
import { ConfirmDialog, Modal } from "@/components/Modal";
import CopyButton from "@/components/CopyButton";

export default function ParameterDetailPage() {
  const router = useRouter();
  const toast = useToast();
  const { value: path, ready } = useQueryParam("path");

  const [meta, setMeta] = useState<ParameterMetadata | null>(null);
  const [current, setCurrent] = useState<Parameter | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);

  const [viewed, setViewed] = useState<{ version: number; value: string; contentType: string } | null>(
    null,
  );

  const [newVersionOpen, setNewVersionOpen] = useState(false);
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [saving, setSaving] = useState(false);

  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(async () => {
    if (!path) return;
    setLoading(true);
    setNotFound(false);
    try {
      const [m, cur] = await Promise.all([api.parameterMetadata(path), api.getParameter(path)]);
      setMeta(m);
      setCurrent(cur.parameter);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setNotFound(true);
      } else {
        toast.error(err, "Failed to load parameter");
      }
    } finally {
      setLoading(false);
    }
  }, [path, toast]);

  useEffect(() => {
    if (ready && path) void load();
  }, [ready, path, load]);

  function openNewVersion() {
    setValue(current?.value ?? "");
    setContentType(current?.content_type ?? meta?.content_type ?? "text/plain");
    setMetadataJson(!isEmptyJson(meta?.metadata_json) ? prettyJson(meta?.metadata_json) : "{}");
    setNewVersionOpen(true);
  }

  async function saveVersion(e: React.FormEvent) {
    e.preventDefault();
    if (!path) return;
    setSaving(true);
    try {
      const res = await api.putParameter({
        path,
        value,
        content_type: contentType.trim() || "text/plain",
        metadata_json: metadataJson.trim() || "{}",
      });
      toast.success(`Saved version ${res.version}`, path);
      setNewVersionOpen(false);
      setViewed(null);
      await load();
    } catch (err) {
      toast.error(err, "Failed to save version");
    } finally {
      setSaving(false);
    }
  }

  async function viewVersion(version: number) {
    if (!path) return;
    try {
      const res = await api.getParameter(path, version);
      setViewed({
        version: res.parameter.version,
        value: res.parameter.value,
        contentType: res.parameter.content_type,
      });
    } catch (err) {
      toast.error(err, "Failed to load version value");
    }
  }

  async function onDelete() {
    if (!path) return;
    setDeleting(true);
    try {
      await api.deleteParameter(path);
      toast.success("Parameter deleted", path);
      await router.push("/parameters");
    } catch (err) {
      toast.error(err, "Failed to delete parameter");
    } finally {
      setDeleting(false);
    }
  }

  if (!ready || loading) return <Loading label="Loading parameter…" />;
  if (!path) {
    return <EmptyState title="No parameter specified">Provide a ?path= query parameter.</EmptyState>;
  }
  if (notFound || !meta) {
    return (
      <>
        <PageHeader
          title="Parameter not found"
          actions={
            <Link className="btn" href="/parameters">
              Back to parameters
            </Link>
          }
        />
        <EmptyState title="Not found">
          No parameter exists at <span className="mono">{path}</span>.
        </EmptyState>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title={<span className="mono">{meta.path}</span>}
        subtitle={
          <Link href="/parameters" className="text-sm">
            ← All parameters
          </Link>
        }
        actions={
          <>
            <button className="btn btn-primary" onClick={openNewVersion}>
              New version
            </button>
            <button className="btn btn-danger" onClick={() => setDeleteOpen(true)}>
              Delete
            </button>
          </>
        }
      />

      <div className="card">
        <div className="card-title">
          Current value
          {current ? <Badge kind="accent">v{current.version}</Badge> : null}
        </div>
        {current ? (
          <>
            <div className="between mb-8">
              <span className="faint text-sm">{current.content_type || "value"}</span>
              <CopyButton label="Copy value" value={current.value} />
            </div>
            <pre className="json-block">{current.value}</pre>
          </>
        ) : (
          <span className="faint">No current value.</span>
        )}
      </div>

      <div className="card">
        <div className="card-title">Metadata</div>
        <KeyValue
          rows={[
            ["Content type", meta.content_type || "—"],
            ["Created", formatUnixMs(meta.created_at_unix_ms)],
            ["Updated", formatUnixMs(meta.updated_at_unix_ms)],
            [
              "Labels",
              labelEntries(meta.labels).length ? (
                <div className="row-wrap">
                  {labelEntries(meta.labels).map(([k, v]) => (
                    <Badge key={k} kind="accent">
                      {k}: v{v}
                    </Badge>
                  ))}
                </div>
              ) : (
                "—"
              ),
            ],
          ]}
        />
        {!isEmptyJson(meta.metadata_json) ? (
          <div className="mt-16">
            <div className="field-label">Metadata JSON</div>
            <JsonView raw={prettyJson(meta.metadata_json)} />
          </div>
        ) : null}
      </div>

      <div className="card">
        <div className="card-title">Version history</div>
        {meta.versions.length === 0 ? (
          <EmptyState title="No versions" />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Version</th>
                  <th>State</th>
                  <th>Created by</th>
                  <th>Created</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {[...meta.versions]
                  .sort((a, b) => b.version - a.version)
                  .map((v) => (
                    <tr key={v.version}>
                      <td>v{v.version}</td>
                      <td>
                        <Badge kind={v.state === "enabled" ? "success" : "neutral"}>{v.state}</Badge>
                      </td>
                      <td>{v.created_by || <span className="faint">—</span>}</td>
                      <td className="nowrap">{formatUnixMs(v.created_at_unix_ms)}</td>
                      <td>
                        <div className="row-actions">
                          <button className="btn btn-sm" onClick={() => viewVersion(v.version)}>
                            View value
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}

        {viewed ? (
          <div className="mt-16">
            <div className="between mb-8">
              <div className="row-wrap">
                <Badge kind="accent">v{viewed.version}</Badge>
                <span className="faint text-sm">{viewed.contentType || "value"}</span>
              </div>
              <div className="row-wrap">
                <CopyButton label="Copy" value={viewed.value} />
                <button className="btn btn-sm" onClick={() => setViewed(null)}>
                  Close
                </button>
              </div>
            </div>
            <pre className="json-block">{viewed.value}</pre>
          </div>
        ) : null}
      </div>

      <Modal
        open={newVersionOpen}
        title="New parameter version"
        onClose={() => setNewVersionOpen(false)}
        footer={
          <>
            <button className="btn" onClick={() => setNewVersionOpen(false)} disabled={saving}>
              Cancel
            </button>
            <button className="btn btn-primary" onClick={saveVersion} disabled={saving}>
              {saving ? <Spinner /> : null}
              Save new version
            </button>
          </>
        }
      >
        <form onSubmit={saveVersion}>
          <Field label="Value" hint="Saving creates a new version and updates the current label.">
            <textarea
              className="textarea mono"
              value={value}
              onChange={(e) => setValue(e.target.value)}
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
        open={deleteOpen}
        title="Delete parameter?"
        danger
        message={
          <>
            Delete <span className="mono">{meta.path}</span> and all its versions?
          </>
        }
        confirmLabel="Delete parameter"
        busy={deleting}
        onConfirm={onDelete}
        onCancel={() => setDeleteOpen(false)}
      />
    </>
  );
}
