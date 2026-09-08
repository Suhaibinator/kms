import { ChevronDown, GitCompareArrows, RotateCcw, Trash2 } from "lucide-react";
import { type Ref, useId, useRef, useState } from "react";
import { AddResourceButton } from "@/components/applications/AddResourceButton";
import { ResourceLink } from "@/components/applications/ResourceLink";
import { Ident } from "@/components/Ident";
import { JsonDiff } from "@/components/JsonDiff";
import { ParameterValueInput } from "@/components/ParameterValueInput";
import { BindingKeyBadge } from "@/components/secrets/SecretBadges";
import { Badge, Button, Field } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { assignRef } from "@/lib/forms";
import { aliasSchema, type JsonSchema } from "@/lib/schema-form";
import type { Application, EnvironmentOverview, OverviewValue } from "@/lib/types";
import {
  addableAliases,
  pinnedSecrets,
  rowChanged,
  type ShipRow,
  shownRowError,
  valueFor,
} from "./model";

/** Above this many rows the editor folds each one to a line until it is opened. */
export const COLLAPSE_ROWS_ABOVE = 3;

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
  /** Opens a pinned secret's workspace in place; without it Manage navigates. */
  onOpenSecret?: (env: string, key: string) => void;
  /** The Environment select's trigger — the step's first control, for the modal's `initialFocus`. */
  initialFocusRef?: Ref<HTMLElement>;
  /** Each row's value control (or its expand button while folded), for focus management. */
  registerControl?: (alias: string, node: HTMLElement | null) => void;
}

function firstLine(value: string): string {
  const end = value.indexOf("\n");
  return end === -1 ? value : `${value.slice(0, end)} …`;
}

function RowCard({
  row,
  env,
  schema,
  disabled,
  collapsible,
  open,
  onToggle,
  onChange,
  onRemove,
  controlRef,
}: {
  row: ShipRow;
  env: EnvironmentOverview | null;
  schema: JsonSchema | null;
  disabled: boolean;
  collapsible: boolean;
  open: boolean;
  onToggle: () => void;
  onChange: (patch: Partial<ShipRow>) => void;
  onRemove: () => void;
  /** Receives the row's value control, or its expand button while folded. */
  controlRef?: Ref<HTMLElement>;
}) {
  const current = valueFor(env, row.alias);
  const error = shownRowError(row);
  const pinned = current?.pinned_version;
  const currentVersion = current?.current_version;
  // The prefill is the baseline for Revert and Show diff. The modal patches
  // `value`/`loaded` in one go, so the first loaded value is that baseline
  // unless the row already carries one.
  const captured = useRef<string | undefined>(undefined);
  if (row.loaded && !row.loadError && captured.current === undefined) captured.current = row.value;
  const original = row.originalValue ?? captured.current;
  const changed = original !== undefined && rowChanged({ ...row, originalValue: original });
  const [showDiff, setShowDiff] = useState(false);
  const [resetGeneration, setResetGeneration] = useState(0);
  const bodyId = useId();
  const editing = row.reuseVersion === undefined && row.loaded;
  return (
    <li
      className="ship-row"
      data-testid={`ship-row-${row.alias}`}
      data-alias={row.alias}
      data-changed={changed ? "true" : "false"}
      data-open={open ? "true" : "false"}
    >
      <div className="ship-row-head">
        <div className="ship-row-idents">
          <Ident kind="alias" value={row.alias} />
          <Badge className="ship-row-type">{row.content_type}</Badge>
          {row.key && row.key !== row.alias ? <Ident kind="key" value={row.key} /> : null}
          {changed ? <Badge kind="accent">changed</Badge> : null}
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
          <div className="ship-row-actions">
            {editing && original !== undefined ? (
              <>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={`Revert ${row.alias}`}
                  disabled={disabled || (!changed && row.draftValid !== false)}
                  onClick={() => {
                    onChange({ value: original, touched: true, reuseVersion: undefined });
                    setResetGeneration((generation) => generation + 1);
                    setShowDiff(false);
                  }}
                >
                  <RotateCcw size={14} aria-hidden /> Revert
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={`${showDiff ? "Hide" : "Show"} diff for ${row.alias}`}
                  aria-pressed={showDiff}
                  disabled={disabled || !changed}
                  onClick={() => setShowDiff((value) => !value)}
                >
                  <GitCompareArrows size={14} aria-hidden /> {showDiff ? "Hide diff" : "Show diff"}
                </Button>
              </>
            ) : null}
            {!row.missing ? (
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`Remove ${row.alias}`}
                disabled={disabled}
                onClick={onRemove}
              >
                <Trash2 size={14} aria-hidden />
              </Button>
            ) : null}
          </div>
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
      ) : row.loadError ? (
        <div className="info-panel" role="alert">
          <p>Could not load the current value: {row.loadError}</p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => onChange({ loaded: false, loadError: undefined })}
          >
            Retry loading {row.alias}
          </Button>
        </div>
      ) : !row.loaded ? (
        <div className="faint text-sm ship-row-loading" role="status">
          Loading the current value…
        </div>
      ) : collapsible && !open ? (
        <div className="ship-row-summary">
          <span className="ship-row-summary-value" title={row.value ? undefined : "empty"}>
            {row.value ? firstLine(row.value) : <span className="faint">(no value)</span>}
          </span>
          <button
            ref={(node) => assignRef(controlRef, node)}
            type="button"
            className="ship-row-expand"
            aria-expanded={false}
            aria-controls={bodyId}
            onClick={onToggle}
          >
            <ChevronDown size={14} aria-hidden /> Edit
          </button>
        </div>
      ) : (
        <div id={bodyId} className="ship-row-body">
          {collapsible ? (
            <button
              type="button"
              className="ship-row-expand"
              aria-expanded
              aria-controls={bodyId}
              disabled={error !== null}
              onClick={onToggle}
            >
              <ChevronDown size={14} aria-hidden /> Collapse
            </button>
          ) : null}
          <Field
            label="Value"
            error={error}
            hint={row.loadError ? `Could not load the current value: ${row.loadError}` : undefined}
          >
            <ParameterValueInput
              contentType={row.content_type}
              value={row.value}
              schema={schema}
              resetKey={String(resetGeneration)}
              onValidityChange={(valid) => onChange({ draftValid: valid })}
              disabled={disabled}
              aria-label={`${row.alias} value`}
              rows={6}
              inputRef={controlRef}
              onChange={(value) => onChange({ value, reuseVersion: undefined, touched: true })}
            />
          </Field>
          {showDiff && changed && original !== undefined ? (
            <div className="ship-row-diff" data-testid={`ship-row-diff-${row.alias}`}>
              <JsonDiff
                before={original}
                after={row.value}
                contentType={row.content_type}
                beforeLabel={
                  currentVersion !== undefined ? `current v${currentVersion}` : "current"
                }
                afterLabel="edited"
                maxHeight="40vh"
              />
            </div>
          ) : null}
        </div>
      )}
    </li>
  );
}

