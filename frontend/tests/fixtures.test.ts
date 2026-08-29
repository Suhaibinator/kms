// The backend fixtures (tests/fixtures/backend/*.json) are the shared contract
// between the Go read models and the TypeScript types. JSON imports widen
// every literal to `string`, so `satisfies` cannot narrow the unions — the
// shape is checked here at runtime instead, with the union members drawn from
// the same tables the UI renders from (readiness, glossary, contract-derive).

import { describe, expect, it } from "vitest";
import { jsonTypeToContentType } from "@/lib/contract-derive";
import { REJECTION_CATEGORIES } from "@/lib/glossary";
import { FINDING_COPY, FIX_FOR, isProductionEnvironment, STATUS_LABEL } from "@/lib/readiness";
import type {
  ApplicationOverview,
  AppStatus,
  EnvironmentOverview,
  EnvStatus,
  Finding,
  FindingCode,
  FleetOverview,
  ReleaseState,
  RolloutState,
  ShipResult,
  SubscriberInstance,
  ValuesState,
} from "@/lib/types";
import fleetJson from "./fixtures/backend/fleet.json";
import incidentJson from "./fixtures/backend/overview-incident.json";
import readyJson from "./fixtures/backend/overview-ready.json";
import setupJson from "./fixtures/backend/overview-setup.json";
import readinessJson from "./fixtures/backend/readiness-cases.json";
import conflictJson from "./fixtures/backend/ship-conflict.json";
import previewJson from "./fixtures/backend/ship-preview.json";

const APP_STATUSES: AppStatus[] = ["blocked", "setup", "attention", "ready"];
const ENV_STATUSES: EnvStatus[] = [
  "blocked",
  "empty",
  "incomplete",
  "unreleased",
  "degraded",
  "rolling",
  "drift",
  "ready",
];
const VALUES_STATES: ValuesState[] = ["empty", "incomplete", "complete"];
const RELEASE_STATES: ReleaseState[] = ["none", "active", "drift", "blocked"];
const ROLLOUT_STATES: RolloutState[] = [
  "no_subscribers",
  "applied",
  "rolling",
  "degraded",
  "stale",
];
const FINDING_CODES = Object.keys(FINDING_COPY) as FindingCode[];
const SUBSCRIBER_STATES = ["", "received", "prepared", "applied", "rejected"];

type Json = Record<string, unknown>;
const isObject = (v: unknown): v is Json => typeof v === "object" && v !== null;

function assertFinding(finding: unknown): asserts finding is Finding {
  expect(isObject(finding)).toBe(true);
  const f = finding as Json;
  expect(FINDING_CODES).toContain(f.code);
  expect(["blocking", "warning", "info"]).toContain(f.severity);
  expect(isObject(f.scope)).toBe(true);
  expect(isObject(f.params)).toBe(true);
  // Copy renders for every finding without throwing or reading a value.
  const copy = FINDING_COPY[f.code as FindingCode](f.params as Finding["params"]);
  expect(copy.length).toBeGreaterThan(10);
}

function assertInstance(instance: unknown): asserts instance is SubscriberInstance {
  expect(isObject(instance)).toBe(true);
  const i = instance as Json;
  for (const key of [
    "identity",
    "client_name",
    "instance_id",
    "rejection_category",
    "diagnostic",
  ]) {
    expect(typeof i[key]).toBe("string");
  }
  expect(SUBSCRIBER_STATES).toContain(i.state);
  for (const key of ["release_version", "activation_revision", "server_timestamp_unix_ms"]) {
    expect(typeof i[key]).toBe("number");
  }
  expect(typeof i.connected).toBe("boolean");
  if (i.state === "rejected") {
    expect(Object.keys(REJECTION_CATEGORIES)).toContain(i.rejection_category);
  }
}

