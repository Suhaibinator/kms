import { Check, Download } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
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
import { displayNamespace, formatUnixMs } from "@/lib/format";
import { useNamespaces } from "@/lib/hooks";
import type {
  AuthMethod,
  CertBundle,
  Identity,
  IdentityCert,
  IdentityKind,
  Namespace,
} from "@/lib/types";
import { createStoredZip } from "@/lib/zip";

const DEFAULT_CERT_DAYS = 90;
const NO_NS: NamespaceSelection = { env: "", app: "" };

function downloadBlob(filename: string, blob: Blob) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

// Trigger a client-side download of text content (cert/key PEM bundles).
function downloadText(filename: string, content: string) {
  downloadBlob(filename, new Blob([content], { type: "application/x-pem-file" }));
}

function certStatus(cert: IdentityCert): { kind: "success" | "warning" | "danger"; label: string } {
  if (cert.revoked_at_unix_ms > 0) return { kind: "danger", label: "revoked" };
  if (cert.not_after_unix_ms > 0 && cert.not_after_unix_ms < Date.now())
    return { kind: "warning", label: "expired" };
  return { kind: "success", label: "valid" };
}

function validCertCount(id: Identity): number {
  return (id.certs ?? []).filter(
    (c) =>
      c.revoked_at_unix_ms === 0 && (c.not_after_unix_ms === 0 || c.not_after_unix_ms > Date.now()),
  ).length;
}

type MethodAvailability = "allowed" | "rejected" | "unknown";

function identityMethodAvailability(
  identity: Identity,
  method: AuthMethod,
  namespaces: Namespace[],
  namespacesLoading: boolean,
  namespacesError: unknown,
): MethodAvailability {
  if (identity.kind === "admin") return "allowed";
  if (!identity.namespace) return "allowed";
  if (namespacesLoading || namespacesError) return "unknown";
  const home = namespaces.find(
    (namespace) =>
      namespace.env === identity.namespace?.env && namespace.app === identity.namespace?.app,
  );
  if (!home) return "unknown";
  return home.allowed_auth_methods.includes(method) ? "allowed" : "rejected";
}

function unavailableMethodReason(method: AuthMethod, availability: MethodAvailability): string {
  if (availability === "rejected") {
    return `The bound namespace does not accept ${method === "mtls" ? "mTLS" : "token"} authentication.`;
  }
  if (availability === "unknown") {
    return "KMS Console could not verify the bound namespace authentication methods.";
  }
  return "";
}

interface Credentials {
  name: string;
  kind: IdentityKind;
  token?: string;
  cert?: CertBundle;
  namespace?: NamespaceSelection | null;
}

type IdentityMode = "application" | "unbound" | "admin";

const WIZARD_STEPS = ["Application", "Authentication", "Save credentials", "Integrate"];

function WizardProgress({ current }: { current: number }) {
  return (
    <ol className="wizard-steps" aria-label="Connect application progress">
      {WIZARD_STEPS.map((label, index) => {
        const number = index + 1;
        const state = number < current ? "complete" : number === current ? "current" : "upcoming";
        return (
          <li
            key={label}
            className={`wizard-step wizard-step-${state}`}
            aria-current={number === current ? "step" : undefined}
          >
            <span className="wizard-step-number" aria-hidden>
              {number < current ? <Check size={16} strokeWidth={2.25} /> : number}
            </span>
            <span>{label}</span>
          </li>
        );
      })}
    </ol>
  );
}

function CertificateRoles({ compact = false }: { compact?: boolean }) {
  return (
    <section className={`certificate-map ${compact ? "certificate-map-compact" : ""}`}>
      <div className="certificate-map-heading">
        <h2>Three certificate roles</h2>
        <span className="faint text-sm">
          For mTLS, only the first pair is delivered to the application; trust roots are managed
          separately.
        </span>
      </div>
      <div className="certificate-role-grid">
        <div className="certificate-role">
          <span className="certificate-role-label">Application holds</span>
          <strong>Per-application client certificate + key</strong>
          <span>
            KMS generates both and returns them once through the admin surface; it does not persist
            the key. The workload presents <code>app.crt</code> and proves possession of{" "}
            <code>app.key</code> during TLS without transmitting the key.
          </span>
        </div>
        <div className="certificate-role">
          <span className="certificate-role-label">KMS trusts</span>
          <strong>Built-in client-issuing CA</strong>
          <span>
            KMS creates and stores this CA in its database, then uses it to verify application
            certificates.
          </span>
        </div>
        <div className="certificate-role">
          <span className="certificate-role-label">Application trusts</span>
          <strong>KMS server CA bundle</strong>
          <span>
            The operator-provided <code>server-ca.crt</code> lets the application verify the KMS
            server certificate.
          </span>
        </div>
      </div>
    </section>
  );
}

