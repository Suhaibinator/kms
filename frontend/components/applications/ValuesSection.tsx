import { Send, SlidersHorizontal } from "lucide-react";
import Link from "next/link";
import CopyButton from "@/components/CopyButton";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { BindingKeyBadge } from "@/components/secrets/SecretBadges";
import { Button } from "@/components/ui/button";
import { countNoun } from "@/lib/format";
import { links } from "@/lib/links";
import type { EnvironmentOverview, OverviewValue } from "@/lib/types";
import { AddResourceButton } from "./AddResourceButton";
import { ResourceLink } from "./ResourceLink";
import { UnreleasedBadge } from "./ValueBadges";

/** What the version chip's tooltip says about the active release's pin. */
function pinTooltip(value: OverviewValue, hasActiveRelease: boolean): string {
  if (!hasActiveRelease) return "No release is active in this environment.";
  return value.pinned_version === undefined
    ? "Not in the active release."
    : `Active release pins v${value.pinned_version}`;
}

export function ValuesSection({
  environment,
  otherKeys,
  onAddValue,
  onAddSecret,
  onOpenSecret,
  onOpenParameter,
  onShip,
  onEditContract,
}: {
  environment: EnvironmentOverview;
  /** Present resources in this namespace that no contract alias resolves to, per kind. */
  otherKeys: { parameters: number; secrets: number };
  onAddValue: (env: string, alias: string) => void;
  onAddSecret: (env: string, alias: string) => void;
  onOpenSecret?: (env: string, key: string) => void;
  onOpenParameter?: (env: string, key: string) => void;
  onShip: (env: string, alias?: string) => void;
  /** An empty contract is fixed at the application, not in this column. */
  onEditContract: () => void;
}) {
  const ns = environment.namespace;
  const env = ns.env;
  const hasActive = Boolean(environment.release.active);
  return (
    <section className="pipeline-section" aria-label={`Values in ${env}`}>
      <h3 className="pipeline-section-title">Values</h3>
      {environment.values.length === 0 ? (
        <div className="pipeline-row">
          <span className="faint text-sm">The contract has no aliases.</span>
          <Button type="button" variant="outline" size="sm" onClick={onEditContract}>
            <SlidersHorizontal size={13} />
            Edit contract
          </Button>
        </div>
      ) : (
        <ul className="pipeline-rows">
          {environment.values.map((value) => {
            const key = value.key ?? value.alias;
            return (
              <li className="pipeline-row" key={value.alias} data-alias={value.alias}>
                {value.kind === "secret" ? (
                  <span className="kind-glyph" role="img" aria-label="Secret">
                    <Icon.secret size={13} />
                  </span>
                ) : null}
                <Ident kind="alias" value={value.alias} tooltip={false} />
                {value.present && value.key && value.key !== value.alias ? (
                  <Ident kind="key" value={value.key} tooltip={false} />
                ) : null}
                {value.present ? (
                  <Ident
                    kind="version"
                    value={String(value.current_version ?? 0)}
                    tooltip={pinTooltip(value, hasActive)}
                  />
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
                {value.kind === "secret" && value.present && value.bound ? (
                  <BindingKeyBadge version={value.current_version ?? 0} />
                ) : null}
                <UnreleasedBadge value={value} hasActiveRelease={hasActive} />
                {value.present ? (
                  <span className="pipeline-row-actions">
                    <CopyButton value={key} label="Copy key" />
                    <ResourceLink
                      kind={value.kind}
                      button
                      env={env}
                      app={ns.app}
                      keyName={key}
                      onOpen={value.kind === "secret" ? onOpenSecret : onOpenParameter}
                      aria-label={`Manage ${value.alias} in ${env}`}
                    >
                      Manage
                    </ResourceLink>
                    {value.kind === "parameter" ? (
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        aria-label={`Edit & ship ${value.alias} in ${env}`}
                        onClick={() => onShip(env, value.alias)}
                      >
                        <Send size={13} />
                        Edit &amp; ship
                      </Button>
                    ) : null}
                  </span>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      {otherKeys.parameters > 0 ? (
        <Link className="pipeline-other-keys text-sm" href={links.parameters(ns)}>
          {otherKeys.parameters} other {countNoun(otherKeys.parameters, "keys")} → Parameters
        </Link>
      ) : null}
      {otherKeys.secrets > 0 ? (
        <Link className="pipeline-other-keys text-sm" href={links.secrets(ns)}>
          {otherKeys.secrets} other {countNoun(otherKeys.secrets, "secrets")} → Secrets
        </Link>
      ) : null}
    </section>
  );
}
