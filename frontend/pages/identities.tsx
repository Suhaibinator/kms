import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Identity, IdentityKind } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { formatUnixMs } from "@/lib/format";
import { Badge, EmptyState, Field, Loading, PageHeader, Pagination, Spinner } from "@/components/ui";
import { ConfirmDialog, Modal } from "@/components/Modal";
import CopyButton from "@/components/CopyButton";

export default function IdentitiesPage() {
  const toast = useToast();
  const [identities, setIdentities] = useState<Identity[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<IdentityKind>("client");
  const [saving, setSaving] = useState(false);

  const [rotateTarget, setRotateTarget] = useState<string | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<string | null>(null);
  const [actionBusy, setActionBusy] = useState(false);

  // One-time token reveal (from create or rotate).
  const [mintedToken, setMintedToken] = useState<{ name: string; token: string } | null>(null);

  const load = useCallback(
    async (token: string) => {
      setLoading(true);
      try {
        const res = await api.listIdentities(100, token || undefined);
        setIdentities(res.identities ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        toast.error(err, "Failed to load identities");
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
    const n = name.trim();
    if (!n) {
      toast.error(new Error("A name is required."), "Missing name");
      return;
    }
    setSaving(true);
    try {
      const res = await api.createIdentity(n, kind);
      setCreateOpen(false);
      setName("");
      setKind("client");
      setMintedToken({ name: res.identity.name, token: res.token });
      toast.success("Identity created", "Save the token below.");
    } catch (err) {
      toast.error(err, "Failed to create identity");
    } finally {
      setSaving(false);
    }
  }

  async function onRotate() {
    if (!rotateTarget) return;
    setActionBusy(true);
    try {
      const res = await api.rotateIdentity(rotateTarget);
      const rotated = rotateTarget;
      setRotateTarget(null);
      setMintedToken({ name: rotated, token: res.token });
      toast.success("Token rotated", "Save the new token below.");
    } catch (err) {
      toast.error(err, "Failed to rotate token");
    } finally {
      setActionBusy(false);
    }
  }

  async function onRevoke() {
    if (!revokeTarget) return;
    setActionBusy(true);
    try {
      await api.revokeIdentity(revokeTarget);
      toast.success("Identity revoked", revokeTarget);
      setRevokeTarget(null);
      await load(pageToken);
    } catch (err) {
      toast.error(err, "Failed to revoke identity");
    } finally {
      setActionBusy(false);
    }
  }

  async function closeTokenReveal() {
    setMintedToken(null);
    goReset();
    await load("");
  }

  return (
    <>
      <PageHeader
        title="Identities"
        subtitle="Admin and client principals that authenticate to the service."
        actions={
          <button className="btn btn-primary" onClick={() => setCreateOpen(true)}>
            New identity
          </button>
        }
      />

      {loading ? (
        <Loading />
      ) : identities.length === 0 ? (
        <EmptyState title="No identities yet">Create an admin or client identity to issue a token.</EmptyState>
      ) : (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Status</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {identities.map((id) => (
                <tr key={id.name}>
                  <td className="mono">{id.name}</td>
                  <td>
                    <Badge kind={id.kind === "admin" ? "accent" : "neutral"}>{id.kind}</Badge>
                  </td>
                  <td>
                    {id.disabled ? (
                      <Badge kind="danger">revoked</Badge>
                    ) : (
                      <Badge kind="success">active</Badge>
                    )}
                  </td>
                  <td className="nowrap">{formatUnixMs(id.created_at_unix_ms)}</td>
                  <td>
                    <div className="row-actions">
                      <button
                        className="btn btn-sm"
                        disabled={id.disabled}
                        onClick={() => setRotateTarget(id.name)}
                      >
                        Rotate token
                      </button>
                      <button
                        className="btn btn-sm btn-danger"
                        disabled={id.disabled}
                        onClick={() => setRevokeTarget(id.name)}
                      >
                        Revoke
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
        title="New identity"
        onClose={() => setCreateOpen(false)}
        footer={
          <>
            <button className="btn" onClick={() => setCreateOpen(false)} disabled={saving}>
              Cancel
            </button>
            <button className="btn btn-primary" onClick={onCreate} disabled={saving}>
              {saving ? <Spinner /> : null}
              Create identity
            </button>
          </>
        }
      >
        <form onSubmit={onCreate}>
          <Field label="Name" hint="Unique identity name, e.g. gradethis-be">
            <input
              className="input mono"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="gradethis-be"
              autoFocus
            />
          </Field>
          <Field label="Kind">
            <select className="select" value={kind} onChange={(e) => setKind(e.target.value as IdentityKind)}>
              <option value="client">client</option>
              <option value="admin">admin</option>
            </select>
          </Field>
        </form>
      </Modal>

      <ConfirmDialog
        open={rotateTarget !== null}
        title="Rotate token?"
        message={
          <>
            Rotating issues a new token for <span className="mono">{rotateTarget}</span> and
            immediately invalidates the current one. Applications must be updated with the new token.
          </>
        }
        confirmLabel="Rotate token"
        busy={actionBusy}
        onConfirm={onRotate}
        onCancel={() => setRotateTarget(null)}
      />

      <ConfirmDialog
        open={revokeTarget !== null}
        title="Revoke identity?"
        danger
        message={
          <>
            Revoke <span className="mono">{revokeTarget}</span>? Its token stops working immediately.
          </>
        }
        confirmLabel="Revoke identity"
        busy={actionBusy}
        onConfirm={onRevoke}
        onCancel={() => setRevokeTarget(null)}
      />

      <Modal
        open={mintedToken !== null}
        title="Save this token now"
        onClose={() => void closeTokenReveal()}
        footer={
          <button className="btn btn-primary" onClick={() => void closeTokenReveal()}>
            I&apos;ve saved it
          </button>
        }
      >
        <div className="warn-panel mb-16">
          <strong>This token is shown once and cannot be retrieved again.</strong> Copy it now and
          store it securely for <span className="mono">{mintedToken?.name}</span>.
        </div>
        <div className="token-reveal">{mintedToken?.token}</div>
        <div className="row-wrap mt-16">
          <CopyButton label="Copy token" value={() => mintedToken?.token ?? ""} />
        </div>
      </Modal>
    </>
  );
}
