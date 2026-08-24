import { RotateCcw, Send } from "lucide-react";
import { Ident, ReleaseIdent } from "@/components/Ident";
import { Button } from "@/components/ui/button";
import { links } from "@/lib/links";
import type { EnvironmentOverview } from "@/lib/types";
import { isUnreleased } from "./ValuesSection";

/** Why "Create first release" is disabled, or null when it may run. */
export function firstReleaseBlocker(environment: EnvironmentOverview): string | null {
  if (environment.release_state === "blocked") return "Resolve the blocking findings first.";
  if (environment.values_state === "empty") return "Add a value for every alias first.";
  if (environment.values_state === "incomplete") {
    const missing = environment.values.filter((value) => !value.present).map((v) => v.alias);
    return missing.length > 0
      ? `Add values for ${missing.map((alias) => `\`${alias}\``).join(", ")} first.`
      : "Some values do not match the contract yet.";
  }
  return null;
}

export function ReleaseSection({
  environment,
  onShip,
  onRollback,
}: {
  environment: EnvironmentOverview;
  onShip: (env: string) => void;
  onRollback: (env: string) => void;
}) {
  const ns = environment.namespace;
  const active = environment.release.active;
  const unreleased = environment.values.filter((value) => isUnreleased(value, Boolean(active)));
  const missing = environment.values.filter((value) => !value.present);
  const blocker = active ? null : firstReleaseBlocker(environment);

  let cta: React.ReactNode;
  if (!active) {
    cta = (
      <>
        <Button
          type="button"
          size="sm"
          disabled={blocker !== null}
          onClick={() => onShip(ns.env)}
          aria-describedby={blocker ? `first-release-${ns.env}` : undefined}
        >
          <Send size={13} />
          Create first release
        </Button>
        {blocker ? (
          <span id={`first-release-${ns.env}`} className="pipeline-cta-reason faint text-sm">
            {blocker}
          </span>
        ) : null}
      </>
    );
  } else if (unreleased.length > 0) {
    cta = (
      <Button type="button" size="sm" onClick={() => onShip(ns.env)}>
        <Send size={13} />
        {unreleased.length} unreleased {unreleased.length === 1 ? "change" : "changes"} → Ship
      </Button>
    );
  } else if (missing.length > 0) {
    cta = (
      <span className="pipeline-cta-reason faint text-sm">
        Missing values for {missing.map((value) => `\`${value.alias}\``).join(", ")}.
      </span>
    );
  } else {
    cta = <span className="pipeline-cta-ok text-sm text-success">Up to date</span>;
  }

  return (
    <section className="pipeline-section" aria-label={`Release in ${ns.env}`}>
      <h3 className="pipeline-section-title">Release</h3>
      {active ? (
        <>
          <div className="pipeline-row">
            <ReleaseIdent
              name={active.name}
              version={active.version}
              href={links.releases({
                app: ns.app,
                env: ns.env,
                name: active.name,
                release: `${active.name}@${active.version}`,
              })}
            />
            <Ident kind="revision" value={String(active.activation_revision)} />
            {active.previous_version > 0 ? (
              <span className="pipeline-previous faint text-sm">
                previous{" "}
                <ReleaseIdent
                  name={active.name}
                  version={active.previous_version}
                  tooltip={false}
                  href={links.releases({
                    app: ns.app,
                    env: ns.env,
                    name: active.name,
                    release: `${active.name}@${active.previous_version}`,
                  })}
                />
              </span>
            ) : null}
          </div>
          {active.is_rolled_back ? (
            <div className="warn-panel text-sm">
              Rolled back. {active.name}@{active.previous_version} is newer and still available to
              re-activate.
            </div>
          ) : null}
          {active.previous_version > 0 ? (
            <div className="pipeline-row">
              <Button type="button" variant="outline" size="sm" onClick={() => onRollback(ns.env)}>
                <RotateCcw size={13} />
                {active.is_rolled_back ? `Re-activate v${active.previous_version}` : "Roll back"}
              </Button>
            </div>
          ) : null}
        </>
      ) : (
        <div className="faint text-sm">No release is active.</div>
      )}
      <div className="pipeline-cta">{cta}</div>
    </section>
  );
}
