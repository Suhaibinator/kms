import { Plus, Send } from "lucide-react";
import Link from "next/link";
import { Ident } from "@/components/Ident";
import { Badge } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { links } from "@/lib/links";
import type { EnvironmentOverview, OverviewValue } from "@/lib/types";

/** True when a present value is newer than (or absent from) the active release's pins. */
export function isUnreleased(value: OverviewValue, hasActiveRelease: boolean): boolean {
  if (!value.present || !hasActiveRelease) return false;
  if (value.pinned_version === undefined) return true;
  return (value.current_version ?? 0) > value.pinned_version;
}

export function ValuesSection({
  environment,
  otherKeys,
  onAddValue,
  onAddSecret,
  onShip,
}: {
  environment: EnvironmentOverview;
  /** Parameters in this namespace that no contract alias resolves to. */
  otherKeys: number;
  onAddValue: (env: string, alias: string) => void;
  onAddSecret: (env: string, alias: string) => void;
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
          {environment.values.map((value) => {
            const unreleased = isUnreleased(value, hasActive);
            return (
              <li className="pipeline-row" key={value.alias} data-alias={value.alias}>
                <Ident kind="alias" value={value.alias} tooltip={false} />
                {value.present ? (
                  <Ident
                    kind="version"
                    value={String(value.current_version ?? 0)}
                    tooltip={false}
                  />
                ) : value.kind === "secret" ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => onAddSecret(env, value.alias)}
                  >
                    <Plus size={13} />
                    Add secret
                  </Button>
                ) : (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => onAddValue(env, value.alias)}
                  >
                    <Plus size={13} />
                    Add value
                  </Button>
                )}
                {unreleased ? (
                  <Tooltip>
                    <TooltipTrigger render={<span className="pipeline-drift" />}>
                      <Badge kind="warning">v{value.current_version ?? 0} unreleased</Badge>
                    </TooltipTrigger>
                    <TooltipContent>
                      {value.pinned_version === undefined
                        ? "Not in the active release; clients do not receive it."
                        : `The active release pins v${value.pinned_version}.`}
                    </TooltipContent>
                  </Tooltip>
                ) : null}
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
                ) : null}
              </li>
            );
          })}
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
