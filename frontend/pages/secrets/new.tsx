import Link from "next/link";
import { useRouter } from "next/router";
import { useEffect, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { Checkbox, Field, Input, PageHeader, Spinner, Textarea } from "@/components/ui";
import { Button, ButtonLink } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { utf8ToBase64 } from "@/lib/encoding";
import { datetimeLocalToUnixMs } from "@/lib/format";
import { useFieldErrors, useNamespaces, useQueryParams } from "@/lib/hooks";
import { links } from "@/lib/links";
import { validateKey, validateMetadataJson, validateValueSize } from "@/lib/validation";

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
  const [contentType, setContentType] = useState("text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [expires, setExpires] = useState("");

  const [clientBound, setClientBound] = useState(false);
  const [ack, setAck] = useState(false);
  const [generateToken, setGenerateToken] = useState(false);

  const [saving, setSaving] = useState(false);

  // A field reports its problem only once the operator has left it, so a
  // half-typed key never looks like a mistake.
  const errors = useFieldErrors<"key" | "value" | "metadata" | "expires">();

  // Mirrors of the server's rules. The value has no content-type parse rule —
  // only the size cap — and no message here ever quotes the value itself.
  const keyError = validateKey(key.trim());
  // validateValueSize accepts "" (it only caps the size), so the required rule
  // lives here.
  const valueError = value ? validateValueSize(value) : "A secret value is required.";
  const metadataError = validateMetadataJson(metadataJson);
  const expiresError =
    expires && (datetimeLocalToUnixMs(expires) ?? 0) <= Date.now()
      ? "Expiry must be in the future."
      : null;
  const shownKeyError = errors.shown("key", keyError);
  const shownValueError = errors.shown("value", valueError);
  const shownMetadataError = errors.shown("metadata", metadataError);
  const shownExpiresError = errors.shown("expires", expiresError);
  // Namespace and acknowledgement have no field to blur, so they surface on submit.
  const shownAppError = errors.submitted && !ns.app ? "Choose an application." : null;
  const shownEnvError = errors.submitted && !ns.env ? "Choose an environment." : null;
  const shownAckError =
    errors.submitted && clientBound && !ack
      ? "You must acknowledge the permanent-loss semantics for client-bound secrets."
      : null;
  const blocked = !!(
    shownKeyError ||
    shownValueError ||
    shownMetadataError ||
    shownExpiresError ||
    shownAppError ||
    shownEnvError ||
    shownAckError
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
    // Every problem now has an inline message beside the field that caused it.
    if (!ns.env || !ns.app) return;
    if (clientBound && !ack) return;
    if (keyError || valueError || metadataError || expiresError) return;

    const k = key.trim();
    const expiresMs = datetimeLocalToUnixMs(expires) ?? 0;

    setSaving(true);
    try {
      const res = await api.createSecret({
        env: ns.env,
        app: ns.app,
        key: k,
        value_base64: utf8ToBase64(value),
        content_type: contentType.trim() || "text/plain",
        metadata_json: metadataJson.trim() || "{}",
        client_bound: clientBound,
        // The server must mint the key share for every new client-bound secret.
        generate_access_token: clientBound || generateToken,
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
  const listLink = links.secrets(ns.env && ns.app ? ns : undefined);

  return (
    <>
      <PageHeader
        title="New secret"
        subtitle={
          <Link href={listLink} className="text-sm">
            ← All secrets
          </Link>
        }
      />

      <div className="card" style={{ maxWidth: 720 }}>
        <form onSubmit={submit}>
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

          <Field
            label="Value"
            hint="Encrypted at rest with authenticated encryption. Never stored in plaintext."
            error={shownValueError}
          >
            <Textarea
              className="font-mono"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              onBlur={() => errors.touch("value")}
              placeholder="secret value…"
              autoComplete="off"
              spellCheck={false}
            />
          </Field>

          <div className="form-row">
            <Field label="Content type">
              <Input value={contentType} onChange={(e) => setContentType(e.target.value)} />
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

          <Field label="Metadata JSON" error={shownMetadataError}>
            <Input
              className="font-mono"
              value={metadataJson}
              onChange={(e) => setMetadataJson(e.target.value)}
              onBlur={() => errors.touch("metadata")}
            />
          </Field>

          <hr className="divider" />

          <div className="checkbox-row mb-4">
            <Checkbox
              id="client-bound"
              checked={clientBound}
              onCheckedChange={(checked) => {
                setClientBound(checked);
                if (!checked) {
                  setAck(false);
                  setGenerateToken(false);
                }
              }}
            />
            <label htmlFor="client-bound">
              <strong>Client-bound encryption</strong>
              <div className="faint text-sm">
                The value is additionally wrapped with a key derived from a client access token. The
                server alone cannot decrypt it.
              </div>
            </label>
          </div>

          {clientBound ? (
            <>
              <div className="danger-panel mb-4">
                <strong>Permanent-loss warning.</strong> There is no recovery escrow. Losing{" "}
                <em>either</em> the server master key <em>or</em> this secret&apos;s client access
                token makes the value <strong>permanently unrecoverable</strong>. Frontend reveal,
                CLI plaintext output, and admin export are all impossible for client-bound secrets.
                Opting in is an explicit acceptance of this risk.
              </div>

              <div className="mb-4">
                <div className="checkbox-row">
                  <Checkbox id="ack" checked={ack} onCheckedChange={setAck} />
                  <label htmlFor="ack">
                    I understand that loss of the master key or the client access token destroys
                    this secret with no recovery path.
                  </label>
                </div>
                {shownAckError ? (
                  <div className="field-error" role="alert">
                    {shownAckError}
                  </div>
                ) : null}
              </div>

              <div className="info-panel mb-4">
                A new client access token will be generated and shown once after creation. Store it
                immediately; the server cannot recover it later.
              </div>
            </>
          ) : (
            <div className="checkbox-row mb-4">
              <Checkbox
                id="gen-token-std"
                checked={generateToken}
                onCheckedChange={setGenerateToken}
              />
              <label htmlFor="gen-token-std">
                Also generate a per-secret access token (shown once after creation).
              </label>
            </div>
          )}

          <div className="form-actions">
            <Button type="submit" disabled={saving || blocked}>
              {saving ? <Spinner /> : null}
              Create secret
            </Button>
            <ButtonLink href={listLink} variant="outline">
              Cancel
            </ButtonLink>
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
          configuration now. If this is a client-bound secret, losing this token means the value can
          never be recovered.
        </div>
        <div className="token-reveal">{mintedToken}</div>
        <div className="row-wrap mt-4">
          <CopyButton label="Copy token" value={() => mintedToken ?? ""} />
        </div>
      </Modal>
    </>
  );
}
