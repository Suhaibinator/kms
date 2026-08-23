import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Modal } from "@/components/Modal";
import { Badge, Button, Field, Pagination, Spinner } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { useCursorPagination, useLatestRequest } from "@/lib/hooks";
import type {
  ConfigurationRelease,
  ReleaseSubscriberState,
  ReleaseSummary,
  ReleaseValidationError,
} from "@/lib/types";
import { refText, releaseKey } from "./utils";

export interface ActivationFailure {
  operation: "Activation" | "Rollback" | "Validation";
  target: string;
  violations: ReleaseValidationError[];
}

function lifecycleCell(state: ReleaseSubscriberState | undefined) {
  if (!state) return <span className="faint">—</span>;
  const prefix =
    state.state === "rejected" && state.rejection_category ? `${state.rejection_category} · ` : "";
  const summary = `${prefix}v${state.release_version} · r${state.activation_revision}`;
  if (!state.diagnostic) return summary;
  return (
    <details>
      <summary>{summary}</summary>
      <div className="text-sm">{state.diagnostic}</div>
    </details>
  );
}

export function ReleaseWorkspace({
  summary,
  releases,
  busyAction,
  activationFailure,
  onDismissFailure,
  onClose,
  onValidate,
  onActivate,
}: {
  summary: ReleaseSummary | null;
  releases: ReleaseSummary[];
  busyAction: string;
  activationFailure: ActivationFailure | null;
  onDismissFailure: () => void;
  onClose: () => void;
  onValidate: (release: ConfigurationRelease) => void;
  onActivate: (summary: ReleaseSummary) => void;
}) {
  const toast = useToast();
  const release = summary?.release ?? null;
  // Stable identity for the open release: the summary object is replaced by
  // every list refresh, and resetting on that would bounce the user back to
  // Overview after each activation.
  const key = release ? releaseKey(release) : "";
  const [section, setSection] = useState("overview");
  // "" means "use the derived default" (the next-lower version, if loaded).
  const [compareKey, setCompareKey] = useState("");
  const { begin } = useLatestRequest();
  const [subscribers, setSubscribers] = useState<ReleaseSubscriberState[]>([]);
  const [subscriberRevision, setSubscriberRevision] = useState(0);
  const [subscribersLoading, setSubscribersLoading] = useState(false);
  const subscriberScope = release
    ? JSON.stringify([release.namespace.env, release.namespace.app, release.name])
    : "";
  const subscriberPaging = useCursorPagination(subscriberScope);

  const sameNameReleases = useMemo(
    () =>
      release
        ? releases
            .filter((candidate) => candidate.release.name === release.name)
            .sort((a, b) => b.release.version - a.release.version)
        : [],
    [release, releases],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: reset per release identity (`key`), not per summary object.
  useEffect(() => {
    setSection("overview");
    setCompareKey("");
    setSubscribers([]);
    setSubscriberRevision(0);
  }, [key]);

  const defaultCompareKey = useMemo(() => {
    const previous = release
      ? sameNameReleases.find((candidate) => candidate.release.version < release.version)
      : undefined;
    return previous ? releaseKey(previous.release) : "";
  }, [release, sameNameReleases]);
  const effectiveCompareKey = compareKey || defaultCompareKey;

  const loadSubscribers = useCallback(async () => {
    if (!release) return;
    const run = begin();
    setSubscribersLoading(true);
    try {
      const response = await api.releaseSubscribers(
        release.namespace,
        release.name,
        1000,
        subscriberPaging.pageToken || undefined,
        { signal: run.signal },
      );
      if (!run.current) return;
      setSubscribers(response.subscribers ?? []);
      setSubscriberRevision(response.current_revision ?? 0);
      subscriberPaging.setNextToken(response.next_page_token ?? "");
    } catch (error) {
      if (run.current && !isAbortError(error)) {
        toast.error(error, "Failed to load rollout status");
      }
    } finally {
      if (run.current) setSubscribersLoading(false);
    }
  }, [begin, release, subscriberPaging.pageToken, subscriberPaging.setNextToken, toast]);

  useEffect(() => {
    if (section === "rollout" && release) void loadSubscribers();
  }, [loadSubscribers, release, section]);

  const comparison = sameNameReleases.find(
    (candidate) => releaseKey(candidate.release) === effectiveCompareKey,
  )?.release;
  const diff = useMemo(() => {
    if (!release || !comparison) return [];
    const fromByAlias = new Map(comparison.entries.map((entry) => [entry.alias, entry]));
    const toByAlias = new Map(release.entries.map((entry) => [entry.alias, entry]));
    const aliases = new Set([...fromByAlias.keys(), ...toByAlias.keys()]);
    return [...aliases].sort().flatMap((alias) => {
      const from = fromByAlias.get(alias);
      const to = toByAlias.get(alias);
      const left = from
        ? `${from.kind} ${refText(from)}@${from.version}${from.parameter_digest ? ` ${from.parameter_digest.slice(0, 12)}` : ""}`
        : "—";
      const right = to
        ? `${to.kind} ${refText(to)}@${to.version}${to.parameter_digest ? ` ${to.parameter_digest.slice(0, 12)}` : ""}`
        : "—";
      return left === right ? [] : [{ alias, left, right }];
    });
  }, [comparison, release]);

  const subscriberInstances = useMemo(() => {
    const grouped = new Map<
      string,
      {
        identity: string;
        client: string;
        instance: string;
        connected: boolean;
        states: Partial<
          Record<"received" | "prepared" | "applied" | "rejected", ReleaseSubscriberState>
        >;
        latestRevision: number;
      }
    >();
    for (const state of subscribers) {
      const key = JSON.stringify([state.identity, state.client_name, state.instance_id]);
      const row = grouped.get(key) ?? {
        identity: state.identity,
        client: state.client_name,
        instance: state.instance_id,
        connected: false,
        states: {},
        latestRevision: 0,
      };
      if (["received", "prepared", "applied", "rejected"].includes(state.state)) {
        row.states[state.state as keyof typeof row.states] = state;
      }
      row.connected ||= state.connected;
      row.latestRevision = Math.max(row.latestRevision, state.activation_revision);
      grouped.set(key, row);
    }
    return [...grouped.values()].sort(
      (a, b) =>
        a.identity.localeCompare(b.identity) ||
        a.client.localeCompare(b.client) ||
        a.instance.localeCompare(b.instance),
    );
  }, [subscribers]);

  return (
    <Modal
      open={Boolean(release)}
      workspace
      title={release ? `Release ${releaseKey(release)}` : "Release details"}
      onClose={onClose}
    >
      {release && summary ? (
        <Tabs value={section} onValueChange={(value) => setSection(String(value))}>
          <div className="release-workspace-toolbar">
            <TabsList variant="line" aria-label="Release details">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="entries">Entries</TabsTrigger>
              <TabsTrigger value="compare">Compare</TabsTrigger>
              <TabsTrigger value="rollout">Rollout status</TabsTrigger>
            </TabsList>
            <div className="row-wrap">
              <Button
                variant="outline"
                size="sm"
                disabled={Boolean(busyAction)}
                onClick={() => onValidate(release)}
              >
                {busyAction === `validate:${releaseKey(release)}` ? <Spinner /> : null}
                Validate
              </Button>
              <Button
                size="sm"
                disabled={summary.current || Boolean(busyAction)}
                onClick={() => onActivate(summary)}
              >
                Activate
              </Button>
            </div>
          </div>

          {activationFailure ? (
            <section className="danger-panel mb-4" role="alert">
              <div className="between">
                <div>
                  <strong>
                    {activationFailure.operation === "Validation"
                      ? `${activationFailure.target} failed validation`
                      : `${activationFailure.operation} blocked for ${activationFailure.target}`}
                  </strong>
                  <div className="text-sm mt-2">
                    {activationFailure.operation === "Validation"
                      ? "Resolve every violation before activating this release."
                      : "The active release and activation revision were not changed."}
                  </div>
                </div>
                <Button variant="outline" size="sm" onClick={onDismissFailure}>
                  Dismiss
                </Button>
              </div>
              <div className="table-wrap mt-3">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Alias</th>
                      <th>Code</th>
                      <th>Schema pointer</th>
                      <th>Message</th>
                    </tr>
                  </thead>
                  <tbody>
                    {activationFailure.violations.map((violation) => (
                      <tr key={JSON.stringify(violation)}>
                        <td className="mono">{violation.alias || "release"}</td>
                        <td>
                          <Badge kind="danger">{violation.code}</Badge>
                        </td>
                        <td className="mono">{violation.schema_pointer || "—"}</td>
                        <td>{violation.message}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          ) : null}

          <TabsContent value="overview">
            <div className="release-overview-grid">
              <div className="stat">
                <div className="stat-label">State</div>
                <div className="stat-badges">
                  {summary.current ? (
                    <Badge kind="success">current · rev {summary.activation_revision}</Badge>
                  ) : summary.previous ? (
                    <Badge kind="warning">previous</Badge>
                  ) : (
                    <Badge>inactive</Badge>
                  )}
                </div>
              </div>
              <div className="stat">
                <div className="stat-label">Entries</div>
                <div className="stat-value">{release.entries.length}</div>
              </div>
              <div className="stat">
                <div className="stat-label">Schema</div>
                <div className="stat-sub mono mt-2">
                  {release.schema_id ? `${release.schema_id}@${release.schema_version}` : "none"}
                </div>
              </div>
            </div>
            <dl className="kv card mt-4">
              <dt>Namespace</dt>
              <dd className="mono">
                {release.namespace.env}/{release.namespace.app}
              </dd>
              <dt>Created</dt>
              <dd>{formatUnixMs(release.created_at_unix_ms)}</dd>
              <dt>Created by</dt>
              <dd className="mono">{release.created_by || "—"}</dd>
              <dt>Digest</dt>
              <dd className="release-copy-value">
                <code>{release.digest}</code>
                <CopyButton value={release.digest} label="Copy digest" />
              </dd>
              <dt>Metadata</dt>
              <dd>
                <pre className="json-block release-metadata-json">
                  {release.metadata_json || "{}"}
                </pre>
              </dd>
            </dl>
          </TabsContent>

          <TabsContent value="entries">
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Alias</th>
                    <th>Kind</th>
                    <th>Reference</th>
                    <th>Version</th>
                    <th>Content type</th>
                    <th>Parameter digest</th>
                    <th>Secret protection</th>
                    <th>Metadata</th>
                  </tr>
                </thead>
                <tbody>
                  {release.entries.map((entry) => (
                    <tr key={entry.alias}>
                      <td className="mono">{entry.alias}</td>
                      <td>{entry.kind}</td>
                      <td className="mono">{refText(entry)}</td>
                      <td>{entry.version}</td>
                      <td>{entry.content_type || "—"}</td>
                      <td className="mono">{entry.parameter_digest || "—"}</td>
                      <td>
                        {entry.kind === "secret"
                          ? [
                              entry.has_access_token ? "token" : "no token",
                              entry.client_bound ? "client-bound" : "shared",
                            ].join(" · ")
                          : "—"}
                      </td>
                      <td className="mono">{entry.metadata_json || "{}"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </TabsContent>

          <TabsContent value="compare">
            <div className="max-w-md mb-4">
              <Field label="Compare with">
                <AppSelect
                  value={effectiveCompareKey}
                  onValueChange={setCompareKey}
                  placeholder="Choose another version…"
                  options={sameNameReleases
                    .filter((candidate) => candidate.release.version !== release.version)
                    .map((candidate) => ({
                      value: releaseKey(candidate.release),
                      label: releaseKey(candidate.release),
                    }))}
                />
              </Field>
            </div>
            {!comparison ? (
              <div className="faint">No other loaded version is available for comparison.</div>
            ) : diff.length === 0 ? (
              <div className="info-panel">No manifest differences.</div>
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Alias</th>
                      <th>{releaseKey(comparison)}</th>
                      <th>{releaseKey(release)}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {diff.map((item) => (
                      <tr key={item.alias}>
                        <td className="mono">{item.alias}</td>
                        <td className="mono">{item.left}</td>
                        <td className="mono">{item.right}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </TabsContent>

          <TabsContent value="rollout">
            <div className="between mb-3">
              <div className="text-sm faint">
                Activation revision {subscriberRevision || "—"} · up to 1,000 state rows per page
              </div>
              <Button
                variant="outline"
                size="sm"
                disabled={subscribersLoading}
                onClick={() => void loadSubscribers()}
              >
                {subscribersLoading ? <Spinner /> : <RefreshCw size={15} aria-hidden />}
                Refresh
              </Button>
            </div>
            {subscribersLoading ? (
              <div className="loading-block" role="status">
                <Spinner /> Loading rollout status…
              </div>
            ) : subscriberInstances.length === 0 ? (
              <div className="faint">No subscriber state recorded.</div>
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Identity</th>
                      <th>Client</th>
                      <th>Instance</th>
                      <th>Received</th>
                      <th>Prepared</th>
                      <th>Applied</th>
                      <th>Rejected</th>
                      <th>Connection</th>
                      <th>Lag</th>
                    </tr>
                  </thead>
                  <tbody>
                    {subscriberInstances.map((instance) => (
                      <tr
                        key={JSON.stringify([
                          instance.identity,
                          instance.client,
                          instance.instance,
                        ])}
                      >
                        <td>{instance.identity}</td>
                        <td>{instance.client}</td>
                        <td className="mono">{instance.instance}</td>
                        <td className="mono">{lifecycleCell(instance.states.received)}</td>
                        <td className="mono">{lifecycleCell(instance.states.prepared)}</td>
                        <td className="mono">{lifecycleCell(instance.states.applied)}</td>
                        <td className="mono">{lifecycleCell(instance.states.rejected)}</td>
                        <td>
                          <Badge kind={instance.connected ? "success" : "neutral"}>
                            {instance.connected ? "connected" : "disconnected"}
                          </Badge>
                        </td>
                        <td>{Math.max(0, subscriberRevision - instance.latestRevision)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <Pagination
              hasNext={!subscribersLoading && subscriberPaging.hasNext}
              onNext={subscriberPaging.next}
              hasPrevious={!subscribersLoading && subscriberPaging.hasPrevious}
              onPrevious={subscriberPaging.previous}
              onReset={subscriberPaging.reset}
              showReset={!subscribersLoading && subscriberPaging.hasPrevious}
              page={subscriberPaging.page}
            />
          </TabsContent>
        </Tabs>
      ) : null}
    </Modal>
  );
}
