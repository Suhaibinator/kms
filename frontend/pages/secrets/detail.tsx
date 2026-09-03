import { ArrowLeft } from "lucide-react";
import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Icon } from "@/components/icons";
import { JsonEditor } from "@/components/JsonEditor";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { SecretContentTypeSelect } from "@/components/secrets/SecretContentTypeSelect";
import { SecretValueField } from "@/components/secrets/SecretValueField";
import {
  Badge,
  EmptyState,
  Field,
  Input,
  JsonView,
  KeyValue,
  PageHeader,
  PageTitle,
  SecretStateBadge,
  Skeleton,
  Spinner,
  TableSkeleton,
} from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError, type ResourceRef } from "@/lib/api";
import { crumbs } from "@/lib/crumbs";
import {
  base64ByteLength,
  base64ToUtf8,
  looksLikeText,
  secretValueBase64,
  validateSecretValue,
} from "@/lib/encoding";
import {
  displayNamespace,
  displayPath,
  formatUnixMs,
  isEmptyJson,
  labelEntries,
  prettyJson,
} from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors, useLatestRequest, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { SecretMetadata, SecretVersion } from "@/lib/types";
import { validateMetadataJson } from "@/lib/validation";

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
  const refKey = `${env}\u0000${app}\u0000${key}`;
  const activeRefKey = useRef(refKey);
  // Update during render so a response cannot land in the gap between a route
  // change rendering and its effect cleanup aborting the previous request.
  activeRefKey.current = refKey;

  const [secret, setSecret] = useState<SecretMetadata | null>(null);
  const [loadState, setLoadState] = useState<
    "idle" | "loading" | "success" | "not-found" | "error"
  >("idle");
  // A reload triggered by an action refreshes in place; only a first load (or a
  // change of ref) is allowed to blank the page.
  const [refreshing, setRefreshing] = useState(false);
  const request = useLatestRequest();
  const revealRequest = useLatestRequest();

  // Reveal flow.
  const [revealTarget, setRevealTarget] = useState<number | null>(null); // version pending confirm
  const [revealToken, setRevealToken] = useState("");
  const [revealBusy, setRevealBusy] = useState(false);
  const [revealed, setRevealed] = useState<Revealed | null>(null);
  const [valueVisible, setValueVisible] = useState(false);
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

  const load = useCallback(
    async (options?: { background?: boolean }) => {
      if (!hasRef) return;
      const run = request.begin();
      const background = options?.background === true;
      if (background) setRefreshing(true);
      else {
        setLoadState("loading");
        setSecret(null);
      }
      try {
        const res = await api.secretMetadata(ref, { signal: run.signal });
        if (!run.current) return;
        setSecret(res.secret);
        // The reveal select only offers enabled versions, so defaulting to a
        // disabled `current` would leave it blank with Reveal still enabled.
        const enabled = (res.secret.versions ?? []).filter((v) => v.state === "enabled");
        const cur = res.secret.labels?.current;
        setSelectedVersion(
          typeof cur === "number" && enabled.some((v) => v.version === cur)
            ? cur
            : ([...enabled].sort((a, b) => b.version - a.version)[0]?.version ?? null),
        );
        setLoadState("success");
      } catch (err) {
        if (!run.current || isAbortError(err)) return;
        if (err instanceof ApiError && err.status === 404) {
          setLoadState("not-found");
        } else {
          // A failed background refresh keeps the data it already has; only a
          // foreground load has nothing to fall back to.
          if (!background) setLoadState("error");
          toast.error(err, "Failed to load secret");
        }
      } finally {
        if (run.current) setRefreshing(false);
      }
    },
    [hasRef, ref, request, toast],
  );

  useEffect(() => {
    if (!ready) return;
    revealRequest.abort();
    setRevealTarget(null);
    setRevealBusy(false);
    if (hasRef) {
      setRevealed(null);
      setValueVisible(false);
      setRevealToken("");
      void load();
    } else {
      setLoadState("idle");
      setSecret(null);
    }
    return () => {
      request.abort();
      revealRequest.abort();
    };
  }, [ready, hasRef, load, request, revealRequest]);

  // Auto-hide countdown for the revealed value.
  useEffect(() => {
    if (!revealed) {
      setSecondsLeft(0);
      setValueVisible(false);
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
      const run = revealRequest.begin();
      const token = secret?.client_bound ? revealToken : undefined;
      // Clear the credential from React state as the request starts. The local
      // copy exists only for this in-flight call and is never persisted.
      setRevealToken("");
      setRevealBusy(true);
      try {
        const res = await api.revealSecret(ref, version, "", token, { signal: run.signal });
        if (!run.current || activeRefKey.current !== refKey) return;
        setRevealed({
          version: res.version,
          valueBase64: res.value_base64,
          contentType: res.content_type,
          isText: looksLikeText(res.value_base64),
        });
        setValueVisible(true);
        // No value in the toast — metadata only.
        toast.success(`Revealed version ${res.version}`, "Recorded in the audit log.");
      } catch (err) {
        if (!run.current || activeRefKey.current !== refKey || isAbortError(err)) return;
        toast.error(err, "Reveal failed");
      } finally {
        if (run.current && activeRefKey.current === refKey) {
          setRevealBusy(false);
          setRevealTarget(null);
        }
      }
    },
    [hasRef, ref, refKey, revealToken, revealRequest, secret?.client_bound, toast],
  );

  const openReveal = useCallback((version: number) => {
    setRevealToken("");
    setRevealTarget(version);
  }, []);

  const closeReveal = useCallback(() => {
    setRevealToken("");
    setRevealTarget(null);
  }, []);

  const runAction = useCallback(async () => {
    if (!hasRef || !confirm) return;
    setActionBusy(true);
    try {
      if (confirm.kind === "delete") {
        await api.deleteSecret(ref);
        toast.success("Secret deleted", displayPath(ref));
        setConfirm(null);
        await router.push(links.secrets({ env, app }));
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
      await load({ background: true });
    } catch (err) {
      toast.error(err, "Action failed");
    } finally {
      setActionBusy(false);
    }
  }, [hasRef, ref, env, app, confirm, toast, load, router]);

  const backLink = hasRef ? links.secrets({ env, app }) : links.secrets();
  const trail = hasRef ? crumbs.secret(ref) : undefined;

  // Header and card frames come straight from the URL, so they paint at once
  // and only the values fill in — no full-page spinner swap.
  if (!ready || (hasRef && (loadState === "idle" || loadState === "loading"))) {
    return (
      <>
        <PageHeader
          documentTitle={hasRef ? displayPath(ref) : "Secret"}
          title={hasRef ? <span className="mono">{displayPath(ref)}</span> : "Secret"}
          breadcrumbs={trail}
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
            <ButtonLink variant="outline" href={links.secrets()}>
              Browse secrets
            </ButtonLink>
          }
        >
          Provide ?env=, ?app=, and ?key= query parameters.
        </EmptyState>
      </>
    );
  }
  if (loadState === "not-found") {
    return (
      <>
        <PageHeader
          title="Secret not found"
          breadcrumbs={trail}
          actions={
            <ButtonLink variant="outline" href={backLink}>
              <ArrowLeft size={16} aria-hidden /> Back to secrets
            </ButtonLink>
          }
        />
        <EmptyState icon={<Icon.secret size={20} />} title="Not found">
          No secret exists at <span className="mono">{displayPath(ref)}</span>.
        </EmptyState>
      </>
    );
  }
  if (loadState === "error" || !secret) {
    return (
      <>
        <PageHeader
          title="Could not load secret"
          breadcrumbs={trail}
          actions={<Button onClick={() => void load()}>Try again</Button>}
        />
        <EmptyState icon={<Icon.secret size={20} />} title="Secret unavailable">
          The server could not load <span className="mono">{displayPath(ref)}</span>. Check the
          connection and try again.
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
        title={
          <span className="row-wrap">
            <span className="mono">{displayPath(ref)}</span>
            {refreshing ? <Spinner /> : null}
          </span>
        }
        subtitle={displayNamespace(ref)}
        breadcrumbs={trail}
        actions={
          <>
            <Button variant="outline" onClick={() => setNewVersionOpen(true)}>
              New version
            </Button>
            <Button variant="destructive" onClick={() => setConfirm({ kind: "delete" })}>
              Delete
            </Button>
          </>
        }
      />

      <div className="card">
        <div className="card-title">Metadata</div>
        <KeyValue
          rows={[
            [
              "Namespace",
              <span className="mono" key="ns">
                {displayNamespace(ref)}
              </span>,
            ],
            [
              "Key",
              <span className="mono" key="key">
                {key}
              </span>,
            ],
            ["Content type", secret.content_type || "—"],
            [
              "Mode",
              secret.client_bound ? (
                <Badge kind="warning" key="mode">
                  client-bound
                </Badge>
              ) : (
                <Badge kind="neutral" key="mode">
                  standard (master-key)
                </Badge>
              ),
            ],
            ["Access token", secret.has_access_token ? "yes" : "no"],
            ["Current version", typeof current === "number" ? `v${current}` : "—"],
            ["Created", formatUnixMs(secret.created_at_unix_ms)],
            ["Updated", formatUnixMs(secret.updated_at_unix_ms)],
            [
              "Labels",
              labelEntries(secret.labels).length ? (
                <div className="row-wrap" key="labels">
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
          <div className="mt-4">
            <div className="field-label">Metadata JSON</div>
            <JsonView raw={prettyJson(secret.metadata_json)} copyLabel="Copy metadata" />
          </div>
        ) : null}
      </div>

      {/* Reveal */}
      <div className="card">
        <div className="card-title">Secret value</div>
        {revealed ? (
          <div className="reveal-box">
            <div className="between mb-2">
              <div className="row-wrap">
                <Badge kind="accent">version {revealed.version}</Badge>
                <span className="faint text-sm">{revealed.contentType || "value"}</span>
              </div>
              <div className="row-wrap">
                {valueVisible ? (
                  <CopyButton
                    label="Copy value"
                    value={() =>
                      revealed.isText ? base64ToUtf8(revealed.valueBase64) : revealed.valueBase64
                    }
                  />
                ) : null}
                <Button
                  variant="outline"
                  size="sm"
                  aria-expanded={valueVisible}
                  aria-controls="revealed-secret-value"
                  onClick={() => setValueVisible((visible) => !visible)}
                >
                  {valueVisible ? "Hide value" : "Show value"}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setRevealed(null);
                    setValueVisible(false);
                  }}
                >
                  Forget value
                </Button>
              </div>
            </div>
            {valueVisible ? (
              revealed.isText ? (
                <div id="revealed-secret-value" className="reveal-value">
                  {base64ToUtf8(revealed.valueBase64)}
                </div>
              ) : (
                <div id="revealed-secret-value">
                  <div className="warn-panel mb-2">
                    Binary value ({base64ByteLength(revealed.valueBase64)} bytes) — shown
                    base64-encoded.
                  </div>
                  <div className="reveal-value">{revealed.valueBase64}</div>
                </div>
              )
            ) : (
              <div id="revealed-secret-value" className="secret-concealed" aria-live="polite">
                Value concealed. Choose “Show value” to place the plaintext on screen.
              </div>
            )}
            <div className="reveal-countdown">Decrypted value is forgotten in {secondsLeft}s.</div>
          </div>
        ) : (
          <div>
            <div className="warn-panel mb-4">
              {secret.client_bound
                ? "Revealing this client-bound value requires the token used for the selected version. The token is sent only in this request body and is not stored by the console. "
                : "Revealing decrypts the secret and records an audit event. "}
              The value auto-hides after {REVEAL_SECONDS} seconds.
            </div>
            <div className="row-wrap">
              <label className="field-label" htmlFor="reveal-version">
                Version
              </label>
              <AppSelect
                id="reveal-version"
                className="w-44"
                value={selectedVersion === null ? "" : String(selectedVersion)}
                disabled={enabledVersions.length === 0}
                onValueChange={(version) => setSelectedVersion(version ? Number(version) : null)}
                placeholder="No enabled versions"
                options={enabledVersions.map((version) => ({
                  value: String(version.version),
                  label: `v${version.version}${version.version === current ? " (current)" : ""}`,
                }))}
              />
              <Button
                disabled={selectedVersion === null || enabledVersions.length === 0}
                onClick={() => selectedVersion !== null && openReveal(selectedVersion)}
              >
                Reveal secret
              </Button>
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
                      onReveal={openReveal}
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
            <span className="mono">{displayPath(ref)}</span>. This is recorded in the audit log. The
            value will auto-hide after {REVEAL_SECONDS} seconds.
            {secret.client_bound ? (
              <Field
                label="Token for this version"
                hint="The token is used only for this reveal request and is not saved."
                className="mt-4"
              >
                <Input
                  type="password"
                  value={revealToken}
                  required
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(event) => setRevealToken(event.target.value)}
                />
              </Field>
            ) : null}
          </>
        }
        confirmLabel="Reveal"
        busy={revealBusy}
        confirmDisabled={secret.client_bound && revealToken.length === 0}
        onConfirm={() => revealTarget !== null && doReveal(revealTarget)}
        onCancel={closeReveal}
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
              This deletes the secret <span className="mono">{displayPath(ref)}</span> and all of
              its versions.
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
            Destroying version {confirm?.kind === "destroy" ? confirm.version : ""} of{" "}
            <span className="mono">{displayPath(ref)}</span> permanently erases its key material.
            The value can never be recovered. This cannot be undone.
            {confirm?.kind === "destroy" && confirm.version === current ? (
              <div className="mt-2">
                <strong>This is the current version</strong> — applications reading it will start
                failing.
              </div>
            ) : null}
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
          void load({ background: true });
        }}
      />
    </>
  );
}

