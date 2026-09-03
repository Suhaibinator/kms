import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Modal } from "@/components/Modal";
import { Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import type { ContractEntry } from "@/lib/contract-derive";
import type { Application, ConfigurationReleaseEntry, EnvironmentOverview } from "@/lib/types";
import { validateContract } from "@/lib/validation";
import { ContractEditor } from "./ContractEditor";

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
 * Edit the mutable description and structured contract. The application and
 * release names are immutable ownership coordinates, and schema repinning is
 * performed by the previewed defaults workflow. Warns when the contract differs from an active release's shape,
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
  const descriptionRef = useRef<HTMLInputElement>(null);
  const [description, setDescription] = useState("");
  const [contract, setContract] = useState<ContractEntry[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setDescription(application.description);
    setContract((prefillContract ?? application.contract).map((entry) => ({ ...entry })));
    setSaving(false);
  }, [open, application, prefillContract]);

  const contractProblem = validateContract(contract);
  const blocking = contractProblem;
  const diverging = useMemo(
    () => divergingEnvironments(contract, environments),
    [contract, environments],
  );
  const dirty =
    description !== application.description ||
    JSON.stringify(contract) !== JSON.stringify(application.contract);

  async function submit() {
    if (saving) return;
    if (blocking) return;
    setSaving(true);
    try {
      const { application: updated } = await api.updateApplication({
        name: application.name,
        description,
        release_name: application.release_name,
        schema_version: application.schema_version,
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
          Schema pin:{" "}
          {application.schema_version ? (
            <span className="mono">
              {application.name}/{application.release_name}@{application.schema_version}
            </span>
          ) : (
            "not pinned"
          )}
          . Apply defaults with definition updates to change this pin.
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
