import { useEffect, useState } from "react";
import { Modal } from "@/components/Modal";
import { Button, Field, Input } from "@/components/ui";
import { api, apiFetch } from "@/lib/api";
import { isProductionEnvironment } from "@/lib/readiness";
import type { NamespaceRef, SubscriberInstance } from "@/lib/types";

export function ReleasePinDialog({
  namespace,
  name,
  schemaVersion,
  instance,
  unpin,
  onClose,
  onSaved,
}: {
  namespace: NamespaceRef;
  name: string;
  schemaVersion: number;
  instance: SubscriberInstance;
  unpin: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [version, setVersion] = useState(
    String(instance.pin_version || instance.last_applied_version || instance.release_version || ""),
  );
  const [typed, setTyped] = useState("");
  const [validated, setValidated] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [choices, setChoices] = useState<number[]>([]);
  const [guard] = useState(instance.pin_revision ?? 0);
  const stale = guard !== (instance.pin_revision ?? 0) || (!unpin && !instance.connected);
  const production = isProductionEnvironment(namespace.env);
  const target = unpin ? 0 : Number(version);
  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      let token = "";
      const versions: number[] = [];
      do {
        const page = await api.listReleases(
          namespace,
          name,
          100,
          token,
          { signal: controller.signal },
          schemaVersion,
        );
        versions.push(...page.releases.map((item) => item.release.version));
        token = page.next_page_token;
      } while (token && !controller.signal.aborted);
      if (!controller.signal.aborted) setChoices(versions);
    })().catch(() => {
      /* Exact version entry and server validation remain available. */
    });
    return () => controller.abort();
  }, [namespace, name, schemaVersion]);
  async function review() {
    setBusy(true);
    setError("");
    try {
      const result = await api.validateRelease(namespace, name, target, schemaVersion);
      if (!result.valid) {
        setError(result.errors.map((item) => item.message || item.code).join("; "));
        return;
      }
      setValidated(target);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Pin operation failed");
    } finally {
      setBusy(false);
    }
  }
  async function save() {
    setBusy(true);
    setError("");
    try {
      await apiFetch("/release-subscribers/pin", {
        method: "POST",
        body: {
          namespace,
          name,
          schema_version: schemaVersion,
          identity: instance.identity,
          client_name: instance.client_name,
          instance_id: instance.instance_id,
          session_id: instance.session_id,
          version: target,
          expected_pin_revision: guard,
        },
      });
      onSaved();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Pin operation failed");
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      title={unpin ? "Unpin instance" : instance.pin_version ? "Change pin" : "Pin to release"}
      description="This applies to this client application process. KMS restarts preserve the pin; a new client process follows the active track."
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          {!unpin && validated !== target ? (
            <Button
              onClick={() => void review()}
              disabled={busy || stale || !Number.isSafeInteger(target) || target <= 0}
            >
              Validate release
            </Button>
          ) : (
            <Button
              onClick={() => void save()}
              disabled={busy || stale || (production && typed !== namespace.env)}
            >
              {unpin ? "Unpin" : "Assign release"}
            </Button>
          )}
        </>
      }
    >
      <p>
        {instance.client_name}/{instance.instance_id} · {namespace.env}/{namespace.app} · schema{" "}
        {schemaVersion}
      </p>
      <p>
        Last applied:{" "}
        {instance.last_applied_version
          ? `schema ${schemaVersion}, v${instance.last_applied_version}`
          : "not reported"}
        . Target assignment waits for application acknowledgement.
      </p>
      {unpin ? (
        <p>
          The instance will follow this track’s active release. A failed application keeps its last
          working configuration. Without an active release, it keeps that configuration until the
          first activation.
        </p>
      ) : (
        <Field label="Release version">
          <Input
            aria-label="Release version"
            type="number"
            min={1}
            list="pin-release-versions"
            value={version}
            disabled={busy}
            onChange={(event) => {
              setVersion(event.target.value);
              setValidated(null);
            }}
          />
        </Field>
      )}
      <datalist id="pin-release-versions">
        {choices.map((value) => (
          <option key={value} value={value}>
            Schema {schemaVersion}, release {value}
          </option>
        ))}
      </datalist>
      {validated === target && !unpin ? (
        <p>
          Validated schema {schemaVersion}, release {target}. This changes only the selected
          process.
        </p>
      ) : null}
      {production ? (
        <Field label={`Type ${namespace.env} to confirm`}>
          <Input
            aria-label="Confirm environment"
            value={typed}
            disabled={busy}
            onChange={(event) => setTyped(event.target.value)}
          />
        </Field>
      ) : null}
      {stale ? (
        <p role="alert">The instance or its pin changed. Close and reopen this dialog.</p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </Modal>
  );
}
