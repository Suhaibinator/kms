import { useEffect, useId, useMemo, useState } from "react";
import { Modal } from "@/components/Modal";
import { Field, Input, Spinner } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import type { ContractEntry } from "@/lib/contract-derive";
import { useFieldErrors } from "@/lib/hooks";
import type { Application, ConfigurationReleaseEntry, EnvironmentOverview } from "@/lib/types";
import {
  firstError,
  validateContract,
  validateReleaseName,
  validateSchemaPin,
} from "@/lib/validation";
import { ContractEditor } from "./ContractEditor";

type DefinitionField = "releaseName" | "schemaPin";

/** The shape a release must match: alias, kind and (for parameters) content type. */
function shapeOf(entries: ReadonlyArray<ContractEntry | ConfigurationReleaseEntry>): string {
  return entries
    .map(
      (entry) =>
        `${entry.alias}|${entry.kind}|${entry.kind === "parameter" ? (entry.content_type ?? "") : ""}`,
    )
    .sort()
    .join("\n");
}

/** Environments whose active release no longer matches `contract`. */
export function divergingEnvironments(
  contract: readonly ContractEntry[],
  environments: readonly EnvironmentOverview[],
): EnvironmentOverview[] {
  const shape = shapeOf(contract);
  return environments.filter(
    (environment) =>
      environment.release.active && shapeOf(environment.release.active.entries) !== shape,
  );
}

/**
 * Edit definition: basics, schema pin and the structured contract in one
 * form. Warns when the contract differs from an active release's shape,
 * because the next ship must match the contract and that release no longer
 * will.
 */
export function ApplicationDefinitionModal({
  open,
  application,
  schemaJson,
  environments,
  prefillContract,
  onClose,
  onSaved,
}: {
  open: boolean;
  application: Application;
  schemaJson?: string | null;
  environments: EnvironmentOverview[];
  /** A derived contract to start from instead of the application's own. */
  prefillContract?: ContractEntry[] | null;
  onClose: () => void;
  onSaved: (application: Application) => void;
}) {
  const toast = useToast();
  const formId = useId();
  const [description, setDescription] = useState("");
  const [releaseName, setReleaseName] = useState("runtime");
  const [schemaID, setSchemaID] = useState("");
  const [schemaVersion, setSchemaVersion] = useState("");
  const [contract, setContract] = useState<ContractEntry[]>([]);
  const [saving, setSaving] = useState(false);
  const { touch, markAllTouched, reset, shown } = useFieldErrors<DefinitionField>();

  useEffect(() => {
    if (!open) return;
    setDescription(application.description);
    setReleaseName(application.release_name);
    setSchemaID(application.schema_id);
    setSchemaVersion(application.schema_version ? String(application.schema_version) : "");
    setContract((prefillContract ?? application.contract).map((entry) => ({ ...entry })));
    setSaving(false);
    reset();
  }, [open, application, prefillContract, reset]);

  const releaseNameProblem = validateReleaseName(releaseName.trim());
  const schemaPinProblem = validateSchemaPin(schemaID, schemaVersion);
  const contractProblem = validateContract(contract);
  const blocking = firstError(releaseNameProblem, schemaPinProblem, contractProblem);
  const shownSchemaPinProblem = shown("schemaPin", schemaPinProblem);
  const diverging = useMemo(
    () => divergingEnvironments(contract, environments),
    [contract, environments],
  );

  async function submit() {
    markAllTouched();
    if (saving || blocking) return;
    setSaving(true);
    try {
      const { application: updated } = await api.updateApplication({
        name: application.name,
        description,
        release_name: releaseName.trim(),
        schema_id: schemaID.trim(),
        schema_version: Number(schemaVersion || 0),
        contract,
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
      open={open}
      title={`Edit ${application.name}`}
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
            Save definition
          </Button>
        </>
      }
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
          <Field
            label="Release name"
            hint="Every environment release uses this name."
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
              aria-invalid={shownSchemaPinProblem ? true : undefined}
              onChange={(event) => setSchemaVersion(event.target.value)}
              onBlur={() => touch("schemaPin")}
            />
          </Field>
        </div>
        <Field label="Contract" hint="Aliases the application reads; secrets have no content type.">
          <ContractEditor value={contract} onChange={setContract} schemaJson={schemaJson} />
        </Field>
        {diverging.length > 0 ? (
          <div className="warn-panel text-sm" role="status">
            <strong>Differs from the active release</strong> in{" "}
            {diverging
              .map((environment) => {
                const active = environment.release.active;
                return `${environment.namespace.env} (${active?.name}@${active?.version})`;
              })
              .join(", ")}
            . New releases must match this contract; ship one there after saving.
          </div>
        ) : null}
      </form>
    </Modal>
  );
}
