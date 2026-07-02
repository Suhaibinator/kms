import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Namespace } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { formatUnixMs } from "@/lib/format";
import { EmptyState, Field, Loading, PageHeader, Pagination } from "@/components/ui";
import { Modal } from "@/components/Modal";

export default function NamespacesPage() {
  const toast = useToast();
  const [namespaces, setNamespaces] = useState<Namespace[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  const [modalOpen, setModalOpen] = useState(false);
  const [path, setPath] = useState("");
  const [description, setDescription] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(
    async (token: string) => {
      setLoading(true);
      try {
        const res = await api.listNamespaces(100, token || undefined);
        setNamespaces(res.namespaces ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        toast.error(err, "Failed to load namespaces");
      } finally {
        setLoading(false);
      }
    },
    [toast],
  );

  useEffect(() => {
    void load(pageToken);
  }, [load, pageToken]);

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
      toast.error(new Error("A namespace path is required."), "Missing path");
      return;
    }
    setSaving(true);
    try {
      await api.createNamespace(p, description.trim());
      toast.success("Namespace created", p);
      setModalOpen(false);
      setPath("");
      setDescription("");
      goReset();
      await load("");
    } catch (err) {
      toast.error(err, "Failed to create namespace");
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Namespaces"
        subtitle="Logical groupings for parameters and secrets."
        actions={
          <button className="btn btn-primary" onClick={() => setModalOpen(true)}>
            New namespace
          </button>
        }
      />

      {loading ? (
        <Loading />
      ) : namespaces.length === 0 ? (
        <EmptyState title="No namespaces yet">
          Create a namespace to organize your configuration paths.
        </EmptyState>
      ) : (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Path</th>
                <th>Description</th>
                <th>Created by</th>
                <th>Created</th>
              </tr>
            </thead>
            <tbody>
              {namespaces.map((ns) => (
                <tr key={ns.path}>
                  <td className="cell-path">{ns.path}</td>
                  <td>{ns.description || <span className="faint">—</span>}</td>
                  <td>{ns.created_by || <span className="faint">—</span>}</td>
                  <td className="nowrap">{formatUnixMs(ns.created_at_unix_ms)}</td>
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
        open={modalOpen}
        title="New namespace"
        onClose={() => setModalOpen(false)}
        footer={
          <>
            <button className="btn" onClick={() => setModalOpen(false)} disabled={saving}>
              Cancel
            </button>
            <button
              className="btn btn-primary"
              onClick={onCreate}
              disabled={saving}
            >
              {saving ? "Creating…" : "Create namespace"}
            </button>
          </>
        }
      >
        <form onSubmit={onCreate}>
          <Field label="Path" hint="Absolute path, e.g. /prod/payments">
            <input
              className="input mono"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/prod/payments"
              autoFocus
            />
          </Field>
          <Field label="Description" hint="Optional.">
            <input
              className="input"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Payments service configuration"
            />
          </Field>
        </form>
      </Modal>
    </>
  );
}
