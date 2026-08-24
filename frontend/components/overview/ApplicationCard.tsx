import Link from "next/link";
import { Ident, ReleaseIdent } from "@/components/Ident";
import { StatusChip } from "@/components/StatusChip";
import { formatRelative, formatUnixMs } from "@/lib/format";
import { links } from "@/lib/links";
import type { ApplicationOverview, FleetApplication } from "@/lib/types";

export interface ApplicationCardProps {
  fleet: FleetApplication;
  /** The per-app overview when it was fetched (first 25 apps); null on failure, undefined when skipped. */
  overview?: ApplicationOverview | null;
}

/** One fleet card: app chip, status, a status dot per environment, the active
 *  release per environment, rejected instances, and the last activation. */
export default function ApplicationCard({ fleet, overview }: ApplicationCardProps) {
  const name = fleet.application.name;
  const envOverviews = new Map(
    (overview?.environments ?? []).map((env) => [env.namespace.env, env] as const),
  );
  const rejected = overview
    ? overview.environments.reduce((sum, env) => sum + env.rollout.rejected, 0)
    : null;
  const lastActivation = overview
    ? overview.environments.reduce(
        (latest, env) => Math.max(latest, env.release.active?.created_at_unix_ms ?? 0),
        0,
      )
    : 0;

  return (
    <article className={`fleet-card fleet-card-${fleet.status}`} data-app={name}>
      <header className="fleet-card-head">
        <Ident kind="app" value={name} href={links.application(name)} tooltip={false} />
        <StatusChip status={fleet.status} />
      </header>
      {fleet.application.description ? (
        <p className="fleet-card-desc">{fleet.application.description}</p>
      ) : null}

      {fleet.environments.length === 0 ? (
        <p className="fleet-card-empty">No environments yet.</p>
      ) : (
        <ul className="fleet-envs">
          {fleet.environments.map((env) => {
            const detail = envOverviews.get(env.env);
            const active = detail?.release.active;
            return (
              <li key={env.env} className={`fleet-env ${env.production ? "fleet-env-prod" : ""}`}>
                <Link
                  href={links.application(name, { env: env.env })}
                  className="fleet-env-link"
                  aria-label={`${env.env}: ${env.status}${env.production ? " (production)" : ""}`}
                >
                  <StatusChip status={env.status} production={env.production} size="dot" />
                  <span className="fleet-env-name">{env.env}</span>
                </Link>
                <span className="fleet-env-release">
                  {active ? (
                    <ReleaseIdent name={active.name} version={active.version} tooltip={false} />
                  ) : overview === undefined ? (
                    <span className="faint">—</span>
                  ) : (
                    <span className="faint">no release</span>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}

      <footer className="fleet-card-foot">
        <span
          className={`fleet-card-rejected ${rejected ? "fleet-card-rejected-some" : ""}`}
          title="Instances that rejected the active release"
        >
          {rejected === null ? "—" : `${rejected} rejected`}
        </span>
        <span
          className="fleet-card-activated"
          title={lastActivation ? formatUnixMs(lastActivation) : undefined}
        >
          {lastActivation ? `activated ${formatRelative(lastActivation)}` : "never activated"}
        </span>
      </footer>
    </article>
  );
}
