import Link from "next/link";
import { useEffect, useId, useRef, useState } from "react";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { Badge, Checkbox, Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { useFieldErrors } from "@/lib/hooks";
import {
  identitiesRelyingOn,
  identitiesWithUnknownPolicyImpact,
  methodLabel,
} from "@/lib/identity-methods";
import { links } from "@/lib/links";
import type { AuthMethod, Identity, Namespace } from "@/lib/types";

/** How many identity pages the impact check walks before giving up (200 per page). */
const MAX_IDENTITY_PAGES = 10;

type IdentityScanStatus = "loading" | "complete" | "incomplete" | "error";

export function sameMethods(a: readonly AuthMethod[], b: readonly AuthMethod[]): boolean {
  return a.length === b.length && a.every((method) => b.includes(method));
}

/** The auth methods a namespace accepts, as chips. `none` is a danger state. */
export function AuthMethodBadges({ methods }: { methods: AuthMethod[] }) {
  if (!methods || methods.length === 0) {
    return <Badge kind="danger">none</Badge>;
  }
  return (
    <div className="row-wrap">
      {methods.includes("mtls") ? <Badge kind="accent">mTLS</Badge> : null}
      {methods.includes("token") ? <Badge kind="neutral">token</Badge> : null}
    </div>
  );
}

function AuthMethodsField({
  methods,
  onChange,
  error,
}: {
  methods: AuthMethod[];
  onChange: (next: AuthMethod[]) => void;
  error?: string | null;
}) {
  function toggle(method: AuthMethod, on: boolean) {
    const set = new Set(methods);
    if (on) set.add(method);
    else set.delete(method);
    onChange([...set]);
  }
  return (
    <Field
      label="Allowed auth methods"
      hint="mTLS is the strongest posture. Adding token permits bearer-token clients into this namespace."
      error={error}
    >
      <div className="checkbox-row">
        <Checkbox
          id="method-mtls"
          checked={methods.includes("mtls")}
          onCheckedChange={(checked) => toggle("mtls", checked)}
        />
        <label htmlFor="method-mtls">
          <strong>mTLS</strong>
          <div className="faint text-sm">Client certificates from the built-in CA.</div>
        </label>
      </div>
      <div className="checkbox-row">
        <Checkbox
          id="method-token"
          checked={methods.includes("token")}
          onCheckedChange={(checked) => toggle("token", checked)}
        />
        <label htmlFor="method-token">
          <strong>Token</strong>
          <div className="faint text-sm">
            Bearer tokens. Possession-free — anyone holding the string is the app.
          </div>
        </label>
      </div>
    </Field>
  );
}

/**
 * Description and allowed auth methods for one namespace. Removing a method is
 * a fleet-affecting change, so the editor scans the identities bound anywhere
 * and confirms which of them stop authenticating; a partial or failed scan is
 * reported as unknown impact rather than as no impact.
 *
 * Shared by the namespaces list and the environment page.
 */
export function NamespaceSettingsModal({
  namespace,
  onClose,
  onSaved,
}: {
  /** The namespace being edited; `null` closes the modal. */
  namespace: Namespace | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [description, setDescription] = useState("");
  const [methods, setMethods] = useState<AuthMethod[]>([]);
  const [saving, setSaving] = useState(false);
  const errors = useFieldErrors<"methods">();
  // The Save button sits in the modal footer, outside the form element; the
  // HTML `form` attribute is what lets Enter in the body submit it.
  const formId = useId();
  const descriptionRef = useRef<HTMLInputElement>(null);
  // Identities bound anywhere, loaded when the editor opens so removing an
  // auth method can say which of them it breaks. Keep completeness separate:
  // partial or failed scans must never be interpreted as zero impact.
  const [identities, setIdentities] = useState<Identity[]>([]);
  const [identityScanStatus, setIdentityScanStatus] = useState<IdentityScanStatus>("loading");
  const [identityScanRetry, setIdentityScanRetry] = useState(0);
  const identitiesRun = useRef(0);
  const [confirmRemoval, setConfirmRemoval] = useState(false);

  const reset = errors.reset;
  useEffect(() => {
    if (!namespace) return;
    setDescription(namespace.description);
    setMethods(namespace.allowed_auth_methods ?? []);
    setConfirmRemoval(false);
    reset();
  }, [namespace, reset]);

  const methodsError = methods.length === 0 ? "Select at least one allowed auth method." : null;
  const shownMethodsError = errors.shown("methods", methodsError);
  const dirty =
    namespace !== null &&
    (description !== namespace.description ||
      !sameMethods(methods, namespace.allowed_auth_methods ?? []));

  useEffect(() => {
    if (!namespace) return;
    // Reading the retry generation makes an explicit retry start a fresh scan.
    void identityScanRetry;
    const controller = new AbortController();
    const run = ++identitiesRun.current;
    setIdentities([]);
    setIdentityScanStatus("loading");
    void (async () => {
      try {
        const all: Identity[] = [];
        let token: string | undefined;
        let complete = false;
        for (let page = 0; page < MAX_IDENTITY_PAGES; page += 1) {
          const res = await api.listIdentities(200, token, { signal: controller.signal });
          all.push(...(res.identities ?? []));
          token = res.next_page_token || undefined;
          if (!token) {
            complete = true;
            break;
          }
        }
        if (run !== identitiesRun.current) return;
        setIdentities(all);
        setIdentityScanStatus(complete ? "complete" : "incomplete");
      } catch {
        if (run === identitiesRun.current) setIdentityScanStatus("error");
      }
    })();
    return () => {
      identitiesRun.current += 1;
      controller.abort();
    };
  }, [namespace, identityScanRetry]);

  const removedMethods = namespace
    ? (namespace.allowed_auth_methods ?? []).filter((method) => !methods.includes(method))
    : [];
  const affected = namespace
    ? removedMethods
        .map((method) => ({
          method,
          identities: identitiesRelyingOn(identities, namespace, method),
        }))
        .filter((entry) => entry.identities.length > 0)
    : [];
  const affectedNames = [
    ...new Set(affected.flatMap((entry) => entry.identities.map((i) => i.name))),
  ];
  const affectedCount = affectedNames.length;
  const unknownPolicyImpact = namespace
    ? removedMethods.some(
        (method) => identitiesWithUnknownPolicyImpact(identities, namespace, method).length > 0,
      )
    : false;

  function onSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (!namespace) return;
    errors.markAllTouched();
    if (methodsError) return;
    // Removing a method identities rely on is a fleet-affecting change: confirm first.
    if (
      removedMethods.length > 0 &&
      (identityScanStatus !== "complete" || affectedCount > 0 || unknownPolicyImpact)
    ) {
      setConfirmRemoval(true);
      return;
    }
    void save();
  }

  async function save() {
    if (!namespace) return;
    setSaving(true);
    try {
      await api.updateNamespace({
        env: namespace.env,
        app: namespace.app,
        description: description.trim(),
        allowed_auth_methods: methods,
      });
      toast.success("Namespace updated", `${namespace.env}/${namespace.app}`);
      setConfirmRemoval(false);
      onSaved();
    } catch (err) {
      toast.error(err, "Failed to update namespace");
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <Modal
        mobileFullScreen
        open={namespace !== null}
        title={namespace ? `Edit ${namespace.env}/${namespace.app}` : "Edit namespace"}
        onClose={onClose}
        dismissible={!saving}
        dirty={dirty}
        initialFocus={descriptionRef}
        footer={(close) => (
          <>
            {methodsError ? (
              <p className="footer-note" role="status">
                {methodsError}
              </p>
            ) : null}
            <Button type="button" variant="outline" onClick={close} disabled={saving}>
              Cancel
            </Button>
            <Button form={formId} type="submit" disabled={methodsError !== null} loading={saving}>
              Save changes
            </Button>
          </>
        )}
      >
        {namespace ? (
          <form id={formId} onSubmit={onSubmit}>
            <Field label="Namespace">
              {/* readOnly, not disabled: this is a display of what is being
                  edited, so it has to stay legible and selectable. The muted
                  ground is what says it cannot be typed in. */}
              <Input
                className="font-mono bg-muted"
                value={`${namespace.env}/${namespace.app}`}
                readOnly
              />
            </Field>
            <Field label="Description" hint="Optional.">
              <Input
                ref={descriptionRef}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </Field>
            <AuthMethodsField
              methods={methods}
              onChange={(next) => {
                setMethods(next);
                errors.touch("methods");
              }}
              error={shownMethodsError}
            />
            {removedMethods.length > 0 && identityScanStatus === "loading" ? (
              <p className="faint text-sm" role="status">
                Checking identities… You can continue, but impact is unknown until this finishes.
              </p>
            ) : null}
            {removedMethods.length > 0 && identityScanStatus === "error" ? (
              <div className="warn-panel text-sm" role="status">
                Could not check which identities rely on the removed method. Impact is unknown.{" "}
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setIdentityScanRetry((value) => value + 1)}
                >
                  Retry identity check
                </Button>
              </div>
            ) : null}
            {removedMethods.length > 0 && identityScanStatus === "incomplete" ? (
              <div className="warn-panel text-sm" role="status">
                Checked the first 2,000 identities, but more exist. The impact list is incomplete.{" "}
                <Link href={links.identities({ env: namespace.env, app: namespace.app })}>
                  Review bound identities
                </Link>
                .
              </div>
            ) : null}
            {affected.map((entry) => (
              <div key={entry.method} className="warn-panel text-sm" role="status">
                <strong>
                  Removing {methodLabel(entry.method)} authentication breaks{" "}
                  {entry.identities.length}{" "}
                  {entry.identities.length === 1 ? "identity" : "identities"}:
                </strong>{" "}
                <span className="mono">{entry.identities.map((i) => i.name).join(", ")}</span>.{" "}
                {entry.identities.length === 1 ? "It stops" : "They stop"} authenticating on the
                next RPC.
              </div>
            ))}
            {removedMethods.length > 0 &&
            identityScanStatus === "complete" &&
            unknownPolicyImpact ? (
              <div className="warn-panel text-sm" role="status">
                Active unbound or differently bound clients also hold this credential type. Their
                policies are not included in the identity list, so additional impact is unknown.
              </div>
            ) : null}
          </form>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={confirmRemoval}
        title="Remove authentication method?"
        danger
        message={
          identityScanStatus === "complete" && !unknownPolicyImpact ? (
            <>
              Saving removes {removedMethods.map(methodLabel).join(" and ")} authentication from{" "}
              <span className="mono">{namespace ? `${namespace.env}/${namespace.app}` : ""}</span>.{" "}
              {affectedCount} {affectedCount === 1 ? "identity stops" : "identities stop"}{" "}
              authenticating on the next RPC:{" "}
              <span className="mono">{affectedNames.join(", ")}</span>.
            </>
          ) : (
            <>
              Saving removes {removedMethods.map(methodLabel).join(" and ")} authentication from{" "}
              <span className="mono">{namespace ? `${namespace.env}/${namespace.app}` : ""}</span>.{" "}
              {identityScanStatus === "complete" && unknownPolicyImpact
                ? "Active unbound or differently bound clients may have policy-granted access, so the number of credentials this disables is unknown."
                : identityScanStatus === "loading"
                  ? "The identity check is still running, so the number of credentials this disables is unknown."
                  : identityScanStatus === "incomplete"
                    ? "More than 2,000 identities exist, so the number of credentials this disables is unknown."
                    : "The identity check failed, so the number of credentials this disables is unknown."}
            </>
          )
        }
        confirmLabel={
          identityScanStatus === "complete" && !unknownPolicyImpact
            ? `Save and break ${affectedCount} ${affectedCount === 1 ? "identity" : "identities"}`
            : "Save with unknown impact"
        }
        busy={saving}
        onConfirm={() => void save()}
        onCancel={() => setConfirmRemoval(false)}
      />
    </>
  );
}
