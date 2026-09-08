import { useEffect, useId, useRef, useState } from "react";
import { Modal } from "@/components/Modal";
import { Badge, Checkbox, Field, Input, KeyValue, Spinner } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { FileInput } from "@/components/ui/file-input";
import { useToast } from "@/context/ToastContext";
import { ApiError, api } from "@/lib/api";
import type { DefaultsApplyResponse, DefaultsApplyStatus, DefaultsArtifactBody } from "@/lib/types";

const MAX_ARTIFACT_BYTES = 4 * 1024 * 1024;

const STATUS_LABEL: Record<DefaultsApplyStatus, string> = {
  create: "Create",
  unchanged: "Unchanged",
  update: "Update",
  blocked: "Blocked",
};

const STATUS_TONE: Record<DefaultsApplyStatus, "success" | "neutral" | "warning" | "danger"> = {
  create: "success",
  unchanged: "neutral",
  update: "warning",
  blocked: "danger",
};

type BusyState = "reading" | "preview" | "execute" | null;

export function ImportDefaultsModal({
  application,
  environment,
  production,
  schemaVersion = 0,
  open,
  onClose,
  onImported,
}: {
  application: string;
  environment: string;
  production: boolean;
  schemaVersion?: number;
  open: boolean;
  onClose: () => void;
  onImported: () => void | Promise<void>;
}) {
  const toast = useToast();
  const fileInputId = useId();
  const requestSequence = useRef(0);
  const chooseRef = useRef<HTMLButtonElement>(null);
  const [artifact, setArtifact] = useState<DefaultsArtifactBody | null>(null);
  const [fileName, setFileName] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [preview, setPreview] = useState<DefaultsApplyResponse | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState<BusyState>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    requestSequence.current += 1;
    setArtifact(null);
    setFileName("");
    setOverwrite(false);
    setPreview(null);
    setConfirmation("");
    setBusy(null);
    setError(null);
  }, [open]);

  async function previewArtifact(
    raw: DefaultsArtifactBody,
    allowOverwrite: boolean,
    _allowDefinitionUpdate = false,
  ) {
    const request = ++requestSequence.current;
    setBusy("preview");
    setPreview(null);
    setError(null);
    try {
      const result = await api.importApplicationDefaults({
        env: environment,
        app: application,
        schemaVersion,
        artifact: raw,
        overwrite: allowOverwrite,
        updateDefinition: false,
      });
      if (request !== requestSequence.current) return;
      setPreview(result);
    } catch (cause) {
      if (request !== requestSequence.current) return;
      setError(cause instanceof Error ? cause.message : "Could not preview this artifact.");
    } finally {
      if (request === requestSequence.current) setBusy(null);
    }
  }

  async function selectFile(file: File | undefined) {
    requestSequence.current += 1;
    setArtifact(null);
    setFileName(file?.name ?? "");
    setOverwrite(false);
    setPreview(null);
    setConfirmation("");
    setError(null);
    if (!file) return;
    if (file.size > MAX_ARTIFACT_BYTES) {
      setError("Defaults artifacts must be 4 MiB or smaller.");
      return;
    }
    setBusy("reading");
    try {
      const raw = await file.arrayBuffer();
      setArtifact(raw);
      await previewArtifact(raw, false, false);
    } catch {
      setError("The selected file could not be read.");
      setBusy(null);
    }
  }

  function changeOverwrite(checked: boolean) {
    setOverwrite(checked);
    setConfirmation("");
    if (artifact) void previewArtifact(artifact, checked);
  }

  async function execute() {
    if (
      !artifact ||
      !preview ||
      busy ||
      preview.entries.some((entry) => entry.status === "blocked")
    )
      return;
    if (production && confirmation !== environment) return;
    const request = ++requestSequence.current;
    setBusy("execute");
    setError(null);
    try {
      const result = await api.importApplicationDefaults({
        env: environment,
        app: application,
        schemaVersion,
        artifact,
        overwrite,
        updateDefinition: false,
        execute: true,
        planDigest: preview.plan_digest,
      });
      if (request !== requestSequence.current) return;
      toast.success(
        "Defaults imported",
        `${environment}/${application}: ${result.entries.filter((entry) => entry.applied_version > 0).length} value(s) written.`,
      );
      await onImported();
      onClose();
    } catch (cause) {
      if (request !== requestSequence.current) return;
      if (cause instanceof ApiError && cause.code === "aborted") {
        setPreview(null);
        setError("The import plan is stale. Preview the artifact again before importing.");
      } else {
        setError(cause instanceof Error ? cause.message : "Could not import defaults.");
      }
    } finally {
      if (request === requestSequence.current) setBusy(null);
    }
  }

  const blocked = preview?.entries.some((entry) => entry.status === "blocked") ?? false;
  const definitionReady = !preview?.definition_changed;
  const productionConfirmed = !production || confirmation === environment;
  const canExecute =
    Boolean(preview) && !blocked && definitionReady && productionConfirmed && busy === null;
  // Why Import defaults is disabled, in the order the user will resolve it.
  const blockedReason = canExecute
    ? null
    : !artifact
      ? "Select a defaults artifact to preview."
      : busy === "reading" || busy === "preview"
        ? "Previewing…"
        : busy === "execute"
          ? null
          : !preview
            ? "Preview the artifact again before importing."
            : blocked
              ? "Enable overwrite to replace the differing values."
              : !definitionReady
                ? "Use an artifact that matches this schema track's definition."
                : !productionConfirmed
                  ? `Type ${environment} to confirm the production import.`
                  : null;

  return (
    <Modal
      mobileFullScreen
      open={open}
      title={`Import defaults to ${environment}`}
      onClose={onClose}
      dismissible={busy === null}
      dirty={artifact !== null && busy === null}
      initialFocus={chooseRef}
      // wide, not workspace: the preview is five short columns, and the
      // workspace footprint opened a 1200px × full-height box around ~180px of
      // content. CloneEnvironmentModal shows a comparable table at wide.
      wide
      footer={(close) => (
        <>
          {blockedReason ? (
            <p className="footer-note" role="status">
              {blockedReason}
            </p>
          ) : null}
          <Button type="button" variant="outline" onClick={close} disabled={busy !== null}>
            Cancel
          </Button>
          {artifact && !preview ? (
            <Button
              type="button"
              variant="outline"
              onClick={() => void previewArtifact(artifact, overwrite)}
              disabled={busy !== null}
              loading={busy === "preview"}
            >
              Preview again
            </Button>
          ) : null}
          <Button
            type="button"
            onClick={() => void execute()}
            disabled={!canExecute}
            loading={busy === "execute"}
          >
            Import defaults
          </Button>
        </>
      )}
    >
      <div className="stack">
        <div className="info-panel text-sm">
          Parameters are imported into{" "}
          <span className="mono">
            {environment}/{application}
          </span>
          . Secrets, releases, schemas, applications, and environments are never created by this
          operation. The artifact must match the selected schema track's definition.
        </div>

        <Field
          label="Defaults artifact"
          htmlFor={fileInputId}
          hint={
            fileName
              ? `Selected ${fileName}. The artifact contents are not displayed.`
              : "Select a kms-config-defaults/v1 JSON file (maximum 4 MiB)."
          }
          error={error && !artifact ? error : null}
        >
          <FileInput
            id={fileInputId}
            accept="application/json,.json"
            disabled={busy !== null}
            fileName={fileName}
            buttonLabel="Choose artifact…"
            buttonRef={chooseRef}
            onFile={(file) => void selectFile(file)}
          />
        </Field>

        {busy === "reading" || busy === "preview" ? (
          <div className="loading-block" role="status">
            <Spinner />
            {busy === "reading" ? "Reading artifact…" : "Previewing import…"}
          </div>
        ) : null}

        {error && artifact ? (
          <div className="danger-panel text-sm" role="alert">
            {error}
          </div>
        ) : null}

        {preview ? (
          <section className="stack" aria-label="Defaults import preview">
            <KeyValue
              rows={[
                [
                  "Profile",
                  <span className="mono" key="profile">
                    {preview.profile}
                  </span>,
                ],
                [
                  "Schema SHA-256",
                  <span className="mono break-all" key="schema-digest">
                    {preview.schema_sha256}
                  </span>,
                ],
              ]}
            />

            {blocked ? (
              <div className="danger-panel text-sm" role="alert">
                Existing values differ. Enable overwrite to create new versions for only those
                parameters, then review the fresh preview.
              </div>
            ) : null}
            {preview.definition_changed ? (
              <div className="warn-panel text-sm">
                The imported contract or schema digest differs from the application definition.
                Use an artifact that matches the selected schema track before importing.
              </div>
            ) : null}
            {preview.missing_secrets.length > 0 ? (
              <div className="warn-panel text-sm">
                <strong>Secrets still needed:</strong>{" "}
                {preview.missing_secrets.map((name) => (
                  <span className="mono" key={name}>
                    {name}{" "}
                  </span>
                ))}
              </div>
            ) : null}

            <div className="checkbox-row">
              <Checkbox
                id="defaults-overwrite"
                checked={overwrite}
                disabled={busy !== null}
                onCheckedChange={changeOverwrite}
              />
              <label htmlFor="defaults-overwrite">
                <strong>Overwrite differing parameter values</strong>
                <span className="block faint text-sm">
                  Identical values are always skipped. Enabling this immediately creates a new
                  preview; it does not write anything.
                </span>
              </label>
            </div>

            <div className="table-wrap card-table">
              <table className="data">
                <thead>
                  <tr>
                    <th>Alias</th>
                    <th>Key</th>
                    <th>Content type</th>
                    <th>Status</th>
                    <th>Version</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.entries.map((entry) => (
                    <tr key={entry.alias}>
                      <td data-label="Alias" className="mono">
                        {entry.alias}
                      </td>
                      <td data-label="Key" className="mono">
                        {entry.key}
                      </td>
                      <td data-label="Content type">{entry.content_type}</td>
                      <td data-label="Status">
                        <Badge kind={STATUS_TONE[entry.status]}>{STATUS_LABEL[entry.status]}</Badge>
                      </td>
                      <td data-label="Version">
                        {entry.applied_version > 0
                          ? `v${entry.applied_version} · rev${entry.revision}`
                          : entry.current_version > 0
                            ? `current v${entry.current_version}`
                            : "new"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {production ? (
              <Field
                label={
                  <>
                    Type <span className="mono">{environment}</span> to confirm production import
                  </>
                }
              >
                <Input
                  className="font-mono"
                  value={confirmation}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={busy !== null}
                  onChange={(event) => setConfirmation(event.target.value)}
                />
              </Field>
            ) : null}
          </section>
        ) : null}
      </div>
    </Modal>
  );
}
