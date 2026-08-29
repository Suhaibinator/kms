import Link from "next/link";
import { useCallback, useEffect, useId, useState } from "react";
import type { RollbackDialogProps } from "@/components/applications/contracts";
import { Ident, ReleaseIdent } from "@/components/Ident";
import { Modal } from "@/components/Modal";
import { entryHrefResolver, ViolationTable } from "@/components/releases/ViolationTable";
import { Badge, Button, Field, Input, Spinner } from "@/components/ui";
import { ApiError, api, isAbortError, isConflict } from "@/lib/api";
import { links } from "@/lib/links";
import { isProductionEnvironment } from "@/lib/readiness";
import type { OverviewActiveRelease, ReleaseValidationError } from "@/lib/types";

export type { RollbackDialogProps };

type Target = Pick<OverviewActiveRelease, "version" | "previous_version">;

type Check =
  | { kind: "loading" }
  | { kind: "none" }
  | { kind: "valid" }
  | { kind: "invalid"; violations: ReleaseValidationError[] }
  | { kind: "blocked"; message: string }
  | { kind: "error"; message: string };

type Outcome =
  | { kind: "idle" }
  | { kind: "busy" }
  | { kind: "moved"; message: string }
  | { kind: "already" }
  | { kind: "error"; message: string };

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Re-activates the previous release with a compare-and-swap on the active
 * version. The previous release is validated as soon as the dialog opens — a
 * disabled secret or an edited contract can make it unactivatable — and
 * Confirm stays disabled until it validates. Production asks for the
 * environment name.
 */
