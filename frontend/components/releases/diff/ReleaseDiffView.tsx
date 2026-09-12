import Link from "next/link";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Ident } from "@/components/Ident";
import { SearchField } from "@/components/SearchField";
import { Badge, Checkbox, Skeleton } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  ApiError,
  api,
  isUnreachableError,
  type ReleaseDiffQuery,
  type ResourceRef,
} from "@/lib/api";
import { links } from "@/lib/links";
import {
  buildRows,
  type DiffKindFilter,
  type DiffRowModel,
  type DiffView,
  filterRows,
  type GroupMode,
  groupRows,
  releaseDiffAsText,
  sideKey,
} from "@/lib/release-diff";
import type {
  OverviewRollout,
  ReleaseDiffPin,
  ReleaseDiffResponse,
  ReleaseEntryKind,
} from "@/lib/types";
import { useNow } from "@/lib/useNow";
import { cn } from "@/lib/utils";
import { ReleaseDiffRow } from "./ReleaseDiffRow";
import { ReleaseDiffStrip } from "./ReleaseDiffStrip";
import { useReleaseDiff } from "./useReleaseDiff";
import { useValueViewMode } from "./ValueChange";

/** The server's error message as a sentence, so prose can follow it. */
function sentence(text: string): string {
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

export interface ReleaseDiffViewProps {
  /** What to fetch; the view owns the request (useLatestRequest). */
  query: ReleaseDiffQuery;
  /** Modal/dialog embedding: no rollout cell, tighter groups, shorter value panes. */
  compact?: boolean;
  /** Controlled filter state (the page passes URL state). */
  view?: "changed" | "all";
  q?: string;
  onViewChange?(view: "changed" | "all"): void;
  onQueryChange?(q: string): void;
  /** Page-supplied; the strip cell is hidden when undefined. */
  rollout?: OverviewRollout | null;
  /** From entryHrefResolver. */
  resolveHref?: (alias: string) => string | null;
  /** Modal contexts (dirty guard): open the resource in place instead of navigating. */
  onOpenResource?: (ref: ResourceRef, kind: ReleaseEntryKind) => void;
  onRollback?: () => void;
  /** The page uses it to rewrite label URLs to numbers and set the title. */
  onLoaded?(diff: ReleaseDiffResponse): void;
}

const valueKey = (pin: ReleaseDiffPin) =>
  `${pin.ref.namespace.env}/${pin.ref.namespace.app}/${pin.ref.key}@${pin.version}`;

/** The response with lazily loaded values written into their pins. */
function withValues(
  diff: ReleaseDiffResponse,
  values: ReadonlyMap<string, string>,
): ReleaseDiffResponse {
  if (values.size === 0) return diff;
  const patch = (pin: ReleaseDiffPin | undefined): ReleaseDiffPin | undefined => {
    if (!pin || pin.value_state === "present" || pin.value_state === "secret") return pin;
    const value = values.get(valueKey(pin));
    return value === undefined ? pin : { ...pin, value_state: "present", value };
  };
  return {
    ...diff,
    rows: diff.rows.map((row) => ({ ...row, from: patch(row.from), to: patch(row.to) })),
  };
}

function Skeletons() {
  return (
    <div className="release-diff" aria-busy="true" data-testid="release-diff">
      <div className="stat-strip release-diff-strip">
        {["Changed", "Added", "Removed", "Secrets repinned", "Schema", "Rollout"].map((label) => (
          <div key={label} className="stat">
            <div className="stat-label">{label}</div>
            <div className="stat-value flex items-center" style={{ height: "1.25em" }}>
              <Skeleton width="40%" height={22} />
            </div>
          </div>
        ))}
      </div>
      <div className="release-diff-skeleton-rows">
        {[0, 1, 2, 3, 4].map((index) => (
          <div key={index} className="release-diff-skeleton-row" />
        ))}
      </div>
    </div>
  );
}

/**
 * The comparison of two releases: banners, the summary strip, a toolbar,
 * and rows grouped with "Needs attention" first. Owns its request; the page
 * owns the URL, the pickers and the header actions.
 */
export function ReleaseDiffView({
  query,
  compact = false,
  view: controlledView,
  q: controlledQ,
  onViewChange,
  onQueryChange,
  rollout,
  resolveHref,
  onOpenResource,
  onRollback,
  onLoaded,
}: ReleaseDiffViewProps) {
  const sameVersion =
    typeof query.from === "number" &&
    typeof query.to === "number" &&
    query.from === query.to &&
    !query.toEnv;
  const { diff, loading, error, reload } = useReleaseDiff(sameVersion ? null : query);
  const now = useNow();
  const ids = useId();
  const [mode, setMode] = useValueViewMode();
  const [internalView, setInternalView] = useState<DiffView>("changed");
  const [internalQ, setInternalQ] = useState("");
  const view = controlledView ?? internalView;
  const q = controlledQ ?? internalQ;
  const setView = (next: DiffView) => {
    if (onViewChange) onViewChange(next);
    else setInternalView(next);
  };
  const setQ = (next: string) => {
    if (onQueryChange) onQueryChange(next);
    else setInternalQ(next);
  };
  const [kind, setKind] = useState<DiffKindFilter>("all");
  const [groupMode, setGroupMode] = useState<GroupMode>("kind");
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const [values, setValues] = useState<ReadonlyMap<string, string>>(() => new Map());
  const [loadingValues, setLoadingValues] = useState<ReadonlySet<string>>(() => new Set());
  const loadedFor = useRef<ReleaseDiffResponse | null>(null);

  useEffect(() => {
    if (diff && onLoaded && loadedFor.current !== diff) {
      loadedFor.current = diff;
      onLoaded(diff);
    }
  }, [diff, onLoaded]);

  const patched = useMemo(() => (diff ? withValues(diff, values) : null), [diff, values]);
  const models = useMemo(() => (patched ? buildRows(patched, { now }) : []), [patched, now]);
  const filtered = useMemo(() => filterRows(models, q, kind, view), [models, q, kind, view]);
  const groups = useMemo(() => groupRows(filtered, groupMode), [filtered, groupMode]);

  const toggle = useCallback((alias: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(alias)) next.delete(alias);
      else next.add(alias);
      return next;
    });
  }, []);

  const loadValues = useCallback(
    async (model: DiffRowModel) => {
      const pins = [model.row.from, model.row.to].filter(
        (pin): pin is ReleaseDiffPin =>
          Boolean(pin) && model.kind === "parameter" && pin?.value_state !== "present",
      );
      // Both pins of an unchanged row name the same version: fetch it once.
      const wanted = [
        ...new Map(
          pins.filter((pin) => !values.has(valueKey(pin))).map((pin) => [valueKey(pin), pin]),
        ).values(),
      ];
      if (wanted.length === 0) return;
      setLoadingValues((current) => new Set([...current, model.alias]));
      try {
        const loaded = await Promise.all(
          wanted.map((pin) =>
            api
              .getParameter(
                { env: pin.ref.namespace.env, app: pin.ref.namespace.app, key: pin.ref.key },
                pin.version,
              )
              .then((response) => [valueKey(pin), response.parameter.value] as const),
          ),
        );
        setValues((current) => {
          const next = new Map(current);
          for (const [key, value] of loaded) next.set(key, value);
          return next;
        });
        setExpanded((current) => new Set(current).add(model.alias));
      } catch {
        // The row keeps its "Load value" button; a toast would be noise on a
        // page whose other rows are fine.
      } finally {
        setLoadingValues((current) => {
          const next = new Set(current);
          next.delete(model.alias);
          return next;
        });
      }
    },
    [values],
  );

  if (sameVersion) {
    return (
      <div className="release-diff" data-testid="release-diff">
        <div className="info-panel" role="status">
          Pick two different versions.
        </div>
      </div>
    );
  }
  if (loading || (!diff && !error)) return <Skeletons />;
  if (error || !patched) {
    return (
      <div className="release-diff" data-testid="release-diff">
        <ErrorPanel error={error} query={query} onRetry={reload} />
      </div>
    );
  }

  const beforeLabel = sideKey(patched.from);
  const afterLabel = sideKey(patched.to);
  const rolledBack =
    patched.to.current && !patched.cross_environment && patched.to.version < patched.from.version;
  const unchangedCount = patched.counts.unchanged;

  return (
    <div
      className={cn("release-diff", compact && "release-diff-compact")}
      data-testid="release-diff"
      data-identical={patched.identical ? "true" : undefined}
    >
      <div className="release-diff-banners">
        {rolledBack ? (
          <div className="warn-panel" role="status">
            <Badge kind="warning">Rolled back</Badge> v{patched.from.version} → v
            {patched.to.version} is active.
            {onRollback ? (
              <>
                {" "}
                <Button variant="outline" size="xs" onClick={onRollback}>
                  Re-activate v{patched.from.version}
                </Button>
              </>
            ) : null}
          </div>
        ) : null}
        {patched.cross_environment ? (
          <div className="info-panel">
            Comparing <Ident kind="env" value={patched.from.namespace.env} tooltip={false} />{" "}
            against <Ident kind="env" value={patched.to.namespace.env} tooltip={false} />. Entries
            are matched by alias; keys, versions and secrets are per environment, so version numbers
            are expected to differ — look at values.
          </div>
        ) : null}
        {patched.schema_changed ? (
          <div className="warn-panel">
            Different schema tracks (v{patched.from.schema_version} → v{patched.to.schema_version}).
            Aliases are matched by name; an alias that exists only on one track appears as added or
            removed.
          </div>
        ) : null}
        {!patched.values_included ? (
          <div className="info-panel">Entries only; values were not requested.</div>
        ) : null}
      </div>

      <ReleaseDiffStrip diff={patched} rollout={compact ? undefined : rollout} />

      {patched.identical ? (
        <div className="info-panel" role="status" data-testid="release-diff-identical">
          <p>
            No differences. {beforeLabel} and {afterLabel} pin the same versions and values (digest{" "}
            <span className="mono">{patched.to.digest.slice(0, 12)}</span> on both).
          </p>
          {patched.from.digest === patched.to.digest &&
          patched.from.version !== patched.to.version ? (
            <p className="faint">
              Created separately by {patched.from.created_by || "—"} and{" "}
              {patched.to.created_by || "—"}.
            </p>
          ) : null}
        </div>
      ) : (
        <>
          <div className="release-diff-toolbar">
            <SearchField
              value={q}
              onChange={setQ}
              onClear={() => setQ("")}
              label="Filter"
              placeholder="Filter aliases, keys, paths, values"
              className="grow basis-80"
            />
            <Tabs value={kind} onValueChange={(value) => setKind(value as DiffKindFilter)}>
              <TabsList variant="line" aria-label="Kind">
                <TabsTrigger value="all">All</TabsTrigger>
                <TabsTrigger value="parameter">Parameters</TabsTrigger>
                <TabsTrigger value="secret">Secrets</TabsTrigger>
              </TabsList>
            </Tabs>
            <div className="release-diff-toggles">
              <span className="release-diff-toggle-row">
                <Checkbox
                  id={`${ids}-unchanged`}
                  checked={view === "all"}
                  onCheckedChange={(checked) => setView(checked === true ? "all" : "changed")}
                />
                <label htmlFor={`${ids}-unchanged`}>
                  Show unchanged
                  {unchangedCount > 0 ? <span className="faint"> ({unchangedCount})</span> : null}
                </label>
              </span>
              <span className="release-diff-toggle-row">
                <Checkbox
                  id={`${ids}-prefix`}
                  checked={groupMode === "prefix"}
                  onCheckedChange={(checked) => setGroupMode(checked === true ? "prefix" : "kind")}
                />
                <label htmlFor={`${ids}-prefix`}>Group by prefix</label>
              </span>
              <span className="release-diff-expand">
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => setExpanded(new Set(filtered.map((model) => model.alias)))}
                >
                  Expand all
                </Button>
                <Button variant="ghost" size="xs" onClick={() => setExpanded(new Set())}>
                  Collapse all
                </Button>
              </span>
            </div>
            <div className="release-diff-actions">
              {!compact ? (
                <CopyButton
                  label="Copy link"
                  value={() => (typeof window === "undefined" ? "" : window.location.href)}
                />
              ) : null}
              <CopyButton label="Copy as text" value={() => releaseDiffAsText(patched, { now })} />
            </div>
          </div>

          {groups.length === 0 ? (
            <div className="info-panel" role="status">
              {q || kind !== "all"
                ? `No ${kind === "all" ? "entries" : `${kind}s`} match${q ? ` “${q}”` : ""}.`
                : "Nothing to show."}
            </div>
          ) : (
            groups.map((group) => {
              const total =
                groupMode === "prefix"
                  ? models.filter((model) => model.prefix === group.title).length
                  : group.tone === "unchanged"
                    ? unchangedCount
                    : group.tone === "attention"
                      ? models.filter((model) => model.attention && model.change !== "unchanged")
                          .length
                      : group.tone === "secret"
                        ? models.filter(
                            (model) =>
                              model.kind === "secret" &&
                              model.change !== "unchanged" &&
                              !model.attention,
                          ).length
                        : models.filter((model) => model.change === group.tone && !model.attention)
                            .length;
              const filteredAway = total > group.rows.length;
              return (
                <section
                  key={group.id}
                  className={cn("release-diff-group", !compact && "card")}
                  data-group={group.id}
                  aria-label={group.title}
                >
                  <h3 className="release-diff-group-title">
                    <span className="release-diff-group-heading">
                      {group.title}
                      <span className="release-diff-group-count">
                        {filteredAway ? `${group.rows.length} of ${total}` : group.rows.length}
                      </span>
                    </span>
                  </h3>
                  {group.tone === "secret" ? (
                    <p className="release-diff-group-note">
                      Secrets are compared by pinned version and metadata. Values are never shown or
                      fetched.
                    </p>
                  ) : null}
                  <ul className="release-diff-rows">
                    {group.rows.map((model) => (
                      <ReleaseDiffRow
                        key={model.alias}
                        model={model}
                        valuesIncluded={patched.values_included}
                        q={q}
                        expanded={expanded.has(model.alias)}
                        onToggle={() => toggle(model.alias)}
                        beforeLabel={beforeLabel}
                        afterLabel={afterLabel}
                        mode={mode}
                        onModeChange={setMode}
                        now={now}
                        compact={compact}
                        crossEnvironment={patched.cross_environment}
                        href={resolveHref ? resolveHref(model.alias) : null}
                        onOpen={onOpenResource}
                        loadedValue={
                          model.change === "unchanged" && model.row.to
                            ? values.get(valueKey(model.row.to))
                            : undefined
                        }
                        loadingValue={loadingValues.has(model.alias)}
                        onLoadValue={
                          model.kind === "parameter" &&
                          [model.row.from, model.row.to].some(
                            (pin) =>
                              pin &&
                              pin.value_state !== "present" &&
                              pin.value_state !== "unavailable" &&
                              pin.value_state !== "secret",
                          )
                            ? () => loadValues(model)
                            : undefined
                        }
                      />
                    ))}
                  </ul>
                </section>
              );
            })
          )}
        </>
      )}
    </div>
  );
}

