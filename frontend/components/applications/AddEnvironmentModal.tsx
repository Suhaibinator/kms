import { useEffect, useId, useState } from "react";
import { Modal } from "@/components/Modal";
import { Checkbox, Field, Input, Spinner } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useFieldErrors } from "@/lib/hooks";
import { validateEnv } from "@/lib/validation";

export function AddEnvironmentModal({
  app,
  open,
  saving,
  onClose,
  onSave,
}: {
  app: string;
  open: boolean;
  saving: boolean;
  onClose: () => void;
  onSave: (
    environment: string,
    description: string,
    methods: ("mtls" | "token")[],
  ) => Promise<void>;
}) {
  const [environment, setEnvironment] = useState("");
  const [description, setDescription] = useState("");
  const [token, setToken] = useState(false);
  const { touch, markAllTouched, reset, shown } = useFieldErrors<"environment">();
  const formId = useId();
  // An environment is the env half of a namespace, so it follows the label rule.
  const environmentProblem = validateEnv(environment.trim());
  useEffect(() => {
    if (!open) return;
    setEnvironment("");
    setDescription("");
    setToken(false);
    reset();
  }, [open, reset]);

  function submit() {
    markAllTouched();
    if (saving || environmentProblem) return;
    void onSave(environment, description, token ? ["mtls", "token"] : ["mtls"]);
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
            {saving ? <Spinner /> : null}Add environment
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
        <Field label="Description">
          <Input value={description} onChange={(event) => setDescription(event.target.value)} />
        </Field>
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
