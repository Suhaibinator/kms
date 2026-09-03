import { useEffect, useId, useMemo, useState } from "react";
import { JsonEditor } from "@/components/JsonEditor";
import { Modal } from "@/components/Modal";
import { Field } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { deriveSchemaFromContract } from "@/lib/contract-derive";
import type { Application } from "@/lib/types";

/**
 * Derives and registers the next immutable schema version in the
 * application's owned release stream. Upload is registration-only; defaults
 * apply performs the separately previewed pin update.
 */
export function DeriveSchemaDialog({
  open,
  application,
  existingSchemaJson,
  onClose,
  onPinned,
}: {
  open: boolean;
  application: Application;
  existingSchemaJson?: string | null;
  onClose: () => void;
  onPinned: (application: Application) => void;
}) {
  const toast = useToast();
  const formId = useId();
  const derived = useMemo(
    () => deriveSchemaFromContract(application.contract, existingSchemaJson),
    [application.contract, existingSchemaJson],
  );
  const [schemaJson, setSchemaJson] = useState("");
  const [opened, setOpened] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!open) return;
    setSchemaJson(derived.schemaJson);
    setOpened(derived.schemaJson);
    setSaving(false);
  }, [open, derived.schemaJson]);

  const jsonProblem = useMemo(() => {
    try {
      const parsed: unknown = JSON.parse(schemaJson);
      return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
        ? null
        : "Schema must be a JSON object.";
    } catch (cause) {
      return cause instanceof Error ? cause.message : "Schema is not valid JSON.";
    }
  }, [schemaJson]);
  const blocking = jsonProblem;
  const dirty = schemaJson !== opened;

  async function submit() {
    if (saving || blocking) return;
    setSaving(true);
    try {
      const { schema } = await api.createSchema(application.name, schemaJson);
      toast.success(
        "Schema registered",
        `${schema.application}/${schema.release_name}@${schema.version}`,
      );
      onPinned(application);
    } catch (error) {
      toast.error(error, "Failed to register schema");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={open}
      title="Derive schema from contract"
      onClose={onClose}
      dismissible={!saving}
      dirty={dirty && !saving}
      wide
      footer={(close) => (
        <>
          {blocking && !saving ? (
            <p className="footer-note" role="status">
              {blocking}
            </p>
          ) : null}
          <Button type="button" variant="outline" onClick={close} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" loading={saving} disabled={blocking !== null}>
            Register schema
          </Button>
        </>
      )}
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <div className="info-panel mb-4">
          Every parameter alias becomes a required property; secrets are never in the schema.
          Registering creates a new version under{" "}
          <span className="mono">
            {application.name}/{application.release_name}
          </span>
          . Run the previewed defaults apply workflow to move the application pin.
        </div>
        {derived.notes.length > 0 ? (
          <ul className="definition-notes text-sm">
            {derived.notes.map((note) => (
              <li key={note}>{note}</li>
            ))}
          </ul>
        ) : null}
        <Field label="Schema JSON" error={jsonProblem}>
          <JsonEditor
            value={schemaJson}
            onChange={setSchemaJson}
            rows={14}
            maxHeight="50vh"
            onSubmit={() => void submit()}
          />
        </Field>
      </form>
    </Modal>
  );
}
