import { Send, SlidersHorizontal } from "lucide-react";
import Link from "next/link";
import { useMemo } from "react";
import CopyButton from "@/components/CopyButton";
import { Snippet } from "@/components/Highlight";
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
import { valueMatches } from "./valueFilter";

/** What the version chip's tooltip says about the active release's pin. */
export function pinTooltip(value: OverviewValue, hasActiveRelease: boolean): string {
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
  filter = "",
  values,
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
  /** The application page's value filter; matches alias, key or stored value. */
  filter?: string;
  /** `kind:key` → the value stored in this environment (lib/overview valuesByEnv). */
  values?: ReadonlyMap<string, string>;
}) {
  const ns = environment.namespace;
  const env = ns.env;
  const hasActive = Boolean(environment.release.active);
  const { shown, snippets } = useMemo(
    () => valueMatches(environment.values, values, filter),
    [environment.values, values, filter],
  );
  const filtering = filter.trim() !== "" && environment.values.length > 0;
  return (
    <section className="pipeline-section" aria-label={`Values in ${env}`}>
      <h3 className="pipeline-section-title">
        {filtering ? `Values · ${shown.length} of ${environment.values.length}` : "Values"}
      </h3>
      {environment.values.length === 0 ? (
        <div className="pipeline-row">
          <span className="faint text-sm">The contract has no aliases.</span>
          <Button type="button" variant="outline" size="sm" onClick={onEditContract}>
            <SlidersHorizontal size={13} />
            Manage releases
          </Button>
        </div>
      ) : shown.length === 0 ? (
        <div className="pipeline-row">
          <span className="faint text-sm">{`No values match “${filter.trim()}”.`}</span>
        </div>
      ) : (
        <ul className="pipeline-rows">
          {shown.map((value) => {
            const key = value.key ?? value.alias;
            const snippet = snippets.get(value.alias);
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
                    {/* Icon-only: the three actions together are 265.4px
                        against a 266px column content box with the label. */}
                    <CopyButton value={key} label="Copy key" size="icon-sm" />
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
                {/* Only the stored value matched: say so, or the row is on
                    screen with nothing on it the operator typed. */}
                {snippet ? (
                  <Snippet text={snippet.text} ranges={snippet.ranges} context={20} />
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
