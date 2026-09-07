import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useId, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Icon } from "@/components/icons";
import { headerLabels, SortHeaderRow, staticController } from "@/components/SortableTable";
import {
  Badge,
  EmptyState,
  PageHeader,
  Skeleton,
  Spinner,
  StatSkeleton,
  TableSkeleton,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { useLatestRequest, useQueryParams } from "@/lib/hooks";
import type {
  ExpiringAdminCert,
  ExpiringIdentityCert,
  ExpiringSecretVersion,
  PostureResponse,
} from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { useNow } from "@/lib/useNow";

// The look-aheads the selector offers. They are the API's own spelling ("30d"),
// so the chosen value travels to the URL and the request unchanged.
const WINDOWS = [
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
] as const;

type WindowValue = (typeof WINDOWS)[number]["value"];

const DEFAULT_WINDOW: WindowValue = "30d";

// A KEK this old is worth rotating. It is a prompt, not a policy: nothing
// expires, and the server does not refuse anything at 366 days.
const KEK_AGE_WARNING_DAYS = 365;

// Every list here is a server-capped snapshot of one window, so nothing sorts.
// The columns are still declared so the headers come from `SortHeaderRow` like
// every other list's rather than from a hand-rolled `<thead>`.
const ADMIN_CERT_COLUMNS = staticController<ExpiringAdminCert>([
  { id: "identity", label: "Identity" },
  { id: "status", label: "Status" },
  { id: "serial", label: "Serial" },
  { id: "expires", label: "Expires" },
]);
const IDENTITY_CERT_COLUMNS = staticController<ExpiringIdentityCert>([
  { id: "identity", label: "Identity" },
  { id: "environment", label: "Environment" },
  { id: "serial", label: "Serial" },
  { id: "expires", label: "Expires" },
]);
const SECRET_COLUMNS = staticController<ExpiringSecretVersion>([
  { id: "secret", label: "Secret" },
  { id: "version", label: "Version" },
  { id: "expires", label: "Expires" },
]);

/** Enough of a certificate serial to recognise a row; the rest is in `title`. */
const SERIAL_PREVIEW_LENGTH = 12;

function shortSerial(serial: string): string {
  return serial.length > SERIAL_PREVIEW_LENGTH
    ? `${serial.slice(0, SERIAL_PREVIEW_LENGTH)}…`
    : serial;
}

/** A serial cell: shortened so it cannot widen the card, whole on hover and on
 *  the clipboard, because the revoke command needs it exactly. */
function SerialCell({ serial }: { serial: string }) {
  return (
    <td className="mono" data-label="Serial">
      <span className="row-wrap">
        <span title={serial}>{shortSerial(serial)}</span>
        <CopyButton label="Copy serial" value={serial} />
      </span>
    </td>
  );
}

const QUERY_KEYS = ["window"] as const;

function windowFromQuery(raw: string | null): WindowValue {
  const match = WINDOWS.find((w) => w.value === raw);
  return match ? match.value : DEFAULT_WINDOW;
}

/** RFC 3339 → Unix ms, or undefined for an absent or unparseable instant. */
function msOf(iso: string | undefined): number | undefined {
  if (!iso) return undefined;
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? undefined : ms;
}

/** "2160h0m0s" → "90d"; anything else (including "forever") passes through. */
function humanDuration(raw: string): string {
  const match = /^(\d+)h0m0s$/.exec(raw);
  if (!match) return raw;
  const hours = Number(match[1]);
  return hours % 24 === 0 ? `${hours / 24}d` : `${hours}h`;
}

/** A timestamp cell: relative for scanning, absolute in the tooltip. */
function When({ iso, now }: { iso: string; now: number }) {
  const ms = msOf(iso);
  if (ms === undefined) return <span className="faint">—</span>;
  return (
    <span className="nowrap" title={formatUnixMs(ms)}>
      {formatRelative(ms, now)}
    </span>
  );
}

/** The fixed-window note above the admin table. Rendered by the skeleton too,
 *  with the window elided, so the paragraph's lines are reserved either way. */
function AdminCertNote({ window }: { window?: string }) {
  return (
    <p className="faint text-sm">
      Fixed {window ?? "…"} look-ahead: an expired admin certificate is refused by the TLS handshake
      itself, before the server can explain anything.
    </p>
  );
}

/** "Showing the first 200 of 412." — only when the server capped the list. */
function TruncatedNotice({ shown, total }: { shown: number; total: number }) {
  return (
    <p className="faint text-sm mt-2">
      Showing the first {shown} of {total}. Narrow the window to see fewer.
    </p>
  );
}

export default function PosturePage() {
  const { values, ready } = useQueryParams(QUERY_KEYS);
  // On a static export the query is empty until the client router hydrates;
  // fetching before that would use the default window and then refetch.
  if (!ready) return <PostureSkeleton />;
  return <Posture initialWindow={windowFromQuery(values.window)} />;
}

/** The loaded page's structure, not a single table: four stats, the window
 *  selector, and three cards with their own column lists. The one-card version
 *  this replaces reserved 900px against a 1372px page at 1280, so everything
 *  below the fold moved on arrival. */
function PostureSkeleton() {
  return (
    <>
      <div className="card-grid mb-4">
        <StatSkeleton label="Key age" />
        <StatSkeleton label="Admin authentication" />
        <StatSkeleton label="Audit" />
        <StatSkeleton label="Metrics" />
      </div>
      {/* The same row the selector renders: a caption and three buttons at
          --control-h. A <div>, not the loaded <fieldset>, because there is
          nothing here to label. */}
      <div className="row-wrap mb-4 min-w-0" aria-hidden>
        <span className="field-label">Expiring within</span>
        {WINDOWS.map((option) => (
          <Skeleton key={option.value} width="6.5ch" height="var(--control-h)" />
        ))}
      </div>
      <div className="card">
        <h2 className="card-title">Admin certificates</h2>
        <AdminCertNote />
        <TableSkeleton headers={headerLabels(ADMIN_CERT_COLUMNS.columns)} rows={2} />
      </div>
      <div className="card">
        <h2 className="card-title">Identity certificates expiring</h2>
        <TableSkeleton headers={headerLabels(IDENTITY_CERT_COLUMNS.columns)} rows={2} />
      </div>
      <div className="card">
        <h2 className="card-title">Secret versions expiring</h2>
        <TableSkeleton headers={headerLabels(SECRET_COLUMNS.columns)} rows={2} />
      </div>
    </>
  );
}

function Posture({ initialWindow }: { initialWindow: WindowValue }) {
  const toast = useToast();
  const now = useNow();
  const replaceQuery = useQueryReplace("/posture");
  const { begin } = useLatestRequest();
  const windowLabelId = useId();
  const [expiryWindow, setExpiryWindow] = useState<WindowValue>(initialWindow);
  const [posture, setPosture] = useState<PostureResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (selected: WindowValue) => {
      const run = begin();
      setLoading(true);
      try {
        // One window drives both lists; the admin-certificate window is fixed
        // server-side and deliberately not a parameter.
        const res = await api.posture(
          { cert_window: selected, secret_window: selected },
          { signal: run.signal },
        );
        if (!run.current) return;
        setPosture(res);
        setError(null);
      } catch (err) {
        if (!run.current || isAbortError(err)) return;
        // A stale snapshot must not outlive a failed refresh: what it says is
        // expiring may already have been acted on.
        setPosture(null);
        setError(err instanceof Error ? err.message : "Failed to load the security posture.");
        toast.error(err, "Failed to load the security posture");
      } finally {
        if (run.current) setLoading(false);
      }
    },
    [begin, toast],
  );

  useEffect(() => {
    void load(expiryWindow);
  }, [load, expiryWindow]);

  function chooseWindow(next: WindowValue) {
    if (next === expiryWindow) return;
    setExpiryWindow(next);
    replaceQuery({ window: next === DEFAULT_WINDOW ? "" : next });
  }

  const kekAgeDays = posture ? Math.floor(posture.kek.age_seconds / 86_400) : 0;
  const kekStale = kekAgeDays > KEK_AGE_WARNING_DAYS;
  const adminCerts = posture?.admin_certs;
  const identityCerts = posture?.identity_certs_expiring;
  const secretVersions = posture?.secret_versions_expiring;
  const adminRowCount = (adminCerts?.lacking.length ?? 0) + (adminCerts?.expiring.length ?? 0);

  return (
    <>
      <PageHeader
        title="Security posture"
        subtitle="What is about to expire, how old the key is, and whether admin authentication is in its strong posture. Metadata only — never a value, token, or key."
        actions={
          <Button variant="outline" onClick={() => void load(expiryWindow)} disabled={loading}>
            {loading ? <Spinner /> : <RefreshCw size={16} aria-hidden />}
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
        }
      />

      {error ? (
        <div className="danger-panel mb-4" role="alert">
          <div>
            <strong>Could not load the security posture.</strong>
            <div className="text-sm">{error}</div>
          </div>
          <Button variant="outline" size="sm" onClick={() => void load(expiryWindow)}>
            Retry
          </Button>
        </div>
      ) : null}

      {posture === null ? (
        error ? null : (
          <PostureSkeleton />
        )
      ) : (
        <>
          <div className="card-grid mb-4">
            <div className="stat">
              <div className="stat-label">Key age</div>
              <div className="stat-value">{posture.kek.active_id ? `${kekAgeDays}d` : "—"}</div>
              <div className="stat-badges">
                {posture.kek.active_id ? (
                  <>
                    <Badge kind={kekStale ? "warning" : "success"}>
                      {kekStale ? "rotation due" : "current"}
                    </Badge>
                    <Badge kind="neutral">
                      {posture.kek.generations === 1
                        ? "never rotated"
                        : `${posture.kek.generations} generations`}
                    </Badge>
                  </>
                ) : (
                  <Badge kind="neutral">no active key</Badge>
                )}
              </div>
              <div className="faint text-sm mono">{posture.kek.active_id || "—"}</div>
            </div>

            {/* The three settings that together decide whether an admin's
                token alone is enough to act. */}
            <div className="stat">
              <div className="stat-label">Admin authentication</div>
              <div className="stat-badges">
                <Badge kind={posture.auth.admin_client_cert_required ? "success" : "warning"}>
                  {posture.auth.admin_client_cert_required
                    ? "client cert required"
                    : "client cert relaxed"}
                </Badge>
                <Badge kind={posture.auth.mtls_enabled ? "success" : "neutral"}>
                  {posture.auth.mtls_enabled ? "mTLS on" : "mTLS off"}
                </Badge>
                <Badge kind={posture.auth.tls_enabled ? "success" : "danger"}>
                  {posture.auth.tls_enabled ? "TLS on" : "TLS off"}
                </Badge>
              </div>
            </div>

            <div className="stat">
              <div className="stat-label">Audit</div>
              <div className="stat-value-sm">
                {posture.audit.enabled ? humanDuration(posture.audit.retain_duration) : "—"}
              </div>
              <div className="stat-badges">
                <Badge kind={posture.audit.enabled ? "success" : "danger"}>
                  {posture.audit.enabled ? "recording" : "off"}
                </Badge>
                <Badge kind={posture.audit.archive_enabled ? "success" : "neutral"}>
                  {posture.audit.archive_enabled ? "archiving" : "no archive"}
                </Badge>
              </div>
            </div>

            <div className="stat">
              <div className="stat-label">Metrics</div>
              <div className="stat-badges">
                <Badge kind={posture.metrics_enabled ? "success" : "neutral"}>
                  {posture.metrics_enabled ? "exported" : "off"}
                </Badge>
              </div>
              <div className="faint text-sm">
                Snapshot taken <When iso={posture.generated_at} now={now} />
              </div>
            </div>
          </div>

          {/* A segmented control, not a filter row: `.filters` drops a bare
              button by one label block to line it up with labelled siblings,
              and there are none here. The caption is a span, not a <legend>,
              because a rendered legend is lifted out of the fieldset's flex
              formatting context and would sit on its own line above the row.
              min-w-0 undoes the fieldset's min-content floor so it can wrap. */}
          <fieldset className="row-wrap mb-4 min-w-0" aria-labelledby={windowLabelId}>
            <span className="field-label" id={windowLabelId}>
              Expiring within
            </span>
            {WINDOWS.map((option) => (
              <Button
                key={option.value}
                type="button"
                variant={option.value === expiryWindow ? "outline" : "ghost"}
                aria-pressed={option.value === expiryWindow}
                onClick={() => chooseWindow(option.value)}
              >
                {option.label}
              </Button>
            ))}
          </fieldset>

          <div className="card">
            <h2 className="card-title">Admin certificates</h2>
            <AdminCertNote window={humanDuration(posture.windows.admin_cert)} />
            {adminRowCount === 0 ? (
              <EmptyState
                icon={<Icon.identity size={20} />}
                title="Every admin has a valid certificate"
              >
                No enabled admin is missing a client certificate or about to lose one.
              </EmptyState>
            ) : (
              <div className="table-wrap card-table">
                <table className="data">
                  <thead>
                    <SortHeaderRow controller={ADMIN_CERT_COLUMNS} />
                  </thead>
                  <tbody>
                    {adminCerts?.lacking.map((identity) => (
                      <tr key={`lacking-${identity}`}>
                        <td data-label="Identity">{identity}</td>
                        <td data-label="Status">
                          <Badge kind="danger">no certificate</Badge>
                        </td>
                        <td data-label="Serial">
                          <span className="faint">—</span>
                        </td>
                        <td data-label="Expires">
                          <span className="faint">—</span>
                        </td>
                      </tr>
                    ))}
                    {adminCerts?.expiring.map((cert) => (
                      <tr key={`expiring-${cert.serial}`}>
                        <td data-label="Identity">{cert.identity}</td>
                        <td data-label="Status">
                          <Badge kind="warning">expiring</Badge>
                        </td>
                        <SerialCell serial={cert.serial} />
                        <td data-label="Expires">
                          <When iso={cert.not_after} now={now} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="card">
            <h2 className="card-title">Identity certificates expiring</h2>
            {identityCerts && identityCerts.items.length > 0 ? (
              <>
                <div className="table-wrap card-table">
                  <table className="data">
                    <thead>
                      <SortHeaderRow controller={IDENTITY_CERT_COLUMNS} />
                    </thead>
                    <tbody>
                      {identityCerts.items.map((cert) => (
                        <tr key={cert.serial}>
                          <td data-label="Identity">{cert.identity}</td>
                          <td data-label="Environment">
                            {cert.env && cert.app ? (
                              <span className="cell-path">{`${cert.env}/${cert.app}`}</span>
                            ) : (
                              <span className="faint">unbound</span>
                            )}
                          </td>
                          <SerialCell serial={cert.serial} />
                          <td data-label="Expires">
                            <When iso={cert.not_after} now={now} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                {identityCerts.truncated ? (
                  <TruncatedNotice shown={identityCerts.items.length} total={identityCerts.total} />
                ) : null}
              </>
            ) : (
              <EmptyState icon={<Icon.identity size={20} />} title="No certificates expiring">
                No unrevoked client certificate expires in this window.
              </EmptyState>
            )}
          </div>

          <div className="card">
            <h2 className="card-title">Secret versions expiring</h2>
            {secretVersions && secretVersions.items.length > 0 ? (
              <>
                <div className="table-wrap card-table">
                  <table className="data">
                    <thead>
                      <SortHeaderRow controller={SECRET_COLUMNS} />
                    </thead>
                    <tbody>
                      {secretVersions.items.map((version) => (
                        <tr key={`${version.env}/${version.app}/${version.key}#${version.version}`}>
                          <td data-label="Secret">
                            <span className="cell-path">{`/${version.env}/${version.app}/${version.key}`}</span>
                          </td>
                          <td data-label="Version">v{version.version}</td>
                          <td data-label="Expires">
                            <When iso={version.expires_at} now={now} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                {secretVersions.truncated ? (
                  <TruncatedNotice
                    shown={secretVersions.items.length}
                    total={secretVersions.total}
                  />
                ) : null}
              </>
            ) : (
              <EmptyState icon={<Icon.secret size={20} />} title="No secret versions expiring">
                No enabled secret version expires in this window.
              </EmptyState>
            )}
          </div>
        </>
      )}
    </>
  );
}
