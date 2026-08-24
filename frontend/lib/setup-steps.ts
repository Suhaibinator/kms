// The setup checklist, derived purely from what the console already knows
// (plan §2.6): how many applications and namespaces exist, and — once an
// application exists — its overview. The first-run checklist on `/` and the
// SetupPanel on an application page render the same list; only the inputs
// differ. Nothing here fetches.

import type { SetupAction } from "@/components/applications/contracts";
import type { ApplicationOverview, EnvironmentOverview, FindingCode } from "@/lib/types";

export type SetupStepId =
  | "token"
  | "application"
  | "contract"
  | "schema"
  | "environment"
  | "values"
  | "release"
  | "sdk"
  | "applied";

export type SetupStepState = "done" | "current" | "todo";

export interface SetupStepAction {
  label: string;
  action: SetupAction;
}

/** A per-environment line under a step (values, release). */
export interface SetupStepItem {
  env: string;
  production: boolean;
  done: boolean;
  detail: string;
  action?: SetupStepAction;
}

export interface SetupStep {
  id: SetupStepId;
  title: string;
  detail: string;
  state: SetupStepState;
  /** Informational: rendered with an info mark, never counted, never "current". */
  informational?: boolean;
  /** Optional: counted separately, never blocks the next step from being current. */
  optional?: boolean;
  action?: SetupStepAction;
  items?: SetupStepItem[];
}

export interface DeriveSetupStepsInput {
  applicationCount: number;
  namespaceCount: number;
  overview: ApplicationOverview | null;
}

// Contract/alignment findings that keep the contract step from counting as done.
const CONTRACT_FINDINGS: ReadonlySet<FindingCode> = new Set<FindingCode>([
  "contract_empty",
  "schema_property_missing_alias",
  "schema_required_missing_alias",
  "alias_not_in_schema",
  "contract_type_mismatch",
]);

const plural = (n: number, one: string, many = `${one}s`): string => `${n} ${n === 1 ? one : many}`;

function envHasActiveRelease(env: EnvironmentOverview): boolean {
  return Boolean(env.release.active);
}

function firstMissingAlias(env: EnvironmentOverview): string | undefined {
  return env.values.find((value) => !value.present || !value.key)?.alias;
}

function valuesDetail(env: EnvironmentOverview): string {
  const total = env.values.length;
  const present = env.values.filter((value) => value.present && value.key).length;
  if (env.values_state === "complete") return `${plural(total, "alias", "aliases")} set`;
  if (env.values_state === "empty") return `0 of ${total} set`;
  const missing = total - present;
  return missing > 0 ? `${present} of ${total} set` : "Some values do not match the contract";
}

/**
 * The ordered checklist. Steps are `done` when their evidence is present,
 * the first non-done, non-informational, non-optional step is `current`, and
 * everything after it is `todo`. Optional steps (schema) never block.
 */
