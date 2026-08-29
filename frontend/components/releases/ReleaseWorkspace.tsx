import { useEffect, useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { NamespaceIdent, ReleaseIdent } from "@/components/Ident";
import { Modal } from "@/components/Modal";
import { RolloutPanel } from "@/components/ship/RolloutPanel";
import { Badge, Button, Field, JsonView } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatUnixMs } from "@/lib/format";
import type { ConfigurationRelease, ReleaseSummary } from "@/lib/types";
import { refText, releaseKey } from "./utils";
import {
  type ActivationFailure,
  ActivationFailurePanel,
  type ViolationTableProps,
} from "./ViolationTable";

export type { ActivationFailure } from "./ViolationTable";

export function ReleaseWorkspace({
  summary,
  releases,
  busyAction,
  activationFailure,
  onDismissFailure,
  resolveHref,
  onClose,
  onValidate,
  onActivate,
  onRollback,
}: {
  summary: ReleaseSummary | null;
  releases: ReleaseSummary[];
  busyAction: string;
  activationFailure: ActivationFailure | null;
  onDismissFailure: () => void;
  /** Links each violation's alias to the resource it pins. */
  resolveHref?: ViolationTableProps["resolveHref"];
  onClose: () => void;
  onValidate: (release: ConfigurationRelease) => void;
  onActivate: (summary: ReleaseSummary) => void;
  /** Roll back to the previous version of this name; shown only for the current release. */
  onRollback?: (current: ReleaseSummary, previous: ReleaseSummary) => void;
}) {
  const release = summary?.release ?? null;
  // Stable identity for the open release: the summary object is replaced by
  // every list refresh, and resetting on that would bounce the user back to
  // Overview after each activation.
  const key = release ? releaseKey(release) : "";
  const [section, setSection] = useState("overview");
  // "" means "use the derived default" (the next-lower version, if loaded).
  const [compareKey, setCompareKey] = useState("");

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
  }, [key]);

  const defaultCompareKey = useMemo(() => {
    const previous = release
      ? sameNameReleases.find((candidate) => candidate.release.version < release.version)
      : undefined;
    return previous ? releaseKey(previous.release) : "";
  }, [release, sameNameReleases]);
  const effectiveCompareKey = compareKey || defaultCompareKey;

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

  const previousSummary = summary?.current
    ? sameNameReleases.find((candidate) => candidate.previous)
    : undefined;

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
                loading={busyAction === `validate:${releaseKey(release)}`}
                onClick={() => onValidate(release)}
              >
                Validate
              </Button>
              {onRollback && summary.current && previousSummary ? (
                <Button
                  variant="destructive"
                  size="sm"
                  disabled={Boolean(busyAction)}
                  onClick={() => onRollback(summary, previousSummary)}
                >
                  Roll back to previous
                </Button>
              ) : null}
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
            <ActivationFailurePanel
              failure={activationFailure}
              onDismiss={onDismissFailure}
              resolveHref={resolveHref}
            />
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
                <div className="stat-value-sm mono">
                  {release.schema_id ? `${release.schema_id}@${release.schema_version}` : "none"}
                </div>
              </div>
            </div>
            <dl className="kv card mt-4">
              <dt>Namespace</dt>
              <dd>
                <NamespaceIdent ns={release.namespace} />
              </dd>
              <dt>Release</dt>
              <dd>
                <ReleaseIdent name={release.name} version={release.version} />
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
                <JsonView raw={release.metadata_json || "{}"} />
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
            {/* The hook streams (or polls) only while this tab is open. */}
            <RolloutPanel
              namespace={release.namespace}
              releaseName={release.name}
              activationRevision={summary.activation_revision}
              enabled={section === "rollout"}
              caption={
                summary.current
                  ? "Live state for every instance subscribed to this release name, up to 1,000 rows."
                  : "This version is not active; instances report against the current activation. Up to 1,000 rows."
              }
              onRollback={
                onRollback && summary.current && previousSummary
                  ? () => onRollback(summary, previousSummary)
                  : undefined
              }
              rollbackDisabled={Boolean(busyAction)}
            />
          </TabsContent>
        </Tabs>
      ) : null}
    </Modal>
  );
}
