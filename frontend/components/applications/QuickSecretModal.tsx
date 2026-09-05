import { useEffect, useId, useMemo, useRef, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { JsonEditor } from "@/components/JsonEditor";
import { Modal } from "@/components/Modal";
import { SecretContentTypeSelect } from "@/components/secrets/SecretContentTypeSelect";
import { SecretValueField } from "@/components/secrets/SecretValueField";
import { Checkbox, Field, Input } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import type { ResourceRef } from "@/lib/api";
import { secretValueBase64, validateSecretValue } from "@/lib/encoding";
import { datetimeLocalToUnixMs, isEmptyJson } from "@/lib/format";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors } from "@/lib/hooks";
import type { CreateSecretResponse } from "@/lib/types";
import {
  firstError,
  validateBindingKey,
  validateKey,
  validateMetadataJson,
} from "@/lib/validation";
import type { QuickSecretSeed } from "./shared";

type QuickSecretField = "environment" | "key" | "value" | "metadata" | "expires" | "bindingKey";

function localDatetimeValue(ms: number): string {
  const date = new Date(ms);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`;
}

export interface QuickSecretRequest {
  environment: string;
  key: string;
  valueBase64: string;
  contentType: string;
  metadataJson: string;
  expiresAtUnixMs: number;
  bindingKey?: string;
  generateAccessToken: boolean;
}

export function QuickSecretModal({
  app,
  environments,
  seed,
  saving,
  onClose,
  onSave,
  onCreated,
}: {
  app: string;
  environments: string[];
  seed: QuickSecretSeed | null;
  saving: boolean;
  onClose: () => void;
  onSave: (request: QuickSecretRequest) => Promise<CreateSecretResponse>;
  onCreated: (ref: ResourceRef) => void;
}) {
  const { identity } = useAuth();
  const identityBoundary = identity ? `${identity.kind}\u0000${identity.name}` : "";
  const seedOpen = seed !== null;
  const seedEnvironment = seed?.environment ?? "";
  const seedKey = seed?.key ?? "";
  const [environment, setEnvironment] = useState("");
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [alreadyBase64, setAlreadyBase64] = useState(false);
  const [contentType, setContentType] = useState("text/plain");
  const [metadataJson, setMetadataJson] = useState("{}");
  const [expires, setExpires] = useState("");
  const [bindVersion, setBindVersion] = useState(false);
  const [bindingKey, setBindingKey] = useState("");
  const [generateToken, setGenerateToken] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [mintedToken, setMintedToken] = useState<string | null>(null);
  const [createdRef, setCreatedRef] = useState<ResourceRef | null>(null);
  const [seeded, setSeeded] = useState({ environment: "", key: "" });
  const errors = useFieldErrors<QuickSecretField>();
  const { formRef, requestFocus } = useFocusFirstInvalid();
  const environmentRef = useRef<HTMLButtonElement>(null);
  const keyRef = useRef<HTMLInputElement>(null);
  const valueRef = useRef<HTMLElement | null>(null);
  const formInstance = useRef(0);
  const formId = useId();
  const bindId = `${formId}-bind`;
  const tokenId = `${formId}-token`;
  const expiresMin = useMemo(() => localDatetimeValue(Date.now()), []);

  useEffect(() => {
    void identityBoundary;
    formInstance.current += 1;
    if (!seedOpen) {
      setValue("");
      setBindingKey("");
      setMintedToken(null);
      setCreatedRef(null);
      return;
    }
    const initialEnvironment =
      seedEnvironment || (environments.length === 1 ? environments[0] : "");
    setEnvironment(initialEnvironment);
    setKey(seedKey);
    setValue("");
    setAlreadyBase64(false);
    setContentType("text/plain");
    setMetadataJson("{}");
    setExpires("");
    setBindVersion(false);
    setBindingKey("");
    setGenerateToken(false);
    setAdvancedOpen(false);
    setMintedToken(null);
    setCreatedRef(null);
    setSeeded({ environment: initialEnvironment, key: seedKey });
    errors.reset();
    return () => {
      // Ignore a create response if this form was closed, replaced, or
      // unmounted while the write was in flight.
      formInstance.current += 1;
    };
  }, [seedOpen, seedEnvironment, seedKey, identityBoundary, environments, errors.reset]);

  const environmentProblem = environment ? null : "Choose an environment.";
  const keyProblem = validateKey(key.trim());
  const valueProblem = validateSecretValue(value, alreadyBase64);
  const metadataProblem = validateMetadataJson(metadataJson);
  const expiresProblem =
    expires && (datetimeLocalToUnixMs(expires) ?? 0) <= Date.now()
      ? "Expiry must be in the future."
      : null;
  const bindingKeyProblem = bindVersion ? validateBindingKey(bindingKey) : null;
  const shownEnvironmentProblem = errors.shown("environment", environmentProblem);
  const shownKeyProblem = errors.shown("key", keyProblem);
  const shownValueProblem = errors.shown("value", valueProblem);
  const shownMetadataProblem = errors.shown("metadata", metadataProblem);
  const shownExpiresProblem = errors.shown("expires", expiresProblem);
  const shownBindingKeyProblem = errors.shown("bindingKey", bindingKeyProblem);
  const blocking = firstError(
    environmentProblem,
    keyProblem,
    valueProblem,
    metadataProblem,
    expiresProblem,
    bindingKeyProblem,
  );
  const blocked = Boolean(
    firstError(
      shownEnvironmentProblem,
      shownKeyProblem,
      shownValueProblem,
      shownMetadataProblem,
      shownExpiresProblem,
      shownBindingKeyProblem,
    ),
  );
  const advancedHasError = Boolean(
    shownMetadataProblem || shownExpiresProblem || shownBindingKeyProblem,
  );
  const dirty =
    value !== "" ||
    key !== seeded.key ||
    environment !== seeded.environment ||
    alreadyBase64 ||
    contentType !== "text/plain" ||
    !isEmptyJson(metadataJson) ||
    expires !== "" ||
    bindVersion ||
    bindingKey !== "" ||
    generateToken;
  const initialFocus = !seeded.environment ? environmentRef : !seeded.key ? keyRef : valueRef;

  async function submit() {
    errors.markAllTouched();
    if (saving) return;
    if (blocking) {
      if (metadataProblem || expiresProblem || bindingKeyProblem) setAdvancedOpen(true);
      requestFocus();
      return;
    }

    const ref = { env: environment, app, key: key.trim() };
    const submittedForm = formInstance.current;
    const requestBindingKey = bindVersion ? bindingKey : undefined;
    setBindingKey("");
    let response: CreateSecretResponse;
    try {
      response = await onSave({
        environment,
        key: ref.key,
        valueBase64: secretValueBase64(value, alreadyBase64),
        contentType: contentType.trim() || "text/plain",
        metadataJson: metadataJson.trim() || "{}",
        expiresAtUnixMs: datetimeLocalToUnixMs(expires) ?? 0,
        ...(requestBindingKey !== undefined ? { bindingKey: requestBindingKey } : null),
        generateAccessToken: generateToken,
      });
    } catch {
      return;
    }
    if (formInstance.current !== submittedForm) return;
    setValue("");
    setCreatedRef(ref);
    if (response.access_token) setMintedToken(response.access_token);
    else onCreated(ref);
  }

  function finishTokenReveal() {
    const ref = createdRef;
    setMintedToken(null);
    setCreatedRef(null);
    if (ref) onCreated(ref);
  }

  return (
    <Modal
      open={seedOpen}
      wide
      title={mintedToken ? "Save this access token now" : "New secret"}
      description={mintedToken ? undefined : "Create the value without leaving this application."}
      onClose={mintedToken ? () => undefined : onClose}
      dismissible={!saving && mintedToken === null}
      dirty={dirty && !saving && mintedToken === null}
      initialFocus={mintedToken ? undefined : initialFocus}
      footer={
        mintedToken ? (
          <Button onClick={finishTokenReveal}>I&apos;ve saved it — manage secret</Button>
        ) : (
          (close) => (
            <>
              <Button type="button" variant="outline" onClick={close} disabled={saving}>
                Cancel
              </Button>
              <Button form={formId} type="submit" loading={saving} disabled={blocked}>
                Create secret
              </Button>
            </>
          )
        )
      }
    >
      {mintedToken ? (
        <>
          <div className="danger-panel mb-4">
            <strong>This token will never be shown again.</strong> Store it in the application
            configuration now. Access tokens and binding keys are independent credentials.
          </div>
          <div className="token-reveal">{mintedToken}</div>
          <div className="row-wrap mt-4">
            <CopyButton label="Copy token" value={() => mintedToken} />
          </div>
        </>
      ) : (
        <form
          id={formId}
          ref={formRef}
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <div className="form-row">
            <Field label="Application">
              <Input className="font-mono" value={app} disabled />
            </Field>
            <Field label="Environment" error={shownEnvironmentProblem}>
              <AppSelect
                ref={environmentRef}
                className="font-mono"
                value={environment}
                onValueChange={setEnvironment}
                onBlur={() => errors.touch("environment")}
                placeholder="Select environment…"
                options={environments.map((item) => ({ value: item, label: item }))}
              />
            </Field>
          </div>
          <Field
            label="Secret key"
            hint="Examples: stripe-api-key or billing/webhook-secret"
            error={shownKeyProblem}
          >
            <Input
              ref={keyRef}
              className="font-mono"
              value={key}
              onChange={(event) => setKey(event.target.value)}
              onBlur={() => errors.touch("key")}
              placeholder="stripe-api-key"
            />
          </Field>
          <div className="form-row secret-write-primary">
            <Field label="Content type">
              <SecretContentTypeSelect value={contentType} onValueChange={setContentType} />
            </Field>
          </div>
          <Field
            label="Secret value"
            hint={
              alreadyBase64
                ? "Sent as standard base64 and decoded by the server."
                : "Stored encrypted. Generate a random value or paste text."
            }
            error={shownValueProblem}
          >
            <SecretValueField
              value={value}
              onChange={setValue}
              base64={alreadyBase64}
              onBase64Change={setAlreadyBase64}
              inputRef={valueRef}
              onBlur={() => errors.touch("value")}
            />
          </Field>

          <details
            className="advanced-panel advanced-panel-modal"
            open={advancedOpen}
            onToggle={(event) => setAdvancedOpen(event.currentTarget.open || advancedHasError)}
          >
            <summary>Advanced options</summary>
            <div className="advanced-panel-content">
              <Field label="Expires at" hint="Optional." error={shownExpiresProblem}>
                <Input
                  type="datetime-local"
                  min={expiresMin}
                  value={expires}
                  onChange={(event) => setExpires(event.target.value)}
                  onBlur={() => {
                    errors.touch("expires");
                    if (expiresProblem) setAdvancedOpen(true);
                  }}
                />
              </Field>
              <Field label="Metadata JSON" error={shownMetadataProblem}>
                <JsonEditor
                  toolbar="minimal"
                  rows={3}
                  maxHeight="30vh"
                  value={metadataJson}
                  onChange={setMetadataJson}
                  onBlur={() => {
                    errors.touch("metadata");
                    if (metadataProblem) setAdvancedOpen(true);
                  }}
                  onSubmit={() => void submit()}
                />
              </Field>
              <div className="checkbox-row">
                <Checkbox
                  id={bindId}
                  checked={bindVersion}
                  onCheckedChange={(checked) => {
                    setBindVersion(checked);
                    if (!checked) setBindingKey("");
                  }}
                />
                <label htmlFor={bindId}>
                  <strong>Bind this version to an application key</strong>
                  <div className="faint text-sm">
                    KMS needs the same binding key to decrypt this version and never stores it.
                  </div>
                </label>
              </div>
              {bindVersion ? (
                <Field
                  label="Binding key"
                  hint="At least 32 UTF-8 bytes. Used only for this request."
                  error={shownBindingKeyProblem}
                >
                  <Input
                    className="font-mono"
                    type="password"
                    value={bindingKey}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setBindingKey(event.target.value)}
                    onBlur={() => {
                      errors.touch("bindingKey");
                      if (bindingKeyProblem) setAdvancedOpen(true);
                    }}
                  />
                </Field>
              ) : null}
              <div className="checkbox-row">
                <Checkbox id={tokenId} checked={generateToken} onCheckedChange={setGenerateToken} />
                <label htmlFor={tokenId}>
                  Generate a per-secret access token (shown once after creation).
                </label>
              </div>
            </div>
          </details>
        </form>
      )}
    </Modal>
  );
}
