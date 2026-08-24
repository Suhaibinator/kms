import { Sparkles } from "lucide-react";
import { useMemo } from "react";
import type { SetupAction } from "@/components/applications/contracts";
import SetupChecklist from "@/components/onboarding/SetupChecklist";
import { deriveSetupSteps } from "@/lib/setup-steps";

export interface FirstRunChecklistProps {
  /** Existing namespaces; > 0 switches to the "adopt existing environments" variant. */
  namespaceCount: number;
  onCreateApplication: () => void;
}

/**
 * The overview's zero-state for an admin with no applications yet: the full
 * setup checklist with step 1 wired to the create wizard. With namespaces
 * already present it explains that creating application X adopts every X namespace.
 */
export default function FirstRunChecklist({
  namespaceCount,
  onCreateApplication,
}: FirstRunChecklistProps) {
  const adopt = namespaceCount > 0;
  const steps = useMemo(
    () => deriveSetupSteps({ applicationCount: 0, namespaceCount, overview: null }),
    [namespaceCount],
  );
  const onAction = (action: SetupAction) => {
    if (action.kind === "create-app") onCreateApplication();
  };
  return (
    <section className="card setup-card" aria-labelledby="first-run-title">
      <div className="setup-card-head">
        <span className="setup-card-icon" aria-hidden>
          <Sparkles size={18} strokeWidth={1.9} />
        </span>
        <div>
          <h2 id="first-run-title" className="section-title">
            {adopt ? "Adopt your existing environments" : "Set up your first application"}
          </h2>
          <p className="setup-card-sub">
            {adopt
              ? `${namespaceCount} environment ${namespaceCount === 1 ? "namespace" : "namespaces"} exist without an application. Create the application they belong to and they attach by name.`
              : "Seven steps from an empty store to a client running on an activated release."}
          </p>
        </div>
      </div>
      <SetupChecklist steps={steps} onAction={onAction} />
    </section>
  );
}
