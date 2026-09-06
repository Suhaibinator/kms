import { SlidersHorizontal } from "lucide-react";
import CopyButton from "@/components/CopyButton";
import { Ident } from "@/components/Ident";
import { Badge } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { isProductionEnvironment } from "@/lib/readiness";
import type { ApplicationConfigurationRow, EnvironmentOverview } from "@/lib/types";
import { AddResourceButton } from "./AddResourceButton";
import { ResourceLink } from "./ResourceLink";

/** Tooltips are not scroll containers; a megabyte JSON value is not a tooltip. */
const TITLE_MAX_CHARS = 200;

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

export function ConfigurationMatrix({
  app,
  environments,
  rows,
  onAddSecret,
  onOpenSecret,
  onOpenParameter,
  onEdit,
}: ConfigurationMatrixProps) {
  return (
    <div className="table-wrap application-matrix">
      <p className="mobile-comparison-hint">Scroll horizontally to compare environments.</p>
      <table className="data">
        <thead>
          <tr>
            <th className="matrix-key">Key</th>
            <th>Kind</th>
            {environments.map((env) => (
              <th key={env.env} scope="col">
                <Ident
                  kind="env"
                  value={env.env}
                  production={env.production ?? isProductionEnvironment(env.env)}
                  tooltip={false}
                />
              </th>
            ))}
            <th />
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={`${row.kind}:${row.key}`}>
              <td className="mono matrix-key">{row.key}</td>
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
                    app={app}
                    onAddSecret={onAddSecret}
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
          ))}
          {rows.length === 0 ? (
            <tr>
              <td colSpan={environments.length + 3} className="faint">
                No parameters or secrets have been created.
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}

function MatrixCell({
  row,
  environment,
  app,
  onAddSecret,
  onOpenSecret,
  onOpenParameter,
}: {
  row: ApplicationConfigurationRow;
  environment: string;
  app: string;
  onAddSecret: (environment: string, key: string) => void;
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
    return <Badge kind="danger">missing</Badge>;
  }
  if (row.kind === "secret")
    return (
      <ResourceLink
        kind="secret"
        env={environment}
        app={app}
        keyName={row.key}
        onOpen={onOpenSecret}
      >
        <span className="secret-cell">
          Secret v{cell.version}
          {cell.bound ? " · binding key" : ""}
        </span>
      </ResourceLink>
    );
  const value = cell.value ?? "";
  const title = value.length > TITLE_MAX_CHARS ? `${value.slice(0, TITLE_MAX_CHARS)}…` : value;
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
        aria-label={`Open ${row.key} in ${environment}`}
      >
        {value === "" ? "(empty)" : value}
      </ResourceLink>
      <span className="matrix-value-meta faint text-sm">
        <span>
          v{cell.version} · {cell.content_type}
        </span>
        <CopyButton value={value} label="Copy" className="matrix-copy" />
      </span>
    </div>
  );
}
