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
  /** A ticking clock (lib/useNow.ts) so "activated 2m ago" stays honest. */
  now?: number;
}

/** One fleet card: app chip, status, a status dot per environment, the active
 *  release per environment, rejected instances, and the last activation. */
export default function ApplicationCard({ fleet, overview, now }: ApplicationCardProps) {
  const name = fleet.application.name;
  const envOverviews = new Map(
    (overview?.environments ?? []).map((env) => [env.namespace.env, env] as const),
  );
  const rejected = overview
    ? overview.environments.reduce((sum, env) => sum + env.rollout.rejected, 0)
    : null;
  // The environment with the newest activation; its pair is the card's
  // "what changed" when it has a previous release to compare against.
  const latestEnv = overview
    ? overview.environments.reduce<(typeof overview.environments)[number] | null>(
        (latest, env) =>
          (env.release.active?.created_at_unix_ms ?? 0) >
          (latest?.release.active?.created_at_unix_ms ?? 0)
            ? env
            : latest,
        null,
      )
    : null;
  const latestActive = latestEnv?.release.active;
  const lastActivation = latestActive?.created_at_unix_ms ?? 0;
  const compareHref =
    latestEnv && latestActive && latestActive.previous_version > 0
      ? links.releaseCompare({
          app: name,
          env: latestEnv.namespace.env,
          name: latestActive.name,
          schemaVersion: latestActive.schema_version,
          from: latestActive.previous_version,
          to: latestActive.version,
        })
      : null;

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
                  href={links.environment(name, env.env)}
                  className="fleet-env-link"
                  aria-label={`${env.env}: ${env.status}${env.production ? " (production)" : ""}`}
                >
                  <StatusChip status={env.status} production={env.production} size="dot" />
                  <span className="fleet-env-name">{env.env}</span>
                  {env.production ? (
                    <span className="fleet-env-prod-pill" aria-hidden>
                      prod
                    </span>
                  ) : null}
                </Link>
                <span
                  className="fleet-env-release"
                  title={active ? `schema v${active.schema_version}` : undefined}
                >
                  {active ? (
                    // The schema version is a card's least useful 70px: it sits
                    // in the title here and in full on the environment page.
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
        {compareHref && latestEnv ? (
          <Link
            href={compareHref}
            className="fleet-card-activated"
            title={`${formatUnixMs(lastActivation)} in ${latestEnv.namespace.env} · what changed`}
          >
            activated {formatRelative(lastActivation, now)}
          </Link>
        ) : (
          <span
            className="fleet-card-activated"
            title={lastActivation ? formatUnixMs(lastActivation) : undefined}
          >
            {lastActivation
              ? `activated ${formatRelative(lastActivation, now)}`
              : "never activated"}
          </span>
        )}
      </footer>
    </article>
  );
}
