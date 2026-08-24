import { SHIP_STEPS, type ShipStepId } from "@/lib/glossary";
import { cn } from "@/lib/utils";

/**
 * The guided header: the four ship steps as a `.wizard-steps` strip with the
 * current step's concept blurb underneath. Express mode simply omits it.
 */
export function ShipSteps({ current }: { current: ShipStepId }) {
  const currentIndex = SHIP_STEPS.findIndex((step) => step.id === current);
  const blurb = SHIP_STEPS[currentIndex]?.blurb ?? "";
  return (
    <div className="ship-steps" data-testid="ship-steps">
      <ol className="wizard-steps" aria-label="Ship steps">
        {SHIP_STEPS.map((step, index) => {
          const state =
            index < currentIndex ? "complete" : index === currentIndex ? "current" : "upcoming";
          return (
            <li
              key={step.id}
              className={cn(
                "wizard-step",
                state === "current" && "wizard-step-current",
                state === "complete" && "wizard-step-complete",
              )}
              aria-current={state === "current" ? "step" : undefined}
              data-step={step.id}
              data-state={state}
            >
              <span className="wizard-step-number" aria-hidden="true">
                {index + 1}
              </span>
              <span>{step.title}</span>
            </li>
          );
        })}
      </ol>
      {blurb ? <p className="ship-step-blurb">{blurb}</p> : null}
    </div>
  );
}
