import { Send } from "lucide-react";
import Link from "next/link";
import { Ident } from "@/components/Ident";
import { Button } from "@/components/ui/button";
import { links } from "@/lib/links";
import type { EnvironmentOverview } from "@/lib/types";
import { AddResourceButton } from "./AddResourceButton";
import { ResourceLink } from "./ResourceLink";
import { UnreleasedBadge } from "./ValueBadges";

export { isUnreleased } from "./ValueBadges";

export function ValuesSection({
  environment,
  otherKeys,
  onAddValue,
  onAddSecret,
  onOpenSecret,
  onShip,
}: {
  environment: EnvironmentOverview;
  /** Parameters in this namespace that no contract alias resolves to. */
  otherKeys: number;
  onAddValue: (env: string, alias: string) => void;
  onAddSecret: (env: string, alias: string) => void;
  onOpenSecret?: (env: string, key: string) => void;
  onShip: (env: string, alias?: string) => void;
}) {
  const env = environment.namespace.env;
  const hasActive = Boolean(environment.release.active);
  return (
    <section className="pipeline-section" aria-label={`Values in ${env}`}>
      <h3 className="pipeline-section-title">Values</h3>
      {environment.values.length === 0 ? (
        <div className="faint text-sm">The contract has no aliases.</div>
      ) : (
        <ul className="pipeline-rows">
          {environment.values.map((value) => (
            <li className="pipeline-row" key={value.alias} data-alias={value.alias}>
              <Ident kind="alias" value={value.alias} tooltip={false} />
              {value.present ? (
                <Ident kind="version" value={String(value.current_version ?? 0)} tooltip={false} />
              ) : (
                <AddResourceButton
                  kind={value.kind}
                  onClick={() =>
                    value.kind === "secret"
                      ? onAddSecret(env, value.alias)
                      : onAddValue(env, value.alias)
                  }
                />
              )}
              <UnreleasedBadge value={value} hasActiveRelease={hasActive} />
              {value.kind === "parameter" && value.present ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="pipeline-row-action"
                  aria-label={`Edit & ship ${value.alias} in ${env}`}
                  onClick={() => onShip(env, value.alias)}
                >
                  <Send size={13} />
                  Edit &amp; ship
                </Button>
              ) : value.kind === "secret" && value.present ? (
                <ResourceLink
                  kind="secret"
                  button
                  className="pipeline-row-action"
                  env={env}
                  app={environment.namespace.app}
                  keyName={value.key ?? value.alias}
                  onOpen={onOpenSecret}
                >
                  Manage
                </ResourceLink>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {otherKeys > 0 ? (
        <Link
          className="pipeline-other-keys text-sm"
          href={links.parameters({ env, app: environment.namespace.app })}
        >
          {otherKeys} other {otherKeys === 1 ? "key" : "keys"} → Parameters
        </Link>
      ) : null}
    </section>
  );
}
