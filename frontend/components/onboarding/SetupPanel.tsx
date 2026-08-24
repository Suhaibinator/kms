import { useMemo } from "react";
import type { SetupPanelProps } from "@/components/applications/contracts";
import SetupChecklist from "@/components/onboarding/SetupChecklist";
import { deriveSetupSteps, isSetupComplete, setupProgress } from "@/lib/setup-steps";

export type { SetupPanelProps };

/**
 * The application page's collapsible setup checklist. Open while steps
 * remain; once everything is done it collapses to a one-line summary so a
 * finished application is not nagged.
 */
export default function SetupPanel({ overview, onAction }: SetupPanelProps) {
  const steps = useMemo(
    () =>
      deriveSetupSteps({
        applicationCount: 1,
        namespaceCount: overview.environments.length,
        overview,
      }),
    [overview],
  );
  const { done, total } = setupProgress(steps);
  const complete = isSetupComplete(steps);
  return (
    <details className="advanced-panel setup-panel" open={!complete} data-complete={complete}>
      <summary className="setup-panel-summary">
        <span>{complete ? "Setup · complete" : `Setup · ${done} of ${total} done`}</span>
        <span className="setup-panel-meter" aria-hidden>
          <span
            className="setup-panel-meter-fill"
            style={{ width: `${total > 0 ? Math.round((done / total) * 100) : 0}%` }}
          />
        </span>
      </summary>
      <div className="advanced-panel-content setup-panel-content">
        <SetupChecklist steps={steps} onAction={onAction} compact />
      </div>
    </details>
  );
}
