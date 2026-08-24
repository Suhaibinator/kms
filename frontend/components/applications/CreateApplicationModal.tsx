import { useEffect, useId, useState } from "react";
import { Modal } from "@/components/Modal";
import { Field, Input, Spinner, Textarea } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useFieldErrors } from "@/lib/hooks";
import type { Application, ApplicationContractField } from "@/lib/types";
import {
  firstError,
  validateApplicationName,
  validateContract,
  validateReleaseName,
  validateSchemaPin,
} from "@/lib/validation";

const EMPTY_CONTRACT = `[
  {"alias":"runtime","kind":"parameter","content_type":"json"}
]`;

function parseContract(raw: string): ApplicationContractField[] {
  const value: unknown = JSON.parse(raw);
  if (!Array.isArray(value)) throw new Error("Contract must be a JSON array.");
  return value as ApplicationContractField[];
}

/** The fields of the application form that carry their own validation. */
type CreateField = "name" | "releaseName" | "schemaPin" | "contract";

/**
 * Validates the contract textarea: first that it is a JSON array at all, then
 * that every entry satisfies the server's per-field and duplicate-alias rules.
 */
function validateContractText(raw: string): string | null {
  let fields: ApplicationContractField[];
  try {
    fields = parseContract(raw);
  } catch (cause) {
    return cause instanceof Error ? cause.message : "Contract must be a JSON array.";
  }
  return validateContract(fields);
}

export function CreateApplicationModal({
  open,
  saving,
  initial,
  onClose,
  onSave,
}: {
  open: boolean;
  saving: boolean;
  initial?: Application | null;
  onClose: () => void;
  onSave: (app: {
    name: string;
    description: string;
    release_name: string;
    schema_id: string;
    schema_version: number;
    contract: ApplicationContractField[];
  }) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [releaseName, setReleaseName] = useState("runtime");
  const [schemaID, setSchemaID] = useState("");
  const [schemaVersion, setSchemaVersion] = useState("");
  const [contract, setContract] = useState(EMPTY_CONTRACT);
  const [error, setError] = useState("");
  const { touch, markAllTouched, reset, shown } = useFieldErrors<CreateField>();
  // The submit button lives in the modal footer, outside the form element; the
  // HTML `form` attribute is what still makes Enter in the body submit it.
  const formId = useId();
  useEffect(() => {
    if (!open) return;
    setName(initial?.name ?? "");
    setDescription(initial?.description ?? "");
    setReleaseName(initial?.release_name ?? "runtime");
    setSchemaID(initial?.schema_id ?? "");
    setSchemaVersion(initial?.schema_version ? String(initial.schema_version) : "");
    setContract(initial ? JSON.stringify(initial.contract, null, 2) : EMPTY_CONTRACT);
    setError("");
    reset();
  }, [open, initial, reset]);

  // Three different naming rules meet on this form, so each field is checked
  // against its own: the application name is a namespace label, the release
  // name is a relative key, and the contract aliases have their own grammar.
  const nameProblem = validateApplicationName(name.trim());
  const releaseNameProblem = validateReleaseName(releaseName.trim());
  const schemaPinProblem = validateSchemaPin(schemaID, schemaVersion);
  const contractProblem = validateContractText(contract);
  const blocking = firstError(
    // An existing application's name is fixed and its input disabled, so a
    // legacy name that predates the current rule cannot block an edit here.
    initial ? null : nameProblem,
    releaseNameProblem,
    schemaPinProblem,
    contractProblem,
  );
  const shownSchemaPinProblem = shown("schemaPin", schemaPinProblem);

  function submit() {
    markAllTouched();
    if (saving || blocking) return;
    try {
      setError("");
      void onSave({
        name,
        description,
        release_name: releaseName,
        schema_id: schemaID,
        schema_version: Number(schemaVersion || 0),
        contract: parseContract(contract),
      });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Invalid contract");
    }
  }

  return (
    <Modal
      open={open}
      title={initial ? `Edit ${initial.name}` : "New application"}
      onClose={onClose}
      dismissible={!saving}
      wide
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" disabled={saving || blocking !== null}>
            {saving ? <Spinner /> : null}
            {initial ? "Save contract" : "Create application"}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="info-panel mb-4">
          The application owns this shape. Every environment release must use the same release name,
          schema pin, aliases, kinds, and parameter content types.
        </div>
        <div className="form-row">
          <Field
            label="Application name"
            hint="Lowercase letters, digits, and hyphens."
            error={initial ? null : shown("name", nameProblem)}
          >
            <Input
              className="font-mono"
              value={name}
              disabled={Boolean(initial)}
              onChange={(event) => setName(event.target.value)}
              onBlur={() => touch("name")}
              placeholder="payments-api"
            />
          </Field>
          <Field
            label="Release name"
            hint="Defaults to runtime when left blank."
            error={shown("releaseName", releaseNameProblem)}
          >
            <Input
              className="font-mono"
              value={releaseName}
              onChange={(event) => setReleaseName(event.target.value)}
              onBlur={() => touch("releaseName")}
            />
          </Field>
        </div>
        <Field label="Description">
          <Input value={description} onChange={(event) => setDescription(event.target.value)} />
        </Field>
        <div className="form-row">
          <Field
            label="Schema ID"
            hint="Optional; specify both ID and version."
            error={shownSchemaPinProblem}
          >
            <Input
              className="font-mono"
              value={schemaID}
              onChange={(event) => setSchemaID(event.target.value)}
              onBlur={() => touch("schemaPin")}
            />
          </Field>
          <Field label="Schema version">
            <Input
              type="number"
              min={1}
              value={schemaVersion}
              // The pin is one rule across two inputs; the message sits under
              // Schema ID, but both controls are part of the invalid pair.
              aria-invalid={shownSchemaPinProblem ? true : undefined}
              onChange={(event) => setSchemaVersion(event.target.value)}
              onBlur={() => touch("schemaPin")}
            />
          </Field>
        </div>
        <Field
          label="Shared release contract"
          hint="JSON array of {alias, kind, content_type}. Secrets omit content_type."
          error={shown("contract", contractProblem)}
        >
          <Textarea
            className="font-mono"
            rows={9}
            value={contract}
            onChange={(event) => setContract(event.target.value)}
            onBlur={() => touch("contract")}
          />
        </Field>
        {error ? <div className="danger-panel">{error}</div> : null}
      </form>
    </Modal>
  );
}
