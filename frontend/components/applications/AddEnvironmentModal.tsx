import { useEffect, useId, useState } from "react";
import { Modal } from "@/components/Modal";
import { Checkbox, Field, Input, Spinner } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useFieldErrors } from "@/lib/hooks";
import { isProductionEnvironment } from "@/lib/readiness";
import { validateEnv } from "@/lib/validation";
import type { CloneSeed } from "./shared";

const EMPTY = "__empty__";

export function AddEnvironmentModal({
  app,
  environments = [],
  open,
  saving,
  onClose,
  onSave,
  onClone,
}: {
  app: string;
  /** Existing environment names, offered under "Start from". */
  environments?: string[];
  open: boolean;
  saving: boolean;
  onClose: () => void;
  onSave: (
    environment: string,
    description: string,
    methods: ("mtls" | "token")[],
  ) => Promise<void>;
  /** "Copy values from <env>" hands over to the clone flow instead of saving. */
  onClone?: (seed: CloneSeed) => void;
}) {
  const [environment, setEnvironment] = useState("");
  const [description, setDescription] = useState("");
  const [token, setToken] = useState(false);
  const [startFrom, setStartFrom] = useState(EMPTY);
  const { touch, markAllTouched, reset, shown } = useFieldErrors<"environment">();
  const formId = useId();
  // An environment is the env half of a namespace, so it follows the label rule.
  const environmentProblem = validateEnv(environment.trim());
  const production = isProductionEnvironment(environment.trim());
  useEffect(() => {
    if (!open) return;
    setEnvironment("");
    setDescription("");
    setToken(false);
    setStartFrom(EMPTY);
    reset();
  }, [open, reset]);

  function submit() {
    markAllTouched();
    if (saving || environmentProblem) return;
    const methods: ("mtls" | "token")[] = token ? ["mtls", "token"] : ["mtls"];
    if (startFrom !== EMPTY && onClone) {
      onClone({ source: startFrom, target: environment.trim(), description, methods });
      return;
    }
    void onSave(environment, description, methods);
  }

  return (
    <Modal
      open={open}
      title={`Add environment to ${app}`}
      onClose={onClose}
      dismissible={!saving}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button form={formId} type="submit" disabled={saving || environmentProblem !== null}>
            {saving ? <Spinner /> : null}
            {startFrom !== EMPTY ? "Continue" : "Add environment"}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <Field
          label="Environment"
          hint="Examples: dev, staging, prod, prod-gcp"
          error={shown("environment", environmentProblem)}
        >
          <Input
            className="font-mono"
            value={environment}
            onChange={(event) => setEnvironment(event.target.value)}
            onBlur={() => touch("environment")}
            placeholder="prod-gcp"
          />
        </Field>
        {production ? (
          <div className="warn-panel mb-4 text-sm">
            <span className="mono">{environment.trim()}</span> is a production environment: shipping
            and rolling back there ask you to type its name.
          </div>
        ) : null}
        <Field label="Description">
          <Input value={description} onChange={(event) => setDescription(event.target.value)} />
        </Field>
        {environments.length > 0 && onClone ? (
          <Field
            label="Start from"
            hint="Copying writes new parameter versions in the new environment; secrets are never copied."
          >
            <AppSelect
              value={startFrom}
              onValueChange={(next) => setStartFrom(next || EMPTY)}
              options={[
                { value: EMPTY, label: "Empty" },
                ...environments.map((env) => ({ value: env, label: `Copy values from ${env}` })),
              ]}
            />
          </Field>
        ) : null}
        <div className="checkbox-row">
          <Checkbox id="allow-environment-token" checked={token} onCheckedChange={setToken} />
          <label htmlFor="allow-environment-token">
            <strong>Also allow bearer tokens</strong>
            <span className="faint text-sm">mTLS is always enabled and recommended.</span>
          </label>
        </div>
      </form>
    </Modal>
  );
}