function TokenCredentialRoles({ compact = false }: { compact?: boolean }) {
  return (
    <section className={`certificate-map ${compact ? "certificate-map-compact" : ""}`}>
      <div className="certificate-map-heading">
        <h2>Token and server trust</h2>
        <span className="faint text-sm">No client certificate or private key is issued.</span>
      </div>
      <div className="certificate-role-grid certificate-role-grid-token">
        <div className="certificate-role">
          <span className="certificate-role-label">Application sends</span>
          <strong>Bearer token</strong>
          <span>
            Store the one-time token in a secret manager and send it with each request. Anyone who
            obtains it can authenticate as this identity.
          </span>
        </div>
        <div className="certificate-role">
          <span className="certificate-role-label">Application trusts</span>
          <strong>KMS server CA bundle</strong>
          <span>
            The operator-provided <code>server-ca.crt</code> verifies the KMS server. The built-in
            client-issuing CA is not used in this token-only connection.
          </span>
        </div>
      </div>
    </section>
  );
}

export default function IdentitiesPage() {
  const toast = useToast();
  const {
    namespaces,
    loading: namespacesLoading,
    error: namespacesError,
    reload: reloadNamespaces,
  } = useNamespaces();
  const [identities, setIdentities] = useState<Identity[]>([]);
  const [loading, setLoading] = useState(true);
  const [nextToken, setNextToken] = useState("");
  const [pageStack, setPageStack] = useState<string[]>([]);
  const [pageToken, setPageToken] = useState("");
  const loadController = useRef<AbortController | null>(null);

  // Create form.
  const [createOpen, setCreateOpen] = useState(false);
  const [createStep, setCreateStep] = useState(1);
  const [name, setName] = useState("");
  const [identityMode, setIdentityMode] = useState<IdentityMode>("application");
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
  const [revokeCertTarget, setRevokeCertTarget] = useState<{ name: string; serial: string } | null>(
    null,
  );
  const [revokeCertBusy, setRevokeCertBusy] = useState(false);

  const [rotateTarget, setRotateTarget] = useState<string | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<string | null>(null);
  const [actionBusy, setActionBusy] = useState(false);

  const [caBusy, setCaBusy] = useState(false);

  const load = useCallback(
    async (token: string) => {
      loadController.current?.abort();
      const controller = new AbortController();
      loadController.current = controller;
      setLoading(true);
      try {
        const res = await api.listIdentities(100, token || undefined, {
          signal: controller.signal,
        });
        if (loadController.current !== controller) return;
        setIdentities(res.identities ?? []);
        setNextToken(res.next_page_token ?? "");
      } catch (err) {
        if (!isAbortError(err) && loadController.current === controller) {
          toast.error(err, "Failed to load identities");
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
    void load(pageToken);
    return () => loadController.current?.abort();
  }, [load, pageToken]);

  const certsTarget = useMemo(
    () => identities.find((id) => id.name === certsTargetName) ?? null,
    [identities, certsTargetName],
  );
  const certIssueAvailability = certsTarget
    ? identityMethodAvailability(
        certsTarget,
        "mtls",
        namespaces,
        namespacesLoading,
        namespacesError,
      )
    : "unknown";
  const certIssueReason = unavailableMethodReason("mtls", certIssueAvailability);

  const selectedNamespace = useMemo(
    () => namespaces.find((ns) => ns.env === bindNs.env && ns.app === bindNs.app) ?? null,
    [bindNs, namespaces],
  );
  const allowedMethods = selectedNamespace?.allowed_auth_methods ?? [];
  const incompatibleMethods = methods.filter((method) => !allowedMethods.includes(method));
  const authConflict =
    identityMode === "application" && selectedNamespace !== null && incompatibleMethods.length > 0;

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
    setName("");
    setIdentityMode("application");
    setBindNs(NO_NS);
    setMethods(["mtls"]);
    setCertDays(String(DEFAULT_CERT_DAYS));
    setCreateStep(1);
    setCreateOpen(true);
  }

  function changeIdentityMode(mode: IdentityMode) {
    setIdentityMode(mode);
    setBindNs(NO_NS);
    setMethods(mode === "admin" ? ["token"] : ["mtls"]);
  }

  function toggleMethod(method: AuthMethod, on: boolean) {
    const set = new Set(methods);
    if (on) set.add(method);
    else set.delete(method);
    setMethods([...set]);
  }

  function continueToAuthentication() {
    const n = name.trim();
    if (!n) {
      toast.error(new Error("A name is required."), "Missing name");
      return;
    }
    if (identityMode === "application" && namespacesLoading) {
      toast.error(
        new Error("Wait for the namespace list to finish loading."),
        "Namespaces loading",
      );
      return;
    }
    if (identityMode === "application" && namespacesError) {
      toast.error(
        new Error("Retry loading namespaces before continuing."),
        "Namespaces unavailable",
      );
      return;
    }
    if (identityMode === "application" && (!bindNs.env || !bindNs.app)) {
      toast.error(
        new Error("Choose the namespace this application will use."),
        "Missing namespace",
      );
      return;
    }
    if (identityMode === "application" && !selectedNamespace) {
      toast.error(new Error("Choose an existing namespace."), "Unknown namespace");
      return;
    }
    setCreateStep(2);
  }

  async function onCreate() {
    const n = name.trim();
    if (!n) {
      toast.error(new Error("A name is required."), "Missing name");
      setCreateStep(1);
      return;
    }
    if (methods.length === 0) {
      toast.error(
        new Error("Select at least one auth method (mTLS, token, or both)."),
        "No auth method",
      );
      return;
    }
    if (identityMode === "application" && (!selectedNamespace || authConflict)) {
      toast.error(
        new Error("Select a credential method accepted by the application namespace."),
        "Incompatible authentication",
      );
      return;
    }
    const days = Number(certDays);
    if (methods.includes("mtls") && (!Number.isFinite(days) || days <= 0)) {
      toast.error(
        new Error("Certificate lifetime must be a positive number of days."),
        "Invalid certificate lifetime",
      );
      return;
    }
    const ttlSeconds =
      methods.includes("mtls") && Number.isFinite(days) && days > 0 ? Math.round(days * 86400) : 0;
    const namespace =
      identityMode === "application" && bindNs.env && bindNs.app
        ? { env: bindNs.env, app: bindNs.app }
        : null;

    setSaving(true);
    try {
      const res = await api.createIdentity({
        name: n,
        kind: identityMode === "admin" ? "admin" : "client",
        namespace,
        auth_methods: methods,
        cert_ttl_seconds: ttlSeconds,
      });
      setCreateOpen(false);
      setCredentials({
        name: res.identity.name,
        kind: res.identity.kind,
        token: res.token,
        cert: res.cert,
        namespace,
      });
      toast.success(
        identityMode === "application"
          ? "Application identity created"
          : identityMode === "admin"
            ? "Administrator identity created"
            : "Unbound client identity created",
        "Save the credentials below.",
      );
    } catch (err) {
      toast.error(err, "Failed to create identity");
    } finally {
      setSaving(false);
    }
  }

  async function onIssueCert() {
    if (!certsTargetName) return;
    if (certIssueAvailability !== "allowed") {
      toast.error(new Error(certIssueReason), "Certificate issuance unavailable");
      return;
    }
    const days = Number(issueDays);
    const ttlSeconds = Number.isFinite(days) && days > 0 ? Math.round(days * 86400) : 0;
    setIssueBusy(true);
    try {
      const res = await api.issueCert(certsTargetName, ttlSeconds);
      // Close the manage modal so the one-time bundle takes focus, then refresh.
      const name = certsTargetName;
      const namespace = certsTarget?.namespace ?? null;
      const kind = certsTarget?.kind ?? "client";
      setCertsTargetName(null);
      setCredentials({ name, kind, cert: res.cert, namespace });
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
    const target = identities.find((identity) => identity.name === rotateTarget);
    if (!target) return;
    const tokenAvailability = identityMethodAvailability(
      target,
      "token",
      namespaces,
      namespacesLoading,
      namespacesError,
    );
    if (tokenAvailability !== "allowed") {
      toast.error(
        new Error(unavailableMethodReason("token", tokenAvailability)),
        "Token rotation unavailable",
      );
      return;
    }
    setActionBusy(true);
    try {
      const res = await api.rotateIdentity(rotateTarget);
      const name = rotateTarget;
      const namespace = target?.namespace ?? null;
      const kind = target?.kind ?? "client";
      setRotateTarget(null);
      setCredentials({ name, kind, token: res.token, namespace });
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
      toast.success(
        "Client-issuing CA exported",
        "This is for inspecting or verifying KMS-issued client certificates, not server trust.",
      );
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
        subtitle="Connect applications and manage the credentials they use to authenticate to KMS."
        actions={
          <button className="btn btn-primary" onClick={openCreate}>
            Connect application
          </button>
        }
      />

      <CertificateRoles />

      <div className="info-panel mb-16">
        An identity <strong>bound to a namespace</strong> may read, list, and subscribe within that
        namespace with no policy required — the credential <em>is</em> the app (the implicit
        home-namespace grant). Writes and any cross-namespace access still need an explicit policy.
        Client certificates prove possession; bearer tokens do not, so prefer mTLS-only namespaces.
      </div>

      <details className="advanced-panel mb-16">
        <summary>Advanced identity and CA tools</summary>
        <div className="advanced-panel-content">
          <div>
            <strong>Administrator and unbound identities</strong>
            <div className="faint text-sm">
              Open <em>Connect application</em>, then use Advanced identity type in the first step.
            </div>
          </div>
          <div className="advanced-ca-action">
            <div>
              <strong>Export client-issuing CA</strong>
              <div className="faint text-sm">
                For inspection or systems that validate KMS-issued <em>client</em> certificates. It
                is not <code>server-ca.crt</code> and applications must not use it to trust the KMS
                server.
              </div>
            </div>
            <button className="btn" onClick={() => void downloadCa()} disabled={caBusy}>
              {caBusy ? <Spinner /> : null}
              <Download size={16} aria-hidden />
              Export client-issuing CA
            </button>
          </div>
        </div>
      </details>

      {loading ? (
        <TableSkeleton
          headers={["Name", "Kind", "Namespace", "Credentials", "Status", "Created"]}
        />
      ) : identities.length === 0 ? (
        <EmptyState
          icon={<Icon.identity size={20} />}
          title="No identities yet"
          actions={
            <button className="btn btn-primary" onClick={openCreate}>
              Connect application
            </button>
          }
        >
          Create an application identity and issue its first client certificate.
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
                const tokenAvailability = identityMethodAvailability(
                  id,
                  "token",
                  namespaces,
                  namespacesLoading,
                  namespacesError,
                );
                const tokenRotationReason = unavailableMethodReason("token", tokenAvailability);
                const tokenReasonId = `token-rotation-${id.name}`;
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
                        {!id.has_token && certCount === 0 ? <span className="faint">—</span> : null}
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
                          <span
                            className={
                              tokenAvailability === "allowed" ? undefined : "action-unavailable"
                            }
                            title={
                              tokenAvailability === "allowed" ? undefined : tokenRotationReason
                            }
                          >
                            <button
                              className="btn btn-sm"
                              disabled={id.disabled || tokenAvailability !== "allowed"}
                              aria-describedby={
                                tokenAvailability === "allowed" ? undefined : tokenReasonId
                              }
                              onClick={() => setRotateTarget(id.name)}
                            >
                              Rotate token
                            </button>
                            {tokenAvailability !== "allowed" ? (
                              <span id={tokenReasonId} className="action-unavailable-reason">
                                Rotation unavailable: {tokenRotationReason}
                              </span>
                            ) : null}
                          </span>
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
        hasPrevious={pageStack.length > 0}
        onPrevious={goPrevious}
        onReset={goReset}
        showReset={pageStack.length > 0}
      />

      {/* Connect application */}
      <Modal
        open={createOpen}
        wide
        title={
          createStep === 1
            ? "Connect application — choose application"
            : "Connect application — authentication"
        }
        onClose={saving ? () => undefined : () => setCreateOpen(false)}
        footer={
          <>
            <button className="btn" onClick={() => setCreateOpen(false)} disabled={saving}>
              Cancel
            </button>
            {createStep === 2 ? (
              <button className="btn" onClick={() => setCreateStep(1)} disabled={saving}>
                Back
              </button>
            ) : null}
            {createStep === 1 ? (
              <button
                className="btn btn-primary"
                onClick={continueToAuthentication}
                disabled={
                  identityMode === "application" && (namespacesLoading || Boolean(namespacesError))
                }
              >
                Continue to authentication
              </button>
            ) : (
              <button
                className="btn btn-primary"
                onClick={() => void onCreate()}
                disabled={saving || methods.length === 0 || authConflict}
              >
                {saving ? <Spinner /> : null}
                {identityMode === "application"
                  ? "Create application credentials"
                  : "Create identity credentials"}
              </button>
            )}
          </>
        }
      >
        <WizardProgress current={createStep} />
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (createStep === 1) continueToAuthentication();
            else void onCreate();
          }}
        >
          {createStep === 1 ? (
            <>
              <div className="wizard-intro">
                Choose the namespace this application belongs to. It receives implicit read, list,
                and subscribe access within that namespace.
              </div>
              <Field label="Application identity name" hint="Unique name, e.g. gradethis-be">
                <input
                  className="input mono"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="gradethis-be"
                  required
                  autoComplete="off"
                  spellCheck={false}
                />
              </Field>

              {identityMode === "application" ? (
                namespacesLoading ? (
                  <div className="info-panel loading-inline mb-16" role="status">
                    <Spinner />
                    Loading application namespaces…
                  </div>
                ) : namespacesError ? (
                  <div className="danger-panel namespace-load-error mb-16" role="alert">
                    <div>
                      <strong>Could not load application namespaces.</strong>
                      <div className="text-sm">
                        Retry the namespace list before connecting an application.
                      </div>
                    </div>
                    <button className="btn btn-sm" type="button" onClick={reloadNamespaces}>
                      Retry namespace list
                    </button>
                  </div>
                ) : namespaces.length > 0 ? (
                  <Field
                    label="Application namespace"
                    hint="The credential will be bound to this exact environment/application pair."
                  >
                    <div className="form-row namespace-picker-row">
                      <NamespacePicker
                        namespaces={namespaces}
                        value={bindNs}
                        onChange={setBindNs}
                        envId="bind-env"
                        appId="bind-app"
                      />
                    </div>
                  </Field>
                ) : (
                  <div className="warn-panel mb-16">
                    No application namespaces are available.{" "}
                    <Link href="/namespaces">Create a namespace</Link> before connecting an
                    application, or choose an advanced identity type below.
                  </div>
                )
              ) : identityMode === "admin" ? (
                <div className="danger-panel mb-16">
                  Administrator identities are unbound and receive a bearer token with full admin
                  authority. Create one only for trusted operators or automation.
                </div>
              ) : (
                <div className="warn-panel mb-16">
                  This client identity will be unbound. It receives no implicit namespace access;
                  every operation must be granted by an explicit policy.
                </div>
              )}

              <details className="advanced-panel advanced-panel-modal">
                <summary>Advanced identity type</summary>
                <div className="advanced-panel-content">
                  <Field
                    label="Identity type"
                    hint="Most applications should stay namespace-bound."
                  >
                    <select
                      className="select"
                      value={identityMode}
                      onChange={(e) => changeIdentityMode(e.target.value as IdentityMode)}
                    >
                      <option value="application">Namespace-bound application (recommended)</option>
                      <option value="unbound">Unbound client / tooling</option>
                      <option value="admin">Administrator</option>
                    </select>
                  </Field>
                </div>
              </details>
            </>
          ) : (
            <>
              <div className="wizard-selection-summary">
                <div>
                  <span className="faint text-sm">Identity</span>
                  <strong className="mono">{name.trim()}</strong>
                </div>
                <div>
                  <span className="faint text-sm">Namespace</span>
                  <strong className="mono">
                    {identityMode === "application"
                      ? `${bindNs.env}/${bindNs.app}`
                      : identityMode === "admin"
                        ? "administrator (unbound)"
                        : "unbound client"}
                  </strong>
                </div>
              </div>

              {selectedNamespace && identityMode === "application" ? (
                <div className="namespace-auth-summary">
                  <span>This namespace accepts:</span>
                  <div className="row-wrap">
                    {allowedMethods.includes("mtls") ? <Badge kind="accent">mTLS</Badge> : null}
                    {allowedMethods.includes("token") ? <Badge kind="neutral">token</Badge> : null}
                    {allowedMethods.length === 0 ? <Badge kind="danger">none</Badge> : null}
                  </div>
                </div>
              ) : null}

              <Field
                label="Credential method"
                hint={
                  identityMode === "admin"
                    ? "Administrators always receive a bearer token; optionally issue a client certificate too."
                    : "mTLS is recommended. Select token only when the target namespace explicitly permits it."
                }
              >
                <div className="auth-choice auth-choice-recommended">
                  <div className="checkbox-row">
                    <input
                      id="new-mtls"
                      type="checkbox"
                      checked={methods.includes("mtls")}
                      onChange={(e) => toggleMethod("mtls", e.target.checked)}
                    />
                    <label htmlFor="new-mtls">
                      <span className="auth-choice-title">
                        <strong>mTLS client certificate</strong>
                        <Badge kind="success">recommended</Badge>
                      </span>
                      <span className="faint text-sm">
                        Proof of possession: the application must hold both its certificate and
                        private key.
                      </span>
                    </label>
                  </div>
                </div>
                <div className="auth-choice">
                  <div className="checkbox-row">
                    <input
                      id="new-token"
                      type="checkbox"
                      checked={methods.includes("token")}
                      disabled={identityMode === "admin"}
                      onChange={(e) => toggleMethod("token", e.target.checked)}
                    />
                    <label htmlFor="new-token">
                      <span className="auth-choice-title">
                        <strong>Bearer token</strong>
                        {identityMode === "admin" ? <Badge kind="warning">required</Badge> : null}
                      </span>
                      <span className="faint text-sm">
                        Anyone holding the token can use it; deploy it through a secret manager.
                      </span>
                    </label>
                  </div>
                </div>
              </Field>

              {authConflict ? (
                <div className="danger-panel mb-16" role="alert">
                  <strong>Every selected method must be accepted by this namespace.</strong>{" "}
                  Deselect {incompatibleMethods.join(" and ")} or update the namespace before
                  creating credentials. KMS will not issue credentials that this application cannot
                  use.
                </div>
              ) : null}

              {methods.includes("mtls") ? (
                <Field
                  label="Certificate lifetime (days)"
                  hint="90 days is the recommended starting point. Issue a replacement before expiry."
                >
                  <input
                    className="input"
                    type="number"
                    min={1}
                    value={certDays}
                    onChange={(e) => setCertDays(e.target.value)}
                    required
                  />
                </Field>
              ) : null}

              {methods.includes("mtls") ? (
                <CertificateRoles compact />
              ) : (
                <TokenCredentialRoles compact />
              )}
            </>
          )}
        </form>
      </Modal>

      {/* One-time credentials */}
      <CredentialsModal
        key={credentials ? `${credentials.name}:${credentials.cert?.serial ?? "token"}` : "empty"}
        credentials={credentials}
        onClose={() => void closeCredentials()}
      />

      {/* Manage certificates */}
      <Modal
        open={certsTarget !== null && revokeCertTarget === null}
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
            {certIssueAvailability !== "allowed" ? (
              <div className="danger-panel mb-16" role="alert">
                <strong>New certificate issuance is disabled.</strong> {certIssueReason} Update the
                bound namespace to accept mTLS before issuing another certificate. Existing
                certificates can still be inspected and revoked below.
              </div>
            ) : null}
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
                    disabled={certIssueAvailability !== "allowed"}
                    style={{ width: 140 }}
                    value={issueDays}
                    onChange={(e) => setIssueDays(e.target.value)}
                  />
                </div>
                <button
                  className="btn btn-primary"
                  onClick={() => void onIssueCert()}
                  disabled={issueBusy || certIssueAvailability !== "allowed"}
                  title={certIssueAvailability === "allowed" ? undefined : certIssueReason}
                >
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
              <EmptyState icon={<Icon.identity size={20} />} title="No certificates issued">
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
                                    setRevokeCertTarget({
                                      name: certsTarget.name,
                                      serial: cert.serial,
                                    })
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
            certificates stop working on the next RPC; existing watch streams close on the next
            heartbeat.
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

const SNIPPET_LANGUAGES = ["go", "python", "typescript"] as const;
type SnippetLanguage = (typeof SNIPPET_LANGUAGES)[number];

function integrationSnippets(credentials: Credentials): Record<SnippetLanguage, string> {
  const certFile = `${credentials.name}.crt`;
  const keyFile = `${credentials.name}.key`;
  const namespace = credentials.namespace
    ? `${credentials.namespace.env}/${credentials.namespace.app}`
    : "prod/app";
  const goNamespace = credentials.namespace
    ? `    Namespace: "${namespace}",`
    : `    Namespace: "${namespace}", // Required for relative keys on an unbound identity.`;
  const pythonNamespace = credentials.namespace
    ? `    namespace="${namespace}",`
    : `    namespace="${namespace}",  # Replace with the target for this unbound identity.`;
  const typescriptNamespace = credentials.namespace
    ? `  namespace: "${namespace}",`
    : `  namespace: "${namespace}", // Replace with the target for this unbound identity.`;

  if (credentials.cert) {
    return {
      go: `package main

import (
    "log"

    "github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func main() {
    client, err := kmsclient.NewClient(kmsclient.Config{
        Endpoint: "kms.internal:8443",
${goNamespace}
        TLS: kmsclient.MTLSFromFiles(
            "${certFile}",
            "${keyFile}",
            "server-ca.crt",
        ),
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()
}`,
      python: `from kms_paramstore import Client, mtls_from_files

with Client(
    "kms.internal:8443",
${pythonNamespace}
    tls=mtls_from_files(
        "${certFile}",
        "${keyFile}",
        "server-ca.crt",
    ),
) as client:
    # Application work goes here.
    pass`,
      typescript: `import { createClient, mtlsFromFiles } from "@suhaibinator/kms";

const client = createClient({
  endpoint: "kms.internal:8443",
${typescriptNamespace}
  credentials: mtlsFromFiles(
    "${certFile}",
    "${keyFile}",
    "server-ca.crt",
  ),
});

try {
  // Application work goes here.
} finally {
  await client.close();
}`,
    };
  }

  return {
    go: `package main

import (
    "log"
    "os"

    "github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func main() {
    client, err := kmsclient.NewClient(kmsclient.Config{
        Endpoint: "kms.internal:8443",
${goNamespace}
        Token: os.Getenv("KMS_TOKEN"),
        TLS: kmsclient.TLSFromFiles("server-ca.crt"),
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()
}`,
    python: `import os

from kms_paramstore import Client, tls_from_files

with Client(
    "kms.internal:8443",
${pythonNamespace}
    token=os.environ["KMS_TOKEN"],
    tls=tls_from_files("server-ca.crt"),
) as client:
    # Application work goes here.
    pass`,
    typescript: `import { createClient, tlsFromFiles } from "@suhaibinator/kms";

const client = createClient({
  endpoint: "kms.internal:8443",
${typescriptNamespace}
  token: process.env.KMS_TOKEN!,
  credentials: tlsFromFiles("server-ca.crt"),
});

try {
  // Application work goes here.
} finally {
  await client.close();
}`,
  };
}

function CredentialsModal({
  credentials,
  onClose,
}: {
  credentials: Credentials | null;
  onClose: () => void;
}) {
  const [stage, setStage] = useState<3 | 4>(3);
  const [language, setLanguage] = useState<SnippetLanguage>("go");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const snippets = credentials ? integrationSnippets(credentials) : null;

  function downloadBoth() {
    if (!credentials?.cert) return;
    const archive = createStoredZip([
      { name: `${credentials.name}.crt`, content: credentials.cert.cert_pem, mode: 0o644 },
      { name: `${credentials.name}.key`, content: credentials.cert.key_pem, mode: 0o600 },
    ]);
    downloadBlob(`${credentials.name}-mtls-credentials.zip`, archive);
  }

  function onSnippetTabKeyDown(event: React.KeyboardEvent<HTMLButtonElement>, index: number) {
    let nextIndex: number | null = null;
    if (event.key === "ArrowRight") nextIndex = (index + 1) % SNIPPET_LANGUAGES.length;
    else if (event.key === "ArrowLeft") {
      nextIndex = (index - 1 + SNIPPET_LANGUAGES.length) % SNIPPET_LANGUAGES.length;
    } else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = SNIPPET_LANGUAGES.length - 1;
    if (nextIndex === null) return;

    event.preventDefault();
    setLanguage(SNIPPET_LANGUAGES[nextIndex]);
    tabRefs.current[nextIndex]?.focus();
  }

  return (
    <Modal
      open={credentials !== null}
      wide
      dismissible={false}
      title={
        stage === 3
          ? "Save these credentials now"
          : credentials?.kind === "admin"
            ? "Use the administrator identity"
            : "Integrate the application"
      }
      onClose={onClose}
      footer={
        stage === 3 ? (
          <button className="btn btn-primary" onClick={() => setStage(4)}>
            I&apos;ve saved {credentials?.cert && credentials.token ? "them" : "it"} — continue
          </button>
        ) : (
          <>
            <button className="btn" onClick={() => setStage(3)}>
              Back to credentials
            </button>
            <button className="btn btn-primary" onClick={onClose}>
              Done
            </button>
          </>
        )
      }
    >
      {credentials ? (
        <>
          <WizardProgress current={stage} />
          {stage === 3 ? (
            <>
              <div className="warn-panel mb-16">
                <strong>Shown once and never retrievable again.</strong> Store these credentials
                securely for <span className="mono">{credentials.name}</span> before continuing.
              </div>

              {credentials.cert ? (
                <>
                  <div className="credential-deployment mb-8">
                    <div>
                      <strong>Download and deploy the application credential pair</strong>
                      <div className="faint text-sm">
                        The ZIP contains <code>{credentials.name}.crt</code> and{" "}
                        <code>{credentials.name}.key</code>. Extract both into your workload&apos;s
                        secret store or read-only credential volume. Never commit the private key or
                        place it in a browser bundle.
                      </div>
                    </div>
                    <button className="btn btn-primary" onClick={downloadBoth}>
                      <Download size={16} aria-hidden />
                      Download both (.zip)
                    </button>
                  </div>
                  <div className="danger-panel mb-16">
                    <strong>The ZIP itself contains the private key.</strong> Your browser may save
                    the archive with default file permissions. Protect it immediately, extract it
                    only in a secure location, and delete the ZIP after secure deployment.
                  </div>
                </>
              ) : null}

              {credentials.token ? (
                <div className="mb-16">
                  <div className="field-label">Bearer token</div>
                  <div className="faint text-sm mb-8">
                    Store this as <code>KMS_TOKEN</code> in the workload&apos;s secret manager.
                    Never expose it to browser code or source control.
                  </div>
                  <div className="token-reveal">{credentials.token}</div>
                  <div className="row-wrap mt-8">
                    <CopyButton label="Copy token" value={() => credentials.token ?? ""} />
                  </div>
                </div>
              ) : null}

              {credentials.cert ? (
                <div>
                  <div className="field-label">Per-application client certificate (PEM)</div>
                  <div className="faint text-sm mb-8">
                    Serial <span className="mono">{credentials.cert.serial}</span> · expires{" "}
                    {formatUnixMs(credentials.cert.not_after_unix_ms)}
                  </div>
                  <div className="token-reveal credential-pem">{credentials.cert.cert_pem}</div>
                  <div className="row-wrap mt-8 mb-16">
                    <CopyButton
                      label="Copy certificate"
                      value={() => credentials.cert?.cert_pem ?? ""}
                    />
                    <button
                      className="btn btn-sm"
                      onClick={() =>
                        credentials.cert &&
                        downloadText(`${credentials.name}.crt`, credentials.cert.cert_pem)
                      }
                    >
                      <Download size={14} aria-hidden />
                      Download .crt
                    </button>
                  </div>

                  <div className="field-label">Per-application private key (PEM)</div>
                  <div className="danger-panel mb-8">
                    The private key is never stored server-side. If you lose it, revoke this
                    certificate and issue a new one.
                  </div>
                  <div className="token-reveal credential-pem">{credentials.cert.key_pem}</div>
                  <div className="row-wrap mt-8">
                    <CopyButton label="Copy key" value={() => credentials.cert?.key_pem ?? ""} />
                    <button
                      className="btn btn-sm"
                      onClick={() =>
                        credentials.cert &&
                        downloadText(`${credentials.name}.key`, credentials.cert.key_pem)
                      }
                    >
                      <Download size={14} aria-hidden />
                      Download .key
                    </button>
                  </div>
                </div>
              ) : null}
            </>
          ) : (
            <>
              {credentials.cert ? <CertificateRoles compact /> : <TokenCredentialRoles compact />}

              <div className={credentials.cert ? "danger-panel mb-16" : "info-panel mb-16"}>
                <strong>You still need the KMS server CA.</strong> Obtain <code>server-ca.crt</code>{" "}
                from the operator who configured the KMS server certificate. The built-in
                client-issuing CA exported from this page is not server trust
                {credentials.cert ? "; it establishes trust in the opposite direction" : ""}.
              </div>

              {!credentials.namespace && credentials.kind === "client" ? (
                <div className="warn-panel mb-16">
                  <strong>An unbound client has no default namespace.</strong> The snippets below
                  use <code>prod/app</code> as an explicit placeholder; replace it with the target
                  namespace. Alternatively, use absolute keys such as <code>/prod/app/key</code>. A
                  policy grants authorization but does not tell an SDK where a relative key belongs.
                </div>
              ) : null}

              {snippets ? (
                <div className="integration-snippets">
                  <div className="between mb-8">
                    <h2>SDK configuration</h2>
                    <CopyButton
                      label={`Copy ${language} snippet`}
                      value={() => snippets[language]}
                    />
                  </div>
                  <div
                    className="tabs"
                    role="tablist"
                    aria-label="SDK language"
                    aria-orientation="horizontal"
                  >
                    {SNIPPET_LANGUAGES.map((item, index) => (
                      <button
                        key={item}
                        ref={(node) => {
                          tabRefs.current[index] = node;
                        }}
                        id={`snippet-tab-${item}`}
                        className={`tab ${language === item ? "active" : ""}`}
                        type="button"
                        role="tab"
                        tabIndex={language === item ? 0 : -1}
                        aria-selected={language === item}
                        aria-controls="integration-snippet"
                        onClick={() => setLanguage(item)}
                        onKeyDown={(event) => onSnippetTabKeyDown(event, index)}
                      >
                        {item === "go" ? "Go" : item === "python" ? "Python" : "TypeScript"}
                      </button>
                    ))}
                  </div>
                  <pre
                    id="integration-snippet"
                    className="integration-code"
                    role="tabpanel"
                    aria-labelledby={`snippet-tab-${language}`}
                  >
                    <code>{snippets[language]}</code>
                  </pre>
                </div>
              ) : null}

              {credentials.kind === "admin" ? (
                <div className="info-panel">
                  <strong>No policy is required for an administrator identity.</strong> It already
                  has administrative authority across KMS. Protect this credential as a privileged
                  secret and use it only from trusted operator tooling.
                </div>
              ) : (
                <div className="policy-next-step">
                  <div>
                    <strong>
                      {credentials.namespace
                        ? "Reads work now; writes need a policy."
                        : "Choose a target namespace and grant it with a policy."}
                    </strong>
                    <div className="faint text-sm">
                      Create an allow policy with subject <code>{credentials.name}</code>.{" "}
                      {credentials.namespace ? (
                        <>
                          Grant the required write operations for{" "}
                          <code>{displayNamespace(credentials.namespace)}</code>.
                        </>
                      ) : (
                        <>
                          Grant the required operations for each target namespace. The SDK also
                          needs an explicit namespace or absolute key path.
                        </>
                      )}
                    </div>
                  </div>
                  <Link className="btn" href="/policies">
                    Open policies
                  </Link>
                </div>
              )}
            </>
          )}
        </>
      ) : null}
    </Modal>
  );
}
