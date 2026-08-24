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
  return (
    <div className="pipeline-scroll">
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
