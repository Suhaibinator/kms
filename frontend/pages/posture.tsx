import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Icon } from "@/components/icons";
import {
  Badge,
  EmptyState,
  PageHeader,
  Spinner,
  StatSkeleton,
  TableSkeleton,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { useLatestRequest, useQueryParams } from "@/lib/hooks";
import type { PostureResponse } from "@/lib/types";
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

const ADMIN_CERT_HEADERS = ["Identity", "Status", "Serial", "Expires"];
const IDENTITY_CERT_HEADERS = ["Identity", "Environment", "Serial", "Expires"];
const SECRET_HEADERS = ["Secret", "Version", "Expires"];

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

function PostureSkeleton() {
  return (
    <>
      <div className="card-grid mb-4">
        <StatSkeleton label="Key age" />
        <StatSkeleton label="Admin authentication" />
        <StatSkeleton label="Audit" />
        <StatSkeleton label="Metrics" />
      </div>
      <div className="card">
        <TableSkeleton headers={IDENTITY_CERT_HEADERS} rows={5} />
      </div>
    </>
  );
}

function Posture({ initialWindow }: { initialWindow: WindowValue }) {
  const toast = useToast();
  const now = useNow();
  const replaceQuery = useQueryReplace("/posture");
  const { begin } = useLatestRequest();
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

          <fieldset className="filters">
            <legend className="faint text-sm">Expiring within</legend>
            {WINDOWS.map((option) => (
              <Button
                key={option.value}
                type="button"
                size="sm"
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
            <p className="faint text-sm">
              Fixed {humanDuration(posture.windows.admin_cert)} look-ahead: an expired admin
              certificate is refused by the TLS handshake itself, before the server can explain
              anything.
            </p>
            {adminRowCount === 0 ? (
              <EmptyState
                icon={<Icon.identity size={20} />}
                title="Every admin has a valid certificate"
              >
                No enabled admin is missing a client certificate or about to lose one.
              </EmptyState>
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      {ADMIN_CERT_HEADERS.map((header) => (
                        <th key={header}>{header}</th>
                      ))}
                    </tr>
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
                        <td className="mono" data-label="Serial">
                          {cert.serial}
                        </td>
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
                <div className="table-wrap">
                  <table className="data">
                    <thead>
                      <tr>
                        {IDENTITY_CERT_HEADERS.map((header) => (
                          <th key={header}>{header}</th>
                        ))}
                      </tr>
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
                          <td className="mono" data-label="Serial">
                            {cert.serial}
                          </td>
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
                <div className="table-wrap">
                  <table className="data">
                    <thead>
                      <tr>
                        {SECRET_HEADERS.map((header) => (
                          <th key={header}>{header}</th>
                        ))}
                      </tr>
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