function VersionRow({
  v,
  isCurrent,
  onReveal,
  onConfirm,
}: {
  v: SecretVersion;
  isCurrent: boolean;
  onReveal: (version: number) => void;
  onConfirm: (
    c:
      | { kind: "disable" | "enable" | "promote"; version: number }
      | { kind: "destroy"; version: number },
  ) => void;
}) {
  const destroyed = v.state === "destroyed";
  const expired = v.expires_at_unix_ms > 0 && v.expires_at_unix_ms <= Date.now();
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
          <div className="row-wrap">
            {formatUnixMs(v.expires_at_unix_ms)}
            {expired ? <Badge kind="warning">expired</Badge> : null}
          </div>
        ) : (
          <span className="faint">never</span>
        )}
      </td>
      <td>
        <div className="row-actions">
          {v.state === "enabled" ? (
            <Button variant="outline" size="sm" onClick={() => onReveal(v.version)}>
              Reveal
            </Button>
          ) : null}
          {!isCurrent && v.state === "enabled" ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => onConfirm({ kind: "promote", version: v.version })}
            >
              Promote
            </Button>
          ) : null}
          {v.state === "enabled" ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => onConfirm({ kind: "disable", version: v.version })}
            >
              Disable
            </Button>
          ) : v.state === "disabled" ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => onConfirm({ kind: "enable", version: v.version })}
            >
              Enable
            </Button>
          ) : null}
          {!destroyed ? (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => onConfirm({ kind: "destroy", version: v.version })}
            >
              Destroy
            </Button>
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
  const [alreadyBase64, setAlreadyBase64] = useState(false);
  const [contentType, setContentType] = useState(secret.content_type || "text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [clientToken, setClientToken] = useState("");
  const [saving, setSaving] = useState(false);
  const errors = useFieldErrors<"value" | "metadata" | "token">();
  const { reset: resetErrors } = errors;
  const valueRef = useRef<HTMLElement | null>(null);
  const { formRef, requestFocus } = useFocusFirstInvalid();

  useEffect(() => {
    if (open) {
      setValue("");
      setAlreadyBase64(false);
      setContentType(secret.content_type || "text/plain");
      setMetadataJson("{}");
      setClientToken("");
      resetErrors();
    }
  }, [open, secret.content_type, resetErrors]);

  // A secret value has no parse rule server-side — only the size cap (and the
  // base64 alphabet when passed through) — and the message reports the size
  // alone, never the value.
  const valueError = validateSecretValue(value, alreadyBase64);
  const metadataError = validateMetadataJson(metadataJson);
  const tokenError =
    secret.client_bound && !clientToken.trim()
      ? "The client access token is required to add a version to a client-bound secret."
      : null;
  const shownValueError = errors.shown("value", valueError);
  const shownMetadataError = errors.shown("metadata", metadataError);
  const shownTokenError = errors.shown("token", tokenError);
  const blocked = !!(shownValueError || shownMetadataError || shownTokenError);
  const dirty =
    value !== "" ||
    clientToken !== "" ||
    !isEmptyJson(metadataJson) ||
    contentType !== (secret.content_type || "text/plain");
  const currentVersion = secret.labels?.current;
  const nextVersion = Math.max(0, ...secret.versions.map((v) => v.version)) + 1;

  async function submit(e?: React.SyntheticEvent) {
    e?.preventDefault();
    if (saving) return;
    errors.markAllTouched();
    // Every problem now has an inline message beside the field that caused it;
    // move focus there so the button never looks dead.
    if (valueError || metadataError || tokenError) {
      requestFocus();
      return;
    }
    setSaving(true);
    try {
      const res = await api.createSecret({
        env: secret.env,
        app: secret.app,
        key: secret.key,
        value_base64: secretValueBase64(value, alreadyBase64),
        content_type: contentType.trim() || "text/plain",
        metadata_json: metadataJson.trim() || "{}",
        client_bound: secret.client_bound,
        generate_access_token: false,
        expires_at_unix_ms: 0,
        ...(secret.client_bound ? { secret_token: clientToken.trim() } : null),
      });
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
      description={`Saving creates v${nextVersion} and makes it current.`}
      onClose={onClose}
      dismissible={!saving}
      dirty={dirty}
      initialFocus={valueRef}
      footer={(close) => (
        <>
          <Button variant="outline" onClick={close} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={submit} loading={saving} disabled={blocked}>
            Save new version
          </Button>
        </>
      )}
    >
      <form ref={formRef} onSubmit={submit}>
        <Field
          label="Value"
          hint={
            <>
              Stored encrypted.{" "}
              {typeof currentVersion === "number" ? (
                <span className="mono" data-testid="version-transition">
                  v{currentVersion} → v{nextVersion}
                </span>
              ) : (
                <span className="mono" data-testid="version-transition">
                  v{nextVersion}
                </span>
              )}{" "}
              becomes current.
              {alreadyBase64 ? " Sent as-is: standard base64, decoded by the server." : ""}
            </>
          }
          error={shownValueError}
        >
          <SecretValueField
            value={value}
            onChange={setValue}
            base64={alreadyBase64}
            onBase64Change={setAlreadyBase64}
            inputRef={valueRef}
            onBlur={() => errors.touch("value")}
          />
        </Field>
        <Field label="Content type" className="value-type-field">
          <SecretContentTypeSelect value={contentType} onValueChange={setContentType} />
        </Field>
        <Field label="Metadata JSON" error={shownMetadataError}>
          <JsonEditor
            toolbar="minimal"
            rows={3}
            maxHeight="30vh"
            value={metadataJson}
            onChange={setMetadataJson}
            onBlur={() => errors.touch("metadata")}
            onSubmit={() => void submit()}
          />
        </Field>
        {secret.client_bound ? (
          <Field
            label="Client access token"
            hint="Required. The existing token re-wraps the new version's key. It is sent once and never stored."
            error={shownTokenError}
          >
            <Input
              className="font-mono"
              type="password"
              value={clientToken}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setClientToken(e.target.value)}
              onBlur={() => errors.touch("token")}
              placeholder="client access token"
            />
          </Field>
        ) : null}
      </form>
    </Modal>
  );
}
