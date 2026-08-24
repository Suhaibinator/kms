import { describe, expect, it } from "vitest";
import { deriveSetupSteps, isSetupComplete, setupProgress } from "@/lib/setup-steps";
import type { ApplicationOverview, EnvironmentOverview } from "@/lib/types";
import readyJson from "./fixtures/backend/overview-ready.json";
import setupJson from "./fixtures/backend/overview-setup.json";

const ready = readyJson as unknown as ApplicationOverview;
const setup = setupJson as unknown as ApplicationOverview;

const byId = (steps: ReturnType<typeof deriveSetupSteps>) =>
  Object.fromEntries(steps.map((step) => [step.id, step]));

function withEnv(
  base: ApplicationOverview,
  patch: (env: EnvironmentOverview) => EnvironmentOverview,
): ApplicationOverview {
  return { ...base, environments: base.environments.map(patch) };
}

describe("deriveSetupSteps", () => {
  it("lists the nine steps in order with the token note first", () => {
    const steps = deriveSetupSteps({ applicationCount: 0, namespaceCount: 0, overview: null });
    expect(steps.map((step) => step.id)).toEqual([
      "token",
      "application",
      "contract",
      "schema",
      "environment",
      "values",
      "release",
      "sdk",
      "applied",
    ]);
    expect(steps[0]).toMatchObject({ informational: true, state: "done" });
    expect(steps[0]?.detail).toContain("admin identity rotate");
  });

  it("makes 'create application' current on an empty store and everything else todo", () => {
    const steps = byId(
      deriveSetupSteps({ applicationCount: 0, namespaceCount: 0, overview: null }),
    );
    expect(steps.application).toMatchObject({
      state: "current",
      action: { label: "Create application", action: { kind: "create-app" } },
    });
    for (const id of ["contract", "environment", "values", "release", "sdk", "applied"]) {
      expect(steps[id]?.state).toBe("todo");
      expect(steps[id]?.action).toBeUndefined();
    }
    expect(steps.schema).toMatchObject({ optional: true, state: "todo" });
    expect(setupProgress(Object.values(steps))).toEqual({ done: 0, total: 7 });
    expect(isSetupComplete(Object.values(steps))).toBe(false);
  });

  it("switches to the adopt variant when namespaces exist without an application", () => {
    const steps = byId(
      deriveSetupSteps({ applicationCount: 0, namespaceCount: 3, overview: null }),
    );
    expect(steps.application?.title).toBe("Create the application for your environments");
    expect(steps.application?.detail).toContain("3 environment namespaces already exist");
    expect(steps.application?.detail).toContain("`*/X`");
    expect(steps.environment?.detail).toContain("attach by name");
  });

  it("marks application and contract done for a fresh app and asks for an environment", () => {
    const steps = byId(
      deriveSetupSteps({ applicationCount: 1, namespaceCount: 0, overview: setup }),
    );
    expect(steps.application?.state).toBe("done");
    expect(steps.contract).toMatchObject({ state: "done" });
    expect(steps.contract?.detail).toBe("3 aliases aligned with the schema.");
    // schema_unpinned is only info: optional, never current.
    expect(steps.schema).toMatchObject({
      state: "todo",
      optional: true,
      action: { action: { kind: "register-schema" } },
    });
    expect(steps.environment).toMatchObject({
      state: "current",
      action: { action: { kind: "add-environment" } },
    });
    expect(steps.values?.state).toBe("todo");
    expect(steps.values?.items).toBeUndefined();
  });

  it("treats contract alignment findings as an unfinished contract", () => {
    const overview: ApplicationOverview = {
      ...setup,
      findings: [
        ...setup.findings,
        {
          code: "alias_not_in_schema",
          severity: "warning",
          scope: { alias: "rate_limits" },
          params: { alias: "rate_limits" },
        },
      ],
    };
    const steps = byId(deriveSetupSteps({ applicationCount: 1, namespaceCount: 0, overview }));
    expect(steps.contract).toMatchObject({
      state: "current",
      action: { label: "Edit contract", action: { kind: "edit-definition" } },
    });
    expect(steps.contract?.detail).toContain("1 alignment finding");
    expect(steps.environment?.state).toBe("todo");
  });

  it("completes every step for a ready application with applied instances", () => {
    const steps = deriveSetupSteps({ applicationCount: 1, namespaceCount: 2, overview: ready });
    expect(steps.every((step) => step.state === "done")).toBe(true);
    expect(isSetupComplete(steps)).toBe(true);
    const map = byId(steps);
    expect(map.values?.items?.map((item) => [item.env, item.done, item.detail])).toEqual(
      ready.environments.map((env) => [
        env.namespace.env,
        true,
        `${env.values.length} aliases set`,
      ]),
    );
    expect(map.release?.items?.map((item) => item.detail)).toEqual(
      ready.environments.map(
        (env) => `${env.release.active?.name}@${env.release.active?.version} active`,
      ),
    );
    expect(map.values?.items?.map((item) => item.production)).toEqual(
      ready.environments.map((env) => env.production),
    );
  });

  it("breaks the values step down per environment with fill-values actions", () => {
    const overview = withEnv(ready, (env) =>
      env.namespace.env === "prod"
        ? {
            ...env,
            values_state: "incomplete",
            values: env.values.map((value) =>
              value.alias === "db_password" ? { ...value, present: false, key: undefined } : value,
            ),
          }
        : env,
    );
    const total = ready.environments[0]?.values.length ?? 0;
    const steps = byId(deriveSetupSteps({ applicationCount: 1, namespaceCount: 2, overview }));
    expect(steps.values).toMatchObject({
      state: "current",
      action: {
        label: "Fill values",
        action: { kind: "fill-values", env: "prod", alias: "db_password" },
      },
    });
    expect(steps.values?.detail).toBe("1 environment still missing values.");
    expect(steps.values?.items).toEqual([
      expect.objectContaining({ env: "dev", done: true }),
      expect.objectContaining({
        env: "prod",
        done: false,
        detail: `${total - 1} of ${total} set`,
        action: {
          label: "Fill values",
          action: { kind: "fill-values", env: "prod", alias: "db_password" },
        },
      }),
    ]);
    // Later steps keep reporting their own evidence: the release is still active.
    expect(steps.release?.state).toBe("done");
  });

  it("asks to ship per environment without an active release", () => {
    const overview = withEnv(ready, (env) =>
      env.namespace.env === "prod"
        ? {
            ...env,
            release_state: "none",
            release: { ...env.release, active: undefined },
            rollout: { ...env.rollout, total: 0, applied_current: 0 },
          }
        : env,
    );
    const steps = byId(deriveSetupSteps({ applicationCount: 1, namespaceCount: 2, overview }));
    expect(steps.release).toMatchObject({
      state: "current",
      action: { label: "Ship", action: { kind: "ship", env: "prod" } },
    });
    expect(steps.release?.items?.[1]).toMatchObject({
      env: "prod",
      done: false,
      detail: "No release active",
    });
    // dev still has subscribers, so sdk/applied are done regardless of prod.
    expect(steps.sdk?.state).toBe("done");
    expect(steps.applied?.state).toBe("done");
  });

  it("asks to connect the SDK when no environment has subscribers, then waits for applied", () => {
    const noSubs = withEnv(ready, (env) => ({
      ...env,
      rollout_state: "no_subscribers",
      rollout: { ...env.rollout, total: 0, connected: 0, applied_current: 0 },
    }));
    let steps = byId(
      deriveSetupSteps({ applicationCount: 1, namespaceCount: 2, overview: noSubs }),
    );
    expect(steps.sdk).toMatchObject({
      state: "current",
      action: { label: "Connect SDK", action: { kind: "connect", env: "dev" } },
    });
    expect(steps.applied?.state).toBe("todo");
    expect(setupProgress(Object.values(steps))).toEqual({ done: 5, total: 7 });

    const pending = withEnv(ready, (env) => ({
      ...env,
      rollout: { ...env.rollout, total: 2, connected: 2, applied_current: 0, pending: 2 },
    }));
    steps = byId(deriveSetupSteps({ applicationCount: 1, namespaceCount: 2, overview: pending }));
    expect(steps.sdk?.state).toBe("done");
    expect(steps.sdk?.detail).toBe("4 instances subscribed in dev, prod.");
    expect(steps.applied).toMatchObject({ state: "current" });
    expect(steps.applied?.detail).toContain("Waiting for a subscribed instance");
  });

  it("flags a pinned schema that is missing from the registry", () => {
    const overview: ApplicationOverview = {
      ...ready,
      findings: [{ code: "schema_missing", severity: "blocking", scope: {}, params: {} }],
    };
    const steps = byId(deriveSetupSteps({ applicationCount: 1, namespaceCount: 2, overview }));
    expect(steps.schema).toMatchObject({ state: "todo", optional: true });
    expect(steps.schema?.detail).toContain(
      `${ready.application.schema_id}@${ready.application.schema_version}`,
    );
    expect(steps.schema?.detail).toContain("not in the registry");
  });
});
