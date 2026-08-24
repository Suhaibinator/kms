import { Plus } from "lucide-react";
import { useEffect, useId, useState } from "react";
import type { CloneEnvironmentModalProps } from "@/components/applications/contracts";
import { ConfirmDialog, Modal } from "@/components/Modal";
import { Badge, Checkbox, Field, Input, Spinner } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { useToast } from "@/context/ToastContext";
import { api } from "@/lib/api";
import { useFieldErrors } from "@/lib/hooks";
import { isProductionEnvironment } from "@/lib/readiness";
import type { CloneEnvironmentItem, CloneEnvironmentResponse } from "@/lib/types";
import { validateEnv } from "@/lib/validation";
import type { CloneSeed } from "./shared";

export type { CloneEnvironmentModalProps };

const ACTION_LABEL: Record<CloneEnvironmentItem["action"], string> = {
  copied: "Copied",
  needs_value: "Needs a value",
  exists: "Already existed",
  missing_in_source: "Missing in source",
  error: "Failed",
};

const ACTION_TONE: Record<
  CloneEnvironmentItem["action"],
  "success" | "warning" | "neutral" | "danger"
> = {
  copied: "success",
  needs_value: "warning",
  exists: "neutral",
  missing_in_source: "warning",
  error: "danger",
};

/**
 * Create (or attach) an environment by copying another environment's
 * parameter values. Secret values are never copied: they come back as
 * `needs_value` with an Add secret button each. A production target asks for
 * its name to be typed before anything is written.
 */
