import Link from "next/link";
import { useRouter } from "next/router";
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { useToast } from "@/context/ToastContext";
import { useNamespaces, useQueryParams } from "@/lib/hooks";
import { datetimeLocalToUnixMs } from "@/lib/format";
import { utf8ToBase64 } from "@/lib/encoding";
import { Field, PageHeader, Spinner } from "@/components/ui";
import { Modal } from "@/components/Modal";
import NamespacePicker, { type NamespaceSelection } from "@/components/NamespacePicker";
import CopyButton from "@/components/CopyButton";

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
  const { values: queryValues, ready: queryReady } = useQueryParams(["env", "app"]);

  const [ns, setNs] = useState<NamespaceSelection>(NO_NS);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [expires, setExpires] = useState("");

  const [clientBound, setClientBound] = useState(false);
  const [ack, setAck] = useState(false);
  const [generateToken, setGenerateToken] = useState(false);
  const [providedToken, setProvidedToken] = useState("");

  const [saving, setSaving] = useState(false);

  // Shown once after creation if the server minted an access token.
  const [mintedToken, setMintedToken] = useState<string | null>(null);
  const [createdRef, setCreatedRef] = useState<{ env: string; app: string; key: string } | null>(null);

  const seeded = useRef(false);
  useEffect(() => {
    if (!queryReady || seeded.current) return;
    seeded.current = true;
    const env = queryValues.env ?? "";
    const app = queryValues.app ?? "";
    if (env || app) setNs({ env, app });
  }, [queryReady, queryValues]);

  useEffect(() => {
    if (nsError) toast.error(nsError, "Failed to load namespaces");
  }, [nsError, toast]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ns.env || !ns.app) {
      toast.error(new Error("Choose a namespace for the secret."), "Missing namespace");
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
      if (!generateToken && !providedToken.trim()) {
        toast.error(
          new Error("Client-bound secrets need an access token: generate one, or supply your own."),
          "Access token required",
        );
        return;
      }
    }

    const expiresMs = datetimeLocalToUnixMs(expires) ?? 0;
    const secretToken = clientBound && !generateToken ? providedToken.trim() : undefined;

    setSaving(true);
    try {
      const res = await api.createSecret(
        {
          env: ns.env,
          app: ns.app,
          key: k,
          value_base64: utf8ToBase64(value),
          content_type: contentType.trim() || "text/plain",
          metadata_json: metadataJson.trim() || "{}",
          client_bound: clientBound,
          generate_access_token: generateToken,
          expires_at_unix_ms: expiresMs,
        },
        secretToken,
      );
      // Clear plaintext inputs from the DOM immediately.
      setValue("");
      setProvidedToken("");
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

          <Field label="Key" hint="Relative to the namespace, e.g. stripe-api-key or billing/webhook-secret">
            <input
              className="input mono"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder="stripe-api-key"
              autoFocus
            />
          </Field>

          <Field
            label="Value"
            hint="Encrypted at rest with authenticated encryption. Never stored in plaintext."
          >
            <textarea
              className="textarea mono"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="secret value…"
              autoComplete="off"
              spellCheck={false}
            />
          </Field>

          <div className="form-row">
            <Field label="Content type">
              <input
                className="input"
                value={contentType}
                onChange={(e) => setContentType(e.target.value)}
              />
            </Field>
            <Field label="Expires at" hint="Optional.">
              <input
                className="input"
                type="datetime-local"
                value={expires}
                onChange={(e) => setExpires(e.target.value)}
              />
            </Field>
          </div>

          <Field label="Metadata JSON">
            <input
              className="input mono"
              value={metadataJson}
              onChange={(e) => setMetadataJson(e.target.value)}
            />
          </Field>

          <hr className="divider" />

          <div className="checkbox-row mb-16">
            <input
              id="client-bound"
              type="checkbox"
              checked={clientBound}
              onChange={(e) => {
                setClientBound(e.target.checked);
                if (!e.target.checked) {
                  setAck(false);
                  setGenerateToken(false);
                  setProvidedToken("");
                }
              }}
            />
            <label htmlFor="client-bound">
              <strong>Client-bound encryption</strong>
              <div className="faint text-sm">
                The value is additionally wrapped with a key derived from a client
                access token. The server alone cannot decrypt it.
              </div>
            </label>
          </div>

          {clientBound ? (
            <>
              <div className="danger-panel mb-16">
                <strong>Permanent-loss warning.</strong> There is no recovery escrow.
                Losing <em>either</em> the server master key <em>or</em> this secret&apos;s
                client access token makes the value <strong>permanently unrecoverable</strong>.
                Frontend reveal, CLI plaintext output, and admin export are all impossible
                for client-bound secrets. Opting in is an explicit acceptance of this risk.
              </div>

              <div className="checkbox-row mb-16">
                <input id="ack" type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} />
                <label htmlFor="ack">
                  I understand that loss of the master key or the client access token
                  destroys this secret with no recovery path.
                </label>
              </div>

              <div className="checkbox-row mb-16">
                <input
                  id="gen-token"
                  type="checkbox"
                  checked={generateToken}
                  onChange={(e) => setGenerateToken(e.target.checked)}
                />
                <label htmlFor="gen-token">
                  Generate a new access token for me (shown once after creation).
                </label>
              </div>

              {!generateToken ? (
                <Field
                  label="Client access token"
                  hint="Required when not generating one. Sent once to derive the encryption key; never stored server-side in recoverable form."
                >
                  <input
                    className="input mono"
                    type="password"
                    value={providedToken}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(e) => setProvidedToken(e.target.value)}
                    placeholder="existing client access token"
                  />
                </Field>
              ) : null}
            </>
          ) : (
            <div className="checkbox-row mb-16">
              <input
                id="gen-token-std"
                type="checkbox"
                checked={generateToken}
                onChange={(e) => setGenerateToken(e.target.checked)}
              />
              <label htmlFor="gen-token-std">
                Also generate a per-secret access token (shown once after creation).
              </label>
            </div>
          )}

          <div className="form-actions">
            <button type="submit" className="btn btn-primary" disabled={saving}>
              {saving ? <Spinner /> : null}
              Create secret
            </button>
            <Link href="/secrets" className="btn">
              Cancel
            </Link>
          </div>
        </form>
      </div>

      {/* One-time access token reveal */}
      <Modal
        open={mintedToken !== null}
        title="Save this access token now"
        onClose={finishTokenReveal}
        footer={
          <button className="btn btn-primary" onClick={finishTokenReveal}>
            I&apos;ve saved it — continue
          </button>
        }
      >
        <div className="danger-panel mb-16">
          <strong>This token will never be shown again.</strong> Store it in your
          application&apos;s configuration now. If this is a client-bound secret, losing
          this token means the value can never be recovered.
        </div>
        <div className="token-reveal">{mintedToken}</div>
        <div className="row-wrap mt-16">
          <CopyButton label="Copy token" value={() => mintedToken ?? ""} />
        </div>
      </Modal>
    </>
  );
}
