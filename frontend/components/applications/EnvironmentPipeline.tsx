import type { Application, ApplicationConfigurationRow, EnvironmentOverview } from "@/lib/types";
import { type EnvironmentCallbacks, EnvironmentColumn } from "./EnvironmentColumn";

/** Non-production environments first (stable), production last. */
export function orderEnvironments(environments: EnvironmentOverview[]): EnvironmentOverview[] {
  return [
    ...environments.filter((environment) => !environment.production),
    ...environments.filter((environment) => environment.production),
  ];
}

export function EnvironmentPipeline({
  application,
  environments,
  rows,
  focusEnv,
  callbacks,
}: {
  application: Application;
  environments: EnvironmentOverview[];
  rows: ApplicationConfigurationRow[];
  /** The `?env=` column to scroll to and focus. */
  focusEnv?: string | null;
  callbacks: EnvironmentCallbacks;
}) {
  const ordered = orderEnvironments(environments);
  // The scroller takes focus so keyboard users can reach offscreen columns
  // with the arrow keys; the ring comes from .pipeline-scroll:focus-visible.
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: a horizontal scroll container must be focusable to scroll by keyboard
    // biome-ignore lint/a11y/useSemanticElements: a fieldset groups form controls and cannot scroll
    <div className="pipeline-scroll" tabIndex={0} role="group" aria-label="Environment pipeline">
      <div className="pipeline" data-columns={ordered.length}>
        {ordered.map((environment) => (
          <EnvironmentColumn
            key={environment.namespace.env}
            application={application}
            environment={environment}
            rows={rows}
            focused={focusEnv === environment.namespace.env}
            callbacks={callbacks}
          />
        ))}
      </div>
    </div>
  );
}
