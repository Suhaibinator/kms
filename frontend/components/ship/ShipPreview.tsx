import { RefreshCw } from "lucide-react";
import { FindingList } from "@/components/FindingList";
import { Ident, ReleaseIdent } from "@/components/Ident";
import { ViolationTable, type ViolationTableProps } from "@/components/releases/ViolationTable";
import { Badge, Button, Checkbox, Spinner } from "@/components/ui";
import type { FixAction } from "@/lib/readiness";
import type { Finding, ShipEntryChange, ShipPreview as ShipPreviewData } from "@/lib/types";
import { cn } from "@/lib/utils";
import type { DriftCandidate } from "./model";

export interface ShipPreviewProps {
  application: string;
  preview: ShipPreviewData | null;
  /** A dry run is in flight. */
  loading: boolean;
  /** Edits happened after the preview was taken; a new dry run is pending. */
  stale: boolean;
  /** The last dry run failed outright (network, preflight). */
  error: string | null;
  /** Whether a dry run can be requested at all (every row parses). */
  ready: boolean;
  drift: DriftCandidate[];
  optIns: string[];
  disabled: boolean;
  onToggleOptIn: (alias: string, include: boolean) => void;
  onRefresh: () => void;
  onFix: (action: FixAction, finding: Finding) => void;
  /** Links a violation's alias to its resource page. */
  resolveHref?: ViolationTableProps["resolveHref"];
  /** Jumps to the alias's editor row from a violation. */
  onEditAlias?: ViolationTableProps["onEdit"];
}

const CHANGE_TONE: Record<ShipEntryChange, "accent" | "warning" | "danger" | "neutral"> = {
  edited: "accent",
  pinned: "warning",
  included: "neutral",
  missing: "danger",
};

function versionArrow(from?: number, to?: number): string {
  if (from === undefined && to === undefined) return "—";
  if (from === undefined) return `→ v${to}`;
  if (to === undefined) return `v${from} → ?`;
  return from === to ? `v${to}` : `v${from} → v${to}`;
}

