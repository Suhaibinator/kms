import { useEffect, useId, useMemo, useRef, useState } from "react";
import { JsonEditor } from "@/components/JsonEditor";
import { Modal } from "@/components/Modal";
import { Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { deriveSchemaFromContract } from "@/lib/contract-derive";
import type { Application } from "@/lib/types";

/** The default registry id for an application's schema. */
export function defaultSchemaId(application: Pick<Application, "name" | "release_name">): string {
  return `${application.name}-${application.release_name}`;
}

/**
 * Derives a JSON Schema from the contract, registers it and pins the
 * application to the new version. The schema is registered first so a failed
 * pin leaves a usable schema behind, never a dangling pin.
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
  const schemaIdRef = useRef<HTMLInputElement>(null);
  const derived = useMemo(
    () => deriveSchemaFromContract(application.contract, existingSchemaJson),
    [application.contract, existingSchemaJson],
  );
  const [schemaId, setSchemaId] = useState("");
  const [schemaJson, setSchemaJson] = useState("");
  const [opened, setOpened] = useState({ schemaId: "", schemaJson: "" });
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!open) return;
    const initial = {
      schemaId: application.schema_id || defaultSchemaId(application),
      schemaJson: derived.schemaJson,
    };
    setSchemaId(initial.schemaId);
    setSchemaJson(initial.schemaJson);
    setOpened(initial);
    setSaving(false);
  }, [open, application, derived.schemaJson]);

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
  const idProblem = schemaId.trim() ? null : "Schema ID is required.";
  const blocking = idProblem ?? jsonProblem;
  const dirty = schemaId !== opened.schemaId || schemaJson !== opened.schemaJson;

  async function submit() {
    if (saving || blocking) return;
    setSaving(true);
    try {
      const { schema } = await api.createSchema(schemaId.trim(), schemaJson);
      const { application: updated } = await api.updateApplication({
        name: application.name,
        description: application.description,
        release_name: application.release_name,
        schema_id: schema.id,
        schema_version: schema.version,
        contract: application.contract,
      });
      toast.success("Schema registered", `${schema.id}@${schema.version} is pinned.`);
      onPinned(updated);
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
      initialFocus={schemaIdRef}
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
            Register and pin
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
          Registering creates a new schema version and pins the application to it.
        </div>
        {derived.notes.length > 0 ? (
          <ul className="definition-notes text-sm">
            {derived.notes.map((note) => (
              <li key={note}>{note}</li>
            ))}
          </ul>
        ) : null}
        <Field label="Schema ID" error={idProblem}>
          <Input
            ref={schemaIdRef}
            className="font-mono"
            value={schemaId}
            onChange={(event) => setSchemaId(event.target.value)}
          />
        </Field>
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