function assertEnvironment(env: unknown): asserts env is EnvironmentOverview {
  expect(isObject(env)).toBe(true);
  const e = env as Json;
  const ns = e.namespace as Json;
  expect(typeof ns.env).toBe("string");
  expect(typeof ns.app).toBe("string");
  expect(Array.isArray(ns.allowed_auth_methods)).toBe(true);
  expect(e.production).toBe(isProductionEnvironment(ns.env as string));
  expect(ENV_STATUSES).toContain(e.status);
  expect(VALUES_STATES).toContain(e.values_state);
  expect(RELEASE_STATES).toContain(e.release_state);
  expect(ROLLOUT_STATES).toContain(e.rollout_state);
  for (const value of e.values as unknown[]) {
    const v = value as Json;
    expect(typeof v.alias).toBe("string");
    expect(["parameter", "secret"]).toContain(v.kind);
    expect(typeof v.present).toBe("boolean");
  }
  const release = e.release as Json;
  expect(typeof release.latest_version).toBe("number");
  expect(typeof release.release_count).toBe("number");
  if (release.active !== undefined) {
    const active = release.active as Json;
    expect(typeof active.name).toBe("string");
    expect(typeof active.version).toBe("number");
    expect(typeof active.activation_revision).toBe("number");
    expect(typeof active.previous_version).toBe("number");
    expect(typeof active.is_rolled_back).toBe("boolean");
    expect(Array.isArray(active.entries)).toBe(true);
  }
  const rollout = e.rollout as Json;
  for (const key of [
    "total",
    "connected",
    "applied_current",
    "applied_divergent",
    "rejected",
    "pending",
    "stale",
  ]) {
    expect(typeof rollout[key]).toBe("number");
  }
  expect(Array.isArray(rollout.other_release_names)).toBe(true);
  expect(typeof rollout.truncated).toBe("boolean");
  for (const instance of rollout.rejected_instances as unknown[]) assertInstance(instance);
  for (const finding of e.findings as unknown[]) assertFinding(finding);
}

function assertOverview(overview: unknown): asserts overview is ApplicationOverview {
  expect(isObject(overview)).toBe(true);
  const o = overview as Json;
  const application = o.application as Json;
  expect(typeof application.name).toBe("string");
  expect(typeof application.release_name).toBe("string");
  expect(Array.isArray(application.contract)).toBe(true);
  expect(APP_STATUSES).toContain(o.status);
  for (const finding of o.findings as unknown[]) assertFinding(finding);
  for (const env of o.environments as unknown[]) assertEnvironment(env);
  expect(Array.isArray(o.rows)).toBe(true);
  if (o.schema_json !== undefined) expect(() => JSON.parse(o.schema_json as string)).not.toThrow();
}

function assertShipResult(result: unknown): asserts result is ShipResult {
  expect(isObject(result)).toBe(true);
  const r = result as Json;
  expect([
    "preview",
    "activated",
    "rejected",
    "release_created_not_activated",
    "conflict",
  ]).toContain(r.status);
  const preview = r.preview as Json;
  expect(typeof preview.base_version).toBe("number");
  expect(typeof preview.release_name).toBe("string");
  for (const entry of preview.entries as unknown[]) {
    const e = entry as Json;
    expect(typeof e.alias).toBe("string");
    expect(["parameter", "secret"]).toContain(e.kind);
    expect(typeof e.key).toBe("string");
    expect(["edited", "pinned", "included", "missing"]).toContain(e.change);
  }
  const validation = preview.validation as Json;
  expect(typeof validation.valid).toBe("boolean");
  expect(Array.isArray(validation.errors)).toBe(true);
  for (const finding of preview.warnings as unknown[]) assertFinding(finding);
  expect(Array.isArray(r.parameters)).toBe(true);
}

function assertFleet(fleet: unknown): asserts fleet is FleetOverview {
  expect(isObject(fleet)).toBe(true);
  for (const item of (fleet as Json).applications as unknown[]) {
    const a = item as Json;
    expect(typeof (a.application as Json).name).toBe("string");
    expect(APP_STATUSES).toContain(a.status);
    for (const env of a.environments as unknown[]) {
      const e = env as Json;
      expect(typeof e.env).toBe("string");
      expect(ENV_STATUSES).toContain(e.status);
      expect(e.production).toBe(isProductionEnvironment(e.env as string));
    }
  }
}

