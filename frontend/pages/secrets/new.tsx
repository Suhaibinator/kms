import { Button, ButtonLink } from "@/components/ui/button";
import Link from "next/link";
import { useRouter } from "next/router";
import { useEffect, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import { Checkbox, Field, Input, PageHeader, Spinner, Textarea } from "@/components/ui";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { utf8ToBase64 } from "@/lib/encoding";
import { datetimeLocalToUnixMs } from "@/lib/format";
import { useNamespaces, useQueryParams } from "@/lib/hooks";
import { validateKey, validateMetadataJson, validateValueSize } from "@/lib/validation";

const NO_NS: NamespaceSelection = { env: "", app: "" };

function detailLink(ref: { env: string; app: string; key: string }): string {
  return `/secrets/detail?env=${encodeURIComponent(ref.env)}&app=${encodeURIComponent(
    ref.app,
  )}&key=${encodeURIComponent(ref.key)}`;
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
  const [touched, setTouched] = useState({ key: false, value: false, metadata: false });
  function touch(field: keyof typeof touched) {
    setTouched((t) => ({ ...t, [field]: true }));
  }

  // Mirrors of the server's rules. The value has no content-type parse rule —
  // only the size cap — and no message here ever quotes the value itself.
  const keyError = validateKey(key.trim());
  const valueError = validateValueSize(value);
  const metadataError = validateMetadataJson(metadataJson);
  const shownKeyError = touched.key ? keyError : null;
  const shownValueError = touched.value ? valueError : null;
  const shownMetadataError = touched.metadata ? metadataError : null;
  const blocked = !!(shownKeyError || shownValueError || shownMetadataError);

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
    setTouched({ key: true, value: true, metadata: true });
    if (!ns.env || !ns.app) {
      toast.error(new Error("Choose an environment for the secret."), "Missing environment");
      return;
    }
    const k = key.trim();
    if (!k) {
      toast.error(new Error("A secret key is required."), "Missing key");
      return;
    }
    if (!value) {
      toast.error(new Error("A secret value is required."), "Missing value");
      return;
    }
    if (clientBound) {
      if (!ack) {
        toast.error(
          new Error("You must acknowledge the permanent-loss semantics for client-bound secrets."),
          "Acknowledgment required",
        );
        return;
      }
    }
    // Inline messages carry the detail; the fields are now all touched.
    if (keyError || valueError || metadataError) return;

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
        await router.push(detailLink(ref));
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
    if (dest) void router.push(detailLink(dest));
  }

  return (
    <>
      <PageHeader
        title="New secret"
        subtitle={
          <Link href="/secrets" className="text-sm">
            ← All secrets
          </Link>
        }
      />

      <div className="card" style={{ maxWidth: 720 }}>
        <form onSubmit={submit}>
          <div className="form-row">
            <NamespacePicker namespaces={namespaces} value={ns} onChange={setNs} />
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
              onBlur={() => touch("key")}
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
              onBlur={() => touch("value")}
              placeholder="secret value…"
              autoComplete="off"
              spellCheck={false}
            />
          </Field>

          <div className="form-row">
            <Field label="Content type">
              <Input value={contentType} onChange={(e) => setContentType(e.target.value)} />
            </Field>
            <Field label="Expires at" hint="Optional.">
              <Input
                type="datetime-local"
                value={expires}
                onChange={(e) => setExpires(e.target.value)}
              />
            </Field>
          </div>

          <Field label="Metadata JSON" error={shownMetadataError}>
            <Input
              className="font-mono"
              value={metadataJson}
              onChange={(e) => setMetadataJson(e.target.value)}
              onBlur={() => touch("metadata")}
            />
          </Field>

          <hr className="divider" />

          <div className="checkbox-row mb-16">
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
              <div className="danger-panel mb-16">
                <strong>Permanent-loss warning.</strong> There is no recovery escrow. Losing{" "}
                <em>either</em> the server master key <em>or</em> this secret&apos;s client access
                token makes the value <strong>permanently unrecoverable</strong>. Frontend reveal,
                CLI plaintext output, and admin export are all impossible for client-bound secrets.
                Opting in is an explicit acceptance of this risk.
              </div>

              <div className="checkbox-row mb-16">
                <Checkbox id="ack" checked={ack} onCheckedChange={setAck} />
                <label htmlFor="ack">
                  I understand that loss of the master key or the client access token destroys this
                  secret with no recovery path.
                </label>
              </div>

              <div className="info-panel mb-16">
                A new client access token will be generated and shown once after creation. Store it
                immediately; the server cannot recover it later.
              </div>
            </>
          ) : (
            <div className="checkbox-row mb-16">
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
            <ButtonLink href="/secrets" variant="outline">
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
        <div className="danger-panel mb-16">
          <strong>This token will never be shown again.</strong> Store it in your application&apos;s
          configuration now. If this is a client-bound secret, losing this token means the value can
          never be recovered.
        </div>
        <div className="token-reveal">{mintedToken}</div>
        <div className="row-wrap mt-16">
          <CopyButton label="Copy token" value={() => mintedToken ?? ""} />
        </div>
      </Modal>
    </>
  );
}
