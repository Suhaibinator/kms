import { Check, Info } from "lucide-react";
import type { SetupAction } from "@/components/applications/contracts";
import { Ident } from "@/components/Ident";
import { Button } from "@/components/ui/button";
import type { SetupStep, SetupStepItem } from "@/lib/setup-steps";

export interface SetupChecklistProps {
  steps: SetupStep[];
  onAction: (action: SetupAction) => void;
  /** Tighter rows for the application page's collapsible panel. */
  compact?: boolean;
  className?: string;
}

// Step copy uses `backticks` for names; render those as code so they read as
// identifiers rather than prose.
function Detail({ text }: { text: string }) {
  const parts = text.split(/`([^`]+)`/);
  return (
    <>
      {parts.map((part, index) =>
        index % 2 === 1 ? (
          <code key={`${index}-${part}`} className="setup-code">
            {part}
          </code>
        ) : (
          part
        ),
      )}
    </>
  );
}

const WIZARD_STATE = { done: "complete", current: "current", todo: "upcoming" } as const;

function StepNumber({ step, index }: { step: SetupStep; index: number }) {
  if (step.informational) {
    return (
      <span className="wizard-step-number setup-step-info" aria-hidden>
        <Info size={13} strokeWidth={2.25} />
      </span>
    );
  }
  return (
    <span className="wizard-step-number" aria-hidden>
      {step.state === "done" ? <Check size={14} strokeWidth={2.5} /> : index}
    </span>
  );
}

function ItemRow({
  item,
  onAction,
}: {
  item: SetupStepItem;
  onAction: (action: SetupAction) => void;
}) {
  const action = item.action;
  return (
    <li className={`setup-item ${item.done ? "setup-item-done" : ""}`} data-env={item.env}>
      <span className="setup-item-mark" aria-hidden>
        {item.done ? <Check size={12} strokeWidth={2.5} /> : null}
      </span>
      <Ident kind="env" value={item.env} production={item.production} tooltip={false} />
      <span className="setup-item-detail">{item.detail}</span>
      {action ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="setup-item-action"
          onClick={() => onAction(action.action)}
        >
          {action.label}
        </Button>
      ) : null}
    </li>
  );
}

/**
 * The ordered setup steps as `<ol class="setup-steps">`, reusing the identity
 * wizard's numbered-circle styling. The current step carries `aria-current`
 * and its action as a primary button; optional steps that are still open get
 * an outline button so they never read as blocking.
 */
export default function SetupChecklist({
  steps,
  onAction,
  compact = false,
  className,
}: SetupChecklistProps) {
  let number = 0;
  return (
    <ol
      className={`setup-steps ${compact ? "setup-steps-compact" : ""} ${className ?? ""}`.trim()}
      aria-label="Setup steps"
    >
      {steps.map((step) => {
        if (!step.informational) number += 1;
        const primary = step.state === "current";
        const action = step.action;
        const hasItemActions = step.items?.some((item) => item.action) ?? false;
        return (
          <li
            key={step.id}
            className={`setup-step setup-step-${step.state} wizard-step-${WIZARD_STATE[step.state]} ${
              step.optional ? "setup-step-optional" : ""
            }`}
            data-step={step.id}
            data-state={step.state}
            aria-current={primary ? "step" : undefined}
          >
            <StepNumber step={step} index={number} />
            <div className="setup-step-body">
              <div className="setup-step-title">
                {step.title}
                {step.optional ? <span className="setup-step-tag">optional</span> : null}
              </div>
              <div className="setup-step-detail">
                <Detail text={step.detail} />
              </div>
              {step.items && step.items.length > 0 ? (
                <ul className="setup-items">
                  {step.items.map((item) => (
                    <ItemRow key={item.env} item={item} onAction={onAction} />
                  ))}
                </ul>
              ) : null}
            </div>
            {action && !hasItemActions && (primary || step.optional) ? (
              <Button
                type="button"
                variant={primary ? "default" : "outline"}
                size="sm"
                className="setup-step-action"
                onClick={() => onAction(action.action)}
              >
                {action.label}
              </Button>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}
