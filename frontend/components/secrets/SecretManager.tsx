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
import {
  ApiError,
  api,
  isAbortError,
  PurgeCleanupPendingApiError,
  type ResourceRef,
} from "@/lib/api";
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
  datetimeLocalToUnixMs,
  formatUnixMs,
  isEmptyJson,
  labelEntries,
  prettyJson,
} from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors, useLatestRequest, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import type {
  SecretBindingCohortResponse,
  SecretMetadata,
  SecretVersion,
  SecretVersionSetResponse,
} from "@/lib/types";
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

type BindingAction =
  | { kind: "bind" | "unbind" | "rotate" | "purge"; version: number }
  | { kind: "purge-unbound" };

export interface SecretManagerProps {
  /** Omit on the dedicated page, which reads its reference from the URL. */
  resourceRef?: ResourceRef;
  surface?: "page" | "workspace";
  onClose?: () => void;
  onChanged?: (ref: ResourceRef) => void;
  onDeleted?: (ref: ResourceRef) => void;
}

export default function SecretManager({
  resourceRef,
  surface = "page",
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
            headers={["Version", "State", "Created by", "Created", "Expires"]}
            rows={3}
          />
        </div>
      </>
    );
    if (surface === "workspace") {
      return (
        <Modal
          open
          workspace
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
        <Modal open workspace title="Secret not found" onClose={() => onClose?.()}>
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
        <Modal open workspace title="Could not load secret" onClose={() => onClose?.()}>
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
            secret.bound ? (
              <Badge kind="warning" key="mode">
                binding key
              </Badge>
            ) : (
              <Badge kind="neutral" key="mode">
                master key only
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
                label: `v${version.version}${version.version === current ? " (current)" : ""}${version.bound ? " · bound" : ""}`,
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
        <div className="table-wrap">
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
        open
        workspace
        title={
          <span className="row-wrap">
            <span className="mono">{displayPath(ref)}</span>
            {refreshing ? <Spinner /> : null}
          </span>
        }
        description={displayNamespace(ref)}
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
      <td>
        <div className="row-wrap">
          v{v.version}
          {isCurrent ? <Badge kind="accent">current</Badge> : null}
        </div>
      </td>
      <td>
        <div className="row-wrap">
          <SecretStateBadge state={v.state} />
          {v.bound ? <Badge kind="warning">bound</Badge> : null}
          {v.has_access_token ? <Badge kind="accent">access token</Badge> : null}
        </div>
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
          {isCurrent && !destroyed ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                onBindingAction({ kind: v.bound ? "unbind" : "bind", version: v.version })
              }
            >
              {v.bound ? "Unbind" : "Bind"}
            </Button>
          ) : null}
          {isCurrent && v.bound && !destroyed ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => onBindingAction({ kind: "rotate", version: v.version })}
            >
              Rotate key
            </Button>
          ) : null}
          {v.bound && !destroyed && canPurge ? (
            <Button
              variant="destructive"
              size="sm"
              aria-label={`Purge cohort containing version ${v.version}`}
              onClick={() => onBindingAction({ kind: "purge", version: v.version })}
            >
              Purge cohort
            </Button>
          ) : null}
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
  const [expires, setExpires] = useState("");
  const [bindVersion, setBindVersion] = useState(false);
  const [bindingKey, setBindingKey] = useState("");
  const [generateToken, setGenerateToken] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [confirmTokenRotation, setConfirmTokenRotation] = useState(false);
  const [mintedToken, setMintedToken] = useState<string | null>(null);
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
      setBindVersion(false);
      setBindingKey("");
      setGenerateToken(false);
      setAdvancedOpen(false);
      setConfirmTokenRotation(false);
      setMintedToken(null);
      resetErrors();
    } else {
      setValue("");
      setBindingKey("");
      setMintedToken(null);
      setConfirmTokenRotation(false);
    }
    setSaving(false);
    return () => {
      // A response from a closed or replaced version form must not reveal a
      // token in whichever secret happens to be mounted next.
      formInstance.current += 1;
    };
  }, [open, secret.content_type, resetErrors]);

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
    bindVersion ||
    bindingKey !== "" ||
    !isEmptyJson(metadataJson) ||
    expires !== "" ||
    generateToken ||
    contentType !== (secret.content_type || "text/plain");
  const currentVersion = secret.labels?.current;
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
    if (generateToken && secret.has_access_token) {
      setConfirmTokenRotation(true);
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
        generate_access_token: generateToken,
        expires_at_unix_ms: datetimeLocalToUnixMs(expires) ?? 0,
      });
      if (formInstance.current !== submittedForm) return;
      // Clear the plaintext from the field immediately.
      setValue("");
      if (res.access_token) {
        setMintedToken(res.access_token);
        toast.success(
          `Created version ${res.version}`,
          secret.has_access_token ? "Save the replacement access token." : "Save the access token.",
        );
      } else {
        toast.success(`Created version ${res.version}`, "New version is now current.");
        onSaved();
      }
    } catch (err) {
      if (formInstance.current !== submittedForm) return;
      toast.error(err, "Failed to create version");
    } finally {
      if (formInstance.current === submittedForm) setSaving(false);
    }
  }

  function finishTokenReveal() {
    setMintedToken(null);
    onSaved();
  }

  return (
    <>
      <Modal
        open={open}
        wide
        title={mintedToken ? "Save this access token now" : "New secret version"}
        description={
          mintedToken ? undefined : `Saving creates v${nextVersion} and makes it current.`
        }
        onClose={mintedToken ? () => undefined : onClose}
        dismissible={!saving && mintedToken === null}
        dirty={dirty && mintedToken === null}
        initialFocus={mintedToken ? undefined : valueRef}
        footer={
          mintedToken ? (
            <Button onClick={finishTokenReveal}>I&apos;ve saved it — continue</Button>
          ) : (
            (close) => (
              <>
                <Button variant="outline" onClick={close} disabled={saving}>
                  Cancel
                </Button>
                <Button onClick={submit} loading={saving} disabled={blocked}>
                  {generateToken && secret.has_access_token
                    ? "Create version & rotate token"
                    : "Save new version"}
                </Button>
              </>
            )
          )
        }
      >
        {mintedToken ? (
          <>
            <div className="danger-panel mb-4">
              <strong>
                {secret.has_access_token
                  ? "The previous token no longer works."
                  : "This token will never be shown again."}
              </strong>{" "}
              This token controls every version that requires token protection.
            </div>
            <div className="token-reveal">{mintedToken}</div>
            <div className="row-wrap mt-4">
              <CopyButton label="Copy token" value={() => mintedToken} />
            </div>
          </>
        ) : (
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
                      Protection does not carry forward from the current version. Choose it for each
                      new version.
                    </div>
                  </label>
                </div>
                {bindVersion ? (
                  <Field
                    label="Binding key"
                    hint="At least 32 UTF-8 bytes. Used only for this write and never retained by KMS."
                    error={shownBindingKeyError}
                  >
                    <Input
                      className="font-mono"
                      type="password"
                      value={bindingKey}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => setBindingKey(event.target.value)}
                      onBlur={() => {
                        errors.touch("bindingKey");
                        if (bindingKeyError) setAdvancedOpen(true);
                      }}
                      placeholder="application binding key"
                    />
                  </Field>
                ) : null}
                <div className="checkbox-row">
                  <Checkbox
                    id="token-new-version"
                    checked={generateToken}
                    onCheckedChange={setGenerateToken}
                  />
                  <label htmlFor="token-new-version">
                    <strong>
                      {secret.has_access_token ? "Rotate access token" : "Generate access token"}
                    </strong>
                    <div className="faint text-sm">The new token is shown once after creation.</div>
                  </label>
                </div>
                {generateToken && secret.has_access_token ? (
                  <div className="warn-panel">
                    The existing token will immediately stop working for every token-protected
                    version of this secret.
                  </div>
                ) : null}
              </div>
            </details>
          </form>
        )}
      </Modal>
      <ConfirmDialog
        open={confirmTokenRotation}
        title="Rotate access token?"
        danger
        message={
          <>
            Creating v{nextVersion} will replace the access token for{" "}
            <span className="mono">{displayPath(secret)}</span>. Clients using the current token
            will fail until they receive the replacement.
          </>
        }
        confirmLabel="Create version & rotate token"
        busy={saving}
        onConfirm={() => {
          setConfirmTokenRotation(false);
          void save();
        }}
        onCancel={() => setConfirmTokenRotation(false)}
      />
    </>
  );
}

