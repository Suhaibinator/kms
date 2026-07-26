import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import type { AuthMethod, CertBundle, Identity, IdentityCert, IdentityKind } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { useNamespaces } from "@/lib/hooks";
import { displayNamespace, formatUnixMs } from "@/lib/format";
import {
  Badge,
  EmptyState,
  Field,
  PageHeader,
  Pagination,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { Icon } from "@/components/icons";
import { ConfirmDialog, Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import CopyButton from "@/components/CopyButton";

const DEFAULT_CERT_DAYS = 90;
const NO_NS: NamespaceSelection = { env: "", app: "" };

// Trigger a client-side download of text content (cert/key PEM bundles).
function downloadText(filename: string, content: string) {
  const blob = new Blob([content], { type: "application/x-pem-file" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

function certStatus(cert: IdentityCert): { kind: "success" | "warning" | "danger"; label: string } {
  if (cert.revoked_at_unix_ms > 0) return { kind: "danger", label: "revoked" };
  if (cert.not_after_unix_ms > 0 && cert.not_after_unix_ms < Date.now())
    return { kind: "warning", label: "expired" };
  return { kind: "success", label: "valid" };
}

function validCertCount(id: Identity): number {
  return (id.certs ?? []).filter(
    (c) => c.revoked_at_unix_ms === 0 && (c.not_after_unix_ms === 0 || c.not_after_unix_ms > Date.now()),
  ).length;
}

interface Credentials {
  name: string;
  token?: string;
  cert?: CertBundle;
}

export default function IdentitiesPage() {
  const toast = useToast();
  const { namespaces } = useNamespaces();
  const [identities, setIdentities] = useState<Identity[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");

  // Create form.
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<IdentityKind>("client");
  const [bindNs, setBindNs] = useState<NamespaceSelection>(NO_NS);
  const [methods, setMethods] = useState<AuthMethod[]>(["mtls"]);
  const [certDays, setCertDays] = useState(String(DEFAULT_CERT_DAYS));
  const [saving, setSaving] = useState(false);

  // One-time credential display (from create or issue-cert).
  const [credentials, setCredentials] = useState<Credentials | null>(null);

  // Manage-certs modal (by identity name; certs read from the live list).
  const [certsTargetName, setCertsTargetName] = useState<string | null>(null);
  const [issueDays, setIssueDays] = useState(String(DEFAULT_CERT_DAYS));
  const [issueBusy, setIssueBusy] = useState(false);
  const [revokeCertTarget, setRevokeCertTarget] = useState<{ name: string; serial: string } | null>(null);
  const [revokeCertBusy, setRevokeCertBusy] = useState(false);

  const [rotateTarget, setRotateTarget] = useState<string | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<string | null>(null);
  const [actionBusy, setActionBusy] = useState(false);

  const [caBusy, setCaBusy] = useState(false);

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

  const certsTarget = useMemo(
    () => identities.find((id) => id.name === certsTargetName) ?? null,
    [identities, certsTargetName],
  );

  function goNext() {
    if (!nextToken) return;
    setPageStack((s) => [...s, pageToken]);
    setPageToken(nextToken);
  }
  function goReset() {
    setPageStack([]);
    setPageToken("");
  }

  function openCreate() {
    setName("");
    setKind("client");
    setBindNs(NO_NS);
    setMethods(["mtls"]);
    setCertDays(String(DEFAULT_CERT_DAYS));
    setCreateOpen(true);
  }

  function toggleMethod(method: AuthMethod, on: boolean) {
    const set = new Set(methods);
    if (on) set.add(method);
    else set.delete(method);
    setMethods([...set]);
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    const n = name.trim();
    if (!n) {
      toast.error(new Error("A name is required."), "Missing name");
      return;
    }
    if (methods.length === 0) {
      toast.error(new Error("Select at least one auth method (mTLS, token, or both)."), "No auth method");
      return;
    }
    const days = Number(certDays);
    const ttlSeconds =
      methods.includes("mtls") && Number.isFinite(days) && days > 0
        ? Math.round(days * 86400)
        : 0;
    const namespace = bindNs.env && bindNs.app ? { env: bindNs.env, app: bindNs.app } : null;

    setSaving(true);
    try {
      const res = await api.createIdentity({
        name: n,
        kind,
        namespace,
        auth_methods: methods,
        cert_ttl_seconds: ttlSeconds,
      });
      setCreateOpen(false);
      setCredentials({ name: res.identity.name, token: res.token, cert: res.cert });
      toast.success("Identity created", "Save the credentials below.");
    } catch (err) {
      toast.error(err, "Failed to create identity");
    } finally {
      setSaving(false);
    }
  }

  async function onIssueCert() {
    if (!certsTargetName) return;
    const days = Number(issueDays);
    const ttlSeconds = Number.isFinite(days) && days > 0 ? Math.round(days * 86400) : 0;
    setIssueBusy(true);
    try {
      const res = await api.issueCert(certsTargetName, ttlSeconds);
      // Close the manage modal so the one-time bundle takes focus, then refresh.
      const name = certsTargetName;
      setCertsTargetName(null);
      setCredentials({ name, cert: res.cert });
      toast.success("Certificate issued", "Save the PEM bundle below.");
      await load(pageToken);
    } catch (err) {
      toast.error(err, "Failed to issue certificate");
    } finally {
      setIssueBusy(false);
    }
  }

  async function onRevokeCert() {
    if (!revokeCertTarget) return;
    setRevokeCertBusy(true);
    try {
      await api.revokeCert(revokeCertTarget.name, revokeCertTarget.serial);
      toast.success("Certificate revoked", revokeCertTarget.serial);
      setRevokeCertTarget(null);
      await load(pageToken);
    } catch (err) {
      toast.error(err, "Failed to revoke certificate");
    } finally {
      setRevokeCertBusy(false);
    }
  }

  async function onRotate() {
    if (!rotateTarget) return;
    setActionBusy(true);
    try {
      const res = await api.rotateIdentity(rotateTarget);
      const name = rotateTarget;
      setRotateTarget(null);
      setCredentials({ name, token: res.token });
      toast.success("Token rotated", "Save the new token below.");
    } catch (err) {
      toast.error(err, "Failed to rotate token");
    } finally {
      setActionBusy(false);
    }
  }

  async function onRevokeIdentity() {
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

  async function downloadCa() {
    setCaBusy(true);
    try {
      const res = await api.ca();
      downloadText("kms-client-ca.crt", res.cert_pem);
      toast.success("Client CA certificate downloaded", "Use it to validate KMS-issued client certificates.");
    } catch (err) {
      toast.error(err, "Failed to fetch CA certificate");
    } finally {
      setCaBusy(false);
    }
  }

  async function closeCredentials() {
    setCredentials(null);
    goReset();
    await load("");
  }

  return (
    <>
      <PageHeader
        title="Identities"
        subtitle="Admin and client principals that authenticate to the service."
        actions={
          <>
            <button className="btn" onClick={() => void downloadCa()} disabled={caBusy}>
              {caBusy ? <Spinner /> : null}
              Download CA cert
            </button>
            <button className="btn btn-primary" onClick={openCreate}>
              New identity
            </button>
          </>
        }
      />

      <div className="info-panel mb-16">
        An identity <strong>bound to a namespace</strong> may read, list, and subscribe within
        that namespace with no policy required — the credential <em>is</em> the app (the implicit
        home-namespace grant). Writes and any cross-namespace access still need an explicit policy.
        Client certificates prove possession; bearer tokens do not, so prefer mTLS-only namespaces.
      </div>

      {loading ? (
        <TableSkeleton
          headers={[
            "Name",
            "Kind",
            "Namespace",
            "Credentials",
            "Status",
            "Created",
          ]}
        />
      ) : identities.length === 0 ? (
        <EmptyState
          icon={<Icon.identity size={20} />}
          title="No identities yet"
          actions={
            <button className="btn btn-primary" onClick={openCreate}>
              New identity
            </button>
          }
        >
          Create an admin or client identity to issue a token or certificate.
        </EmptyState>
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Namespace</th>
                <th>Credentials</th>
                <th>Status</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {identities.map((id) => {
                const certCount = validCertCount(id);
                return (
                  <tr key={id.name}>
                    <td className="mono" data-label="Name">
                      {id.name}
                    </td>
                    <td data-label="Kind">
                      <Badge kind={id.kind === "admin" ? "accent" : "neutral"}>{id.kind}</Badge>
                    </td>
                    <td data-label="Namespace">
                      {id.namespace ? (
                        <span className="chip">{displayNamespace(id.namespace)}</span>
                      ) : (
                        <span className="faint">unbound</span>
                      )}
                    </td>
                    <td data-label="Credentials">
                      <div className="row-wrap">
                        {id.has_token ? <Badge kind="neutral">token</Badge> : null}
                        {certCount > 0 ? (
                          <Badge kind="accent">
                            {certCount} cert{certCount === 1 ? "" : "s"}
                          </Badge>
                        ) : null}
                        {!id.has_token && certCount === 0 ? (
                          <span className="faint">—</span>
                        ) : null}
                      </div>
                    </td>
                    <td data-label="Status">
                      {id.disabled ? (
                        <Badge kind="danger">revoked</Badge>
                      ) : (
                        <Badge kind="success">active</Badge>
                      )}
                    </td>
                    <td className="nowrap" data-label="Created">
                      {formatUnixMs(id.created_at_unix_ms)}
                    </td>
                    <td>
                      <div className="row-actions">
                        <button
                          className="btn btn-sm"
                          disabled={id.disabled}
                          onClick={() => {
                            setIssueDays(String(DEFAULT_CERT_DAYS));
                            setCertsTargetName(id.name);
                          }}
                        >
                          Certificates
                        </button>
                        {id.has_token ? (
                          <button
                            className="btn btn-sm"
                            disabled={id.disabled}
                            onClick={() => setRotateTarget(id.name)}
                          >
                            Rotate token
                          </button>
                        ) : null}
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
                );
              })}
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

      {/* Create identity */}
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
          <div className="form-row">
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
              <select
                className="select"
                value={kind}
                onChange={(e) => setKind(e.target.value as IdentityKind)}
              >
                <option value="client">client</option>
                <option value="admin">admin</option>
              </select>
            </Field>
          </div>

          <Field
            label="Bound namespace"
            hint="Optional. Leave the app blank for an unbound (admin/tooling) identity. A bound identity gets the implicit home-namespace read grant."
          >
            <div className="form-row">
              <NamespacePicker
                namespaces={namespaces}
                value={bindNs}
                onChange={setBindNs}
                envId="bind-env"
                appId="bind-app"
              />
            </div>
          </Field>

          <Field
            label="Auth methods"
            hint="mTLS mints a one-time client-certificate bundle; token mints a one-time bearer token. Choose one or both."
          >
            <div className="checkbox-row">
              <input
                id="new-mtls"
                type="checkbox"
                checked={methods.includes("mtls")}
                onChange={(e) => toggleMethod("mtls", e.target.checked)}
              />
              <label htmlFor="new-mtls">
                <strong>mTLS</strong> — client certificate (proof of possession)
              </label>
            </div>
            <div className="checkbox-row">
              <input
                id="new-token"
                type="checkbox"
                checked={methods.includes("token")}
                onChange={(e) => toggleMethod("token", e.target.checked)}
              />
              <label htmlFor="new-token">
                <strong>Token</strong> — bearer token
              </label>
            </div>
          </Field>

          {methods.includes("mtls") ? (
            <Field label="Certificate lifetime (days)" hint="Applies to the initial certificate.">
              <input
                className="input"
                type="number"
                min={1}
                value={certDays}
                onChange={(e) => setCertDays(e.target.value)}
              />
            </Field>
          ) : null}
        </form>
      </Modal>

      {/* One-time credentials */}
      <CredentialsModal credentials={credentials} onClose={() => void closeCredentials()} />

      {/* Manage certificates */}
      <Modal
        open={certsTarget !== null}
        wide
        title={certsTarget ? `Certificates — ${certsTarget.name}` : "Certificates"}
        onClose={() => setCertsTargetName(null)}
        footer={
          <button className="btn" onClick={() => setCertsTargetName(null)}>
            Close
          </button>
        }
      >
        {certsTarget ? (
          <>
            <div className="between mb-16">
              <div className="row-wrap" style={{ alignItems: "flex-end" }}>
                <div className="field" style={{ marginBottom: 0 }}>
                  <label className="field-label" htmlFor="issue-days">
                    New cert lifetime (days)
                  </label>
                  <input
                    id="issue-days"
                    className="input"
                    type="number"
                    min={1}
                    style={{ width: 140 }}
                    value={issueDays}
                    onChange={(e) => setIssueDays(e.target.value)}
                  />
                </div>
                <button className="btn btn-primary" onClick={() => void onIssueCert()} disabled={issueBusy}>
                  {issueBusy ? <Spinner /> : null}
                  Issue new certificate
                </button>
              </div>
            </div>
            <div className="faint text-sm mb-16">
              Issue a fresh certificate before an old one expires for zero-downtime rollover.
              Multiple valid certificates can coexist.
            </div>

            {(certsTarget.certs ?? []).length === 0 ? (
              <EmptyState
                icon={<Icon.identity size={20} />}
                title="No certificates issued"
              >
                Issue one above to let this identity authenticate over mTLS.
              </EmptyState>
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Fingerprint</th>
                      <th>State</th>
                      <th>Expires</th>
                      <th>Issued</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {[...(certsTarget.certs ?? [])]
                      .sort((a, b) => b.created_at_unix_ms - a.created_at_unix_ms)
                      .map((cert) => {
                        const status = certStatus(cert);
                        const revocable = status.label === "valid";
                        return (
                          <tr key={cert.serial}>
                            <td className="mono text-sm" title={`serial ${cert.serial}`}>
                              {cert.fingerprint}
                            </td>
                            <td>
                              <Badge kind={status.kind}>{status.label}</Badge>
                            </td>
                            <td className="nowrap">{formatUnixMs(cert.not_after_unix_ms)}</td>
                            <td className="nowrap">{formatUnixMs(cert.created_at_unix_ms)}</td>
                            <td>
                              {revocable ? (
                                <button
                                  className="btn btn-sm btn-danger"
                                  onClick={() =>
                                    setRevokeCertTarget({ name: certsTarget.name, serial: cert.serial })
                                  }
                                >
                                  Revoke
                                </button>
                              ) : null}
                            </td>
                          </tr>
                        );
                      })}
                  </tbody>
                </table>
              </div>
            )}
          </>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={revokeCertTarget !== null}
        title="Revoke certificate?"
        danger
        message={
          <>
            Revoke certificate <span className="mono">{revokeCertTarget?.serial}</span> for{" "}
            <span className="mono">{revokeCertTarget?.name}</span>? It stops authenticating on the
            next RPC. Other certificates for this identity keep working.
          </>
        }
        confirmLabel="Revoke certificate"
        busy={revokeCertBusy}
        onConfirm={() => void onRevokeCert()}
        onCancel={() => setRevokeCertTarget(null)}
      />

      <ConfirmDialog
        open={rotateTarget !== null}
        title="Rotate token?"
        message={
          <>
            Rotating issues a new bearer token for <span className="mono">{rotateTarget}</span>. The
            old token fails new RPCs; an existing watch closes on its next heartbeat. Applications
            must be updated with the new token.
          </>
        }
        confirmLabel="Rotate token"
        busy={actionBusy}
        onConfirm={() => void onRotate()}
        onCancel={() => setRotateTarget(null)}
      />

      <ConfirmDialog
        open={revokeTarget !== null}
        title="Revoke identity?"
        danger
        message={
          <>
            Revoke <span className="mono">{revokeTarget}</span>? Its token and all of its
            certificates stop working on the next RPC; existing watch streams close on the next heartbeat.
          </>
        }
        confirmLabel="Revoke identity"
        busy={actionBusy}
        onConfirm={() => void onRevokeIdentity()}
        onCancel={() => setRevokeTarget(null)}
      />
    </>
  );
}

function CredentialsModal({
  credentials,
  onClose,
}: {
  credentials: Credentials | null;
  onClose: () => void;
}) {
  return (
    <Modal
      open={credentials !== null}
      wide
      dismissible={false}
      title="Save these credentials now"
      onClose={onClose}
      footer={
        <button className="btn btn-primary" onClick={onClose}>
          I&apos;ve saved them
        </button>
      }
    >
      {credentials ? (
        <>
          <div className="warn-panel mb-16">
            <strong>Shown once and never retrievable again.</strong> Store them securely for{" "}
            <span className="mono">{credentials.name}</span>.
          </div>

          {credentials.token ? (
            <div className="mb-16">
              <div className="field-label">Bearer token</div>
              <div className="token-reveal">{credentials.token}</div>
              <div className="row-wrap mt-8">
                <CopyButton label="Copy token" value={() => credentials.token ?? ""} />
              </div>
            </div>
          ) : null}

          {credentials.cert ? (
            <div>
              <div className="field-label">Client certificate (PEM)</div>
              <div className="faint text-sm mb-8">
                Serial <span className="mono">{credentials.cert.serial}</span> · expires{" "}
                {formatUnixMs(credentials.cert.not_after_unix_ms)}
              </div>
              <div className="token-reveal">{credentials.cert.cert_pem}</div>
              <div className="row-wrap mt-8 mb-16">
                <CopyButton label="Copy certificate" value={() => credentials.cert?.cert_pem ?? ""} />
                <button
                  className="btn btn-sm"
                  onClick={() =>
                    credentials.cert && downloadText(`${credentials.name}.crt`, credentials.cert.cert_pem)
                  }
                >
                  Download .crt
                </button>
              </div>

              <div className="field-label">Private key (PEM)</div>
              <div className="danger-panel mb-8">
                The private key is never stored server-side. If you lose it, revoke this certificate
                and issue a new one.
              </div>
              <div className="token-reveal">{credentials.cert.key_pem}</div>
              <div className="row-wrap mt-8">
                <CopyButton label="Copy key" value={() => credentials.cert?.key_pem ?? ""} />
                <button
                  className="btn btn-sm"
                  onClick={() =>
                    credentials.cert && downloadText(`${credentials.name}.key`, credentials.cert.key_pem)
                  }
                >
                  Download .key
                </button>
              </div>
            </div>
          ) : null}
        </>
      ) : null}
    </Modal>
  );
}