/**
 * One present secret and how the release will carry it: the pin the active
 * release already holds, or the version a first release will pin. Secrets are
 * never typed here, so the only action is Manage.
 */
function SecretPin({
  value,
  environment,
  app,
  onOpen,
}: {
  value: OverviewValue;
  environment: string;
  app: string;
  onOpen?: (env: string, key: string) => void;
}) {
  const pin =
    value.pinned_version !== undefined
      ? `pinned v${value.pinned_version}`
      : `will pin v${value.current_version ?? "?"}`;
  return (
    <li className="ship-secret-pin" data-testid={`ship-secret-pin-${value.alias}`}>
      <Ident kind="alias" value={value.alias} />
      <span className="faint text-sm">{pin}</span>
      {value.bound && value.current_version !== undefined ? (
        <BindingKeyBadge version={value.current_version} />
      ) : null}
      <ResourceLink
        kind="secret"
        button
        env={environment}
        app={app}
        keyName={value.key ?? value.alias}
        onOpen={onOpen}
        className="ship-secret-pin-manage"
      >
        Manage
      </ResourceLink>
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
  onOpenSecret,
  initialFocusRef,
  registerControl,
}: ShipEditorProps) {
  const envSelectId = useId();
  const pinsId = useId();
  const addable = addableAliases(application, rows);
  const pins = pinnedSecrets(env);
  // With many rows each folds to a line until opened; a row with a problem or
  // an edit in progress stays open on its own.
  const collapsible = rows.length > COLLAPSE_ROWS_ABOVE;
  const [toggled, setToggled] = useState<ReadonlyMap<string, boolean>>(() => new Map());
  function isOpen(row: ShipRow): boolean {
    if (!collapsible) return true;
    if (shownRowError(row) !== null) return true;
    const choice = toggled.get(row.alias);
    if (choice !== undefined) return choice;
    return row.touched === true;
  }
  return (
    <section className="ship-editor" data-testid="ship-editor" aria-label="Change">
      <div className="ship-env-row">
        <Field label="Environment" htmlFor={envSelectId} className="ship-env-field">
          <AppSelect
            ref={initialFocusRef as Ref<HTMLButtonElement> | undefined}
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
              <AddResourceButton
                kind="secret"
                disabled={disabled}
                onClick={() => onAddSecret(environment, alias)}
              />
            </li>
          ))}
        </ul>
      ) : null}

      {pins.length > 0 ? (
        <section className="ship-secret-pins-section" aria-labelledby={pinsId}>
          <h4 id={pinsId} className="ship-subtitle">
            Secrets pinned in this release
          </h4>
          <ul className="ship-secret-pins" data-testid="ship-secret-pins">
            {pins.map((value) => (
              <SecretPin
                key={value.alias}
                value={value}
                environment={environment}
                app={application.name}
                onOpen={onOpenSecret}
              />
            ))}
          </ul>
        </section>
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
              collapsible={collapsible}
              open={isOpen(row)}
              onToggle={() =>
                setToggled((current) => new Map(current).set(row.alias, !isOpen(row)))
              }
              onChange={(patch) => onRowChange(row.alias, patch)}
              onRemove={() => onRemoveRow(row.alias)}
              controlRef={(node) => registerControl?.(row.alias, node)}
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
