import { SlidersHorizontal } from "lucide-react";
import { useId, useMemo, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Ident } from "@/components/Ident";
import { Icon } from "@/components/icons";
import { SortHeaderRow, useSort } from "@/components/SortableTable";
import { BindingKeyBadge } from "@/components/secrets/SecretBadges";
import { Badge, Checkbox, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { links } from "@/lib/links";
import { aliasesByKey, resourceId, valueForKey } from "@/lib/overview";
import { isProductionEnvironment } from "@/lib/readiness";
import type { SortColumn } from "@/lib/sort";
import type { ApplicationConfigurationRow, EnvironmentOverview } from "@/lib/types";
import { AddResourceButton } from "./AddResourceButton";
import { ResourceLink } from "./ResourceLink";
import { UnreleasedBadge } from "./ValueBadges";

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
  environments: MatrixEnvironment[];
  /** The overview's per-environment contract values, for alias and pin lookup. */
  overview?: EnvironmentOverview[];
  rows: ApplicationConfigurationRow[];
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
  resolved: ReadonlyArray<Map<string, string>>,
): string | undefined {
  const id = resourceId(row.kind, row.key);
  for (const aliases of resolved) {
    const alias = aliases.get(id);
    if (alias !== undefined) return alias === row.key ? undefined : alias;
  }
  return undefined;
}

export function ConfigurationMatrix({
  app,
  environments,
  overview,
  rows,
  onAddSecret,
  onAddValue,
  onOpenSecret,
  onOpenParameter,
  onEdit,
}: ConfigurationMatrixProps) {
  const sort = useSort<ApplicationConfigurationRow>(links.applications(), COLUMNS);
  const [filter, setFilter] = useState("");
  const [incompleteOnly, setIncompleteOnly] = useState(false);
  const incompleteId = useId();

  const overviewByEnv = useMemo(
    () => new Map((overview ?? []).map((environment) => [environment.namespace.env, environment])),
    [overview],
  );
  const resolved = useMemo(
    () => (overview ?? []).map((environment) => aliasesByKey(environment)),
    [overview],
  );
  const aliases = useMemo(
    () => new Map(rows.map((row) => [resourceId(row.kind, row.key), aliasFor(row, resolved)])),
    [rows, resolved],
  );

  const needle = filter.trim().toLowerCase();
  const visible = sort.apply(
    rows.filter((row) => {
      if (incompleteOnly && !isIncomplete(row, environments)) return false;
      if (!needle) return true;
      const alias = aliases.get(resourceId(row.kind, row.key));
      return (
        row.key.toLowerCase().includes(needle) || Boolean(alias?.toLowerCase().includes(needle))
      );
    }),
  );

  // Counted over the rows on screen, so the footer describes the table above it.
  const missing = environments.map(
    (env) => visible.filter((row) => !row.environments[env.env]?.present).length,
  );
  const anyMissing = missing.some((count) => count > 0);

  return (
    <>
      <div className="matrix-toolbar">
        <Input
          type="search"
          className="matrix-filter"
          placeholder="Filter keys"
          aria-label="Filter keys"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
        />
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
                    <th key={env.env} scope="col">
                      <Ident
                        kind="env"
                        value={env.env}
                        href={links.application(app, { env: env.env })}
                        production={env.production ?? isProductionEnvironment(env.env)}
                        tooltip={false}
                      />
                    </th>
                  ))}
                  <th />
                </>
              }
            />
          </thead>
          <tbody>
            {visible.map((row) => {
              const alias = aliases.get(resourceId(row.kind, row.key));
              return (
                <tr key={resourceId(row.kind, row.key)}>
                  <td className="mono matrix-key">
                    {row.key}
                    {alias ? (
                      <span className="matrix-alias">
                        <Ident kind="alias" value={alias} tooltip={false} />
                      </span>
                    ) : null}
                  </td>
                  <td>
                    {/* Kind is a classification, not a state: neutral either way so
                        warning stays reserved for something being wrong. */}
                    <Badge kind="neutral">{row.kind}</Badge>
                  </td>
                  {environments.map((env) => (
                    <td key={env.env}>
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
                  <td>
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
            {rows.length === 0 ? (
              <tr>
                <td colSpan={environments.length + 3} className="faint">
                  No parameters or secrets have been created.
                </td>
              </tr>
            ) : null}
            {rows.length > 0 && visible.length === 0 ? (
              <tr>
                <td colSpan={environments.length + 3} className="faint">
                  No rows match the filter.
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
                  <td key={env.env}>{missing[index] > 0 ? `${missing[index]} missing` : null}</td>
                ))}
                <td />
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
          <span className="matrix-kind" role="img" aria-label="Secret">
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
        {text === "" ? "(empty)" : text}
      </ResourceLink>
      <span className="matrix-value-meta faint text-sm">
        <span>
          v{cell.version} · {cell.content_type}
        </span>
        {drift}
        <CopyButton value={text} label="Copy" className="matrix-copy" />
      </span>
    </div>
  );
}