function ErrorPanel({
  error,
  query,
  onRetry,
}: {
  error: unknown;
  query: ReleaseDiffQuery;
  onRetry: () => void;
}) {
  if (error instanceof ApiError && error.code === "not_found") {
    const audit = `${links.audit()}?env=${encodeURIComponent(query.env)}&app=${encodeURIComponent(query.app)}&key_prefix=${encodeURIComponent(query.name)}&event_type=configuration_release.activate`;
    return (
      <div className="warn-panel" role="alert">
        <p>
          {sentence(error.message || "One side of the comparison is not retained any more.")}{" "}
          Release history keeps a bounded number of versions and days (see{" "}
          <span className="mono">watch.release_retain_versions</span>); the audit log still records
          every activation.
        </p>
        <p>
          <Link href={audit}>Open the audit log for {query.name}</Link>
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.code === "failed_precondition") {
    return (
      <div className="info-panel" role="status">
        <p>
          <Ident kind="release" value={query.name} tooltip={false} /> has no previous release in{" "}
          <Ident kind="env" value={query.env} tooltip={false} />; the active release is the first
          activation, so there is nothing to compare it with. Pick any two versions instead.
        </p>
      </div>
    );
  }
  const message = isUnreachableError(error)
    ? "Could not reach the server."
    : error instanceof Error
      ? error.message
      : "The comparison failed.";
  return (
    <div className="danger-panel" role="alert">
      <div className="between">
        <span>{message}</span>
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      </div>
    </div>
  );
}
