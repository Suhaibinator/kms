import { Plus, SlidersHorizontal } from "lucide-react";
import Link from "next/link";
import CopyButton from "@/components/CopyButton";
import { Ident } from "@/components/Ident";
import { Badge } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { links } from "@/lib/links";
import { isProductionEnvironment } from "@/lib/readiness";
import type { ApplicationConfigurationRow } from "@/lib/types";

/** Tooltips are not scroll containers; a megabyte JSON value is not a tooltip. */
const TITLE_MAX_CHARS = 200;

export interface MatrixEnvironment {
  env: string;
  /** Rings the column header; inferred from the name when omitted. */
  production?: boolean;
}

export function ConfigurationMatrix({
  app,
  environments,
  rows,
  onAddSecret,
  onEdit,
}: {
  app: string;
  environments: MatrixEnvironment[];
  rows: ApplicationConfigurationRow[];
  onAddSecret: (environment: string, key: string) => void;
  onEdit: (row: ApplicationConfigurationRow) => void;
}) {
  return (
    <div className="table-wrap application-matrix">
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
                  <MatrixCell row={row} environment={env.env} app={app} onAddSecret={onAddSecret} />
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
}: {
  row: ApplicationConfigurationRow;
  environment: string;
  app: string;
  onAddSecret: (environment: string, key: string) => void;
}) {
  const cell = row.environments[environment];
  if (!cell?.present) {
    if (row.kind === "secret") {
      return (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => onAddSecret(environment, row.key)}
        >
          <Plus size={13} />
          Add secret
        </Button>
      );
    }
    return <Badge kind="danger">missing</Badge>;
  }
  if (row.kind === "secret")
    return (
      <Link href={links.secretDetail({ env: environment, app, key: row.key })}>
        <span className="secret-cell">
          Secret v{cell.version}
          {cell.bound ? " · binding key" : ""}
        </span>
      </Link>
    );
  const value = cell.value ?? "";
  const title = value.length > TITLE_MAX_CHARS ? `${value.slice(0, TITLE_MAX_CHARS)}…` : value;
  const detail = links.parameterDetail({ env: environment, app, key: row.key });
  return (
    <div className="matrix-value">
      <Link
        href={detail}
        className="mono matrix-value-link"
        title={title}
        aria-label={`Open ${row.key} in ${environment}`}
      >
        {value === "" ? "(empty)" : value}
      </Link>
      <span className="matrix-value-meta faint text-sm">
        <span>
          v{cell.version} · {cell.content_type}
        </span>
        <CopyButton value={value} label="Copy" className="matrix-copy" />
      </span>
    </div>
  );
}
