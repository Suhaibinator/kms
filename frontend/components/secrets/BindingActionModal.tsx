import { useCallback, useEffect, useState } from "react";
import { Modal } from "@/components/Modal";
import { SensitiveValueField } from "@/components/SensitiveValueField";
import { Badge, Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, PurgeCleanupPendingApiError, type ResourceRef } from "@/lib/api";
import { countNoun, displayPath } from "@/lib/format";
import { useFieldErrors, useLatestRequest } from "@/lib/hooks";
import type { SecretBindingCohortResponse, SecretVersionSetResponse } from "@/lib/types";
import { validateBindingKey } from "@/lib/validation";
import { BINDING_ACTION_LABELS, type BindingActionKind } from "./binding-actions";

export type BindingAction =
  | { kind: BindingActionKind; version: number }
  | { kind: "purge-unbound" };

export function BindingActionModal({
  action,
  secretRef,
  onClose,
  onSaved,
}: {
  action: BindingAction | null;
  secretRef: ResourceRef;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [cohortPreview, setCohortPreview] = useState<SecretBindingCohortResponse | null>(null);
  const [unboundPreview, setUnboundPreview] = useState<SecretVersionSetResponse | null>(null);
  const [previewKey, setPreviewKey] = useState("");
  const [operationKey, setOperationKey] = useState("");
  const [newBindingKey, setNewBindingKey] = useState("");
  const [confirmNewBindingKey, setConfirmNewBindingKey] = useState("");
  const [purgeText, setPurgeText] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useLatestRequest();
  const errors = useFieldErrors<
    "previewKey" | "operationKey" | "newBindingKey" | "confirmNewBindingKey" | "purgeText"
  >();
  const { reset: resetErrors } = errors;

  const actionKey = action ? `${action.kind}:${"version" in action ? action.version : "all"}` : "";
  useEffect(() => {
    // Reading the identity is intentional: reopening a different action for
    // the same mounted page must discard the previous action's credentials.
    void actionKey;
    request.abort();
    setCohortPreview(null);
    setUnboundPreview(null);
    setPreviewKey("");
    setOperationKey("");
    setNewBindingKey("");
    setConfirmNewBindingKey("");
    setPurgeText("");
    setBusy(false);
    resetErrors();
  }, [actionKey, request, resetErrors]);

  const needsCohortPreview = action?.kind === "purge";
  const needsUnboundPreview = action?.kind === "purge-unbound";
  const needsPreview = needsCohortPreview || needsUnboundPreview;
  const preview = needsCohortPreview ? cohortPreview : unboundPreview;
  const previewKeyError =
    needsCohortPreview && cohortPreview === null ? validateBindingKey(previewKey) : null;
  const operationKeyError =
    action && action.kind !== "purge-unbound" ? validateBindingKey(operationKey) : null;
  const newBindingKeyError = action?.kind === "rotate" ? validateBindingKey(newBindingKey) : null;
  // Bind types its new key into the operation field; rotate into its own.
  // Either way the operator confirms the key KMS will not store for them.
  const keyToConfirm =
    action?.kind === "bind" ? operationKey : action?.kind === "rotate" ? newBindingKey : null;
  const confirmNewBindingKeyError =
    keyToConfirm !== null && confirmNewBindingKey !== keyToConfirm
      ? "The new binding keys do not match."
      : null;
  const purgeTextError =
    (action?.kind === "purge" || action?.kind === "purge-unbound") &&
    preview !== null &&
    purgeText !== "PURGE"
      ? "Type PURGE exactly to confirm."
      : null;

  const clearCredentials = useCallback(() => {
    setPreviewKey("");
    setOperationKey("");
    setNewBindingKey("");
    setConfirmNewBindingKey("");
    setPurgeText("");
  }, []);

  const close = useCallback(() => {
    clearCredentials();
    setCohortPreview(null);
    setUnboundPreview(null);
    onClose();
  }, [clearCredentials, onClose]);

  async function previewVersions() {
    if (!action || !needsPreview || previewKeyError) {
      errors.markAllTouched();
      return;
    }
    const key = previewKey;
    setPreviewKey("");
    setBusy(true);
    const run = request.begin();
    try {
      const result =
        action.kind === "purge"
          ? await api.previewSecretBindingCohort(secretRef, action.version, key, {
              signal: run.signal,
            })
          : await api.previewSecretUnboundVersions(secretRef, { signal: run.signal });
      if (!run.current) return;
      if (action.kind === "purge") setCohortPreview(result as SecretBindingCohortResponse);
      else setUnboundPreview(result as SecretVersionSetResponse);
      resetErrors();
    } catch (err) {
      if (!run.current) return;
      setPreviewKey(key);
      toast.error(
        err,
        action.kind === "purge"
          ? "Could not preview binding cohort"
          : "Could not preview unbound versions",
      );
    } finally {
      if (run.current) setBusy(false);
    }
  }

  async function mutate() {
    if (!action || (needsPreview && preview === null)) return;
    errors.markAllTouched();
    if (operationKeyError || newBindingKeyError || confirmNewBindingKeyError || purgeTextError) {
      return;
    }

    // Cleared for the duration of the request; put back only when it fails,
    // so a 403, a CAS abort or a dropped connection does not force retyping.
    const oldOrNewKey = operationKey;
    const replacement = newBindingKey;
    const confirmation = confirmNewBindingKey;
    const typedPurgeText = purgeText;
    const restoreCredentials = () => {
      setOperationKey(oldOrNewKey);
      setNewBindingKey(replacement);
      setConfirmNewBindingKey(confirmation);
    };
    clearCredentials();
    setBusy(true);
    const run = request.begin();
    try {
      if (action.kind === "bind") {
        const result = await api.bindSecret(secretRef, action.version, oldOrNewKey, {
          signal: run.signal,
        });
        if (!run.current) return;
        toast.success(
          `Created bound version ${result.current_version}`,
          `Version ${result.previous_version} remains unchanged. Create a new release to use the new version.`,
        );
      } else if (action.kind === "unbind") {
        const result = await api.unbindSecret(secretRef, action.version, oldOrNewKey, {
          signal: run.signal,
        });
        if (!run.current) return;
        toast.success(
          `Created unbound version ${result.current_version}`,
          `Version ${result.previous_version} remains unchanged. Create a new release to use the new version.`,
        );
      } else if (action.kind === "rotate") {
        const result = await api.rotateSecretBindingKey(
          secretRef,
          action.version,
          oldOrNewKey,
          replacement,
          { signal: run.signal },
        );
        if (!run.current) return;
        toast.success(
          `Created version ${result.current_version} with the new binding key`,
          `Version ${result.previous_version} and its historical cohort still require the old key.`,
        );
      } else if (action.kind === "purge" && cohortPreview) {
        const result = await api.purgeSecretBindingCohort(
          secretRef,
          action.version,
          oldOrNewKey,
          cohortPreview.revision,
          cohortPreview.affected_versions,
          { signal: run.signal },
        );
        if (!run.current) return;
        toast.success(
          `Purged ${result.affected_versions.length} ${countNoun(result.affected_versions.length, "versions")}`,
          "Affected versions are permanent tombstones.",
        );
      } else if (action.kind === "purge-unbound" && unboundPreview) {
        const result = await api.purgeSecretUnboundVersions(
          secretRef,
          unboundPreview.revision,
          unboundPreview.affected_versions,
          { signal: run.signal },
        );
        if (!run.current) return;
        toast.success(
          `Purged ${result.affected_versions.length} unbound ${countNoun(result.affected_versions.length, "versions")}`,
          "Affected versions are permanent tombstones; release references and labels were preserved.",
        );
      }
      onSaved();
    } catch (err) {
      if (!run.current) return;
      if (
        (action.kind === "purge" || action.kind === "purge-unbound") &&
        err instanceof PurgeCleanupPendingApiError
      ) {
        toast.info(
          "Purge committed",
          action.kind === "purge-unbound"
            ? "Database artifact cleanup is pending. Do not retry the purge; restart the service to complete cleanup."
            : "Database artifact cleanup is pending. Do not retry with the binding key; restart the service to complete cleanup.",
          { duration: 12_000 },
        );
        onSaved();
        return;
      }
      const purge = action.kind === "purge" || action.kind === "purge-unbound";
      if (err instanceof ApiError && err.code === "aborted") {
        // A purge abort returns to the preview stage, where the key field is
        // not rendered; restoring it there would leave hidden dirty state.
        if (purge) {
          if (action.kind === "purge") setCohortPreview(null);
          else setUnboundPreview(null);
        } else restoreCredentials();
        toast.error(
          err,
          purge
            ? "Version set changed — preview it again"
            : "Current version changed — reload and try again",
        );
      } else {
        // The version set is unchanged, so the typed key and confirmation still hold.
        restoreCredentials();
        setPurgeText(typedPurgeText);
        toast.error(err, `${bindingActionVerb(action.kind)} failed`);
      }
    } finally {
      if (run.current) setBusy(false);
    }
  }

  const previewStage = needsPreview && preview === null;
  const dirty =
    previewKey !== "" ||
    operationKey !== "" ||
    newBindingKey !== "" ||
    confirmNewBindingKey !== "" ||
    purgeText !== "";

  return (
    <Modal
      mobileFullScreen
      open={action !== null}
      title={action ? bindingActionTitle(action) : "Binding key"}
      description={
        action
          ? action.kind === "bind"
            ? "Clone the current version into a new bound current version. The source remains unchanged."
            : action.kind === "unbind"
              ? "Clone the current version into a new unbound current version. The source remains unchanged."
              : action.kind === "rotate"
                ? "Clone the current version under a new binding key. Historical versions keep requiring the old key."
                : action.kind === "purge-unbound"
                  ? "Preview and irreversibly purge every non-destroyed unbound version of this secret."
                  : "KMS discovers only the contiguous bound versions around this anchor that open with the same key."
          : undefined
      }
      onClose={close}
      dismissible={!busy}
      dirty={dirty && !busy}
      footer={(requestClose) => (
        <>
          <Button variant="outline" onClick={requestClose} disabled={busy}>
            Cancel
          </Button>
          {previewStage ? (
            <Button
              onClick={() => void previewVersions()}
              loading={busy}
              disabled={!!previewKeyError}
            >
              {action?.kind === "purge-unbound" ? "Preview unbound versions" : "Preview cohort"}
            </Button>
          ) : (
            <Button
              variant={
                action?.kind === "purge" || action?.kind === "purge-unbound"
                  ? "destructive-solid"
                  : "default"
              }
              onClick={() => void mutate()}
              loading={busy}
              disabled={
                !!operationKeyError ||
                !!newBindingKeyError ||
                !!confirmNewBindingKeyError ||
                !!purgeTextError
              }
            >
              {action ? bindingActionButton(action.kind) : "Continue"}
            </Button>
          )}
        </>
      )}
    >
      {action ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (previewStage) void previewVersions();
            else void mutate();
          }}
        >
          {previewStage && action.kind === "purge" ? (
            <Field
              label="Current binding key"
              hint="Used only to discover the cohort; it is cleared before the preview returns."
              error={errors.shown("previewKey", previewKeyError)}
            >
              <Input
                className="font-mono"
                type="password"
                value={previewKey}
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => setPreviewKey(event.target.value)}
                onBlur={() => errors.touch("previewKey")}
              />
            </Field>
          ) : previewStage ? (
            <div className="warn-panel">
              Preview includes every non-destroyed unbound version, including disabled and expired
              versions. KMS will require the exact revision and version set at confirmation.
            </div>
          ) : (
            <>
              {preview ? (
                <div className="danger-panel mb-4">
                  <strong>
                    {action.kind === "purge-unbound"
                      ? "Every version in this exact set will be destroyed:"
                      : "This exact cohort will be destroyed:"}
                  </strong>
                  <div
                    className="row-wrap mt-2"
                    data-testid={
                      action.kind === "purge-unbound"
                        ? "unbound-purge-versions"
                        : "binding-cohort-versions"
                    }
                  >
                    {preview.affected_versions.map((version) => (
                      <Badge key={version} kind="warning">
                        v{version}
                      </Badge>
                    ))}
                  </div>
                  <div className="faint mt-2 text-sm">
                    <span className="mono">{displayPath(secretRef)}</span>
                    {action.kind === "purge" && cohortPreview
                      ? ` · anchor v${cohortPreview.anchor_version}`
                      : ""}
                    {` · revision ${preview.revision}. KMS will abort if the revision or version set changes before confirmation.`}
                  </div>
                  <div className="mt-2">
                    Release entries and labels remain, but every affected version becomes an
                    unreadable tombstone. If current is affected, its projection is cleared. This
                    cannot be undone.
                  </div>
                </div>
              ) : null}
              {action.kind !== "purge-unbound" ? (
                <Field
                  label={action.kind === "bind" ? "New binding key" : "Current binding key"}
                  hint={
                    action.kind === "bind"
                      ? "Save this key before submitting; KMS does not store it. Cleared while the request runs."
                      : "Used only for this request and cleared while it runs."
                  }
                  error={errors.shown("operationKey", operationKeyError)}
                >
                  {action.kind === "bind" ? (
                    <SensitiveValueField
                      controlLabel="binding key"
                      placeholder="application binding key"
                      value={operationKey}
                      onChange={setOperationKey}
                      onBlur={() => errors.touch("operationKey")}
                    />
                  ) : (
                    <Input
                      className="font-mono"
                      type="password"
                      value={operationKey}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => setOperationKey(event.target.value)}
                      onBlur={() => errors.touch("operationKey")}
                    />
                  )}
                </Field>
              ) : null}
              {action.kind === "rotate" ? (
                <Field
                  label="New binding key"
                  hint="At least 32 UTF-8 bytes. Save this key before submitting; KMS does not store it. KMS creates one new bound version with fresh cryptographic material and salt."
                  error={errors.shown("newBindingKey", newBindingKeyError)}
                >
                  <SensitiveValueField
                    controlLabel="binding key"
                    placeholder="application binding key"
                    value={newBindingKey}
                    onChange={setNewBindingKey}
                    onBlur={() => errors.touch("newBindingKey")}
                  />
                </Field>
              ) : null}
              {keyToConfirm !== null ? (
                <Field
                  label="Confirm new binding key"
                  error={errors.shown("confirmNewBindingKey", confirmNewBindingKeyError)}
                >
                  <Input
                    className="font-mono"
                    type="password"
                    value={confirmNewBindingKey}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setConfirmNewBindingKey(event.target.value)}
                    onBlur={() => errors.touch("confirmNewBindingKey")}
                  />
                </Field>
              ) : null}
              {action.kind === "purge" || action.kind === "purge-unbound" ? (
                <Field
                  label={
                    <>
                      Type <span className="mono">PURGE</span> to confirm
                    </>
                  }
                  error={errors.shown("purgeText", purgeTextError)}
                >
                  <Input
                    className="font-mono"
                    value={purgeText}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setPurgeText(event.target.value)}
                    onBlur={() => errors.touch("purgeText")}
                  />
                </Field>
              ) : null}
            </>
          )}
        </form>
      ) : null}
    </Modal>
  );
}

function bindingActionVerb(kind: BindingAction["kind"]): string {
  return kind === "purge-unbound" ? "Purge unbound versions" : BINDING_ACTION_LABELS[kind];
}

function bindingActionTitle(action: BindingAction): string {
  return "version" in action
    ? `${bindingActionVerb(action.kind)} · v${action.version}`
    : bindingActionVerb(action.kind);
}

function bindingActionButton(kind: BindingAction["kind"]): string {
  return kind === "purge" || kind === "purge-unbound" ? "Purge versions" : bindingActionVerb(kind);
}
