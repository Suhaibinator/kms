import Link from "next/link";
import { useEffect, useId, useRef, useState } from "react";
import { Modal } from "@/components/Modal";
import { Field, Input, Textarea } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useFocusFirstInvalid } from "@/lib/forms";
import { useFieldErrors } from "@/lib/hooks";
import { firstError, validateKey, validateValueSize } from "@/lib/validation";
import type { QuickSecretSeed } from "./shared";

type QuickSecretField = "environment" | "key" | "value";

export function QuickSecretModal({
  app,
  environments,
  seed,
  saving,
  onClose,
  onSave,
}: {
  app: string;
  environments: string[];
  seed: QuickSecretSeed | null;
  saving: boolean;
  onClose: () => void;
  onSave: (request: {
    environment: string;
    key: string;
    value: string;
    contentType: string;
  }) => Promise<void>;
}) {
  const [environment, setEnvironment] = useState("");
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [contentType, setContentType] = useState("text/plain");
  // What the seed opened with, so `dirty` tracks the user's own edits only.
  const [seeded, setSeeded] = useState({ environment: "", key: "" });
  const { touch, markAllTouched, reset, shown } = useFieldErrors<QuickSecretField>();
  const { formRef, requestFocus } = useFocusFirstInvalid();
  const environmentRef = useRef<HTMLButtonElement>(null);
  const keyRef = useRef<HTMLInputElement>(null);
  const valueRef = useRef<HTMLTextAreaElement>(null);
  const formId = useId();

  useEffect(() => {
    if (!seed) return;
    const initialEnvironment =
      seed.environment || (environments.length === 1 ? environments[0] : "");
    setEnvironment(initialEnvironment);
    setKey(seed.key);
    setValue("");
    setContentType("text/plain");
    setSeeded({ environment: initialEnvironment, key: seed.key });
    reset();
  }, [seed, environments, reset]);

  const environmentProblem = environment ? null : "Choose an environment.";
  const keyProblem = validateKey(key.trim());
  const valueProblem = value ? validateValueSize(value) : "Secret value is required.";
  const blocking = firstError(environmentProblem, keyProblem, valueProblem);
  const dirty = value !== "" || key !== seeded.key || environment !== seeded.environment;
  // The first control the user still has to fill.
  const initialFocus = !seeded.environment ? environmentRef : !seeded.key ? keyRef : valueRef;
  const advancedHref = {
    pathname: "/secrets/new",
    query: {
      ...(environment ? { env: environment } : {}),
      app,
      ...(key.trim() ? { key: key.trim() } : {}),
    },
  };

  function submit() {
    markAllTouched();
    if (saving) return;
    if (blocking) {
      requestFocus();
      return;
    }
    void onSave({
      environment,
      key: key.trim(),
      value,
      // The server defaults a blank content type; the form does not second-guess it.
      contentType: contentType.trim() || "text/plain",
    });
  }

  return (
    <Modal
      open={seed !== null}
      title="New secret"
      onClose={onClose}
      dismissible={!saving}
      dirty={dirty && !saving}
      initialFocus={initialFocus}
      footer={(close) => (
        <>
          <Button type="button" variant="outline" onClick={close} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" loading={saving}>
            Create secret
          </Button>
        </>
      )}
    >
      <form
        id={formId}
        ref={formRef}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="form-row">
          <Field label="Application">
            <Input className="font-mono" value={app} disabled />
          </Field>
          <Field label="Environment" error={shown("environment", environmentProblem)}>
            <AppSelect
              ref={environmentRef}
              className="font-mono"
              value={environment}
              onValueChange={setEnvironment}
              onBlur={() => touch("environment")}
              placeholder="Select environment…"
              options={environments.map((item) => ({ value: item, label: item }))}
            />
          </Field>
        </div>
        <Field
          label="Secret key"
          hint="Examples: stripe-api-key or billing/webhook-secret"
          error={shown("key", keyProblem)}
        >
          <Input
            ref={keyRef}
            className="font-mono"
            value={key}
            onChange={(event) => setKey(event.target.value)}
            onBlur={() => touch("key")}
            placeholder="stripe-api-key"
          />
        </Field>
        <Field
          label="Secret value"
          hint="The value is encrypted before it is stored and is never shown in the matrix."
          error={shown("value", valueProblem)}
        >
          <Textarea
            ref={valueRef}
            className="font-mono"
            rows={5}
            value={value}
            onChange={(event) => setValue(event.target.value)}
            onBlur={() => touch("value")}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <Field label="Content type" hint="Defaults to text/plain when left blank.">
          <Input
            className="font-mono"
            value={contentType}
            onChange={(event) => setContentType(event.target.value)}
          />
        </Field>
        <div className="quick-secret-advanced text-sm">
          Need expiration, metadata, an access token, or client-bound protection?{" "}
          <Link href={advancedHref}>Open advanced secret options</Link>.
        </div>
      </form>
    </Modal>
  );
}