function BindingActionModal({
  action,
  secretRef,
  onClose,
  onSaved,
}: {
  action: BindingAction | null;
  secretRef: ResourceRef;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [cohortPreview, setCohortPreview] = useState<SecretBindingCohortResponse | null>(null);
  const [unboundPreview, setUnboundPreview] = useState<SecretVersionSetResponse | null>(null);
  const [previewKey, setPreviewKey] = useState("");
  const [operationKey, setOperationKey] = useState("");
  const [newBindingKey, setNewBindingKey] = useState("");
  const [confirmNewBindingKey, setConfirmNewBindingKey] = useState("");
  const [purgeText, setPurgeText] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useLatestRequest();
  const errors = useFieldErrors<
    "previewKey" | "operationKey" | "newBindingKey" | "confirmNewBindingKey" | "purgeText"
  >();
  const { reset: resetErrors } = errors;

  const actionKey = action ? `${action.kind}:${"version" in action ? action.version : "all"}` : "";
  useEffect(() => {
    // Reading the identity is intentional: reopening a different action for
    // the same mounted page must discard the previous action's credentials.
    void actionKey;
    request.abort();
    setCohortPreview(null);
    setUnboundPreview(null);
    setPreviewKey("");
    setOperationKey("");
    setNewBindingKey("");
    setConfirmNewBindingKey("");
    setPurgeText("");
    setBusy(false);
    resetErrors();
  }, [actionKey, request, resetErrors]);

  const needsCohortPreview = action?.kind === "purge";
  const needsUnboundPreview = action?.kind === "purge-unbound";
  const needsPreview = needsCohortPreview || needsUnboundPreview;
  const preview = needsCohortPreview ? cohortPreview : unboundPreview;
  const previewKeyError =
    needsCohortPreview && cohortPreview === null ? validateBindingKey(previewKey) : null;
  const operationKeyError =
    action && action.kind !== "purge-unbound" ? validateBindingKey(operationKey) : null;
  const newBindingKeyError = action?.kind === "rotate" ? validateBindingKey(newBindingKey) : null;
  const confirmNewBindingKeyError =
    action?.kind === "rotate" && confirmNewBindingKey !== newBindingKey
      ? "The new binding keys do not match."
      : null;
  const purgeTextError =
    (action?.kind === "purge" || action?.kind === "purge-unbound") &&
    preview !== null &&
    purgeText !== "PURGE"
      ? "Type PURGE exactly to confirm."
      : null;

  const clearCredentials = useCallback(() => {
    setPreviewKey("");
    setOperationKey("");
    setNewBindingKey("");
    setConfirmNewBindingKey("");
    setPurgeText("");
  }, []);

  const close = useCallback(() => {
    clearCredentials();
    setCohortPreview(null);
    setUnboundPreview(null);
    onClose();
  }, [clearCredentials, onClose]);

  async function previewVersions() {
    if (!action || !needsPreview || previewKeyError) {
      errors.markAllTouched();
      return;
    }
    const key = previewKey;
    setPreviewKey("");
    setBusy(true);
    const run = request.begin();
    try {
      const result =
        action.kind === "purge"
          ? await api.previewSecretBindingCohort(secretRef, action.version, key, {
              signal: run.signal,
            })
          : await api.previewSecretUnboundVersions(secretRef, { signal: run.signal });
      if (!run.current) return;
      if (action.kind === "purge") setCohortPreview(result as SecretBindingCohortResponse);
      else setUnboundPreview(result as SecretVersionSetResponse);
      resetErrors();
    } catch (err) {
      if (!run.current) return;
      toast.error(
        err,
        action.kind === "purge"
          ? "Could not preview binding cohort"
          : "Could not preview unbound versions",
      );
    } finally {
      if (run.current) setBusy(false);
    }
  }

  async function mutate() {
    if (!action || (needsPreview && preview === null)) return;
    errors.markAllTouched();
    if (operationKeyError || newBindingKeyError || confirmNewBindingKeyError || purgeTextError) {
      return;
    }

    const oldOrNewKey = operationKey;
    const replacement = newBindingKey;
    clearCredentials();
    setBusy(true);
    const run = request.begin();
    try {
      if (action.kind === "bind") {
        const result = await api.bindSecret(secretRef, action.version, oldOrNewKey, {
          signal: run.signal,
        });
        if (!run.current) return;
        toast.success(
          `Created bound version ${result.current_version}`,
          `Version ${result.previous_version} remains unchanged. Create a new release to use the new version.`,
        );
      } else if (action.kind === "unbind") {
        const result = await api.unbindSecret(secretRef, action.version, oldOrNewKey, {
          signal: run.signal,
        });
        if (!run.current) return;
        toast.success(
          `Created unbound version ${result.current_version}`,
          `Version ${result.previous_version} remains unchanged. Create a new release to use the new version.`,
        );
      } else if (action.kind === "rotate") {
        const result = await api.rotateSecretBindingKey(
          secretRef,
          action.version,
          oldOrNewKey,
          replacement,
          { signal: run.signal },
        );
        if (!run.current) return;
        toast.success(
          `Created version ${result.current_version} with the new binding key`,
          `Version ${result.previous_version} and its historical cohort still require the old key.`,
        );
      } else if (action.kind === "purge" && cohortPreview) {
        const result = await api.purgeSecretBindingCohort(
          secretRef,
          action.version,
          oldOrNewKey,
          cohortPreview.revision,
          cohortPreview.affected_versions,
          { signal: run.signal },
        );
        if (!run.current) return;
        toast.success(
          `Purged ${result.affected_versions.length} version${result.affected_versions.length === 1 ? "" : "s"}`,
          "Affected versions are permanent tombstones.",
        );
      } else if (action.kind === "purge-unbound" && unboundPreview) {
        const result = await api.purgeSecretUnboundVersions(
          secretRef,
          unboundPreview.revision,
          unboundPreview.affected_versions,
          { signal: run.signal },
        );
        if (!run.current) return;
        toast.success(
          `Purged ${result.affected_versions.length} unbound version${result.affected_versions.length === 1 ? "" : "s"}`,
          "Affected versions are permanent tombstones; release references and labels were preserved.",
        );
      }
      onSaved();
    } catch (err) {
      if (!run.current) return;
      if (
        (action.kind === "purge" || action.kind === "purge-unbound") &&
        err instanceof PurgeCleanupPendingApiError
      ) {
        toast.info(
          "Purge committed",
          action.kind === "purge-unbound"
            ? "Database artifact cleanup is pending. Do not retry the purge; restart the service to complete cleanup."
            : "Database artifact cleanup is pending. Do not retry with the binding key; restart the service to complete cleanup.",
          { duration: 12_000 },
        );
        onSaved();
        return;
      }
      if (err instanceof ApiError && err.code === "aborted") {
        if (action.kind === "purge") setCohortPreview(null);
        else if (action.kind === "purge-unbound") setUnboundPreview(null);
        toast.error(
          err,
          action.kind === "purge" || action.kind === "purge-unbound"
            ? "Version set changed — preview it again"
            : "Current version changed — reload and try again",
        );
      } else {
        toast.error(err, `${bindingActionVerb(action.kind)} failed`);
      }
    } finally {
      if (run.current) setBusy(false);
    }
  }

  const previewStage = needsPreview && preview === null;
  const dirty =
    previewKey !== "" ||
    operationKey !== "" ||
    newBindingKey !== "" ||
    confirmNewBindingKey !== "" ||
    purgeText !== "";

  return (
    <Modal
      open={action !== null}
      title={action ? bindingActionTitle(action) : "Binding key"}
      description={
        action
          ? action.kind === "bind"
            ? "Clone the current version into a new bound current version. The source remains unchanged."
            : action.kind === "unbind"
              ? "Clone the current version into a new unbound current version. The source remains unchanged."
              : action.kind === "rotate"
                ? "Clone the current version under a new binding key. Historical versions keep requiring the old key."
                : action.kind === "purge-unbound"
                  ? "Preview and irreversibly purge every non-destroyed unbound version of this secret."
                  : "KMS discovers only the contiguous bound versions around this anchor that open with the same key."
          : undefined
      }
      onClose={close}
      dismissible={!busy}
      dirty={dirty && !busy}
      footer={(requestClose) => (
        <>
          <Button variant="outline" onClick={requestClose} disabled={busy}>
            Cancel
          </Button>
          {previewStage ? (
            <Button
              onClick={() => void previewVersions()}
              loading={busy}
              disabled={!!previewKeyError}
            >
              {action?.kind === "purge-unbound" ? "Preview unbound versions" : "Preview cohort"}
            </Button>
          ) : (
            <Button
              variant={
                action?.kind === "purge" || action?.kind === "purge-unbound"
                  ? "destructive-solid"
                  : "default"
              }
              onClick={() => void mutate()}
              loading={busy}
              disabled={
                !!operationKeyError ||
                !!newBindingKeyError ||
                !!confirmNewBindingKeyError ||
                !!purgeTextError
              }
            >
              {action ? bindingActionButton(action.kind) : "Continue"}
            </Button>
          )}
        </>
      )}
    >
      {action ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (previewStage) void previewVersions();
            else void mutate();
          }}
        >
          {previewStage && action.kind === "purge" ? (
            <Field
              label="Current binding key"
              hint="Used only to discover the cohort; it is cleared before the preview returns."
              error={errors.shown("previewKey", previewKeyError)}
            >
              <Input
                className="font-mono"
                type="password"
                value={previewKey}
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => setPreviewKey(event.target.value)}
                onBlur={() => errors.touch("previewKey")}
              />
            </Field>
          ) : previewStage ? (
            <div className="warn-panel">
              Preview includes every non-destroyed unbound version, including disabled and expired
              versions. KMS will require the exact revision and version set at confirmation.
            </div>
          ) : (
            <>
              {preview ? (
                <div className="danger-panel mb-4">
                  <strong>
                    {action.kind === "purge-unbound"
                      ? "Every version in this exact set will be destroyed:"
                      : "This exact cohort will be destroyed:"}
                  </strong>
                  <div
                    className="row-wrap mt-2"
                    data-testid={
                      action.kind === "purge-unbound"
                        ? "unbound-purge-versions"
                        : "binding-cohort-versions"
                    }
                  >
                    {preview.affected_versions.map((version) => (
                      <Badge key={version} kind="warning">
                        v{version}
                      </Badge>
                    ))}
                  </div>
                  <div className="faint mt-2 text-sm">
                    <span className="mono">{displayPath(secretRef)}</span>
                    {action.kind === "purge" && cohortPreview
                      ? ` · anchor v${cohortPreview.anchor_version}`
                      : ""}
                    {` · revision ${preview.revision}. KMS will abort if the revision or version set changes before confirmation.`}
                  </div>
                  <div className="mt-2">
                    Release entries and labels remain, but every affected version becomes an
                    unreadable tombstone. If current is affected, its projection is cleared. This
                    cannot be undone.
                  </div>
                </div>
              ) : null}
              {action.kind !== "purge-unbound" ? (
                <Field
                  label={action.kind === "bind" ? "New binding key" : "Current binding key"}
                  hint="Used only for this request and cleared as soon as it starts."
                  error={errors.shown("operationKey", operationKeyError)}
                >
                  <Input
                    className="font-mono"
                    type="password"
                    value={operationKey}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setOperationKey(event.target.value)}
                    onBlur={() => errors.touch("operationKey")}
                  />
                </Field>
              ) : null}
              {action.kind === "rotate" ? (
                <>
                  <Field
                    label="New binding key"
                    hint="At least 32 UTF-8 bytes. KMS creates one new bound version with fresh cryptographic material and salt."
                    error={errors.shown("newBindingKey", newBindingKeyError)}
                  >
                    <Input
                      className="font-mono"
                      type="password"
                      value={newBindingKey}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => setNewBindingKey(event.target.value)}
                      onBlur={() => errors.touch("newBindingKey")}
                    />
                  </Field>
                  <Field
                    label="Confirm new binding key"
                    error={errors.shown("confirmNewBindingKey", confirmNewBindingKeyError)}
                  >
                    <Input
                      className="font-mono"
                      type="password"
                      value={confirmNewBindingKey}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => setConfirmNewBindingKey(event.target.value)}
                      onBlur={() => errors.touch("confirmNewBindingKey")}
                    />
                  </Field>
                </>
              ) : null}
              {action.kind === "purge" || action.kind === "purge-unbound" ? (
                <Field
                  label={
                    <>
                      Type <span className="mono">PURGE</span> to confirm
                    </>
                  }
                  error={errors.shown("purgeText", purgeTextError)}
                >
                  <Input
                    className="font-mono"
                    value={purgeText}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setPurgeText(event.target.value)}
                    onBlur={() => errors.touch("purgeText")}
                  />
                </Field>
              ) : null}
            </>
          )}
        </form>
      ) : null}
    </Modal>
  );
}

function bindingActionVerb(kind: BindingAction["kind"]): string {
  switch (kind) {
    case "bind":
      return "Bind";
    case "unbind":
      return "Unbind";
    case "rotate":
      return "Rotate binding key";
    case "purge":
      return "Purge cohort";
    case "purge-unbound":
      return "Purge unbound versions";
  }
}

function bindingActionTitle(action: BindingAction): string {
  return "version" in action
    ? `${bindingActionVerb(action.kind)} · v${action.version}`
    : bindingActionVerb(action.kind);
}

function bindingActionButton(kind: BindingAction["kind"]): string {
  return kind === "purge" || kind === "purge-unbound" ? "Purge versions" : bindingActionVerb(kind);
}