export default function CloneEnvironmentModal({
  application,
  environments,
  open,
  onClose,
  onCreated,
  seed,
  onAddSecret,
}: CloneEnvironmentModalProps & {
  /** Prefill from the Add-environment form's "Copy values from…" choice. */
  seed?: CloneSeed | null;
  /** Add secret for a `needs_value` item; the caller closes this modal. */
  onAddSecret?: (env: string, alias: string) => void;
}) {
  const toast = useToast();
  const formId = useId();
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const [description, setDescription] = useState("");
  const [token, setToken] = useState(false);
  const [copyValues, setCopyValues] = useState(true);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<CloneEnvironmentResponse | null>(null);
  const { touch, markAllTouched, reset, shown } = useFieldErrors<"target">();

  useEffect(() => {
    if (!open) return;
    setSource(seed?.source ?? environments[0]?.namespace.env ?? "");
    setTarget(seed?.target ?? "");
    setDescription(seed?.description ?? "");
    setToken(seed?.methods.includes("token") ?? false);
    setCopyValues(true);
    setConfirming(false);
    setBusy(false);
    setResult(null);
    reset();
  }, [open, seed, environments, reset]);

  const targetProblem =
    validateEnv(target.trim()) ??
    (environments.some((environment) => environment.namespace.env === target.trim())
      ? `${target.trim()} already exists. Choose a new environment name.`
      : null);
  const sourceProblem = source ? null : "Choose a source environment.";
  const blocking = sourceProblem ?? targetProblem;
  const production = isProductionEnvironment(target.trim());

  function submit() {
    markAllTouched();
    if (busy || blocking) return;
    if (production) setConfirming(true);
    else void run();
  }

  async function run() {
    setConfirming(false);
    setBusy(true);
    try {
      const response = await api.cloneEnvironment({
        application: application.name,
        source_env: source,
        target_env: target.trim(),
        copy_values: copyValues,
        auth_methods: token ? ["mtls", "token"] : ["mtls"],
        description,
      });
      setResult(response);
      toast.success(
        response.namespace_created ? "Environment created" : "Environment attached",
        `${target.trim()}/${application.name}: ${response.items.filter((item) => item.action === "copied").length} value(s) copied.`,
      );
    } catch (error) {
      toast.error(error, "Failed to clone environment");
    } finally {
      setBusy(false);
    }
  }

  const sourceOptions = environments.map((environment) => ({
    value: environment.namespace.env,
    label: environment.namespace.env,
  }));

  return (
    <>
      <Modal
        open={open}
        title={result ? `${result.namespace.env} created from ${source}` : "Copy an environment"}
        onClose={onClose}
        dismissible={!busy}
        wide
        footer={
          result ? (
            <Button type="button" onClick={() => onCreated(result)}>
              Done
            </Button>
          ) : (
            <>
              <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
                Cancel
              </Button>
              <Button form={formId} type="submit" disabled={busy || blocking !== null}>
                {busy ? <Spinner /> : null}
                {production ? "Create production environment…" : "Create environment"}
              </Button>
            </>
          )
        }
      >
        {result ? (
          <>
            {result.needs_value.length > 0 ? (
              <div className="warn-panel mb-4 text-sm">
                Secret values are never copied.{" "}
                {result.needs_value.map((alias) => `\`${alias}\``).join(", ")}{" "}
                {result.needs_value.length === 1 ? "needs" : "need"} a value in{" "}
                <span className="mono">{result.namespace.env}</span> before a release can be
                shipped.
              </div>
            ) : (
              <div className="info-panel mb-4 text-sm">Every contract alias has a value.</div>
            )}
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Alias</th>
                    <th>Kind</th>
                    <th>Result</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {result.items.map((item) => (
                    <tr key={`${item.kind}:${item.alias}`}>
                      <td className="mono">{item.alias}</td>
                      <td>{item.kind}</td>
                      <td>
                        <Badge kind={ACTION_TONE[item.action]}>{ACTION_LABEL[item.action]}</Badge>{" "}
                        {item.action === "copied" && item.target_version ? (
                          <span className="faint text-sm">
                            v{item.source_version} → v{item.target_version}
                          </span>
                        ) : null}
                        {item.action === "exists" && item.target_version ? (
                          <span className="faint text-sm">kept v{item.target_version}</span>
                        ) : null}
                        {item.error ? (
                          <span className="text-danger text-sm">{item.error}</span>
                        ) : null}
                      </td>
                      <td>
                        {item.action === "needs_value" && onAddSecret ? (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            onClick={() => onAddSecret(result.namespace.env, item.alias)}
                          >
                            <Plus size={13} />
                            Add secret
                          </Button>
                        ) : null}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        ) : (
          <form
            id={formId}
            onSubmit={(event) => {
              event.preventDefault();
              submit();
            }}
          >
            <div className="info-panel mb-4 text-sm">
              Parameter values are copied as new versions in the target; existing target keys are
              kept. Secrets are listed as needing a value.
            </div>
            <div className="form-row">
              <Field label="Copy values from" error={shown("target", sourceProblem)}>
                <AppSelect
                  className="font-mono"
                  value={source}
                  onValueChange={setSource}
                  options={sourceOptions}
                  placeholder="Select environment…"
                />
              </Field>
              <Field
                label="New environment"
                hint="Examples: staging, prod, prod-gcp"
                error={shown("target", targetProblem)}
              >
                <Input
                  className="font-mono"
                  value={target}
                  onChange={(event) => setTarget(event.target.value)}
                  onBlur={() => touch("target")}
                  placeholder="prod"
                />
              </Field>
            </div>
            <Field label="Description">
              <Input value={description} onChange={(event) => setDescription(event.target.value)} />
            </Field>
            <div className="checkbox-row">
              <Checkbox
                id="clone-copy-values"
                checked={copyValues}
                onCheckedChange={setCopyValues}
              />
              <label htmlFor="clone-copy-values">
                <strong>Copy parameter values</strong>
                <span className="faint text-sm">Off creates the namespace only.</span>
              </label>
            </div>
            <div className="checkbox-row">
              <Checkbox id="clone-allow-token" checked={token} onCheckedChange={setToken} />
              <label htmlFor="clone-allow-token">
                <strong>Also allow bearer tokens</strong>
                <span className="faint text-sm">mTLS is always enabled and recommended.</span>
              </label>
            </div>
            {production ? (
              <div className="warn-panel mt-4 text-sm">
                <span className="mono">{target.trim()}</span> is a production environment. You will
                be asked to type its name.
              </div>
            ) : null}
          </form>
        )}
      </Modal>
      <ConfirmDialog
        open={confirming}
        title={`Create ${target.trim()}`}
        message={
          <>
            Copy values from <span className="mono">{source}</span> into the production environment{" "}
            <span className="mono">{target.trim()}</span>?
          </>
        }
        confirmLabel="Create environment"
        requireText={target.trim()}
        busy={busy}
        onConfirm={() => void run()}
        onCancel={() => setConfirming(false)}
      />
    </>
  );
}