/** The Preview step: what a ship would write, release, pin and activate — from a dry run. */
export function ShipPreview({
  application,
  preview,
  loading,
  stale,
  error,
  ready,
  drift,
  optIns,
  disabled,
  onToggleOptIn,
  onRefresh,
  onFix,
  resolveHref,
  onEditAlias,
}: ShipPreviewProps) {
  const nextVersion = preview ? preview.base_version + 1 : null;
  const writes = preview?.entries.filter((entry) => entry.change === "edited") ?? [];
  const status = loading
    ? "Previewing…"
    : !ready
      ? "Fix the values above to preview."
      : stale
        ? "Edited since the last preview."
        : preview
          ? "Up to date."
          : "";

  return (
    <section
      className={cn("ship-preview", stale && "is-stale")}
      data-testid="ship-preview"
      data-stale={stale ? "true" : "false"}
      aria-label="Preview"
    >
      <div className="ship-preview-head">
        <h3 className="ship-section-title">Preview</h3>
        <div className="ship-preview-status">
          {loading ? <Spinner /> : null}
          <span className="faint text-sm" role="status">
            {status}
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled || loading || !ready}
            onClick={onRefresh}
          >
            <RefreshCw size={14} aria-hidden />
            Refresh preview
          </Button>
        </div>
      </div>

      {error ? (
        <div className="danger-panel" role="alert">
          {error}
        </div>
      ) : null}

      {preview ? (
        <div className="ship-preview-body">
          <div className="ship-preview-section">
            <h4 className="ship-subtitle">Writes</h4>
            {writes.length === 0 ? (
              <p className="faint text-sm">No parameter versions will be written.</p>
            ) : (
              <ul className="ship-writes">
                {writes.map((entry) => (
                  <li key={entry.alias} className="ship-write">
                    <Ident kind="alias" value={entry.alias} />
                    <span className="mono">
                      {versionArrow(entry.from_version, entry.to_version)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="ship-preview-section">
            <h4 className="ship-subtitle">
              Release{" "}
              {nextVersion !== null ? (
                <ReleaseIdent name={preview.release_name} version={nextVersion} />
              ) : null}
            </h4>
            <div className="table-wrap">
              <table className="data ship-entries">
                <thead>
                  <tr>
                    <th>Alias</th>
                    <th>Kind</th>
                    <th>Key</th>
                    <th>Version</th>
                    <th>Change</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.entries.map((entry) => {
                    const changed =
                      entry.change === "edited" ||
                      entry.change === "missing" ||
                      entry.from_version !== entry.to_version;
                    return (
                      <tr
                        key={entry.alias}
                        className={cn(changed && "ship-entry-changed")}
                        data-alias={entry.alias}
                        data-changed={changed ? "true" : "false"}
                      >
                        <td className="mono">{entry.alias}</td>
                        <td>{entry.kind}</td>
                        <td className="mono">{entry.key || "—"}</td>
                        <td className="mono">
                          {versionArrow(entry.from_version, entry.to_version)}
                        </td>
                        <td>
                          <Badge kind={CHANGE_TONE[entry.change]}>{entry.change}</Badge>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>

          {drift.length > 0 ? (
            <div className="ship-preview-section" data-testid="ship-drift">
              <h4 className="ship-subtitle">Unreleased changes not included</h4>
              <p className="faint text-sm">
                These resources moved past the active pins. Tick one to pin its current version in
                this release; untouched, clients keep serving the pinned version.
              </p>
              <ul className="ship-optins">
                {drift.map((candidate) => {
                  const id = `ship-optin-${candidate.alias}`;
                  const checked = optIns.includes(candidate.alias);
                  return (
                    <li key={candidate.alias}>
                      <label className="ship-optin" htmlFor={id}>
                        <Checkbox
                          id={id}
                          checked={checked}
                          disabled={disabled}
                          onCheckedChange={(next) => onToggleOptIn(candidate.alias, next === true)}
                        />
                        <span>
                          include <code>{candidate.alias}</code> v{candidate.current}
                          <span className="faint"> (pinned v{candidate.pinned})</span>
                        </span>
                      </label>
                    </li>
                  );
                })}
              </ul>
            </div>
          ) : null}

          <dl className="ship-facts">
            <dt>Schema</dt>
            <dd>
              {preview.schema_version ? (
                <Ident
                  kind="schema"
                  value={`${application}/${preview.release_name}@${preview.schema_version}`}
                />
              ) : (
                <span className="faint">no schema pinned</span>
              )}
            </dd>
            <dt>Activation</dt>
            <dd className="mono" data-testid="ship-activation">
              {preview.base_version > 0
                ? `${preview.release_name}@${preview.base_version} → @${nextVersion}`
                : "first activation"}
            </dd>
          </dl>

          <div className="ship-preview-section" data-testid="ship-validation">
            {preview.validation.valid ? (
              <div className="ship-valid" role="status">
                <Badge kind="success">valid</Badge>
                <span className="text-sm">
                  The candidate release validates against the pinned schema.
                </span>
              </div>
            ) : (
              <div className="danger-panel" role="alert">
                <strong>The candidate release is invalid; Ship stays disabled.</strong>
                {preview.validation.errors.length > 0 ? (
                  <ViolationTable
                    violations={preview.validation.errors}
                    resolveHref={resolveHref}
                    onEdit={onEditAlias}
                  />
                ) : (
                  <div className="text-sm mt-2">
                    The server reported no violations; refresh the preview.
                  </div>
                )}
              </div>
            )}
          </div>

          {preview.warnings.length > 0 ? (
            <FindingList findings={preview.warnings} onFix={onFix} className="ship-warnings" />
          ) : null}
        </div>
      ) : !error && !loading ? (
        <p className="faint text-sm">
          {ready
            ? "The preview runs automatically once you stop typing."
            : "Complete every value to see what would ship."}
        </p>
      ) : null}
    </section>
  );
}
