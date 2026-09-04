import { useRouter } from "next/router";
import { useEffect, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { JsonEditor } from "@/components/JsonEditor";
import { Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { SecretContentTypeSelect } from "@/components/secrets/SecretContentTypeSelect";
import { SecretValueField } from "@/components/secrets/SecretValueField";
import { Checkbox, Field, Input, PageHeader } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { crumbs } from "@/lib/crumbs";
import { secretValueBase64, validateSecretValue } from "@/lib/encoding";
import { datetimeLocalToUnixMs } from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors, useNamespaces, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import { validateBindingKey, validateKey, validateMetadataJson } from "@/lib/validation";

const NO_NS: NamespaceSelection = { env: "", app: "" };

/**
 * A `datetime-local` bound is compared as a literal local-time string, so it has
 * to be built from local parts — `toISOString()` would be off by the zone offset.
 */
function localDatetimeValue(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}`;
}

export default function NewSecretPage() {
  const router = useRouter();
  const toast = useToast();
  const { namespaces, error: nsError } = useNamespaces();
  const { values: queryValues, ready: queryReady } = useQueryParams(["env", "app", "key"]);

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [alreadyBase64, setAlreadyBase64] = useState(false);
  const [contentType, setContentType] = useState("text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [expires, setExpires] = useState("");

  const [bindVersion, setBindVersion] = useState(false);
  const [bindingKey, setBindingKey] = useState("");
  const [generateToken, setGenerateToken] = useState(false);

  const [saving, setSaving] = useState(false);
  const { formRef, requestFocus } = useFocusFirstInvalid();

  // A field reports its problem only once the operator has left it, so a
  // half-typed key never looks like a mistake.
  const errors = useFieldErrors<"key" | "value" | "metadata" | "expires" | "bindingKey">();

  // Mirrors of the server's rules. The value has no content-type parse rule —
  // only the size cap (and the base64 alphabet when passed through) — and no
  // message here ever quotes the value itself.
  const keyError = validateKey(key.trim());
  const valueError = validateSecretValue(value, alreadyBase64);
  const metadataError = validateMetadataJson(metadataJson);
  const expiresError =
    expires && (datetimeLocalToUnixMs(expires) ?? 0) <= Date.now()
      ? "Expiry must be in the future."
      : null;
  const bindingKeyError = bindVersion ? validateBindingKey(bindingKey) : null;
  const shownKeyError = errors.shown("key", keyError);
  const shownValueError = errors.shown("value", valueError);
  const shownMetadataError = errors.shown("metadata", metadataError);
  const shownExpiresError = errors.shown("expires", expiresError);
  const shownBindingKeyError = errors.shown("bindingKey", bindingKeyError);
  // Namespace has no field to blur, so it surfaces on submit.
  const shownAppError = errors.submitted && !ns.app ? "Choose an application." : null;
  const shownEnvError = errors.submitted && !ns.env ? "Choose an environment." : null;
  const blocked = !!(
    shownKeyError ||
    shownValueError ||
    shownMetadataError ||
    shownExpiresError ||
    shownAppError ||
    shownEnvError ||
    shownBindingKeyError
  );

  const expiresMin = useMemo(() => localDatetimeValue(Date.now()), []);

  // Shown once after creation if the server minted an access token.
  const [mintedToken, setMintedToken] = useState<string | null>(null);
  const [createdRef, setCreatedRef] = useState<{ env: string; app: string; key: string } | null>(
    null,
  );

  const seeded = useRef(false);
  useEffect(() => {
    if (!queryReady || seeded.current) return;
    seeded.current = true;
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    const requestedKey = queryValues.key ?? "";
    if (env || app) setNs({ env, app });
    if (requestedKey) setKey(requestedKey);
  }, [queryReady, queryValues]);

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load environments");
  }, [nsError, toast]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    errors.markAllTouched();
    // Every problem now has an inline message beside the field that caused it;
    // move focus there so the button never looks dead.
    if (
      !ns.env ||
      !ns.app ||
      bindingKeyError ||
      keyError ||
      valueError ||
      metadataError ||
      expiresError
    ) {
      requestFocus();
      return;
    }

    const k = key.trim();
    const expiresMs = datetimeLocalToUnixMs(expires) ?? 0;
    const requestBindingKey = bindVersion ? bindingKey : undefined;

    setSaving(true);
    // A binding key is request-local. Clear the React/DOM copy before waiting
    // for the server; a failed request intentionally requires re-entry.
    setBindingKey("");
    try {
      const res = await api.createSecret({
        env: ns.env,
        app: ns.app,
        key: k,
        value_base64: secretValueBase64(value, alreadyBase64),
        content_type: contentType.trim() || "text/plain",
        metadata_json: metadataJson.trim() || "{}",
        ...(requestBindingKey !== undefined ? { binding_key: requestBindingKey } : null),
        generate_access_token: generateToken,
        expires_at_unix_ms: expiresMs,
      });
      // Clear plaintext inputs from the DOM immediately.
      setValue("");
      const ref = { env: ns.env, app: ns.app, key: k };
      setCreatedRef(ref);

      if (res.access_token) {
        // Hold navigation until the operator saves the one-time token.
        setMintedToken(res.access_token);
        toast.success(`Secret created (version ${res.version})`, "Save the access token below.");
      } else {
        toast.success(`Secret created (version ${res.version})`, `${ns.env}/${ns.app}/${k}`);
        await router.push(links.secretDetail(ref));
      }
    } catch (err) {
      toast.error(err, "Failed to create secret");
    } finally {
      setSaving(false);
    }
  }

  function finishTokenReveal() {
    const dest = createdRef;
    setMintedToken(null);
    if (dest) void router.push(links.secretDetail(dest));
  }

  // Back and Cancel return to the list the operator came from, not the unfiltered one.
  const hasNs = !!ns.env && !!ns.app;
  const listLink = links.secrets(hasNs ? ns : undefined);

  return (
    <>
      <PageHeader
        title="New secret"
        subtitle="Encrypted at rest with authenticated encryption; never stored in plaintext."
        breadcrumbs={
          hasNs
            ? [...crumbs.environment(ns), { label: "Secrets", href: listLink }, { label: "New" }]
            : undefined
        }
      />

      <div className="card max-w-[720px]">
        <form ref={formRef} onSubmit={submit}>
          <div className="form-row">
            <NamespacePicker
              namespaces={namespaces}
              value={ns}
              onChange={setNs}
              appError={shownAppError}
              envError={shownEnvError}
            />
          </div>

          <Field
            label="Key"
            hint="Relative to the selected environment, e.g. stripe-api-key or billing/webhook-secret"
            error={shownKeyError}
          >
            <Input
              className="font-mono"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              onBlur={() => errors.touch("key")}
              placeholder="stripe-api-key"
            />
          </Field>

          <div className="form-row">
            <Field label="Content type">
              <SecretContentTypeSelect value={contentType} onValueChange={setContentType} />
            </Field>
            <Field label="Expires at" hint="Optional." error={shownExpiresError}>
              <Input
                type="datetime-local"
                min={expiresMin}
                value={expires}
                onChange={(e) => setExpires(e.target.value)}
                onBlur={() => errors.touch("expires")}
              />
            </Field>
          </div>

          <Field
            label="Value"
            hint={
              alreadyBase64
                ? "Sent as-is: standard base64, decoded by the server. Line breaks are ignored."
                : "Typed text is stored as UTF-8. Generate a random token, or tick the box below to paste base64."
            }
            error={shownValueError}
          >
            <SecretValueField
              value={value}
              onChange={setValue}
              base64={alreadyBase64}
              onBase64Change={setAlreadyBase64}
              onBlur={() => errors.touch("value")}
            />
          </Field>

          <Field label="Metadata JSON" error={shownMetadataError}>
            <JsonEditor
              toolbar="minimal"
              rows={3}
              maxHeight="30vh"
              value={metadataJson}
              onChange={setMetadataJson}
              onBlur={() => errors.touch("metadata")}
            />
          </Field>

          <hr className="divider" />

          <div className="checkbox-row mb-4">
            <Checkbox
              id="bind-version"
              checked={bindVersion}
              onCheckedChange={(checked) => {
                setBindVersion(checked);
                if (!checked) setBindingKey("");
              }}
            />
            <label htmlFor="bind-version">
              <strong>Bind this version to an application key</strong>
              <div className="faint text-sm">
                KMS will need this same binding key to decrypt the version. The key is never stored,
                hashed, or fingerprinted by KMS.
              </div>
            </label>
          </div>

          {bindVersion ? (
            <Field
              label="Binding key"
              hint="At least 32 UTF-8 bytes. Used only for this request; KMS cannot recover it later."
              error={shownBindingKeyError}
            >
              <Input
                className="font-mono"
                type="password"
                value={bindingKey}
                required
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => setBindingKey(event.target.value)}
                onBlur={() => errors.touch("bindingKey")}
                placeholder="application binding key"
              />
            </Field>
          ) : null}

          <div className="checkbox-row mb-4">
            <Checkbox
              id="generate-access-token"
              checked={generateToken}
              onCheckedChange={setGenerateToken}
            />
            <label htmlFor="generate-access-token">
              Also generate a per-secret access token (shown once after creation).
            </label>
          </div>

          <div className="form-actions">
            <ButtonLink href={listLink} variant="outline">
              Cancel
            </ButtonLink>
            <Button type="submit" loading={saving} disabled={blocked}>
              Create secret
            </Button>
          </div>
        </form>
      </div>

      {/* One-time access token reveal */}
      <Modal
        open={mintedToken !== null}
        dismissible={false}
        title="Save this access token now"
        onClose={finishTokenReveal}
        footer={<Button onClick={finishTokenReveal}>I&apos;ve saved it — continue</Button>}
      >
        <div className="danger-panel mb-4">
          <strong>This token will never be shown again.</strong> Store it in your application&apos;s
          configuration now. Access tokens and binding keys are independent credentials.
        </div>
        <div className="token-reveal">{mintedToken}</div>
        <div className="row-wrap mt-4">
          <CopyButton label="Copy token" value={() => mintedToken ?? ""} />
        </div>
      </Modal>
    </>
  );
}
