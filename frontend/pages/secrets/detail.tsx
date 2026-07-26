import Link from "next/link";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError, type ResourceRef } from "@/lib/api";
import type { SecretMetadata, SecretVersion } from "@/lib/types";
import { useToast } from "@/context/ToastContext";
import { useQueryParams } from "@/lib/hooks";
import {
  displayNamespace,
  displayPath,
  formatUnixMs,
  isEmptyJson,
  labelEntries,
  prettyJson,
} from "@/lib/format";
import { base64ByteLength, base64ToUtf8, looksLikeText, utf8ToBase64 } from "@/lib/encoding";
import {
  Badge,
  EmptyState,
  Field,
  JsonView,
  KeyValue,
  PageHeader,
  PageTitle,
  SecretStateBadge,
  Skeleton,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { ConfirmDialog, Modal } from "@/components/Modal";
import CopyButton from "@/components/CopyButton";
import { Icon } from "@/components/icons";

const REVEAL_SECONDS = 30;

// Ephemeral reveal state. This is the only place a secret plaintext lives in
// the client, and only inside component state — never logged or toasted.
interface Revealed {
  version: number;
  valueBase64: string;
  contentType: string;
  isText: boolean;
}

export default function SecretDetailPage() {
  const router = useRouter();
  const toast = useToast();
  const { values, ready } = useQueryParams(["env", "app", "key"]);
  const env = values.env ?? "";
  const app = values.app ?? "";
  const key = values.key ?? "";
  const hasRef = !!env && !!app && !!key;
  const ref = useMemo<ResourceRef>(() => ({ env, app, key }), [env, app, key]);

  const [secret, setSecret] = useState<SecretMetadata | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);

  // Reveal flow.
  const [revealTarget, setRevealTarget] = useState<number | null>(null); // version pending confirm
  const [revealBusy, setRevealBusy] = useState(false);
  const [revealed, setRevealed] = useState<Revealed | null>(null);
  const [secondsLeft, setSecondsLeft] = useState(0);
  const [selectedVersion, setSelectedVersion] = useState<number | null>(null);

  // Version actions.
  const [confirm, setConfirm] = useState<
    | { kind: "disable" | "enable" | "promote"; version: number }
    | { kind: "destroy"; version: number }
    | { kind: "delete" }
    | null
  >(null);
  const [actionBusy, setActionBusy] = useState(false);

  // New version modal.
  const [newVersionOpen, setNewVersionOpen] = useState(false);

  const load = useCallback(async () => {
    if (!hasRef) return;
    setLoading(true);
    setNotFound(false);
    try {
      const res = await api.secretMetadata(ref);
      setSecret(res.secret);
      const cur = res.secret.labels?.current;
      setSelectedVersion(typeof cur === "number" ? cur : res.secret.versions?.[0]?.version ?? null);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setNotFound(true);
      } else {
        toast.error(err, "Failed to load secret");
      }
    } finally {
      setLoading(false);
    }
  }, [hasRef, ref, toast]);

  useEffect(() => {
    if (ready && hasRef) void load();
  }, [ready, hasRef, load]);

  // Clear any revealed value when navigating away or reloading the secret.
  useEffect(() => {
    return () => setRevealed(null);
  }, [env, app, key]);

  // Auto-hide countdown for the revealed value.
  useEffect(() => {
    if (!revealed) {
      setSecondsLeft(0);
      return;
    }
    const expiresAt = Date.now() + REVEAL_SECONDS * 1000;
    setSecondsLeft(REVEAL_SECONDS);
    const iv = window.setInterval(() => {
      const rem = Math.ceil((expiresAt - Date.now()) / 1000);
      if (rem <= 0) {
        setRevealed(null);
        setSecondsLeft(0);
      } else {
        setSecondsLeft(rem);
      }
    }, 250);
    return () => window.clearInterval(iv);
  }, [revealed]);

  const doReveal = useCallback(
    async (version: number) => {
      if (!hasRef) return;
      setRevealBusy(true);
      try {
        const res = await api.revealSecret(ref, version, "");
        setRevealed({
          version: res.version,
          valueBase64: res.value_base64,
          contentType: res.content_type,
          isText: looksLikeText(res.value_base64),
        });
        // No value in the toast — metadata only.
        toast.success(`Revealed version ${res.version}`, "Recorded in the audit log.");
      } catch (err) {
        toast.error(err, "Reveal failed");
      } finally {
        setRevealBusy(false);
        setRevealTarget(null);
      }
    },
    [hasRef, ref, toast],
  );

  const runAction = useCallback(async () => {
    if (!hasRef || !confirm) return;
    setActionBusy(true);
    try {
      if (confirm.kind === "delete") {
        await api.deleteSecret(ref);
        toast.success("Secret deleted", displayPath(ref));
        setConfirm(null);
        await router.push(`/secrets?env=${encodeURIComponent(env)}&app=${encodeURIComponent(app)}`);
        return;
      }
      if (confirm.kind === "promote") {
        const res = await api.promoteSecret(ref, confirm.version);
        toast.success(`Promoted v${res.current_version} to current`);
      } else if (confirm.kind === "destroy") {
        await api.destroySecret(ref, confirm.version);
        toast.success(`Destroyed version ${confirm.version}`);
        // If the destroyed version was revealed, hide it.
        setRevealed((r) => (r && r.version === confirm.version ? null : r));
      } else {
        const enable = confirm.kind === "enable";
        await api.disableSecret(ref, confirm.version, enable);
        toast.success(
          enable ? `Enabled version ${confirm.version}` : `Disabled version ${confirm.version}`,
        );
        if (!enable) setRevealed((r) => (r && r.version === confirm.version ? null : r));
      }
      setConfirm(null);
      await load();
    } catch (err) {
      toast.error(err, "Action failed");
    } finally {
      setActionBusy(false);
    }
  }, [hasRef, ref, env, app, confirm, toast, load, router]);

  const backLink = hasRef
    ? `/secrets?env=${encodeURIComponent(env)}&app=${encodeURIComponent(app)}`
    : "/secrets";

  // Header and card frames come straight from the URL, so they paint at once
  // and only the values fill in — no full-page spinner swap.
  if (!ready || loading) {
    return (
      <>
        <PageHeader
          documentTitle={hasRef ? displayPath(ref) : "Secret"}
          title={hasRef ? <span className="mono">{displayPath(ref)}</span> : "Secret"}
          subtitle={
            <Link href={backLink} className="text-sm">
              ← {hasRef ? displayNamespace(ref) : "Secrets"}
            </Link>
          }
        />
        <div className="card">
          <div className="card-title">Metadata</div>
          <Skeleton height={96} />
        </div>
        <div className="card">
          <div className="card-title">Secret value</div>
          <Skeleton height={64} />
        </div>
        <div className="card">
          <div className="card-title">Versions</div>
          <TableSkeleton
            headers={["Version", "State", "Created by", "Created", "Expires"]}
            rows={3}
          />
        </div>
      </>
    );
  }
  if (!hasRef) {
    return (
      <>
        <PageTitle title="Secret" />
        <EmptyState
          icon={<Icon.secret size={20} />}
          title="No secret specified"
          actions={
            <Link className="btn" href="/secrets">
              Browse secrets
            </Link>
          }
        >
          Provide ?env=, ?app=, and ?key= query parameters.
        </EmptyState>
      </>
    );
  }
  if (notFound || !secret) {
    return (
      <>
        <PageHeader
          title="Secret not found"
          actions={
            <Link className="btn" href={backLink}>
              Back to secrets
            </Link>
          }
        />
        <EmptyState icon={<Icon.secret size={20} />} title="Not found">
          No secret exists at <span className="mono">{displayPath(ref)}</span>.
        </EmptyState>
      </>
    );
  }

  const current = secret.labels?.current;
  const enabledVersions = secret.versions.filter((v) => v.state === "enabled");

  return (
    <>
      <PageHeader
        documentTitle={displayPath(ref)}
        title={<span className="mono">{displayPath(ref)}</span>}
        subtitle={
          <Link href={backLink} className="text-sm">
            ← {displayNamespace(ref)}
          </Link>
        }
        actions={
          <>
            <button className="btn" onClick={() => setNewVersionOpen(true)}>
              New version
            </button>
            <button className="btn btn-danger" onClick={() => setConfirm({ kind: "delete" })}>
              Delete
            </button>
          </>
        }
      />

      <div className="card">
        <div className="card-title">Metadata</div>
        <KeyValue
          rows={[
            ["Namespace", <span className="mono" key="ns">{displayNamespace(ref)}</span>],
            ["Key", <span className="mono" key="key">{key}</span>],
            ["Content type", secret.content_type || "—"],
            [
              "Mode",
              secret.client_bound ? (
                <Badge kind="warning">client-bound</Badge>
              ) : (
                <Badge kind="neutral">standard (master-key)</Badge>
              ),
            ],
            ["Access token", secret.has_access_token ? "yes" : "no"],
            ["Current version", typeof current === "number" ? `v${current}` : "—"],
            ["Created", formatUnixMs(secret.created_at_unix_ms)],
            ["Updated", formatUnixMs(secret.updated_at_unix_ms)],
            [
              "Labels",
              labelEntries(secret.labels).length ? (
                <div className="row-wrap">
                  {labelEntries(secret.labels).map(([k, v]) => (
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
        {!isEmptyJson(secret.metadata_json) ? (
          <div className="mt-16">
            <div className="field-label">Metadata JSON</div>
            <JsonView raw={prettyJson(secret.metadata_json)} />
          </div>
        ) : null}
      </div>

      {/* Reveal */}
      <div className="card">
        <div className="card-title">Secret value</div>
        {secret.client_bound ? (
          <div className="info-panel">
            <strong>Client-bound — value cannot be revealed here.</strong>
            <div className="mt-8">
              This secret is encrypted with a key derived from the consuming
              application&apos;s access token, which the server never stores in a
              recoverable form. The server cannot decrypt it on its own, so there
              is no reveal flow. Only the application holding the token can read
              the value.
            </div>
          </div>
        ) : revealed ? (
          <div className="reveal-box">
            <div className="between mb-8">
              <div className="row-wrap">
                <Badge kind="accent">version {revealed.version}</Badge>
                <span className="faint text-sm">{revealed.contentType || "value"}</span>
              </div>
              <div className="row-wrap">
                <CopyButton
                  label="Copy value"
                  value={() =>
                    revealed.isText ? base64ToUtf8(revealed.valueBase64) : revealed.valueBase64
                  }
                />
                <button className="btn btn-sm" onClick={() => setRevealed(null)}>
                  Hide now
                </button>
              </div>
            </div>
            {revealed.isText ? (
              <div className="reveal-value masked" tabIndex={0} title="Hover, focus, or tap to reveal">
                {base64ToUtf8(revealed.valueBase64)}
              </div>
            ) : (
              <div>
                <div className="warn-panel mb-8">
                  Binary value ({base64ByteLength(revealed.valueBase64)} bytes) — shown
                  base64-encoded.
                </div>
                <div
                  className="reveal-value masked"
                  tabIndex={0}
                  title="Hover, focus, or tap to reveal"
                >
                  {revealed.valueBase64}
                </div>
              </div>
            )}
            <div className="reveal-countdown">
              Auto-hides in {secondsLeft}s. Value is blurred until you hover, focus, or tap it.
            </div>
          </div>
        ) : (
          <div>
            <div className="warn-panel mb-16">
              Revealing decrypts the secret and records an audit event. The value
              auto-hides after {REVEAL_SECONDS} seconds.
            </div>
            <div className="row-wrap">
              <label className="field-label" htmlFor="reveal-version" style={{ margin: 0 }}>
                Version
              </label>
              <select
                id="reveal-version"
                className="select"
                style={{ width: "auto" }}
                value={selectedVersion ?? ""}
                onChange={(e) => setSelectedVersion(Number(e.target.value))}
              >
                {enabledVersions.length === 0 ? (
                  <option value="">no enabled versions</option>
                ) : (
                  enabledVersions.map((v) => (
                    <option key={v.version} value={v.version}>
                      v{v.version}
                      {v.version === current ? " (current)" : ""}
                    </option>
                  ))
                )}
              </select>
              <button
                className="btn btn-primary"
                disabled={selectedVersion === null || enabledVersions.length === 0}
                onClick={() => selectedVersion !== null && setRevealTarget(selectedVersion)}
              >
                Reveal secret
              </button>
            </div>
          </div>
        )}
      </div>

      {/* Versions */}
      <div className="card">
        <div className="card-title">Versions</div>
        {secret.versions.length === 0 ? (
          <EmptyState icon={<Icon.secret size={20} />} title="No versions" />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Version</th>
                  <th>State</th>
                  <th>Created by</th>
                  <th>Created</th>
                  <th>Expires</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {[...secret.versions]
                  .sort((a, b) => b.version - a.version)
                  .map((v) => (
                    <VersionRow
                      key={v.version}
                      v={v}
                      isCurrent={v.version === current}
                      clientBound={secret.client_bound}
                      onReveal={(ver) => setRevealTarget(ver)}
                      onConfirm={setConfirm}
                    />
                  ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Reveal confirmation */}
      <ConfirmDialog
        open={revealTarget !== null}
        title="Reveal secret value?"
        message={
          <>
            You are about to decrypt and display version {revealTarget} of{" "}
            <span className="mono">{displayPath(ref)}</span>. This is recorded in the
            audit log. The value will auto-hide after {REVEAL_SECONDS} seconds.
          </>
        }
        confirmLabel="Reveal"
        busy={revealBusy}
        onConfirm={() => revealTarget !== null && doReveal(revealTarget)}
        onCancel={() => setRevealTarget(null)}
      />

      {/* Version / delete confirmations */}
      <ConfirmDialog
        open={confirm !== null && confirm.kind !== "destroy"}
        title={
          confirm?.kind === "delete"
            ? "Delete secret?"
            : confirm?.kind === "promote"
              ? "Promote version?"
              : confirm?.kind === "enable"
                ? "Enable version?"
                : "Disable version?"
        }
        danger={confirm?.kind === "delete" || confirm?.kind === "disable"}
        message={
          confirm?.kind === "delete" ? (
            <>
              This deletes the secret <span className="mono">{displayPath(ref)}</span> and all
              of its versions.
            </>
          ) : confirm?.kind === "promote" ? (
            <>Make version {confirm.version} the current version?</>
          ) : confirm?.kind === "enable" ? (
            <>Re-enable version {confirm.version} so it can be read again?</>
          ) : (
            <>Disable version {confirm?.version}? It can no longer be retrieved until re-enabled.</>
          )
        }
        confirmLabel={
          confirm?.kind === "delete"
            ? "Delete secret"
            : confirm?.kind === "promote"
              ? "Promote"
              : confirm?.kind === "enable"
                ? "Enable"
                : "Disable"
        }
        busy={actionBusy}
        onConfirm={runAction}
        onCancel={() => setConfirm(null)}
      />

      {/* Destroy requires typed confirmation (irreversible) */}
      <ConfirmDialog
        open={confirm?.kind === "destroy"}
        title="Destroy version — irreversible"
        danger
        requireText="DESTROY"
        message={
          <>
            Destroying version {confirm?.kind === "destroy" ? confirm.version : ""} permanently
            erases its key material. The value can never be recovered. This cannot be undone.
          </>
        }
        confirmLabel="Destroy version"
        busy={actionBusy}
        onConfirm={runAction}
        onCancel={() => setConfirm(null)}
      />

      <NewVersionModal
        open={newVersionOpen}
        secret={secret}
        onClose={() => setNewVersionOpen(false)}
        onSaved={() => {
          setNewVersionOpen(false);
          void load();
        }}
      />
    </>
  );
}

function VersionRow({
  v,
  isCurrent,
  clientBound,
  onReveal,
  onConfirm,
}: {
  v: SecretVersion;
  isCurrent: boolean;
  clientBound: boolean;
  onReveal: (version: number) => void;
  onConfirm: (
    c:
      | { kind: "disable" | "enable" | "promote"; version: number }
      | { kind: "destroy"; version: number },
  ) => void;
}) {
  const destroyed = v.state === "destroyed";
  return (
    <tr>
      <td>
        <div className="row-wrap">
          v{v.version}
          {isCurrent ? <Badge kind="accent">current</Badge> : null}
        </div>
      </td>
      <td>
        <SecretStateBadge state={v.state} />
      </td>
      <td>{v.created_by || <span className="faint">—</span>}</td>
      <td className="nowrap">{formatUnixMs(v.created_at_unix_ms)}</td>
      <td className="nowrap">
        {v.expires_at_unix_ms > 0 ? (
          formatUnixMs(v.expires_at_unix_ms)
        ) : (
          <span className="faint">never</span>
        )}
      </td>
      <td>
        <div className="row-actions">
          {!clientBound && v.state === "enabled" ? (
            <button className="btn btn-sm" onClick={() => onReveal(v.version)}>
              Reveal
            </button>
          ) : null}
          {!isCurrent && v.state === "enabled" ? (
            <button
              className="btn btn-sm"
              onClick={() => onConfirm({ kind: "promote", version: v.version })}
            >
              Promote
            </button>
          ) : null}
          {v.state === "enabled" ? (
            <button
              className="btn btn-sm"
              onClick={() => onConfirm({ kind: "disable", version: v.version })}
            >
              Disable
            </button>
          ) : v.state === "disabled" ? (
            <button
              className="btn btn-sm"
              onClick={() => onConfirm({ kind: "enable", version: v.version })}
            >
              Enable
            </button>
          ) : null}
          {!destroyed ? (
            <button
              className="btn btn-sm btn-danger"
              onClick={() => onConfirm({ kind: "destroy", version: v.version })}
            >
              Destroy
            </button>
          ) : null}
        </div>
      </td>
    </tr>
  );
}

function NewVersionModal({
  open,
  secret,
  onClose,
  onSaved,
}: {
  open: boolean;
  secret: SecretMetadata;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState(secret.content_type || "text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [clientToken, setClientToken] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (open) {
      setValue("");
      setContentType(secret.content_type || "text/plain");
      setMetadataJson("{}");
      setClientToken("");
    }
  }, [open, secret.content_type]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!value) {
      toast.error(new Error("Enter a value for the new version."), "Missing value");
      return;
    }
    if (secret.client_bound && !clientToken.trim()) {
      toast.error(
        new Error("The client access token is required to add a version to a client-bound secret."),
        "Missing client token",
      );
      return;
    }
    setSaving(true);
    try {
      const res = await api.createSecret(
        {
          env: secret.env,
          app: secret.app,
          key: secret.key,
          value_base64: utf8ToBase64(value),
          content_type: contentType.trim() || "text/plain",
          metadata_json: metadataJson.trim() || "{}",
          client_bound: secret.client_bound,
          generate_access_token: false,
          expires_at_unix_ms: 0,
        },
        secret.client_bound ? clientToken.trim() : undefined,
      );
      // Clear the plaintext from the field immediately.
      setValue("");
      setClientToken("");
      toast.success(`Created version ${res.version}`, "New version is now current.");
      onSaved();
    } catch (err) {
      toast.error(err, "Failed to create version");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={open}
      title="New secret version"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={saving}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={submit} disabled={saving}>
            {saving ? <Spinner /> : null}
            Save new version
          </button>
        </>
      }
    >
      <form onSubmit={submit}>
        <Field label="Value" hint="Stored encrypted. The new version becomes current.">
          <textarea
            className="textarea mono"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="secret value…"
            autoComplete="off"
            spellCheck={false}
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
        {secret.client_bound ? (
          <Field
            label="Client access token"
            hint="Required. The existing token re-wraps the new version's key. It is sent once and never stored."
          >
            <input
              className="input mono"
              type="password"
              value={clientToken}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setClientToken(e.target.value)}
              placeholder="client access token"
            />
          </Field>
        ) : null}
      </form>
    </Modal>
  );
}
