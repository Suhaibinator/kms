import { Trash2 } from "lucide-react";
import { useId } from "react";
import { Ident } from "@/components/Ident";
import { SchemaForm } from "@/components/SchemaForm";
import { Badge, Button, Field, Input, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { aliasSchema, buildForm, type JsonSchema } from "@/lib/schema-form";
import type { Application, EnvironmentOverview } from "@/lib/types";
import { addableAliases, rowError, type ShipRow, valueFor } from "./model";

export interface ShipEditorProps {
  application: Application;
  environments: EnvironmentOverview[];
  /** Pinned schema JSON; enables the field-by-field editor for json aliases. */
  schemaJson?: string;
  environment: string;
  env: EnvironmentOverview | null;
  rows: ShipRow[];
  /** Secret aliases with no resource yet; a value must be added outside this modal. */
  blockers: string[];
  disabled: boolean;
  onEnvironmentChange: (environment: string) => void;
  onRowChange: (alias: string, patch: Partial<ShipRow>) => void;
  onAddRow: (alias: string) => void;
  onRemoveRow: (alias: string) => void;
  onAddSecret: (env: string, alias: string) => void;
}

function formatJson(value: string): string {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}

function ValueEditor({
  row,
  disabled,
  onChange,
}: {
  row: ShipRow;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const label = `${row.alias} value`;
  switch (row.content_type) {
    case "boolean":
      return (
        <AppSelect
          value={row.value.trim()}
          onValueChange={onChange}
          disabled={disabled}
          placeholder="Choose…"
          options={[
            { value: "true", label: "true" },
            { value: "false", label: "false" },
          ]}
          className="font-mono"
        />
      );
    case "integer":
    case "float":
      return (
        <Input
          className="font-mono"
          inputMode={row.content_type === "integer" ? "numeric" : "decimal"}
          aria-label={label}
          value={row.value}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => onChange(event.target.value)}
        />
      );
    default:
      return (
        <Textarea
          className="font-mono ship-value-editor"
          aria-label={label}
          value={row.value}
          disabled={disabled}
          spellCheck={false}
          rows={row.content_type === "json" ? 6 : 3}
          onChange={(event) => onChange(event.target.value)}
        />
      );
  }
}

function RowCard({
  row,
  env,
  schema,
  disabled,
  onChange,
  onRemove,
}: {
  row: ShipRow;
  env: EnvironmentOverview | null;
  schema: JsonSchema | null;
  disabled: boolean;
  onChange: (patch: Partial<ShipRow>) => void;
  onRemove: () => void;
}) {
  const current = valueFor(env, row.alias);
  const error = row.loaded ? rowError(row) : null;
  const pinned = current?.pinned_version;
  const currentVersion = current?.current_version;
  const structured = schema !== null && buildForm(schema) !== null;
  return (
    <li className="ship-row" data-testid={`ship-row-${row.alias}`} data-alias={row.alias}>
      <div className="ship-row-head">
        <div className="ship-row-idents">
          <Ident kind="alias" value={row.alias} />
          <Badge className="ship-row-type">{row.content_type}</Badge>
          {row.key && row.key !== row.alias ? <Ident kind="key" value={row.key} /> : null}
        </div>
        <div className="ship-row-meta">
          {row.missing ? (
            <Badge kind="warning">no value yet</Badge>
          ) : (
            <span className="faint text-sm">
              current v{currentVersion ?? "?"}
              {pinned !== undefined && pinned !== currentVersion ? ` · pinned v${pinned}` : ""}
            </span>
          )}
          {!row.missing ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-label={`Remove ${row.alias}`}
              disabled={disabled}
              onClick={onRemove}
            >
              <Trash2 size={14} aria-hidden />
            </Button>
          ) : null}
        </div>
      </div>
      {row.reuseVersion !== undefined ? (
        <div className="ship-row-reuse info-panel">
          <span>
            v{row.reuseVersion} was already written by the previous attempt and will be pinned
            as-is.
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => onChange({ reuseVersion: undefined })}
          >
            Edit value
          </Button>
        </div>
      ) : !row.loaded ? (
        <div className="faint text-sm ship-row-loading" role="status">
          Loading the current value…
        </div>
      ) : (
        <Field
          label="Value"
          error={error}
          hint={row.loadError ? `Could not load the current value: ${row.loadError}` : undefined}
        >
          {structured && schema ? (
            <SchemaForm
              schema={schema}
              value={row.value}
              disabled={disabled}
              jsonLabel={`${row.alias} value`}
              rows={6}
              onChange={(value) => onChange({ value, reuseVersion: undefined })}
            />
          ) : (
            <ValueEditor
              row={row}
              disabled={disabled}
              onChange={(value) => onChange({ value, reuseVersion: undefined })}
            />
          )}
        </Field>
      )}
      {row.loaded &&
      row.reuseVersion === undefined &&
      row.content_type === "json" &&
      !structured ? (
        <div className="ship-row-tools">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={disabled || rowError(row) !== null}
            onClick={() => onChange({ value: formatJson(row.value) })}
          >
            Format
          </Button>
        </div>
      ) : null}
    </li>
  );
}

