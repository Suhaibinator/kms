import { Send, SlidersHorizontal } from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { SearchField } from "@/components/SearchField";
import { BindingKeyBadge } from "@/components/secrets/SecretBadges";
import { MobileListToolbar, SortHeaderRow, useSort } from "@/components/SortableTable";
import { TableSummary } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { countNoun } from "@/lib/format";
import { matchesSearch } from "@/lib/key-search";
import { links } from "@/lib/links";
import type { SortColumn } from "@/lib/sort";
import type { EnvironmentOverview, OverviewValue } from "@/lib/types";
import { AddResourceButton } from "./AddResourceButton";
import { ResourceLink } from "./ResourceLink";
import { UnreleasedBadge, isUnreleased } from "./ValueBadges";
import { pinTooltip } from "./ValuesSection";

/** One word for the row's condition, so the State column can be ordered. */
function stateLabel(value: OverviewValue, hasActiveRelease: boolean): string {
  if (!value.present) return "missing";
  if (isUnreleased(value, hasActiveRelease)) return "unreleased";
  if (value.bound) return "bound";
  return "released";
}

// Module scope so the sort controller's memos stay stable across renders.
const COLUMNS: ReadonlyArray<SortColumn<OverviewValue>> = [
  { id: "alias", label: "Alias", value: (value) => value.alias },
  { id: "kind", label: "Kind", value: (value) => value.kind },
  { id: "key", label: "Key", value: (value) => value.key ?? "" },
  { id: "current", label: "Current", value: (value) => value.current_version },
  { id: "pinned", label: "Pinned", value: (value) => value.pinned_version },
  { id: "state", label: "State" },
];

/**
 * The environment's contract values as a real list table: sortable through
 * `?sort=&dir=`, filterable by alias or key, and carrying the same actions the
 * pipeline column offers in its cramped column layout.
 */
export function EnvironmentValuesTable({
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
  /** An empty contract is fixed at the application, not here. */
  onEditContract: () => void;
}) {
  const ns = environment.namespace;
  const env = ns.env;
  const hasActive = Boolean(environment.release.active);
  const sort = useSort<OverviewValue>("/applications/environment", COLUMNS);
  const [filter, setFilter] = useState("");
  const shown = useMemo(
    () =>
      environment.values.filter((value) =>
        matchesSearch({ key: value.key ?? value.alias, text: value.alias }, filter),
      ),
    [environment.values, filter],
  );
  const trimmed = filter.trim();

  return (
    <section className="environment-values" aria-label={`Values in ${env}`}>
      <div className="between mb-2 items-end">
        <h2 className="section-title">Values</h2>
        <SearchField
          className="w-full max-w-[280px]"
          label="Filter values"
          placeholder="Filter by alias or key"
          value={filter}
          onChange={setFilter}
          onClear={() => setFilter("")}
        />
      </div>
      {environment.values.length === 0 ? (
        <div className="pipeline-row">
          <span className="faint text-sm">The contract has no aliases.</span>
          <Button type="button" variant="outline" size="sm" onClick={onEditContract}>
            <SlidersHorizontal size={13} />
            Manage releases
          </Button>
        </div>
      ) : (
        <div className="table-wrap card-table">
          <MobileListToolbar controller={sort} />
          <table className="data">
            <TableSummary
              shown={shown.length}
              total={environment.values.length}
              filters={trimmed ? 1 : 0}
              noun="values"
            />
            <thead>
              <SortHeaderRow
                controller={sort}
                after={
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                }
              />
            </thead>
            <tbody>
              {shown.length === 0 ? (
                <tr>
                  <td colSpan={COLUMNS.length + 1} className="faint">
                    {`No values match “${trimmed}”.`}
                  </td>
                </tr>
              ) : (
                sort.apply(shown).map((value) => {
                  const key = value.key ?? value.alias;
                  return (
                    <tr key={value.alias} data-alias={value.alias}>
                      <td data-label="Alias">
                        <Ident kind="alias" value={value.alias} tooltip={false} />
                      </td>
                      <td data-label="Kind">
                        <span className="row-wrap">
                          {value.kind === "secret" ? (
                            <span className="kind-glyph" role="img" aria-label="Secret">
                              <Icon.secret size={13} />
                            </span>
                          ) : null}
                          {value.kind}
                        </span>
                      </td>
                      <td data-label="Key">
                        {/* The resolved key is only news when it differs from
                            the alias it was resolved for. */}
                        {value.present && value.key && value.key !== value.alias ? (
                          <Ident kind="key" value={value.key} tooltip={false} />
                        ) : (
                          <span className="faint">—</span>
                        )}
                      </td>
                      <td data-label="Current">
                        {value.present ? (
                          <Ident
                            kind="version"
                            value={String(value.current_version ?? 0)}
                            tooltip={false}
                          />
                        ) : (
                          <span className="faint">—</span>
                        )}
                      </td>
                      <td data-label="Pinned">
                        {value.pinned_version === undefined ? (
                          <span className="faint" title={pinTooltip(value, hasActive)}>
                            —
                          </span>
                        ) : (
                          <Ident
                            kind="version"
                            value={String(value.pinned_version)}
                            tooltip={pinTooltip(value, hasActive)}
                          />
                        )}
                      </td>
                      <td data-label="State">
                        <span className="row-wrap">
                          {value.present ? null : <span className="faint">missing</span>}
                          {value.kind === "secret" && value.present && value.bound ? (
                            <BindingKeyBadge version={value.current_version ?? 0} />
                          ) : null}
                          <UnreleasedBadge value={value} hasActiveRelease={hasActive} />
                          {stateLabel(value, hasActive) === "released" ? (
                            <span className="faint">released</span>
                          ) : null}
                        </span>
                      </td>
                      <td data-label="Actions">
                        <div className="row-actions">
                          {value.present ? (
                            <>
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
                            </>
                          ) : (
                            <AddResourceButton
                              kind={value.kind}
                              aria-label={`${value.kind === "secret" ? "Add secret" : "Add value"} for ${value.alias} in ${env}`}
                              onClick={() =>
                                value.kind === "secret"
                                  ? onAddSecret(env, value.alias)
                                  : onAddValue(env, value.alias)
                              }
                            />
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
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
