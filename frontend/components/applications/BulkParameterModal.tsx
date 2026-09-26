import { useEffect, useId, useMemo, useRef, useState } from "react";
import { JsonDiff } from "@/components/JsonDiff";
import { Modal } from "@/components/Modal";
import { ContentTypeSelect, ParameterValueInput } from "@/components/ParameterValueInput";
import { Badge, Checkbox, Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors } from "@/lib/hooks";
import { canonicalParameterValue, valuesEquivalent } from "@/lib/json-text";
import { isProductionEnvironment } from "@/lib/readiness";
import { aliasSchema } from "@/lib/schema-form";
import type { ApplicationConfigurationRow, ApplicationWriteResult } from "@/lib/types";
import {
  firstError,
  validateKey,
  validateParameterValue,
  validateValueSize,
} from "@/lib/validation";

export function BulkParameterModal({
  app,
  environments,
  row,
  initialEnvironments,
  retryEnvironments,
  results = [],
  schemaJson,
  saving,
  onClose,
  onSave,
}: {
  app: string;
  environments: string[];
  row: ApplicationConfigurationRow | null;
  /** Preselect these targets instead of every environment the key is present in. */
  initialEnvironments?: string[] | null;
  /** After a partial failure: the environments still to write. Narrows the selection only. */
  retryEnvironments: string[] | null;
  results?: ApplicationWriteResult[];
  /** Pinned schema JSON; enables the field-by-field editor for json values. */
  schemaJson?: string | null;
  saving: boolean;
  onClose: () => void;
  onSave: (request: {
    application: string;
    key: string;
    value: string;
    content_type: string;
    metadata_json: string;
    preserve_metadata?: boolean;
    environments: string[];
  }) => Promise<void>;
}) {
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [valueValid, setValueValid] = useState(true);
  const [contentType, setContentType] = useState("string");
  const [source, setSource] = useState<string | null>(null);
  const [reviewSnapshot, setReviewSnapshot] = useState<{
    key: string;
    value: string;
    contentType: string;
    selected: string[];
  } | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  // What the form opened with, so a dismissal only asks when something changed.
  const [opened, setOpened] = useState<{ value: string; contentType: string; key: string }>({
    value: "",
    contentType: "string",
    key: "",
  });
  const { touch, markAllTouched, reset, shown } = useFieldErrors<"key" | "value">();
  const formId = useId();
  const keyRef = useRef<HTMLInputElement | null>(null);
  const valueRef = useRef<HTMLElement | null>(null);
  const { formRef, requestFocus } = useFocusFirstInvalid();
  useEffect(() => {
    if (!row) return;
    reset();
    setKey(row.key);
    const present = environments.filter((environment) => row.environments[environment]?.present);
    const initial = initialEnvironments?.filter((environment) =>
      environments.includes(environment),
    );
    const targets =
      initial ??
      (present.length ? present : environments).filter(
        (environment) => !isProductionEnvironment(environment),
      );
    setSelected(targets);
    const sourceEnvironment =
      targets.find((environment) => row.environments[environment]?.present) ??
      (initial === undefined ? present[0] : undefined);
    setSource(sourceEnvironment ?? null);
    const first = sourceEnvironment ? row.environments[sourceEnvironment] : undefined;
    setReviewSnapshot(null);
    setValue(first?.value ?? "");
    setContentType(first?.content_type ?? "string");
    setOpened({
      value: first?.value ?? "",
      contentType: first?.content_type ?? "string",
      key: row.key,
    });
  }, [row, environments, initialEnvironments, reset]);
  useEffect(() => {
    if (retryEnvironments) setSelected(retryEnvironments);
  }, [retryEnvironments]);
  const succeeded = new Set(
    results.filter((result) => !result.error).map((result) => result.environment),
  );
  const keyLocked = saving || results.length > 0;
  const valueLocked = saving || succeeded.size > 0;
  const complete = results.length > 0 && results.every((result) => !result.error);
  const available = environments.filter(
    (environment) =>
      !succeeded.has(environment) &&
      (retryEnvironments === null || retryEnvironments.includes(environment)),
  );
  const nonProduction = available.filter((environment) => !isProductionEnvironment(environment));
  const allSelected =
    nonProduction.length > 0 &&
    nonProduction.every((environment) => selected.includes(environment));
  const pendingTargets = selected.filter((environment) => available.includes(environment));
  const requiresReview = pendingTargets.length > 1;
  const reviewed =
    reviewSnapshot?.key === key &&
    reviewSnapshot.value === value &&
    reviewSnapshot.contentType === contentType &&
    reviewSnapshot.selected === selected;
  // The same value is written to every selected environment, so it only has to
  // parse once. Memoised because a JSON document may run to a megabyte.
  const keyProblem = validateKey(key.trim());
  const valueProblem = useMemo(
    () => firstError(validateValueSize(value), validateParameterValue(value, contentType)),
    [value, contentType],
  );
  // An existing key's input is disabled, so a legacy key cannot block an edit.
  const blocking = firstError(
    row?.key ? null : keyProblem,
    valueProblem,
    valueValid ? null : "Correct the invalid value fields before applying.",
  );
  const schema = useMemo(
    () => (contentType === "json" ? aliasSchema(schemaJson, key.trim()) : null),
    [schemaJson, contentType, key],
  );
  const differing = useMemo(
    () =>
      row
        ? new Set(
            environments
              .map((environment) => row.environments[environment]?.value)
              .filter((item) => item !== undefined),
          ).size > 1
        : false,
    [row, environments],
  );

  const dirty =
    !valueValid ||
    key !== opened.key ||
    contentType !== opened.contentType ||
    !valuesEquivalent(value, opened.value, contentType);

  function submit() {
    markAllTouched();
    if (saving) return;
    if (blocking) {
      // The message is now beside its field; put focus there too.
      requestFocus();
      return;
    }
    if (pendingTargets.length === 0) return;
    if (requiresReview && !reviewed) {
      setReviewSnapshot({ key, value, contentType, selected });
      return;
    }
    void onSave({
      application: app,
      key,
      value: canonicalParameterValue(value, contentType),
      content_type: contentType,
      metadata_json: "{}",
      preserve_metadata: Boolean(row?.key),
      environments: pendingTargets,
    });
  }

  return (
    <Modal
      mobileFullScreen
      open={row !== null}
      title={row?.key ? `Update ${row.key}` : "New parameter"}
      onClose={onClose}
      dismissible={!saving}
      dirty={dirty && (results.length === 0 || results.some((result) => result.error))}
      initialFocus={row?.key ? valueRef : keyRef}
      wide
      footer={(close) => (
        <>
          {selected.length === 0 && !complete ? (
            <p className="footer-note" role="status">
              Choose at least one target environment.
            </p>
          ) : null}
          <Button type="button" variant="outline" onClick={close} disabled={saving}>
            {results.length ? "Done" : "Cancel"}
          </Button>
          {!complete ? (
            <Button
              form={formId}
              type="submit"
              loading={saving}
              disabled={blocking !== null || pendingTargets.length === 0}
            >
              {requiresReview && !reviewed
                ? `Review ${pendingTargets.length} environments`
                : results.some((result) => result.error)
                  ? "Retry failed environments"
                  : `Apply to ${pendingTargets.length} ${pendingTargets.length === 1 ? "environment" : "environments"}`}
            </Button>
          ) : null}
        </>
      )}
    >
      {results.length > 0 ? (
        <section aria-label="Update results" className="mb-4" aria-live="polite">
          <h3>Update results</h3>
          <ul>
            {results.map((result) => (
              <li key={result.environment}>
                <strong>{result.environment}</strong>:{" "}
                {result.error ? (
                  <span className="text-danger">{result.error}</span>
                ) : (
                  `Saved v${result.version}`
                )}
              </li>
            ))}
          </ul>
          <p>
            Successful environments will not be written again. Your draft is preserved for retry.
          </p>
          {!complete && succeeded.size > 0 ? (
            <p>
              Retries use the same key, content type, and value as the successful writes. Finish
              this operation before starting a different edit.
            </p>
          ) : null}
        </section>
      ) : null}
      {!complete ? (
        <form
          id={formId}
          ref={formRef}
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <div className="warn-panel mb-4">
            <strong>Separate versions will be created.</strong> This does not link environments or
            create shared mutable state. Verify production targets before applying.
            {differing ? " Existing values differ." : ""}
            {source ? (
              <p className="mb-0">
                Starting value: <strong>{source}</strong>. Changing targets keeps your draft.
              </p>
            ) : (
              <p className="mb-0">Enter a new value for the selected targets.</p>
            )}
          </div>
          <div className="form-row">
            <Field label="Key" error={row?.key ? null : shown("key", keyProblem)}>
              <Input
                ref={keyRef}
                className="font-mono"
                value={key}
                disabled={Boolean(row?.key) || keyLocked}
                onChange={(event) => {
                  if (!keyLocked) setKey(event.target.value);
                }}
                onBlur={() => touch("key")}
              />
            </Field>
            <Field label="Content type">
              <ContentTypeSelect
                value={contentType}
                currentValue={value}
                disabled={valueLocked}
                onValueChange={(next) => {
                  if (!valueLocked) setContentType(next);
                }}
                onClearValue={() => {
                  if (!valueLocked) setValue("");
                }}
              />
            </Field>
          </div>
          <Field label="Value" error={shown("value", valueProblem)}>
            <ParameterValueInput
              key={row?.key ?? "new"}
              contentType={contentType}
              value={value}
              storedValue={opened.value}
              schema={schema}
              inputRef={valueRef}
              rows={7}
              disabled={valueLocked}
              onChange={(next) => {
                if (!valueLocked) setValue(next);
              }}
              onValidityChange={setValueValid}
              onBlur={() => touch("value")}
              onSubmit={submit}
            />
          </Field>
          <Field label="Target environments">
            <div className="checkbox-row">
              <Checkbox
                id="all-target-environments"
                checked={allSelected}
                disabled={saving}
                onCheckedChange={(checked) =>
                  setSelected((current) =>
                    checked
                      ? [...new Set([...current, ...nonProduction])]
                      : current.filter((environment) => !nonProduction.includes(environment)),
                  )
                }
              />
              <label htmlFor="all-target-environments">
                <strong>All non-production environments</strong>
              </label>
            </div>
            <div className="environment-check-grid">
              {environments.map((environment) => (
                <div className="checkbox-row" key={environment}>
                  <Checkbox
                    id={`target-environment-${environment}`}
                    checked={selected.includes(environment)}
                    disabled={!available.includes(environment) || saving}
                    onCheckedChange={(checked) =>
                      setSelected((current) =>
                        checked
                          ? [...current, environment]
                          : current.filter((item) => item !== environment),
                      )
                    }
                  />
                  <label className="mono" htmlFor={`target-environment-${environment}`}>
                    {environment}
                  </label>
                  {isProductionEnvironment(environment) ? (
                    <Badge kind="warning">production</Badge>
                  ) : null}
                </div>
              ))}
            </div>
          </Field>
          {reviewed && requiresReview ? (
            <section aria-label="Review environment changes" className="stack">
              <h3>Review environment changes</h3>
              <p>
                Each target receives this complete value as a new version. Releases are unchanged.
              </p>
              {pendingTargets.map((environment) => {
                const previous = row?.environments[environment];
                return (
                  <section key={environment} aria-label={`Changes for ${environment}`}>
                    <h4>
                      {environment}
                      {isProductionEnvironment(environment) ? " · production" : ""}
                    </h4>
                    <p>
                      {previous?.present
                        ? `Current v${previous.version} · ${previous.content_type}`
                        : "No existing value"}{" "}
                      → {contentType}
                    </p>
                    <JsonDiff
                      before={previous?.value ?? ""}
                      after={canonicalParameterValue(value, contentType)}
                      contentType={contentType}
                      maxHeight="20rem"
                    />
                  </section>
                );
              })}
            </section>
          ) : null}
        </form>
      ) : null}
    </Modal>
  );
}
