import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Modal } from "@/components/Modal";
import { ContentTypeSelect, ParameterValueInput } from "@/components/ParameterValueInput";
import { Badge, Checkbox, Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors } from "@/lib/hooks";
import { canonicalParameterValue, valuesEquivalent } from "@/lib/json-text";
import { isProductionEnvironment } from "@/lib/readiness";
import { aliasSchema } from "@/lib/schema-form";
import type { ApplicationConfigurationRow } from "@/lib/types";
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
    environments: string[];
  }) => Promise<void>;
}) {
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("string");
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
    setSelected(initial?.length ? initial : present.length ? present : environments);
    const first = present.length ? row.environments[present[0]] : undefined;
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
  const allSelected = selected.length === environments.length;
  // The same value is written to every selected environment, so it only has to
  // parse once. Memoised because a JSON document may run to a megabyte.
  const keyProblem = validateKey(key.trim());
  const valueProblem = useMemo(
    () => firstError(validateValueSize(value), validateParameterValue(value, contentType)),
    [value, contentType],
  );
  // An existing key's input is disabled, so a legacy key cannot block an edit.
  const blocking = firstError(row?.key ? null : keyProblem, valueProblem);
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
    if (selected.length === 0) return;
    void onSave({
      application: app,
      key,
      value: canonicalParameterValue(value, contentType),
      content_type: contentType,
      metadata_json: "{}",
      environments: selected,
    });
  }

  return (
    <Modal
      open={row !== null}
      title={row?.key ? `Update ${row.key}` : "New parameter"}
      onClose={onClose}
      dismissible={!saving}
      dirty={dirty}
      initialFocus={row?.key ? valueRef : keyRef}
      wide
      footer={(close) => (
        <>
          {selected.length === 0 ? (
            <p className="footer-note" role="status">
              Choose at least one target environment.
            </p>
          ) : null}
          <Button type="button" variant="outline" onClick={close} disabled={saving}>
            Cancel
          </Button>
          <Button
            form={formId}
            type="submit"
            loading={saving}
            disabled={blocking !== null || selected.length === 0}
          >
            Apply to {selected.length} {selected.length === 1 ? "environment" : "environments"}
          </Button>
        </>
      )}
    >
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
          {differing
            ? " Existing values differ; the editor starts from the first selected environment."
            : ""}
        </div>
        <div className="form-row">
          <Field label="Key" error={row?.key ? null : shown("key", keyProblem)}>
            <Input
              ref={keyRef}
              className="font-mono"
              value={key}
              disabled={Boolean(row?.key)}
              onChange={(event) => setKey(event.target.value)}
              onBlur={() => touch("key")}
            />
          </Field>
          <Field label="Content type">
            <ContentTypeSelect
              value={contentType}
              currentValue={value}
              onValueChange={setContentType}
              onClearValue={() => setValue("")}
            />
          </Field>
        </div>
        <Field label="Value" error={shown("value", valueProblem)}>
          <ParameterValueInput
            contentType={contentType}
            value={value}
            schema={schema}
            inputRef={valueRef}
            rows={7}
            onChange={setValue}
            onBlur={() => touch("value")}
            onSubmit={submit}
          />
        </Field>
        <Field label="Target environments">
          <div className="checkbox-row">
            <Checkbox
              id="all-target-environments"
              checked={allSelected}
              onCheckedChange={(checked) => setSelected(checked ? environments : [])}
            />
            <label htmlFor="all-target-environments">
              <strong>All environments</strong>
            </label>
          </div>
          <div className="environment-check-grid">
            {environments.map((environment) => (
              <div className="checkbox-row" key={environment}>
                <Checkbox
                  id={`target-environment-${environment}`}
                  checked={selected.includes(environment)}
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
      </form>
    </Modal>
  );
}
