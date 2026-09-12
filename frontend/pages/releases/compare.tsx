import { ArrowLeft, ArrowLeftRight, ChevronLeft, ChevronRight } from "lucide-react";
import { useRouter } from "next/router";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { Ident, ReleaseIdent } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { ReleaseDiffView } from "@/components/releases/diff/ReleaseDiffView";
import RollbackDialog from "@/components/ship/RollbackDialog";
import { Badge, EmptyState, PageHeader } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api, isAbortError, type ReleaseDiffQuery } from "@/lib/api";
import { crumbs } from "@/lib/crumbs";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { useLatestRequest, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import { parseSchemaVersion } from "@/lib/schema";
import type {
  OverviewActiveRelease,
  OverviewRollout,
  ReleaseDiffResponse,
  ReleaseDiffSide,
  ReleaseSummary,
} from "@/lib/types";
import { useQueryReplace } from "@/lib/url";
import { useNow } from "@/lib/useNow";

type Selector = number | "current" | "previous";

/** `from`/`to` in the URL: a positive integer or one of the track labels. */
function parseSelector(value: string | null): Selector | undefined {
  if (value === "current" || value === "previous") return value;
  if (value && /^\d+$/.test(value)) {
    const version = Number(value);
    if (Number.isSafeInteger(version) && version > 0) return version;
  }
  return undefined;
}

/** The RollbackDialog's view of the active release, from the diff's `to` side. */
function activeFromSide(side: ReleaseDiffSide): OverviewActiveRelease {
  return {
    name: side.name,
    version: side.version,
    activation_revision: side.activation_revision,
    previous_version: side.previous_version,
    created_by: side.created_by,
    created_at_unix_ms: side.created_at_unix_ms,
    is_rolled_back: side.previous_version > side.version,
    schema_version: side.schema_version,
    digest: side.digest,
    entries: [],
  };
}

function versionLabel(summary: ReleaseSummary): string {
  const release = summary.release;
  const key = `${release.name}@${release.schema_version}:${release.version}`;
  return summary.current ? `${key} · current` : summary.previous ? `${key} · previous` : key;
}

function SideBadge({ side }: { side: ReleaseDiffSide }) {
  if (side.current) return <Badge kind="success">current · rev {side.activation_revision}</Badge>;
  if (side.previous) return <Badge kind="warning">previous</Badge>;
  return <Badge>inactive</Badge>;
}

/**
 * `/releases/compare?app=&env=&name=&schema_version=&from=&to=` — two releases
 * of one track side by side (`to_env` compares the same track across two
 * environments). The view owns the diff request; this page owns the URL, the
 * version pickers and the header actions.
 */
export default function ReleaseComparePage() {
  const router = useRouter();
  const pickId = useId();
  const toast = useToast();
  const now = useNow();
  const { values: query, ready } = useQueryParams([
    "app",
    "env",
    "name",
    "schema_version",
    "from",
    "to",
    "to_env",
    "to_schema_version",
    "view",
    "q",
  ]);
  const replaceQuery = useQueryReplace("/releases/compare");
  const app = query.app ?? "";
  const env = query.env ?? "";
  const name = query.name ?? "";
  const schemaVersion = parseSchemaVersion(query.schema_version);
  const invalidSchema =
    query.schema_version !== null && query.schema_version !== "" && schemaVersion === undefined;
  const toSchemaVersion = parseSchemaVersion(query.to_schema_version);
  const invalidToSchema =
    query.to_schema_version !== null &&
    query.to_schema_version !== "" &&
    toSchemaVersion === undefined;
  const from = parseSelector(query.from);
  const to = parseSelector(query.to);
  const toEnv = query.to_env || undefined;
  const urlView = query.view === "all" ? "all" : "changed";
  const urlQ = query.q ?? "";

  const diffQuery = useMemo<ReleaseDiffQuery | null>(() => {
    if (
      !app ||
      !env ||
      !name ||
      schemaVersion === undefined ||
      from === undefined ||
      to === undefined
    )
      return null;
    return {
      env,
      app,
      name,
      schemaVersion,
      from,
      to,
      toEnv,
      toSchemaVersion: toEnv ? toSchemaVersion : undefined,
    };
  }, [app, env, name, schemaVersion, from, to, toEnv, toSchemaVersion]);

  // The last response the view handed back; the header, pickers and rollback
  // dialog read from it. Reset when the query changes so a stale header never
  // sits above a fresh skeleton.
  const [loaded, setLoaded] = useState<{ key: string; diff: ReleaseDiffResponse } | null>(null);
  const queryKey = diffQuery ? JSON.stringify(diffQuery) : "";
  const diff = loaded && loaded.key === queryKey ? loaded.diff : null;
  const onLoaded = useCallback(
    (response: ReleaseDiffResponse) => setLoaded({ key: queryKey, diff: response }),
    [queryKey],
  );

  // Filter state mirrors the URL: local edits win until the URL says otherwise
  // (back/forward), so typing never waits on the router round trip.
  const [local, setLocal] = useState<{ view: "changed" | "all"; q: string } | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a URL change (history navigation) discards the local mirror.
  useEffect(() => setLocal(null), [urlView, urlQ]);
  const view = local?.view ?? urlView;
  const q = local?.q ?? urlQ;
  const qWrite = useRef<ReturnType<typeof setTimeout> | null>(null);
  const onViewChange = (next: "changed" | "all") => {
    setLocal({ view: next, q });
    void replaceQuery({ view: next === "all" ? "all" : "" });
  };
  const onQueryChange = (next: string) => {
    setLocal({ view, q: next });
    if (qWrite.current) clearTimeout(qWrite.current);
    qWrite.current = setTimeout(() => void replaceQuery({ q: next }), 250);
  };
  useEffect(
    () => () => {
      if (qWrite.current) clearTimeout(qWrite.current);
    },
    [],
  );

  // Rewrite `from=previous&to=current` to the numbers the server resolved, so
  // the link the operator copies stays true after the next activation.
  // `router.replace` directly, not `useQueryReplace`: lib/url.ts forbids that
  // helper in an effect because it would fight the form state. This write is
  // not a form's — it records what the server answered with, it runs at most
  // once per label (guarded by `typeof … === "number"`), and the environment
  // page pins its schema track the same way.
  useEffect(() => {
    if (!diff || !ready) return;
    if (typeof from === "number" && typeof to === "number") return;
    void router.replace(
      {
        pathname: "/releases/compare",
        query: {
          ...router.query,
          from: String(diff.from.version),
          to: String(diff.to.version),
        },
      },
      undefined,
      { shallow: true, scroll: false },
    );
  }, [diff, ready, from, to, router]);

  // The track's versions, for the pickers and the older/newer steps.
  const versionsRequest = useLatestRequest();
  const [versions, setVersions] = useState<{ key: string; releases: ReleaseSummary[] }>({
    key: "",
    releases: [],
  });
  const versionsKey = diffQuery ? `${env}/${app}/${name}@${schemaVersion}` : "";
  useEffect(() => {
    if (!versionsKey || schemaVersion === undefined) return;
    const run = versionsRequest.begin();
    api
      .listReleases({ env, app }, name, 100, undefined, { signal: run.signal }, schemaVersion)
      .then((response) => {
        if (run.current) setVersions({ key: versionsKey, releases: response.releases });
      })
      .catch((error: unknown) => {
        if (isAbortError(error) || !run.current) return;
        // The pickers are a convenience; the diff itself reports its own errors.
        setVersions({ key: versionsKey, releases: [] });
      });
  }, [versionsKey, env, app, name, schemaVersion, versionsRequest]);
  const trackVersions = useMemo(
    () =>
      (versions.key === versionsKey ? versions.releases : [])
        .filter((summary) => summary.release.name === name)
        .sort((a, b) => b.release.version - a.release.version),
    [versions, versionsKey, name],
  );
  const options = trackVersions.map((summary) => ({
    value: String(summary.release.version),
    label: versionLabel(summary),
  }));

  // Rollout status belongs to the environment whose active release is `to`.
  const rolloutRequest = useLatestRequest();
  const [rollout, setRollout] = useState<{ key: string; value: OverviewRollout | null }>({
    key: "",
    value: null,
  });
  const rolloutWanted = Boolean(diff?.to.current) && !diff?.cross_environment;
  const rolloutKey = rolloutWanted && diff ? `${queryKey}#${diff.to.activation_revision}` : "";
  useEffect(() => {
    if (!rolloutKey || !diff) return;
    const run = rolloutRequest.begin();
    api
      .applicationOverview(app, [env], { signal: run.signal }, diff.to.schema_version)
      .then((overview) => {
        if (!run.current) return;
        const environment = overview.environments.find(
          (candidate) => candidate.namespace.env === env,
        );
        setRollout({ key: rolloutKey, value: environment?.rollout ?? null });
      })
      .catch((error: unknown) => {
        if (isAbortError(error) || !run.current) return;
        setRollout({ key: rolloutKey, value: null });
      });
  }, [rolloutKey, diff, app, env, rolloutRequest]);

  const [rollbackOpen, setRollbackOpen] = useState(false);

  const resolvedFrom = diff?.from.version ?? (typeof from === "number" ? from : undefined);
  const resolvedTo = diff?.to.version ?? (typeof to === "number" ? to : undefined);

  function swap() {
    if (from === undefined || to === undefined) return;
    const nextFrom = resolvedTo ?? to;
    const nextTo = resolvedFrom ?? from;
    if (toEnv) {
      // The `to` side lives in the other environment: swapping sides swaps
      // environments too, so `env` is always the from side.
      void replaceQuery({
        env: toEnv,
        to_env: env,
        schema_version: String(toSchemaVersion ?? schemaVersion),
        to_schema_version: String(schemaVersion),
        from: String(nextFrom),
        to: String(nextTo),
      });
      return;
    }
    void replaceQuery({ from: String(nextFrom), to: String(nextTo) });
  }

  function step(direction: -1 | 1) {
    if (resolvedTo === undefined || toEnv) return;
    const ordered = trackVersions.map((summary) => summary.release.version);
    const index = ordered.indexOf(resolvedTo);
    if (index === -1) return;
    // `ordered` is newest first, so "newer" walks toward index 0.
    const next = ordered[index - direction];
    if (next === undefined) return;
    void replaceQuery({ to: String(next) });
  }

  function pick(side: "from" | "to", value: string) {
    if (!value) return;
    void replaceQuery({ [side]: value });
  }

  const resolveHref = useCallback(
    (alias: string): string | null => {
      const row = diff?.rows.find((candidate) => candidate.alias === alias);
      const pin = row?.to ?? row?.from;
      if (!row || !pin) return null;
      const ref = { env: pin.ref.namespace.env, app: pin.ref.namespace.app, key: pin.ref.key };
      return row.kind === "secret" ? links.secretDetail(ref) : links.parameterDetail(ref);
    },
    [diff],
  );

  // On a static export the query is empty until the client router hydrates.
  if (!ready) {
    return <PageHeader title="Compare releases" documentTitle="Compare releases" />;
  }

  const back = (
    <ButtonLink
      variant="outline"
      href={
        app && env
          ? links.releases({ app, env, name: name || undefined, schemaVersion })
          : links.releases()
      }
    >
      <ArrowLeft size={16} aria-hidden /> Releases
    </ButtonLink>
  );

  if (!app || !env || !name) {
    return (
      <>
        <PageHeader title="Nothing to compare" documentTitle="Compare releases" actions={back} />
        <EmptyState icon={<Icon.release size={20} />} title="Nothing to compare">
          This page needs <span className="mono">app</span>, <span className="mono">env</span> and{" "}
          <span className="mono">name</span> in its link.
        </EmptyState>
      </>
    );
  }
  if (invalidSchema || invalidToSchema) {
    return (
      <>
        <PageHeader
          title="Invalid schema version"
          documentTitle="Compare releases"
          actions={back}
        />
        <EmptyState title="Invalid schema version">
          Use a nonnegative safe integer; 0 selects schema-free.
        </EmptyState>
      </>
    );
  }
  if (schemaVersion === undefined || from === undefined || to === undefined || !diffQuery) {
    return (
      <>
        <PageHeader title="Nothing to compare" documentTitle="Compare releases" actions={back} />
        <EmptyState icon={<Icon.release size={20} />} title="Pick two versions">
          The link needs <span className="mono">schema_version</span>, and{" "}
          <span className="mono">from</span> and <span className="mono">to</span> as version numbers
          or the labels <span className="mono">current</span> and{" "}
          <span className="mono">previous</span>.
        </EmptyState>
      </>
    );
  }

  const ns = { env, app };
  const breadcrumbs =
    resolvedFrom !== undefined && resolvedTo !== undefined
      ? crumbs.releaseCompare(ns, name, resolvedFrom, resolvedTo, schemaVersion)
      : [
          ...crumbs.environment(ns, schemaVersion),
          { label: "Releases", href: links.releases({ app, env, name, schemaVersion }) },
          { label: "Compare" },
        ];
  const titleText =
    resolvedFrom !== undefined && resolvedTo !== undefined
      ? `Compare ${name} v${resolvedFrom} → v${resolvedTo}`
      : `Compare ${name}`;
  const canRollback =
    diff !== null && diff.to.current && !diff.cross_environment && diff.to.previous_version > 0;
  const rolledBack = diff !== null && diff.to.current && diff.to.version < diff.from.version;
  const stepIndex =
    resolvedTo === undefined
      ? -1
      : trackVersions.findIndex((summary) => summary.release.version === resolvedTo);

  return (
    <>
      <PageHeader
        breadcrumbs={breadcrumbs}
        documentTitle={titleText}
        title={
          <span className="inline-flex flex-wrap items-center gap-2">
            <ReleaseIdent
              name={name}
              version={resolvedFrom ?? 0}
              href={
                resolvedFrom !== undefined
                  ? links.releases({
                      app,
                      env,
                      name,
                      schemaVersion,
                      release: `${name}@${schemaVersion}:${resolvedFrom}`,
                    })
                  : undefined
              }
            />
            <span className="release-diff-arrow" aria-hidden>
              →
            </span>
            <ReleaseIdent
              name={name}
              version={resolvedTo ?? 0}
              href={
                resolvedTo !== undefined
                  ? links.releases({
                      app: diff?.to.namespace.app ?? app,
                      env: diff?.to.namespace.env ?? toEnv ?? env,
                      name,
                      schemaVersion: diff?.to.schema_version ?? toSchemaVersion ?? schemaVersion,
                      release: `${name}@${diff?.to.schema_version ?? toSchemaVersion ?? schemaVersion}:${resolvedTo}`,
                    })
                  : undefined
              }
            />
            {diff && !diff.schema_changed ? (
              <span className="faint text-sm mono">schema v{diff.to.schema_version}</span>
            ) : null}
          </span>
        }
        subtitle={
          diff ? (
            <span className="flex flex-wrap items-center gap-2">
              <span className="inline-flex flex-wrap items-center gap-2">
                <Ident kind="env" value={diff.from.namespace.env} tooltip={false} />
                <SideBadge side={diff.from} />
                <span title={formatUnixMs(diff.from.created_at_unix_ms)}>
                  v{diff.from.version} created {formatRelative(diff.from.created_at_unix_ms, now)}
                  {diff.from.created_by ? ` by ${diff.from.created_by}` : ""}
                </span>
              </span>
              <span className="faint" aria-hidden>
                ·
              </span>
              <span className="inline-flex flex-wrap items-center gap-2">
                {diff.cross_environment ? (
                  <Ident kind="env" value={diff.to.namespace.env} tooltip={false} />
                ) : null}
                <SideBadge side={diff.to} />
                <span title={formatUnixMs(diff.to.created_at_unix_ms)}>
                  v{diff.to.version} created {formatRelative(diff.to.created_at_unix_ms, now)}
                  {diff.to.created_by ? ` by ${diff.to.created_by}` : ""}
                </span>
              </span>
            </span>
          ) : (
            <span>
              <Ident kind="env" value={env} tooltip={false} />
              {toEnv ? (
                <>
                  {" "}
                  against <Ident kind="env" value={toEnv} tooltip={false} />
                </>
              ) : null}
            </span>
          )
        }
        actions={
          <>
            <Button
              variant="outline"
              size="sm"
              onClick={swap}
              data-testid="release-diff-swap"
              aria-label="Swap from and to"
              title="Swap from and to"
            >
              <ArrowLeftRight size={16} aria-hidden />
              Swap
            </Button>
            {!toEnv ? (
              <>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label="Compare with the older version"
                  title="Older version"
                  disabled={stepIndex === -1 || stepIndex >= trackVersions.length - 1}
                  onClick={() => step(-1)}
                >
                  <ChevronLeft size={16} aria-hidden />
                </Button>
                {/* Not a Field: its w-full utility stretched each select across the
                    whole page and stacked the action row three lines tall. */}
                <span className="release-diff-pick">
                  <label htmlFor={`${pickId}-from`}>From</label>
                  <AppSelect
                    id={`${pickId}-from`}
                    value={resolvedFrom === undefined ? "" : String(resolvedFrom)}
                    onValueChange={(value) => pick("from", value)}
                    options={options}
                    disabled={options.length === 0}
                    placeholder={String(from)}
                  />
                </span>
                <span className="release-diff-pick">
                  <label htmlFor={`${pickId}-to`}>To</label>
                  <AppSelect
                    id={`${pickId}-to`}
                    value={resolvedTo === undefined ? "" : String(resolvedTo)}
                    onValueChange={(value) => pick("to", value)}
                    options={options}
                    disabled={options.length === 0}
                    placeholder={String(to)}
                  />
                </span>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label="Compare with the newer version"
                  title="Newer version"
                  disabled={stepIndex <= 0}
                  onClick={() => step(1)}
                >
                  <ChevronRight size={16} aria-hidden />
                </Button>
              </>
            ) : null}
            {canRollback && diff ? (
              <Button
                variant={rolledBack ? "default" : "destructive"}
                size="sm"
                onClick={() => setRollbackOpen(true)}
              >
                {rolledBack
                  ? `Re-activate v${diff.to.previous_version}`
                  : `Roll back to v${diff.to.previous_version}`}
              </Button>
            ) : null}
            {back}
          </>
        }
      />

      <ReleaseDiffView
        query={diffQuery}
        view={view}
        q={q}
        onViewChange={onViewChange}
        onQueryChange={onQueryChange}
        rollout={rolloutWanted ? (rollout.key === rolloutKey ? rollout.value : null) : undefined}
        resolveHref={resolveHref}
        onRollback={canRollback ? () => setRollbackOpen(true) : undefined}
        onLoaded={onLoaded}
      />

      {diff && canRollback ? (
        <RollbackDialog
          namespace={ns}
          name={name}
          active={activeFromSide(diff.to)}
          open={rollbackOpen}
          onClose={() => setRollbackOpen(false)}
          onDone={(result) => {
            setRollbackOpen(false);
            if (result.changed) {
              toast.success(`Rolled back ${name} to version ${result.release.version}`);
              // The comparison that explains what just happened: the release
              // that was active against the one that is active now.
              void replaceQuery({
                from: String(diff.to.version),
                to: String(result.release.version),
              });
            } else {
              toast.info(`${name}@${result.release.version} was already active`);
            }
          }}
        />
      ) : null}
    </>
  );
}
