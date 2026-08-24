import { Ident, ReleaseIdent } from "@/components/Ident";
import { Button } from "@/components/ui";
import type { ShipResult } from "@/lib/types";

export interface ShipConflict {
  /** The 200 `conflict` result, when the server wrote before it noticed. */
  result?: ShipResult;
  /** The server's own words; shown when there is no structured version. */
  message: string;
  /** The version now active, when the server told us. */
  currentVersion?: number;
}

export interface ConflictPanelProps {
  environment: string;
  releaseName: string;
  /** What the preview was taken against. */
  baseVersion: number;
  conflict: ShipConflict;
  disabled: boolean;
  onRepreview: () => void;
  onDiscard: () => void;
}

/**
 * Someone else activated between the preview and the ship. Nothing was
 * overwritten: the CAS refused. Written versions (if any) are reusable, so the
 * only offers are a re-preview against the new base or walking away — never
 * "activate anyway".
 */
export function ConflictPanel({
  environment,
  releaseName,
  baseVersion,
  conflict,
  disabled,
  onRepreview,
  onDiscard,
}: ConflictPanelProps) {
  const written = conflict.result?.parameters ?? [];
  const release = conflict.result?.release;
  const current = conflict.currentVersion;
  return (
    <section className="conflict-panel danger-panel" role="alert" data-testid="ship-conflict">
      <div className="conflict-head">
        <strong>
          <Ident kind="env" value={environment} /> moved
          {current !== undefined ? (
            <>
              {" "}
              to <ReleaseIdent name={releaseName} version={current} />
            </>
          ) : null}{" "}
          while you were previewing against{" "}
          {baseVersion > 0 ? (
            <ReleaseIdent name={releaseName} version={baseVersion} />
          ) : (
            <span>no active release</span>
          )}
          .
        </strong>
        <p className="text-sm conflict-detail">{conflict.message}</p>
      </div>
      <dl className="conflict-facts">
        <dt>Your values</dt>
        <dd>
          {written.length === 0 ? (
            <span className="faint">nothing was written</span>
          ) : (
            <ul className="conflict-written">
              {written.map((entry) => (
                <li key={entry.alias}>
                  <Ident kind="alias" value={entry.alias} /> saved as{" "}
                  <span className="mono">v{entry.version}</span>
                </li>
              ))}
            </ul>
          )}
        </dd>
        <dt>Release</dt>
        <dd>
          {release ? (
            <>
              <ReleaseIdent name={release.name} version={release.version} /> created, not activated
            </>
          ) : (
            <span className="faint">not created</span>
          )}
        </dd>
      </dl>
      <p className="text-sm">
        Re-previewing rebuilds the candidate on the new base
        {written.length > 0 ? " and reuses the saved versions, so nothing is written twice" : ""}.
      </p>
      <div className="conflict-actions">
        <Button type="button" variant="outline" disabled={disabled} onClick={onDiscard}>
          Discard
        </Button>
        <Button type="button" disabled={disabled} onClick={onRepreview}>
          {current !== undefined ? `Re-preview against @${current}` : "Re-preview"}
        </Button>
      </div>
    </section>
  );
}
