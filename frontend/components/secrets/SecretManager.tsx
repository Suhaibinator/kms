import { ArrowLeft } from "lucide-react";
import { useRouter } from "next/router";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Icon } from "@/components/icons";
import { JsonEditor } from "@/components/JsonEditor";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { SensitiveValueField } from "@/components/SensitiveValueField";
import { type BindingAction, BindingActionModal } from "@/components/secrets/BindingActionModal";
import {
  BINDING_ACTION_LABELS,
  type BindingActionKind,
  bindingActions,
} from "@/components/secrets/binding-actions";
import { BindingModeBadge } from "@/components/secrets/SecretBadges";
import { SecretContentTypeSelect } from "@/components/secrets/SecretContentTypeSelect";
import { SecretValueField } from "@/components/secrets/SecretValueField";
import {
  Badge,
  Checkbox,
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAuth } from "@/context/AuthContext";
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
  datetimeLocalToUnixMs,
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
import { validateBindingKey, validateMetadataJson } from "@/lib/validation";

const REVEAL_SECONDS = 30;
const REVEAL_RESPONSE_MISMATCH = "Reveal response did not match the requested secret version.";

function localDatetimeValue(ms: number): string {
  const date = new Date(ms);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`;
}

// Ephemeral reveal state. This is the only place a secret plaintext lives in
// the client, and only inside component state — never logged or toasted.
interface Revealed {
  version: number;
  valueBase64: string;
  contentType: string;
  isText: boolean;
}

export interface SecretManagerProps {
  /** Omit on the dedicated page, which reads its reference from the URL. */
  resourceRef?: ResourceRef;
  surface?: "page" | "workspace";
  /** Workspace surface only: where the caller came from, rendered under the title. */
  context?: ReactNode;
  onClose?: () => void;
  onChanged?: (ref: ResourceRef) => void;
  onDeleted?: (ref: ResourceRef) => void;
}

export default function SecretManager({
  resourceRef,
  surface = "page",
  context,
  onClose,
  onChanged,
  onDeleted,
}: SecretManagerProps = {}) {
  const router = useRouter();
  const toast = useToast();
  const { identity } = useAuth();
  const isAdmin = identity?.kind === "admin";
  const identityBoundary = identity ? `${identity.kind}\u0000${identity.name}` : "";
  const { values, ready: queryReady } = useQueryParams(["env", "app", "key"]);
  const ready = resourceRef ? true : queryReady;
  const env = resourceRef?.env ?? values.env ?? "";
  const app = resourceRef?.app ?? values.app ?? "";
  const key = resourceRef?.key ?? values.key ?? "";
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
  const [revealBindingKey, setRevealBindingKey] = useState("");
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
  const [bindingAction, setBindingAction] = useState<BindingAction | null>(null);

  // New version modal.
  const [newVersionOpen, setNewVersionOpen] = useState(false);
  const [section, setSection] = useState("overview");

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
    setBindingAction(null);
    setNewVersionOpen(false);
    setSection("overview");
    if (hasRef) {
      setRevealed(null);
      setValueVisible(false);
      setRevealBindingKey("");
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

  // An identity boundary invalidates every open credential-bearing flow,
  // including admin-to-admin switches. Nothing transient survives it.
  useEffect(() => {
    void identityBoundary;
    revealRequest.abort();
    setRevealTarget(null);
    setRevealBindingKey("");
    setRevealBusy(false);
    setRevealed(null);
    setValueVisible(false);
    setBindingAction(null);
    setNewVersionOpen(false);
    setConfirm(null);
  }, [identityBoundary, revealRequest]);

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
      if (!hasRef || !isAdmin) return;
      const run = revealRequest.begin();
      const versionInfo = secret?.versions.find((candidate) => candidate.version === version);
      const bindingKey = versionInfo?.bound ? revealBindingKey : undefined;
      // Clear the credential from React state as the request starts. The local
      // copy exists only for this in-flight call and is never persisted.
      setRevealBindingKey("");
      setRevealBusy(true);
      try {
        const res = await api.revealSecret(ref, version, "", bindingKey, {
          signal: run.signal,
        });
        if (!run.current || activeRefKey.current !== refKey) return;
        if (
          res.env !== ref.env ||
          res.app !== ref.app ||
          res.key !== ref.key ||
          res.version !== version
        ) {
          throw new Error(REVEAL_RESPONSE_MISMATCH);
        }
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
    [hasRef, isAdmin, ref, refKey, revealBindingKey, revealRequest, secret?.versions, toast],
  );

  const openReveal = useCallback(
    (version: number) => {
      if (!isAdmin) return;
      setRevealBindingKey("");
      setRevealTarget(version);
    },
    [isAdmin],
  );

  const closeReveal = useCallback(() => {
    setRevealBindingKey("");
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
        if (surface === "workspace") {
          onDeleted?.(ref);
          onClose?.();
        } else {
          await router.push(links.secrets({ env, app }));
        }
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
      onChanged?.(ref);
    } catch (err) {
      toast.error(err, "Action failed");
    } finally {
      setActionBusy(false);
    }
  }, [hasRef, ref, env, app, confirm, toast, load, router, surface, onDeleted, onClose, onChanged]);

  const backLink = hasRef ? links.secrets({ env, app }) : links.secrets();
  const trail = hasRef ? crumbs.secret(ref) : undefined;

  // Header and card frames come straight from the URL, so they paint at once
  // and only the values fill in — no full-page spinner swap.
  if (!ready || (hasRef && (loadState === "idle" || loadState === "loading"))) {
    const loadingCards = (
      <>
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
            headers={["Version", "State & protection", "Created by", "Created", "Expires"]}
            rows={3}
          />
        </div>
      </>
    );
    if (surface === "workspace") {
      return (
        <Modal
          mobileFullScreen
          open
          wide
          title={hasRef ? displayPath(ref) : "Secret"}
          description={hasRef ? displayNamespace(ref) : "Loading secret details"}
          onClose={() => onClose?.()}
        >
          <div className="secret-workspace-stack">{loadingCards}</div>
        </Modal>
      );
    }
    return (
      <>
        <PageHeader
          documentTitle={hasRef ? displayPath(ref) : "Secret"}
          title={hasRef ? <span className="mono">{displayPath(ref)}</span> : "Secret"}
          breadcrumbs={trail}
        />
        {loadingCards}
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
    if (surface === "workspace") {
      return (
        <Modal mobileFullScreen open wide title="Secret not found" onClose={() => onClose?.()}>
          <EmptyState icon={<Icon.secret size={20} />} title="Not found">
            No secret exists at <span className="mono">{displayPath(ref)}</span>.
          </EmptyState>
        </Modal>
      );
    }
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
    if (surface === "workspace") {
      return (
        <Modal mobileFullScreen open wide title="Could not load secret" onClose={() => onClose?.()}>
          <EmptyState
            icon={<Icon.secret size={20} />}
            title="Secret unavailable"
            actions={<Button onClick={() => void load()}>Try again</Button>}
          >
            The server could not load <span className="mono">{displayPath(ref)}</span>. Check the
            connection and try again.
          </EmptyState>
        </Modal>
      );
    }
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
  const currentVersionInfo = secret.versions.find((version) => version.version === current);
  const hasUnboundVersions = secret.versions.some(
    (version) => version.state !== "destroyed" && !version.bound,
  );
  const enabledVersions = secret.versions.filter((v) => v.state === "enabled");
  const revealVersionInfo =
    revealTarget === null
      ? null
      : (secret.versions.find((version) => version.version === revealTarget) ?? null);

  const actions = (
    <>
      <Button variant="outline" onClick={() => setNewVersionOpen(true)}>
        New version
      </Button>
      {isAdmin && hasUnboundVersions ? (
        <Button variant="destructive" onClick={() => setBindingAction({ kind: "purge-unbound" })}>
          Purge unbound versions
        </Button>
      ) : null}
      <Button variant="destructive" onClick={() => setConfirm({ kind: "delete" })}>
        Delete
      </Button>
    </>
  );

  const metadataCard = (
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
            <div className="row-wrap" key="mode">
              <BindingModeBadge bound={secret.bound} />
              {currentVersionInfo ? (
                <BindingActionButtons
                  version={currentVersionInfo}
                  actions={bindingActions(currentVersionInfo, { isCurrent: true, canPurge: false })}
                  onAction={setBindingAction}
                />
              ) : null}
            </div>,
          ],
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
  );

  const revealCard = (
    <div className="card">
      <div className="card-title">Secret value</div>
      {!isAdmin ? (
        <div className="warn-panel">
          Secret values can be revealed only by an administrator. Application identities may resolve
          them through the SDK with the exact-version credentials they require.
        </div>
      ) : revealed ? (
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
            Revealing decrypts the selected version and records an audit event. A binding key, when
            required, is sent only in that request and is not stored by the console. The value
            auto-hides after {REVEAL_SECONDS} seconds.
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
                label: `v${version.version}${version.version === current ? " (current)" : ""}${version.bound ? " · binding key" : ""}`,
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
  );

  const versionsCard = (
    <div className="card">
      <div className="card-title">Versions</div>
      {secret.versions.length === 0 ? (
        <EmptyState icon={<Icon.secret size={20} />} title="No versions" />
      ) : (
        <div className="table-wrap card-table">
          <table className="data">
            <thead>
              <tr>
                <th>Version</th>
                <th>State &amp; protection</th>
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
                    canReveal={isAdmin}
                    canPurge={isAdmin}
                    onReveal={openReveal}
                    onConfirm={setConfirm}
                    onBindingAction={setBindingAction}
                  />
                ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );

  const dialogs = (
    <>
      <ConfirmDialog
        open={revealTarget !== null}
        title="Reveal secret value?"
        message={
          <>
            You are about to decrypt and display version {revealTarget} of{" "}
            <span className="mono">{displayPath(ref)}</span>. This is recorded in the audit log. The
            value will auto-hide after {REVEAL_SECONDS} seconds.
            {revealVersionInfo?.bound ? (
              <Field
                label="Binding key"
                hint="Used only for this reveal request and not saved."
                className="mt-4"
              >
                <Input
                  type="password"
                  value={revealBindingKey}
                  required
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(event) => setRevealBindingKey(event.target.value)}
                />
              </Field>
            ) : null}
          </>
        }
        confirmLabel="Reveal"
        busy={revealBusy}
        confirmDisabled={revealVersionInfo?.bound === true && revealBindingKey.length === 0}
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
          onChanged?.(ref);
        }}
      />

      <BindingActionModal
        action={bindingAction}
        secretRef={ref}
        onClose={() => setBindingAction(null)}
        onSaved={() => {
          setBindingAction(null);
          void load({ background: true });
          onChanged?.(ref);
        }}
      />
    </>
  );

  if (surface === "workspace") {
    return (
      <Modal
        mobileFullScreen
        open
        wide
        title={
          <span className="row-wrap">
            <span className="mono">{displayPath(ref)}</span>
            {refreshing ? <Spinner /> : null}
          </span>
        }
        description={
          context ? (
            <span className="row-wrap">
              {displayNamespace(ref)}
              {context}
            </span>
          ) : (
            displayNamespace(ref)
          )
        }
        onClose={() => onClose?.()}
      >
        <Tabs value={section} onValueChange={(value) => setSection(String(value))}>
          <div className="secret-workspace-toolbar">
            <TabsList variant="line" aria-label="Secret details">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="versions">Versions</TabsTrigger>
            </TabsList>
            <div className="row-wrap">{actions}</div>
          </div>
          <TabsContent value="overview" className="secret-workspace-stack">
            {metadataCard}
            {revealCard}
          </TabsContent>
          <TabsContent value="versions" className="secret-workspace-stack">
            {versionsCard}
          </TabsContent>
          {dialogs}
        </Tabs>
      </Modal>
    );
  }

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
        actions={actions}
      />
      {metadataCard}
      {revealCard}
      {versionsCard}
      {dialogs}
    </>
  );
}

function VersionRow({
  v,
  isCurrent,
  canReveal,
  canPurge,
  onReveal,
  onConfirm,
  onBindingAction,
}: {
  v: SecretVersion;
  isCurrent: boolean;
  canReveal: boolean;
  canPurge: boolean;
  onReveal: (version: number) => void;
  onConfirm: (
    c:
      | { kind: "disable" | "enable" | "promote"; version: number }
      | { kind: "destroy"; version: number },
  ) => void;
  onBindingAction: (action: BindingAction) => void;
}) {
  const destroyed = v.state === "destroyed";
  const expired = v.expires_at_unix_ms > 0 && v.expires_at_unix_ms <= Date.now();
  return (
    <tr>
      <td data-label="Version">
        <div className="row-wrap">
          v{v.version}
          {isCurrent ? <Badge kind="accent">current</Badge> : null}
        </div>
      </td>
      <td data-label="State & protection">
        <div className="row-wrap">
          <SecretStateBadge state={v.state} />
          {v.bound ? <BindingModeBadge bound /> : null}
        </div>
      </td>
      <td data-label="Created by">{v.created_by || <span className="faint">—</span>}</td>
      <td data-label="Created" className="nowrap">
        {formatUnixMs(v.created_at_unix_ms)}
      </td>
      <td data-label="Expires" className="nowrap">
        {v.expires_at_unix_ms > 0 ? (
          <div className="row-wrap">
            {formatUnixMs(v.expires_at_unix_ms)}
            {expired ? <Badge kind="warning">expired</Badge> : null}
          </div>
        ) : (
          <span className="faint">never</span>
        )}
      </td>
      <td data-label="Actions">
        <div className="row-actions">
          {canReveal && v.state === "enabled" ? (
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
          <BindingActionButtons
            version={v}
            actions={bindingActions(v, { isCurrent, canPurge })}
            onAction={onBindingAction}
          />
          {!destroyed ? (
            <Button
              variant="destructive"
              size="sm"
              aria-label={`Destroy version ${v.version}`}
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

/** The Bind / Unbind / Rotate key / Purge cohort buttons for one version. */
function BindingActionButtons({
  version,
  actions,
  onAction,
}: {
  version: SecretVersion;
  actions: BindingActionKind[];
  onAction: (action: BindingAction) => void;
}) {
  return (
    <>
      {actions.map((kind) =>
        kind === "purge" ? (
          <Button
            key={kind}
            variant="destructive"
            size="sm"
            aria-label={`Purge cohort containing version ${version.version}`}
            onClick={() => onAction({ kind, version: version.version })}
          >
            {BINDING_ACTION_LABELS[kind]}
          </Button>
        ) : (
          <Button
            key={kind}
            variant="outline"
            size="sm"
            onClick={() => onAction({ kind, version: version.version })}
          >
            {BINDING_ACTION_LABELS[kind]}
          </Button>
        ),
      )}
    </>
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
  const currentVersion = secret.labels?.current;
  const currentVersionBound =
    typeof currentVersion === "number"
      ? (secret.versions.find((version) => version.version === currentVersion)?.bound ??
        secret.bound)
      : secret.bound;
  const [value, setValue] = useState("");
  const [alreadyBase64, setAlreadyBase64] = useState(false);
  const [contentType, setContentType] = useState(secret.content_type || "text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [expires, setExpires] = useState("");
  const [bindVersion, setBindVersion] = useState(currentVersionBound);
  const [bindingKey, setBindingKey] = useState("");
  const [advancedOpen, setAdvancedOpen] = useState(currentVersionBound);
  const [saving, setSaving] = useState(false);
  const errors = useFieldErrors<"value" | "metadata" | "expires" | "bindingKey">();
  const { reset: resetErrors } = errors;
  const valueRef = useRef<HTMLElement | null>(null);
  const formInstance = useRef(0);
  const { formRef, requestFocus } = useFocusFirstInvalid();

  useEffect(() => {
    formInstance.current += 1;
    if (open) {
      setValue("");
      setAlreadyBase64(false);
      setContentType(secret.content_type || "text/plain");
      setMetadataJson("{}");
      setExpires("");
      setBindVersion(currentVersionBound);
      setBindingKey("");
      setAdvancedOpen(currentVersionBound);
      resetErrors();
    } else {
      setValue("");
      setBindingKey("");
    }
    setSaving(false);
    return () => {
      // A response from a closed or replaced version form must not reveal a
      // token in whichever secret happens to be mounted next.
      formInstance.current += 1;
    };
  }, [open, secret.content_type, currentVersionBound, resetErrors]);

  // A secret value has no parse rule server-side — only the size cap (and the
  // base64 alphabet when passed through) — and the message reports the size
  // alone, never the value.
  const valueError = validateSecretValue(value, alreadyBase64);
  const metadataError = validateMetadataJson(metadataJson);
  const expiresError =
    expires && (datetimeLocalToUnixMs(expires) ?? 0) <= Date.now()
      ? "Expiry must be in the future."
      : null;
  const bindingKeyError = bindVersion ? validateBindingKey(bindingKey) : null;
  const shownValueError = errors.shown("value", valueError);
  const shownMetadataError = errors.shown("metadata", metadataError);
  const shownExpiresError = errors.shown("expires", expiresError);
  const shownBindingKeyError = errors.shown("bindingKey", bindingKeyError);
  const blocked = !!(
    shownValueError ||
    shownMetadataError ||
    shownExpiresError ||
    shownBindingKeyError
  );
  const advancedHasError = !!(shownMetadataError || shownExpiresError || shownBindingKeyError);
  const dirty =
    value !== "" ||
    bindVersion !== currentVersionBound ||
    bindingKey !== "" ||
    !isEmptyJson(metadataJson) ||
    expires !== "" ||
    contentType !== (secret.content_type || "text/plain");
  const nextVersion = Math.max(0, ...secret.versions.map((v) => v.version)) + 1;
  const expiresMin = useMemo(() => localDatetimeValue(Date.now()), []);

  function submit(e?: React.SyntheticEvent) {
    e?.preventDefault();
    if (saving) return;
    errors.markAllTouched();
    // Every problem now has an inline message beside the field that caused it;
    // move focus there so the button never looks dead.
    if (valueError || metadataError || expiresError || bindingKeyError) {
      if (metadataError || expiresError || bindingKeyError) setAdvancedOpen(true);
      requestFocus();
      return;
    }
    void save();
  }

  async function save() {
    if (saving) return;
    const submittedForm = formInstance.current;
    const requestBindingKey = bindVersion ? bindingKey : undefined;
    setSaving(true);
    setBindingKey("");
    try {
      const res = await api.createSecret({
        env: secret.env,
        app: secret.app,
        key: secret.key,
        value_base64: secretValueBase64(value, alreadyBase64),
        content_type: contentType.trim() || "text/plain",
        metadata_json: metadataJson.trim() || "{}",
        ...(requestBindingKey !== undefined ? { binding_key: requestBindingKey } : null),
        expires_at_unix_ms: datetimeLocalToUnixMs(expires) ?? 0,
      });
      if (formInstance.current !== submittedForm) return;
      // Clear the plaintext from the field immediately.
      setValue("");
      toast.success(`Created version ${res.version}`, "New version is now current.");
      onSaved();
    } catch (err) {
      if (formInstance.current !== submittedForm) return;
      toast.error(err, "Failed to create version");
    } finally {
      if (formInstance.current === submittedForm) setSaving(false);
    }
  }

  return (
    <Modal
      mobileFullScreen
      open={open}
      wide
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
        <details
          className="advanced-panel advanced-panel-modal"
          open={advancedOpen}
          onToggle={(event) => setAdvancedOpen(event.currentTarget.open || advancedHasError)}
        >
          <summary>Advanced options</summary>
          <div className="advanced-panel-content">
            <Field label="Expires at" hint="Optional." error={shownExpiresError}>
              <Input
                type="datetime-local"
                min={expiresMin}
                value={expires}
                onChange={(event) => setExpires(event.target.value)}
                onBlur={() => {
                  errors.touch("expires");
                  if (expiresError) setAdvancedOpen(true);
                }}
              />
            </Field>
            <Field label="Metadata JSON" error={shownMetadataError}>
              <JsonEditor
                toolbar="minimal"
                rows={3}
                maxHeight="30vh"
                value={metadataJson}
                onChange={setMetadataJson}
                onBlur={() => {
                  errors.touch("metadata");
                  if (metadataError) setAdvancedOpen(true);
                }}
                onSubmit={() => void submit()}
              />
            </Field>
            <div className="checkbox-row">
              <Checkbox
                id="bind-new-version"
                checked={bindVersion}
                onCheckedChange={(checked) => {
                  setBindVersion(checked);
                  if (!checked) setBindingKey("");
                }}
              />
              <label htmlFor="bind-new-version">
                <strong>Bind only this new version</strong>
                <div className="faint text-sm">
                  {currentVersionBound
                    ? "The current version is bound, so protection is selected here too. Enter its binding key below, or clear this option to create an unbound version."
                    : "Requires a binding key to decrypt only this new version."}
                </div>
              </label>
            </div>
            {bindVersion ? (
              <Field
                label="Binding key"
                hint="At least 32 UTF-8 bytes. Save this key before submitting; KMS does not store it."
                error={shownBindingKeyError}
              >
                <SensitiveValueField
                  controlLabel="binding key"
                  value={bindingKey}
                  onChange={setBindingKey}
                  onBlur={() => {
                    errors.touch("bindingKey");
                    if (bindingKeyError) setAdvancedOpen(true);
                  }}
                  placeholder="application binding key"
                />
              </Field>
            ) : null}
          </div>
        </details>
      </form>
    </Modal>
  );
}