/** The Change step: environment picker, blocker rows for missing secrets, one editor per parameter. */
export function ShipEditor({
  application,
  environments,
  schemaJson,
  environment,
  env,
  rows,
  blockers,
  disabled,
  onEnvironmentChange,
  onRowChange,
  onAddRow,
  onRemoveRow,
  onAddSecret,
}: ShipEditorProps) {
  const envSelectId = useId();
  const addable = addableAliases(application, rows);
  return (
    <section className="ship-editor" data-testid="ship-editor" aria-label="Change">
      <div className="ship-env-row">
        <Field label="Environment" htmlFor={envSelectId} className="ship-env-field">
          <AppSelect
            id={envSelectId}
            value={environment}
            onValueChange={onEnvironmentChange}
            disabled={disabled}
            options={environments.map((candidate) => ({
              value: candidate.namespace.env,
              label: candidate.production
                ? `${candidate.namespace.env} (production)`
                : candidate.namespace.env,
            }))}
          />
        </Field>
        {env ? (
          <div className="ship-env-summary">
            {env.release.active ? (
              <span className="text-sm faint">
                Active{" "}
                <span className="mono">{`${env.release.active.name}@${env.release.active.version}`}</span>
              </span>
            ) : (
              <Badge kind="accent">first release</Badge>
            )}
            {env.production ? <Badge kind="warning">production</Badge> : null}
          </div>
        ) : null}
      </div>

      {blockers.length > 0 ? (
        <ul className="ship-blockers" aria-label="Missing secrets">
          {blockers.map((alias) => (
            <li key={alias} className="ship-blocker" data-testid={`ship-blocker-${alias}`}>
              <Badge kind="danger">blocker</Badge>
              <div className="ship-blocker-body">
                <Ident kind="alias" value={alias} /> is a secret with no value in this environment.
                Secret values are never typed here; add one first and it will be pinned.
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={disabled}
                onClick={() => onAddSecret(environment, alias)}
              >
                Add secret
              </Button>
            </li>
          ))}
        </ul>
      ) : null}

      {rows.length === 0 ? (
        <p className="faint text-sm ship-empty-rows">
          No values are being edited. Add a change below, or ship as-is to pin every alias at its
          current version.
        </p>
      ) : (
        <ul className="ship-rows" aria-label="Changes">
          {rows.map((row) => (
            <RowCard
              key={row.alias}
              row={row}
              env={env}
              schema={row.content_type === "json" ? aliasSchema(schemaJson, row.alias) : null}
              disabled={disabled}
              onChange={(patch) => onRowChange(row.alias, patch)}
              onRemove={() => onRemoveRow(row.alias)}
            />
          ))}
        </ul>
      )}

      {addable.length > 0 ? (
        <div className="ship-add-row">
          <AppSelect
            value=""
            onValueChange={(alias) => {
              if (alias) onAddRow(alias);
            }}
            disabled={disabled}
            placeholder="Add change…"
            options={addable.map((alias) => ({ value: alias, label: alias }))}
            className="ship-add-select"
          />
        </div>
      ) : null}
    </section>
  );
}
