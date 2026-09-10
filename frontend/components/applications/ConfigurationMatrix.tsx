import { SlidersHorizontal } from "lucide-react";
import { useId, useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { EmptyValue } from "@/components/EmptyValue";
import { Snippet } from "@/components/Highlight";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { SortHeaderRow, useSort } from "@/components/SortableTable";
import { BindingKeyBadge } from "@/components/secrets/SecretBadges";
import { Badge, Checkbox } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { matchValueRow } from "@/lib/key-search";
import { links } from "@/lib/links";
import { resourceId, rowValueText, valueForKey } from "@/lib/overview";
import { isProductionEnvironment } from "@/lib/readiness";
import type { SortColumn } from "@/lib/sort";
import type { ApplicationConfigurationRow, EnvironmentOverview } from "@/lib/types";
import { AddResourceButton } from "./AddResourceButton";
import { ResourceLink } from "./ResourceLink";
import { UnreleasedBadge } from "./ValueBadges";
import type { ValueSnippet } from "./valueFilter";

/** Tooltips are not scroll containers; a megabyte JSON value is not a tooltip. */
const TITLE_MAX_CHARS = 200;

// Module scope so the sort controller's memos stay stable across renders. Only
// the two identity columns order; environment cells are badge stacks.
const COLUMNS: ReadonlyArray<SortColumn<ApplicationConfigurationRow>> = [
  { id: "key", label: "Key", value: (row) => row.key, className: "matrix-key" },
  { id: "kind", label: "Kind", value: (row) => row.kind },
];

export interface MatrixEnvironment {
  env: string;
  /** Rings the column header; inferred from the name when omitted. */
  production?: boolean;
}

export interface ConfigurationMatrixProps {
  app: string;
  schemaVersion?: number;
  environments: MatrixEnvironment[];
  /** The overview's per-environment contract values, for alias and pin lookup. */
  overview?: EnvironmentOverview[];
  rows: ApplicationConfigurationRow[];
  /** The application page's value filter; matches key, contract alias or stored value. */
  filter?: string;
  onAddSecret: (environment: string, key: string) => void;
  onAddValue?: (environment: string, key: string) => void;
  onOpenSecret?: (environment: string, key: string) => void;
  onOpenParameter?: (environment: string, key: string) => void;
  onEdit: (row: ApplicationConfigurationRow) => void;
}

/** A row is incomplete when at least one environment has nothing behind the key. */
function isIncomplete(
  row: ApplicationConfigurationRow,
  environments: MatrixEnvironment[],
): boolean {
  return environments.some((env) => !row.environments[env.env]?.present);
}

/**
 * The contract alias a physical key resolves to, from the first environment
 * that resolves it. Only shown when it differs from the key: the pipeline's
 * key chip follows the same rule, and a chip repeating the key is noise.
 */
function aliasFor(
  row: ApplicationConfigurationRow,
  overview: readonly EnvironmentOverview[],
): string | undefined {
  for (const environment of overview) {
    const value = valueForKey(environment, row.kind, row.key);
    if (value) return value.alias === row.key ? undefined : value.alias;
  }
  return undefined;
}

interface MatrixRow {
  row: ApplicationConfigurationRow;
  id: string;
  alias?: string;
  /** Every distinct value the row holds across its environments, for searching. */
  valueText: string;
}

/** A row that passed the filter, with the excerpt that explains a value-only hit. */
type MatrixMatch = MatrixRow & { snippet?: ValueSnippet };

export function ConfigurationMatrix({
  app,
  schemaVersion,
  environments,
  overview,
  rows,
  filter = "",
  onAddSecret,
  onAddValue,
  onOpenSecret,
  onOpenParameter,
  onEdit,
}: ConfigurationMatrixProps) {
  const sort = useSort<ApplicationConfigurationRow>(links.applications(), COLUMNS);
  const [incompleteOnly, setIncompleteOnly] = useState(false);
  const incompleteId = useId();

  const overviewByEnv = useMemo(
    () => new Map((overview ?? []).map((environment) => [environment.namespace.env, environment])),
    [overview],
  );
  const matrixRows = useMemo<MatrixRow[]>(
    () =>
      rows.map((row) => ({
        row,
        id: resourceId(row.kind, row.key),
        alias: aliasFor(row, overview ?? []),
        valueText: rowValueText(row),
      })),
    [rows, overview],
  );

  // Filter, sort and count missing cells in one pass; the footer describes the
  // rows on screen, so it is derived from the same list they render from.
  const { visible, missing } = useMemo(() => {
    const kept: MatrixMatch[] = [];
    for (const entry of matrixRows) {
      if (incompleteOnly && !isIncomplete(entry.row, environments)) continue;
      // The same multi-token semantics the list pages search with, over the
      // key, the contract alias and the values stored behind the row.
      const match = matchValueRow(
        { key: entry.row.key, alias: entry.alias, value: entry.valueText },
        filter,
      );
      if (!match) continue;
      kept.push(
        match.valueRanges.length > 0
          ? { ...entry, snippet: { text: entry.valueText, ranges: match.valueRanges } }
          : entry,
      );
    }
    const sorted = sort.apply(kept.map(({ row }) => row));
    const byId = new Map(kept.map((entry) => [entry.id, entry]));
    const visible = sorted.map((row) => byId.get(resourceId(row.kind, row.key)) as MatrixMatch);
    const missing = environments.map(() => 0);
    for (const { row } of visible) {
      environments.forEach((env, index) => {
        if (!row.environments[env.env]?.present) missing[index] += 1;
      });
    }
    return { visible, missing };
  }, [matrixRows, filter, incompleteOnly, environments, sort.apply]);
  const anyMissing = missing.some((count) => count > 0);

  return (
    <>
      <div className="matrix-toolbar">
        {/* The filter box lives beside the tabs on the application page, so one
            search narrows both this table and the pipeline. */}
        <div className="checkbox-row">
          <Checkbox
            id={incompleteId}
            checked={incompleteOnly}
            onCheckedChange={(checked) => setIncompleteOnly(Boolean(checked))}
          />
          <label htmlFor={incompleteId}>Only incomplete rows</label>
        </div>
      </div>
      <div className="table-wrap application-matrix">
        <p className="mobile-comparison-hint">Scroll horizontally to compare environments.</p>
        <table className="data">
          <thead>
            <SortHeaderRow
              controller={sort}
              after={
                <>
                  {environments.map((env) => (
                    <th key={env.env} scope="col" className="matrix-env">
                      <Ident
                        kind="env"
                        value={env.env}
                        href={links.application(app, { schemaVersion, env: env.env })}
                        production={env.production ?? isProductionEnvironment(env.env)}
                        tooltip={false}
                      />
                    </th>
                  ))}
                  <th className="matrix-actions">
                    <span className="sr-only">Actions</span>
                  </th>
                </>
              }
            />
          </thead>
          <tbody>
            {visible.map(({ row, id, alias, snippet }) => {
              return (
                <tr key={id}>
                  <td className="mono matrix-key">
                    {row.key}
                    {alias ? (
                      <span className="matrix-alias">
                        <Ident kind="alias" value={alias} tooltip={false} />
                      </span>
                    ) : null}
                    {/* Only a stored value matched: show where, so a hit inside
                        a large JSON is explainable from the key column. */}
                    {snippet ? <Snippet text={snippet.text} ranges={snippet.ranges} /> : null}
                  </td>
                  <td>
                    {/* Kind is a classification, not a state: neutral either way so
                        warning stays reserved for something being wrong. */}
                    <Badge kind="neutral">{row.kind}</Badge>
                  </td>
                  {environments.map((env) => (
                    <td key={env.env} className="matrix-env">
                      <MatrixCell
                        row={row}
                        environment={env.env}
                        overview={overviewByEnv.get(env.env)}
                        app={app}
                        onAddSecret={onAddSecret}
                        onAddValue={onAddValue}
                        onOpenSecret={onOpenSecret}
                        onOpenParameter={onOpenParameter}
                      />
                    </td>
                  ))}
                  <td className="matrix-actions">
                    {row.kind === "parameter" ? (
                      <Button variant="outline" size="sm" onClick={() => onEdit(row)}>
                        <SlidersHorizontal size={14} />
                        Edit
                      </Button>
                    ) : null}
                  </td>
                </tr>
              );
            })}
            {visible.length === 0 ? (
              <tr>
                <td colSpan={environments.length + 3} className="faint">
                  {rows.length === 0
                    ? "No parameters or secrets have been created."
                    : "No rows match the filter."}
                </td>
              </tr>
            ) : null}
          </tbody>
          {anyMissing ? (
            <tfoot>
              <tr>
                <td className="matrix-key">Missing</td>
                <td />
                {environments.map((env, index) => (
                  <td key={env.env} className="matrix-env">
                    {missing[index] > 0 ? `${missing[index]} missing` : null}
                  </td>
                ))}
                <td className="matrix-actions" />
              </tr>
            </tfoot>
          ) : null}
        </table>
      </div>
    </>
  );
}

function MatrixCell({
  row,
  environment,
  overview,
  app,
  onAddSecret,
  onAddValue,
  onOpenSecret,
  onOpenParameter,
}: {
  row: ApplicationConfigurationRow;
  environment: string;
  overview?: EnvironmentOverview;
  app: string;
  onAddSecret: (environment: string, key: string) => void;
  onAddValue?: (environment: string, key: string) => void;
  onOpenSecret?: (environment: string, key: string) => void;
  onOpenParameter?: (environment: string, key: string) => void;
}) {
  const cell = row.environments[environment];
  if (!cell?.present) {
    if (row.kind === "secret") {
      return (
        <AddResourceButton
          kind="secret"
          variant="ghost"
          onClick={() => onAddSecret(environment, row.key)}
        />
      );
    }
    return (
      <AddResourceButton
        kind="parameter"
        variant="ghost"
        disabled={!onAddValue}
        onClick={() => onAddValue?.(environment, row.key)}
      />
    );
  }
  // The same overview value the pipeline row reads, so drift renders identically in both tabs.
  const value = valueForKey(overview, row.kind, row.key);
  const drift = value ? (
    <UnreleasedBadge value={value} hasActiveRelease={Boolean(overview?.release.active)} />
  ) : null;
  const label = `Open ${row.key} in ${environment}`;
  if (row.kind === "secret")
    return (
      <div className="matrix-value">
        <ResourceLink
          kind="secret"
          env={environment}
          app={app}
          keyName={row.key}
          onOpen={onOpenSecret}
          className="matrix-secret"
          aria-label={label}
        >
          <span className="kind-glyph" role="img" aria-label="Secret">
            <Icon.secret size={13} />
          </span>
          Secret v{cell.version}
        </ResourceLink>
        {cell.bound || drift ? (
          <span className="matrix-value-meta faint text-sm">
            {cell.bound ? <BindingKeyBadge version={cell.version} /> : null}
            {drift}
          </span>
        ) : null}
      </div>
    );
  const text = cell.value ?? "";
  const title = text.length > TITLE_MAX_CHARS ? `${text.slice(0, TITLE_MAX_CHARS)}…` : text;
  return (
    <div className="matrix-value">
      <ResourceLink
        kind="parameter"
        env={environment}
        app={app}
        keyName={row.key}
        onOpen={onOpenParameter}
        className="mono matrix-value-link"
        title={title}
        aria-label={label}
      >
        {text === "" ? <EmptyValue /> : text}
      </ResourceLink>
      <span className="matrix-value-meta faint text-sm">
        <span>
          v{cell.version} · {cell.content_type}
        </span>
        {drift}
        {/* Sized through utilities: the Button cva's own h-/px- utilities beat
            any component-layer rule, however specific. */}
        <CopyButton value={text} label="Copy" className="matrix-copy h-[22px] px-2" />
      </span>
    </div>
  );
}