export function deriveSetupSteps({
  applicationCount,
  namespaceCount,
  overview,
}: DeriveSetupStepsInput): SetupStep[] {
  const adopt = applicationCount === 0 && namespaceCount > 0;
  const app = overview?.application ?? null;
  const envs = overview?.environments ?? [];
  const appFindings = overview?.findings ?? [];

  const steps: Array<Omit<SetupStep, "state"> & { done: boolean }> = [];

  steps.push({
    id: "token",
    title: "Keep the admin token safe",
    detail:
      "The one-time admin token printed at first start cannot be shown again. If it is lost, mint a new one with `admin identity rotate` on the server.",
    informational: true,
    done: true,
  });

  steps.push({
    id: "application",
    title: adopt ? "Create the application for your environments" : "Create an application",
    detail: app
      ? `\`${app.name}\` reads release \`${app.release_name}\`.`
      : adopt
        ? `${plural(namespaceCount, "environment namespace")} already exist. Creating application \`X\` attaches every \`*/X\` namespace to it automatically.`
        : "Name it, pick a release name, and list the aliases it reads.",
    done: app !== null,
    action: app ? undefined : { label: "Create application", action: { kind: "create-app" } },
  });

  const contractCount = app?.contract.length ?? 0;
  const alignment = appFindings.filter((finding) => CONTRACT_FINDINGS.has(finding.code));
  const contractDone = app !== null && contractCount > 0 && alignment.length === 0;
  steps.push({
    id: "contract",
    title: "Define the contract",
    detail: !app
      ? "The aliases the application reads, each a parameter or a secret."
      : contractCount === 0
        ? "The contract is empty, so nothing can be shipped."
        : alignment.length > 0
          ? `${plural(contractCount, "alias", "aliases")}; ${plural(alignment.length, "alignment finding")} against the schema.`
          : `${plural(contractCount, "alias", "aliases")} aligned with the schema.`,
    done: contractDone,
    action:
      app && !contractDone
        ? { label: "Edit contract", action: { kind: "edit-definition" } }
        : undefined,
  });

  const schemaPinned = Boolean(app?.schema_id);
  const schemaMissing = appFindings.some((finding) => finding.code === "schema_missing");
  const schemaDone = schemaPinned && !schemaMissing;
  steps.push({
    id: "schema",
    title: "Pin a schema",
    detail: !app
      ? "Optional. A JSON Schema validates every release before it activates."
      : schemaMissing
        ? `Pinned schema \`${app.schema_id}@${app.schema_version}\` is not in the registry.`
        : schemaPinned
          ? `\`${app.schema_id}@${app.schema_version}\` validates every release.`
          : "Optional. Releases activate without shape validation until one is pinned.",
    optional: true,
    done: schemaDone,
    action:
      app && !schemaDone
        ? { label: "Register schema", action: { kind: "register-schema" } }
        : undefined,
  });

  const envDone = envs.length > 0;
  steps.push({
    id: "environment",
    title: "Add an environment",
    detail: envDone
      ? envs.map((env) => env.namespace.env).join(", ")
      : adopt && !app
        ? "Existing namespaces attach by name when the application is created."
        : "Each environment is an isolated namespace: dev, staging, prod.",
    done: envDone,
    action:
      app && !envDone
        ? { label: "Add environment", action: { kind: "add-environment" } }
        : undefined,
  });

  const valueItems: SetupStepItem[] = envs.map((env) => ({
    env: env.namespace.env,
    production: env.production,
    done: env.values_state === "complete",
    detail: valuesDetail(env),
    action:
      env.values_state === "complete"
        ? undefined
        : {
            label: "Fill values",
            action: { kind: "fill-values", env: env.namespace.env, alias: firstMissingAlias(env) },
          },
  }));
  const valuesDone = envDone && valueItems.every((item) => item.done);
  const firstIncompleteValues = valueItems.find((item) => !item.done);
  steps.push({
    id: "values",
    title: "Set values",
    detail: !envDone
      ? "Give every contract alias a value in each environment."
      : valuesDone
        ? "Every alias has a value in every environment."
        : `${plural(valueItems.filter((item) => !item.done).length, "environment")} still missing values.`,
    done: valuesDone,
    items: envDone ? valueItems : undefined,
    action: firstIncompleteValues?.action,
  });

  const releaseItems: SetupStepItem[] = envs.map((env) => {
    const active = env.release.active;
    return {
      env: env.namespace.env,
      production: env.production,
      done: envHasActiveRelease(env),
      detail: active ? `${active.name}@${active.version} active` : "No release active",
      action: active
        ? undefined
        : { label: "Ship", action: { kind: "ship", env: env.namespace.env } },
    };
  });
  const releaseDone = envDone && releaseItems.every((item) => item.done);
  const firstUnreleased = releaseItems.find((item) => !item.done);
  steps.push({
    id: "release",
    title: "Activate the first release",
    detail: !envDone
      ? "Ship pins every alias to an exact version and activates it in one step."
      : releaseDone
        ? "A release is active in every environment."
        : `${plural(releaseItems.filter((item) => !item.done).length, "environment")} without an active release.`,
    done: releaseDone,
    items: envDone ? releaseItems : undefined,
    action: firstUnreleased?.action,
  });

  const withSubscribers = envs.filter((env) => env.rollout.total > 0);
  const sdkDone = withSubscribers.length > 0;
  const connectEnv =
    envs.find(envHasActiveRelease)?.namespace.env ?? envs[0]?.namespace.env ?? undefined;
  steps.push({
    id: "sdk",
    title: "Connect the SDK",
    detail: sdkDone
      ? `${plural(
          withSubscribers.reduce((sum, env) => sum + env.rollout.total, 0),
          "instance",
        )} subscribed in ${withSubscribers.map((env) => env.namespace.env).join(", ")}.`
      : "Create a client identity and point the SDK's release loader at this application.",
    done: sdkDone,
    action:
      app && !sdkDone
        ? { label: "Connect SDK", action: { kind: "connect", env: connectEnv } }
        : undefined,
  });

  const applied = envs.reduce((sum, env) => sum + env.rollout.applied_current, 0);
  const appliedDone = applied > 0;
  steps.push({
    id: "applied",
    title: "See it applied",
    detail: appliedDone
      ? `${plural(applied, "instance has", "instances have")} applied the active release.`
      : sdkDone
        ? "Waiting for a subscribed instance to apply the active release."
        : "Each instance reports when it has applied the active release.",
    done: appliedDone,
  });

  let currentAssigned = false;
  return steps.map(({ done, ...step }) => {
    if (step.informational || done) return { ...step, state: "done" };
    if (step.optional) return { ...step, state: "todo" };
    if (!currentAssigned) {
      currentAssigned = true;
      return { ...step, state: "current" };
    }
    return { ...step, state: "todo" };
  });
}

/** Done / total over the required steps (informational and optional excluded). */
export function setupProgress(steps: readonly SetupStep[]): { done: number; total: number } {
  const required = steps.filter((step) => !step.informational && !step.optional);
  return {
    done: required.filter((step) => step.state === "done").length,
    total: required.length,
  };
}

export function isSetupComplete(steps: readonly SetupStep[]): boolean {
  const { done, total } = setupProgress(steps);
  return total > 0 && done === total;
}