export default function RollbackDialog({
  namespace,
  name,
  active,
  open,
  onClose,
  onDone,
}: RollbackDialogProps) {
  const [target, setTarget] = useState<Target | null>(null);
  const [check, setCheck] = useState<Check>({ kind: "loading" });
  const [outcome, setOutcome] = useState<Outcome>({ kind: "idle" });
  const [typed, setTyped] = useState("");
  const formId = useId();
  const production = isProductionEnvironment(namespace.env);
  const busy = outcome.kind === "busy";

  const validate = useCallback(
    async (candidate: Target | null, signal: AbortSignal) => {
      if (!candidate || candidate.previous_version <= 0) {
        setCheck({ kind: "none" });
        return;
      }
      setCheck({ kind: "loading" });
      try {
        const result = await api.validateRelease(namespace, name, candidate.previous_version);
        if (signal.aborted) return;
        if (result.valid) setCheck({ kind: "valid" });
        else if (result.errors.length > 0) setCheck({ kind: "invalid", violations: result.errors });
        else setCheck({ kind: "blocked", message: "The release did not validate." });
      } catch (error) {
        if (signal.aborted || isAbortError(error)) return;
        if (error instanceof ApiError && error.status === 412) {
          if (error.validationErrors.length > 0) {
            setCheck({ kind: "invalid", violations: error.validationErrors });
          } else {
            setCheck({
              kind: "blocked",
              message: error.message || "The previous release can no longer be activated.",
            });
          }
          return;
        }
        setCheck({ kind: "error", message: errorMessage(error) });
      }
    },
    [name, namespace],
  );

  // Every open starts from the caller's view of the active release.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset once per open; `active` changing while open is handled by Refresh.
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    const initial = active
      ? { version: active.version, previous_version: active.previous_version }
      : null;
    setTarget(initial);
    setOutcome({ kind: "idle" });
    setTyped("");
    void validate(initial, controller.signal);
    return () => controller.abort();
  }, [open, validate]);

  const refresh = useCallback(async () => {
    setOutcome({ kind: "busy" });
    try {
      const current = await api.getActiveRelease(namespace, name);
      const next = { version: current.release.version, previous_version: current.previous_version };
      setTarget(next);
      setTyped("");
      setOutcome({ kind: "idle" });
      await validate(next, new AbortController().signal);
    } catch (error) {
      setOutcome({ kind: "error", message: errorMessage(error) });
    }
  }, [name, namespace, validate]);

  const confirmDisabled =
    busy ||
    check.kind !== "valid" ||
    !target ||
    (production && typed !== namespace.env) ||
    outcome.kind === "already";

  async function confirm() {
    if (confirmDisabled || !target) return;
    setOutcome({ kind: "busy" });
    try {
      const result = await api.rollbackRelease({
        env: namespace.env,
        app: namespace.app,
        name,
        expected_current_version: target.version,
      });
      if (!result.changed) {
        setOutcome({ kind: "already" });
      } else {
        setOutcome({ kind: "idle" });
      }
      onDone(result);
    } catch (error) {
      if (isConflict(error)) {
        setOutcome({
          kind: "moved",
          message: `The active release changed meanwhile (expected @${target.version}). Refresh to see what is active now.`,
        });
      } else if (error instanceof ApiError && error.validationErrors.length > 0) {
        setCheck({ kind: "invalid", violations: error.validationErrors });
        setOutcome({ kind: "idle" });
      } else {
        setOutcome({ kind: "error", message: errorMessage(error) });
      }
    }
  }

  const previous = target?.previous_version ?? 0;
  const releasesHref = links.releases({ app: namespace.app, env: namespace.env, name });
  const resolveHref = entryHrefResolver(active?.entries ?? [], namespace, links);

  return (
    <Modal
      open={open}
      title="Roll back release?"
      onClose={busy ? () => undefined : onClose}
      dismissible={!busy}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            {outcome.kind === "already" ? "Close" : "Cancel"}
          </Button>
          {outcome.kind === "moved" ? (
            <Button type="button" variant="outline" onClick={() => void refresh()} disabled={busy}>
              Refresh
            </Button>
          ) : null}
          <Button
            form={formId}
            type="submit"
            variant="destructive-solid"
            disabled={confirmDisabled}
            loading={busy}
            data-testid="rollback-confirm"
          >
            Confirm
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="rollback-dialog"
        data-testid="rollback-dialog"
        onSubmit={(event) => {
          event.preventDefault();
          void confirm();
        }}
      >
        <div className="danger-panel">
          {target && previous > 0 ? (
            <>
              Re-activate <ReleaseIdent name={name} version={previous} /> in{" "}
              <Ident kind="env" value={namespace.env} /> in place of{" "}
              <ReleaseIdent name={name} version={target.version} />. Subscribers receive a new
              activation revision; nothing is deleted.
            </>
          ) : (
            <>
              There is no previous release of <span className="mono">{name}</span> to roll back to
              in <Ident kind="env" value={namespace.env} />.
            </>
          )}
        </div>

        <div className="rollback-check" data-testid="rollback-check" aria-live="polite">
          {check.kind === "loading" ? (
            <span className="row-wrap text-sm faint">
              <Spinner /> Validating <span className="mono">{`${name}@${previous}`}</span>…
            </span>
          ) : check.kind === "valid" ? (
            <span className="row-wrap text-sm">
              <Badge kind="success">valid</Badge>
              <ReleaseIdent name={name} version={previous} /> is valid and can be activated.
            </span>
          ) : check.kind === "invalid" ? (
            <div>
              <div className="row-wrap text-sm">
                <Badge kind="danger">invalid</Badge>
                <ReleaseIdent name={name} version={previous} /> can no longer be activated.
              </div>
              <ViolationTable violations={check.violations} resolveHref={resolveHref} />
              <div className="text-sm mt-3">
                <Link href={releasesHref} className="ship-link">
                  Activate a different version…
                </Link>
              </div>
            </div>
          ) : check.kind === "blocked" ? (
            <div>
              <div className="row-wrap text-sm">
                <Badge kind="danger">blocked</Badge>
                {check.message}
              </div>
              <div className="text-sm mt-3">
                <Link href={releasesHref} className="ship-link">
                  Activate a different version…
                </Link>
              </div>
            </div>
          ) : check.kind === "error" ? (
            <div className="text-sm text-danger" role="alert">
              Could not validate the previous release: {check.message}
            </div>
          ) : null}
        </div>

        {outcome.kind === "moved" ? (
          <div className="info-panel mt-3" role="alert">
            {outcome.message}
          </div>
        ) : outcome.kind === "already" ? (
          <div className="info-panel mt-3" role="status">
            <ReleaseIdent name={name} version={previous} /> is already active; nothing changed.
          </div>
        ) : outcome.kind === "error" ? (
          <div className="danger-panel mt-3" role="alert">
            Rollback failed: {outcome.message}
          </div>
        ) : null}

        {production && check.kind === "valid" && outcome.kind !== "already" ? (
          <Field
            label={
              <>
                Type <span className="mono">{namespace.env}</span> to confirm
              </>
            }
            className="mt-4"
          >
            <Input
              className="font-mono"
              value={typed}
              autoComplete="off"
              spellCheck={false}
              disabled={busy}
              data-testid="rollback-confirm-env"
              onChange={(event) => setTyped(event.target.value)}
            />
          </Field>
        ) : null}
      </form>
    </Modal>
  );
}
