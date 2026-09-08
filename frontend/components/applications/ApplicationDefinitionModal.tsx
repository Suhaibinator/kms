import { useEffect, useId, useRef, useState } from "react";
import { Modal } from "@/components/Modal";
import { Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import type { Application } from "@/lib/types";

/** Edit application metadata without changing any immutable schema-track contract. */
export function ApplicationDefinitionModal({
  open,
  application,
  onClose,
  onSaved,
}: {
  open: boolean;
  application: Application;
  onClose: () => void;
  onSaved: (application: Application) => void;
}) {
  const toast = useToast();
  const formId = useId();
  const descriptionRef = useRef<HTMLInputElement>(null);
  const [description, setDescription] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setDescription(application.description);
    setSaving(false);
  }, [open, application]);

  const dirty = description !== application.description;

  async function submit() {
    if (saving) return;
    setSaving(true);
    try {
      // The overview may project a URL-selected schema track. Read the stored
      // definition before saving metadata so browsing another track cannot
      // silently repin the application's mutation default.
      const { application: stored } = await api.getApplication(application.name);
      const { application: updated } = await api.updateApplication({
        name: application.name,
        description,
        release_name: stored.release_name,
        schema_version: stored.schema_version,
        contract: stored.contract,
      });
      toast.success("Definition updated");
      onSaved(updated);
    } catch (error) {
      toast.error(error, "Failed to update definition");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      mobileFullScreen
      open={open}
      title={`Edit ${application.name}`}
      onClose={onClose}
      dismissible={!saving}
      dirty={dirty && !saving}
      initialFocus={descriptionRef}
      wide
      footer={(close) => (
        <>
          <Button type="button" variant="outline" onClick={close} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" loading={saving}>
            Save definition
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
        <div className="form-row">
          <Field label="Application name">
            <Input className="font-mono" value={application.name} disabled />
          </Field>
          <Field label="Release name" hint="Immutable after the application is created.">
            <Input className="font-mono" value={application.release_name} disabled />
          </Field>
        </div>
        <Field label="Description">
          <Input
            ref={descriptionRef}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <div className="info-panel mb-4 text-sm">
          Selected schema track: v{application.schema_version}
          {application.schema_version === 0 ? " · schema-free" : ""}. Saving the description
          preserves the application's stored default track and every contract.
        </div>
        <Field label="Contract" hint="This selected track's contract is read-only.">
          {application.contract.length ? (
            <ul className="text-sm">
              {application.contract.map((entry) => (
                <li key={entry.alias}>
                  <span className="mono">{entry.alias}</span> · {entry.kind}
                  {entry.content_type ? `/${entry.content_type}` : ""}
                </li>
              ))}
            </ul>
          ) : (
            <p className="faint text-sm">No aliases</p>
          )}
        </Field>
        <p className="faint text-sm">
          A track adopts its contract on its first release, defaults import, or schema upgrade.
          Established contracts are immutable. Use Manage releases to create a first release, or
          upgrade to a new schema track to change an established contract.
        </p>
      </form>
    </Modal>
  );
}