describe("backend fixtures", () => {
  it("overview-ready: every environment ready, nothing to fix", () => {
    assertOverview(readyJson);
    expect(readyJson.status).toBe("ready");
    expect(readyJson.environments.map((env) => env.status)).toEqual(["ready", "ready"]);
    expect(readyJson.environments.every((env) => env.rollout.rejected === 0)).toBe(true);
    // `production` is env-scoped only; the application level carries nothing.
    expect(readyJson.findings).toEqual([]);
    const prod = readyJson.environments.find((env) => env.namespace.env === "prod");
    expect(prod?.findings.map((f) => f.code)).toEqual(["production", "previous_unavailable"]);
    expect(prod?.findings.every((f) => f.severity === "info")).toBe(true);
  });

  it("overview-incident: prod drift plus one rejected config_validation_failed instance", () => {
    assertOverview(incidentJson);
    expect(incidentJson.status).toBe("attention");
    const prod = incidentJson.environments.find((env) => env.namespace.env === "prod");
    expect(prod?.status).toBe("degraded");
    expect(prod?.release_state).toBe("drift");
    expect(prod?.rollout_state).toBe("degraded");
    expect(prod?.rollout.rejected_instances[0]?.rejection_category).toBe(
      "config_validation_failed",
    );
    const drift = prod?.values.find((v) => v.alias === "rate_limits");
    expect(drift?.current_version).toBeGreaterThan(drift?.pinned_version ?? 0);
    // Within a severity, emission order is values → release → rollout.
    expect(prod?.findings.map((f) => f.code)).toEqual([
      "unreleased_changes",
      "instance_rejected",
      "production",
    ]);
  });

  it("overview-setup: an application with no environments", () => {
    assertOverview(setupJson);
    expect(setupJson.status).toBe("setup");
    expect(setupJson.environments).toEqual([]);
    expect(setupJson.findings.map((f) => f.code)).toEqual(["no_environments", "schema_unpinned"]);
  });

  it("ship-preview and ship-conflict", () => {
    assertShipResult(previewJson);
    expect(previewJson.status).toBe("preview");
    expect(previewJson.release).toBeUndefined();
    expect(previewJson.preview.entries.map((e) => e.change)).toEqual([
      "included",
      "included",
      "edited",
    ]);

    assertShipResult(conflictJson);
    expect(conflictJson.status).toBe("conflict");
    expect(conflictJson.error?.current_version).toBeGreaterThan(conflictJson.preview.base_version);
    expect(conflictJson.parameters[0]?.version).toBe(11);
    expect(conflictJson.release?.version).toBe(9);
  });

  it("fleet: the rows-free form with per-environment status", () => {
    assertFleet(fleetJson);
    // Store order: alphabetical by application name.
    expect(fleetJson.applications.map((a) => [a.application.name, a.status])).toEqual([
      ["billing", "setup"],
      ["gradethis", "attention"],
      ["reports", "ready"],
    ]);
  });

  it("readiness-cases: expected states are valid and the type mapping matches the TS table", () => {
    for (const c of readinessJson.cases) {
      expect(VALUES_STATES).toContain(c.expected.values_state);
      expect(RELEASE_STATES).toContain(c.expected.release_state);
      expect(ROLLOUT_STATES).toContain(c.expected.rollout_state);
      expect(ENV_STATUSES).toContain(c.expected.env_status);
      expect(APP_STATUSES).toContain(c.expected.app_status);
      for (const code of c.expected.finding_codes) expect(FINDING_CODES).toContain(code);
      for (const instance of c.input.instances) assertInstance(instance);
    }
    const property = (name: string): unknown => {
      if (name === "string+kms-base64") return { type: "string", format: "kms-base64" };
      if (name === "union") return { type: ["string", "null"] };
      if (name === "absent") return {};
      return { type: name };
    };
    for (const [jsonType, contentType] of Object.entries(readinessJson.type_mapping)) {
      expect(jsonTypeToContentType(property(jsonType))).toBe(contentType);
    }
  });

  it("every finding code has copy, a fix decision and every status a label", () => {
    for (const code of FINDING_CODES) {
      expect(code in FIX_FOR).toBe(true);
      expect(FINDING_COPY[code]({}).length).toBeGreaterThan(10);
    }
    for (const status of [...APP_STATUSES, ...ENV_STATUSES]) {
      expect(STATUS_LABEL[status]).toBeTruthy();
    }
  });
});
