// A mutable in-memory KMS for Playwright: enough of /api/v1 for the console's
// application, ship, release and rollout surfaces to behave like the real
// server. State lives in the test process; `page.route("**/api/v1/**")`
// answers every call from it, so a ship really moves versions and releases,
// a rollback really re-activates, and the overview is recomputed from state
// on each request. The subscriber stream is a 404 so the console exercises
// its polling fallback.

import type { Page, Route } from "@playwright/test";
import type {
  Application,
  ApplicationOverview,
  ConfigurationRelease,
  ConfigurationReleaseEntry,
  EnvironmentOverview,
  EnvStatus,
  Finding,
  Namespace,
  OverviewRollout,
  ReleaseSubscriberState,
  ReleaseSummary,
  ReleaseValidationError,
  ShipChange,
  ShipPreview,
  ShipPreviewEntry,
  ShipRequest,
  ShipResult,
  SubscriberInstance,
  ValidateReleaseResponse,
} from "../../../lib/types";
import incidentJson from "../../fixtures/backend/overview-incident.json";

export interface FakeParameter {
  key: string;
  content_type: string;
  /** versions[i] is version i + 1. */
  versions: string[];
}

export interface FakeSecret {
  key: string;
  versionCount: number;
  bound: boolean;
}

export interface FakeNamespace {
  namespace: Namespace;
  parameters: Record<string, FakeParameter>;
  secrets: Record<string, FakeSecret>;
  releases: ConfigurationRelease[];
  /** 0 = nothing active. */
  active: number;
  previous: number;
  activationRevision: number;
  subscribers: ReleaseSubscriberState[];
}

export interface ActivationContext {
  env: string;
  release: ConfigurationRelease;
  revision: number;
  kind: "ship" | "activate" | "rollback";
  /** The instances as they were before this activation. */
  previous: ReleaseSubscriberState[];
}

export interface ConsoleState {
  application: Application;
  namespaces: Record<string, FakeNamespace>;
  revision: number;
  identity: { name: string; kind: "admin" | "client"; auth_method: string };
  /** What instances report after an activation; default: every one applies. */
  onActivate?: (ctx: ActivationContext) => ReleaseSubscriberState[];
  /** Override validation for a candidate release (ship preview, validate, activate, rollback). */
  validate?: (release: ConfigurationRelease, ctx: { env: string }) => ValidateReleaseResponse;
  /** Force a non-activated ship outcome after the write; return null for the default flow. */
  shipOutcome?: (request: ShipRequest, ctx: { env: string }) => ShipResult["status"] | null;
  /** Every request, for assertions. */
  log: Array<{ method: string; path: string; body: unknown }>;
}

const now = () => Date.now();

function nsKey(env: string, app: string): string {
  return `${env}/${app}`;
}

function sampleValue(contentType: string, version: number): string {
  switch (contentType) {
    case "integer":
      return String(version * 100);
    case "float":
      return `${version}.5`;
    case "boolean":
      return version % 2 === 0 ? "true" : "false";
    case "json":
      return JSON.stringify({ per_minute: version * 100 });
    default:
      return `value-${version}`;
  }
}

function digestOf(entries: ConfigurationReleaseEntry[]): string {
  const seed = entries.map((entry) => `${entry.alias}@${entry.version}`).join(",");
  let hash = 0;
  for (const ch of seed) hash = (hash * 31 + ch.charCodeAt(0)) >>> 0;
  return `sha256:${hash.toString(16).padStart(8, "0")}${"0".repeat(56)}`;
}

/** The incident fixture (prod degraded, one rejected instance, unreleased rate_limits) as live state. */
export function incidentState(): ConsoleState {
  const overview = incidentJson as unknown as ApplicationOverview;
  const state: ConsoleState = {
    application: { ...overview.application },
    namespaces: {},
    revision: 0,
    identity: { name: "admin", kind: "admin", auth_method: "token" },
    log: [],
  };
  for (const env of overview.environments) {
    const ns: FakeNamespace = {
      namespace: { ...env.namespace },
      parameters: {},
      secrets: {},
      releases: [],
      active: env.release.active?.version ?? 0,
      previous: env.release.active?.previous_version ?? 0,
      activationRevision: env.release.active?.activation_revision ?? 0,
      subscribers: [],
    };
    for (const value of env.values) {
      if (!value.present || !value.key) continue;
      const versions = Math.max(1, value.current_version ?? 1);
      if (value.kind === "parameter") {
        const contentType = value.content_type ?? "string";
        ns.parameters[value.key] = {
          key: value.key,
          content_type: contentType,
          versions: Array.from({ length: versions }, (_, i) => sampleValue(contentType, i + 1)),
        };
      } else {
        ns.secrets[value.key] = {
          key: value.key,
          versionCount: versions,
          bound: value.bound ?? false,
        };
      }
    }
    const active = env.release.active;
    const latest = env.release.latest_version;
    if (active) {
      for (let version = 1; version <= latest; version += 1) {
        const behind = active.version - version;
        const entries = active.entries.map((entry) => ({
          ...entry,
          version: Math.max(1, entry.version - Math.max(0, behind)),
        }));
        ns.releases.push({
          namespace: { env: env.namespace.env, app: env.namespace.app },
          name: active.name,
          version,
          schema_version: active.schema_version,
          entries,
          digest: version === active.version ? active.digest : digestOf(entries),
          metadata_json: "{}",
          created_by: active.created_by,
          created_at_unix_ms: active.created_at_unix_ms - (latest - version) * 60_000,
        });
      }
    }
    const rows: ReleaseSubscriberState[] = [];
    const releaseName = overview.application.release_name;
    const rejected = env.rollout.rejected_instances;
    for (const instance of rejected) rows.push(instanceRow(ns, releaseName, instance));
    const appliedCount = env.rollout.applied_current;
    for (let i = 0; i < appliedCount; i += 1) {
      rows.push(
        instanceRow(ns, releaseName, {
          identity: rejected[0]?.identity ?? "admin",
          client_name: rejected[0]?.client_name ?? "api",
          instance_id: `${env.namespace.env}-${i + 1}`,
          state: "applied",
          release_version: ns.active,
          activation_revision: ns.activationRevision,
          rejection_category: "",
          diagnostic: "",
          connected: true,
          server_timestamp_unix_ms: now(),
          applied_divergent: false,
          divergent_field_count: 0,
        }),
      );
    }
    ns.subscribers = rows;
    state.namespaces[env.namespace.env] = ns;
    state.revision = Math.max(state.revision, ns.activationRevision);
  }
  return state;
}

function instanceRow(
  ns: FakeNamespace,
  releaseName: string,
  instance: SubscriberInstance,
): ReleaseSubscriberState {
  return {
    namespace: { env: ns.namespace.env, app: ns.namespace.app },
    release_name: releaseName,
    client_name: instance.client_name,
    instance_id: instance.instance_id,
    identity: instance.identity,
    state: instance.state,
    release_version: instance.release_version,
    activation_revision: instance.activation_revision,
    rejection_category: instance.rejection_category,
    diagnostic: instance.diagnostic,
    client_timestamp_unix_ms: instance.server_timestamp_unix_ms,
    server_timestamp_unix_ms: instance.server_timestamp_unix_ms,
    applied_divergent: instance.applied_divergent,
    divergent_field_count: instance.divergent_field_count,
    connected: instance.connected,
  };
}

/** Every instance applies `release` at `revision`. */
export function allApplied(ctx: ActivationContext): ReleaseSubscriberState[] {
  return ctx.previous.map((row) => ({
    ...row,
    state: "applied",
    release_version: ctx.release.version,
    activation_revision: ctx.revision,
    rejection_category: "",
    diagnostic: "",
    server_timestamp_unix_ms: now(),
  }));
}

/** Every instance applies except `instanceId`, which rejects with `category`. */
export function oneRejected(
  instanceId: string,
  category: string,
  diagnostic: string,
): (ctx: ActivationContext) => ReleaseSubscriberState[] {
  return (ctx) =>
    allApplied(ctx).map((row) =>
      row.instance_id === instanceId
        ? {
            ...row,
            state: "rejected",
            release_version:
              ctx.previous.find((p) => p.instance_id === instanceId)?.release_version ??
              row.release_version,
            rejection_category: category,
            diagnostic,
          }
        : row,
    );
}

// --- read model -------------------------------------------------------------

function activeRelease(ns: FakeNamespace): ConfigurationRelease | null {
  return ns.releases.find((release) => release.version === ns.active) ?? null;
}

function rolloutOf(ns: FakeNamespace): OverviewRollout {
  const rollout: OverviewRollout = {
    total: ns.subscribers.length,
    connected: 0,
    applied_current: 0,
    applied_divergent: 0,
    rejected: 0,
    pending: 0,
    stale: 0,
    other_release_names: [],
    rejected_instances: [],
    truncated: false,
  };
  for (const row of ns.subscribers) {
    const atCurrent = row.activation_revision >= ns.activationRevision;
    const applied = row.state === "applied" && atCurrent;
    if (!row.connected) {
      if (!applied) rollout.stale += 1;
      continue;
    }
    rollout.connected += 1;
    if (applied) rollout.applied_current += 1;
    else if (row.state === "rejected" && atCurrent) {
      rollout.rejected += 1;
      rollout.rejected_instances.push({
        identity: row.identity,
        client_name: row.client_name,
        instance_id: row.instance_id,
        state: row.state,
        release_version: row.release_version,
        activation_revision: row.activation_revision,
        rejection_category: row.rejection_category,
        diagnostic: row.diagnostic,
        connected: row.connected,
        server_timestamp_unix_ms: row.server_timestamp_unix_ms,
        applied_divergent: row.applied_divergent,
        divergent_field_count: row.divergent_field_count,
      });
    } else rollout.pending += 1;
  }
  return rollout;
}

function environmentOverview(state: ConsoleState, ns: FakeNamespace): EnvironmentOverview {
  const env = ns.namespace.env;
  const production = /^prod(-|$)|^production$/.test(env);
  const active = activeRelease(ns);
  const pins = new Map(active?.entries.map((entry) => [entry.alias, entry.version]) ?? []);
  const findings: Finding[] = [];
  const values = state.application.contract.map((field) => {
    const parameter = ns.parameters[field.alias];
    const secret = ns.secrets[field.alias];
    const current = parameter?.versions.length ?? secret?.versionCount;
    const pinned = pins.get(field.alias);
    if (current !== undefined && pinned !== undefined && current !== pinned) {
      findings.push({
        code: "unreleased_changes",
        severity: "warning",
        scope: { env, alias: field.alias },
        params: { alias: field.alias, current, pinned },
      });
    }
    if (current === undefined) {
      findings.push({
        code: "resource_missing",
        severity: "blocking",
        scope: { env, alias: field.alias },
        params: { alias: field.alias, kind: field.kind },
      });
    }
    return {
      alias: field.alias,
      kind: field.kind,
      key: current === undefined ? undefined : field.alias,
      present: current !== undefined,
      content_type: field.kind === "parameter" ? parameter?.content_type : "text/plain",
      current_version: current,
      pinned_version: pinned,
      bound: secret?.bound,
    };
  });
  const rollout = rolloutOf(ns);
  for (const instance of rollout.rejected_instances) {
    findings.push({
      code: "instance_rejected",
      severity: "warning",
      scope: { env, instance: `${instance.client_name}/${instance.instance_id}` },
      params: { category: instance.rejection_category },
    });
  }
  if (!active) {
    findings.push({ code: "no_active_release", severity: "warning", scope: { env }, params: {} });
  }
  if (production) {
    findings.push({ code: "production", severity: "info", scope: { env }, params: {} });
  }
  const missing = values.some((value) => !value.present);
  const drift = values.some(
    (value) => value.pinned_version !== undefined && value.current_version !== value.pinned_version,
  );
  const status: EnvStatus = missing
    ? values.every((value) => !value.present)
      ? "empty"
      : "incomplete"
    : !active
      ? "unreleased"
      : rollout.rejected > 0
        ? "degraded"
        : rollout.pending > 0
          ? "rolling"
          : drift
            ? "drift"
            : "ready";
  return {
    namespace: {
      ...ns.namespace,
      parameter_count: Object.keys(ns.parameters).length,
      secret_count: Object.keys(ns.secrets).length,
    },
    production,
    status,
    values_state: missing ? (status === "empty" ? "empty" : "incomplete") : "complete",
    release_state: !active ? "none" : drift ? "drift" : "active",
    rollout_state:
      rollout.total === 0
        ? "no_subscribers"
        : rollout.rejected > 0
          ? "degraded"
          : rollout.pending > 0
            ? "rolling"
            : rollout.stale > 0
              ? "stale"
              : "applied",
    values,
    release: {
      active: active
        ? {
            name: active.name,
            version: active.version,
            activation_revision: ns.activationRevision,
            previous_version: ns.previous,
            created_by: active.created_by,
            created_at_unix_ms: active.created_at_unix_ms,
            is_rolled_back: ns.previous > active.version,
            schema_version: active.schema_version,
            digest: active.digest,
            entries: active.entries,
          }
        : undefined,
      latest_version: ns.releases.reduce((max, release) => Math.max(max, release.version), 0),
      release_count: ns.releases.length,
    },
    rollout,
    findings,
  };
}

function applicationOverview(state: ConsoleState): ApplicationOverview {
  const environments = Object.values(state.namespaces).map((ns) => environmentOverview(state, ns));
  const rows = state.application.contract.map((field) => ({
    key: field.alias,
    kind: field.kind,
    environments: Object.fromEntries(
      Object.values(state.namespaces).map((ns) => {
        const parameter = ns.parameters[field.alias];
        const secret = ns.secrets[field.alias];
        return [
          ns.namespace.env,
          parameter
            ? {
                present: true,
                value: parameter.versions[parameter.versions.length - 1],
                content_type: parameter.content_type,
                version: parameter.versions.length,
              }
            : secret
              ? {
                  present: true,
                  content_type: "",
                  version: secret.versionCount,
                  bound: secret.bound,
                  has_access_token: false,
                }
              : { present: false, content_type: "", version: 0 },
        ];
      }),
    ),
  }));
  const status = environments.some((env) => env.status === "blocked")
    ? "blocked"
    : environments.length === 0 ||
        environments.every((env) => ["empty", "incomplete", "unreleased"].includes(env.status))
      ? "setup"
      : environments.some((env) => env.status !== "ready")
        ? "attention"
        : "ready";
  return {
    application: { ...state.application, environment_count: environments.length },
    status,
    findings: environments.flatMap((env) => env.findings),
    environments,
    rows,
    schema_json: (incidentJson as { schema_json?: string }).schema_json,
  };
}

// --- ship ---------------------------------------------------------------------

function buildCandidate(
  state: ConsoleState,
  ns: FakeNamespace,
  changes: ShipChange[],
  written: Map<string, number>,
): { release: ConfigurationRelease; entries: ShipPreviewEntry[]; valid: boolean } {
  const active = activeRelease(ns);
  const pins = new Map(active?.entries.map((entry) => [entry.alias, entry.version]) ?? []);
  const byAlias = new Map(changes.map((change) => [change.alias, change]));
  const entries: ShipPreviewEntry[] = [];
  const releaseEntries: ConfigurationReleaseEntry[] = [];
  let valid = true;
  for (const field of state.application.contract) {
    const parameter = ns.parameters[field.alias];
    const secret = ns.secrets[field.alias];
    const current = parameter?.versions.length ?? secret?.versionCount;
    const from = pins.get(field.alias) ?? current;
    const change = byAlias.get(field.alias);
    let to: number | undefined;
    let kind: ShipPreviewEntry["change"] = "included";
    if (change?.value !== undefined) {
      to = written.get(field.alias) ?? (current ?? 0) + 1;
      kind = "edited";
    } else if (change?.version !== undefined) {
      to = change.version;
      kind = "pinned";
    } else if (change?.label === "current") {
      to = current;
      kind = "included";
    } else {
      to = active ? pins.get(field.alias) : current;
    }
    if (to === undefined || current === undefined) {
      kind = "missing";
      valid = false;
    }
    entries.push({
      alias: field.alias,
      kind: field.kind,
      key: field.alias,
      from_version: from,
      to_version: to,
      change: kind,
    });
    releaseEntries.push({
      alias: field.alias,
      kind: field.kind,
      ref: { namespace: { env: ns.namespace.env, app: ns.namespace.app }, key: field.alias },
      version: to ?? 0,
      content_type: field.kind === "parameter" ? (parameter?.content_type ?? "") : "",
      metadata_json: "{}",
      parameter_digest: field.kind === "parameter" ? `sha256:${field.alias}-${to}` : "",
    });
  }
  const latest = ns.releases.reduce((max, release) => Math.max(max, release.version), 0);
  const release: ConfigurationRelease = {
    namespace: { env: ns.namespace.env, app: ns.namespace.app },
    name: state.application.release_name,
    version: latest + 1,
    schema_version: state.application.schema_version,
    entries: releaseEntries,
    digest: digestOf(releaseEntries),
    metadata_json: JSON.stringify({ source: "console.ship" }),
    created_by: state.identity.name,
    created_at_unix_ms: now(),
  };
  return { release, entries, valid };
}

function validateCandidate(
  state: ConsoleState,
  ns: FakeNamespace,
  release: ConfigurationRelease,
  structurallyValid: boolean,
): ValidateReleaseResponse {
  if (!structurallyValid) {
    const errors: ReleaseValidationError[] = release.entries
      .filter((entry) => entry.version === 0)
      .map((entry) => ({
        alias: entry.alias,
        code: "resource_missing",
        schema_pointer: "",
        message: `no resource for alias ${entry.alias}`,
      }));
    return { valid: false, errors };
  }
  return state.validate?.(release, { env: ns.namespace.env }) ?? { valid: true, errors: [] };
}

function activate(
  state: ConsoleState,
  ns: FakeNamespace,
  release: ConfigurationRelease,
  kind: ActivationContext["kind"],
): { activation_revision: number; previous_version: number; changed: boolean } {
  if (ns.active === release.version) {
    return {
      activation_revision: ns.activationRevision,
      previous_version: ns.previous,
      changed: false,
    };
  }
  state.revision += 1;
  const previous = ns.active;
  ns.previous = previous;
  ns.active = release.version;
  ns.activationRevision = state.revision;
  const ctx: ActivationContext = {
    env: ns.namespace.env,
    release,
    revision: state.revision,
    kind,
    previous: ns.subscribers,
  };
  ns.subscribers = (state.onActivate ?? allApplied)(ctx);
  return { activation_revision: state.revision, previous_version: previous, changed: true };
}

function ship(state: ConsoleState, request: ShipRequest): { status: number; body: unknown } {
  const ns = state.namespaces[request.environment];
  if (!ns || request.application !== state.application.name) {
    return error(404, "not_found", "namespace not found");
  }
  for (const change of request.changes ?? []) {
    const field = state.application.contract.find((entry) => entry.alias === change.alias);
    if (!field)
      return error(400, "invalid_argument", `alias ${change.alias} is not in the contract`);
    if (field.kind === "secret" && change.value !== undefined) {
      return error(400, "invalid_argument", "secret values are never shipped");
    }
  }
  if (
    request.expected_active_version !== undefined &&
    request.expected_active_version !== ns.active
  ) {
    return error(
      409,
      "aborted",
      `active version is ${ns.active}, expected ${request.expected_active_version}`,
    );
  }
  const written = new Map<string, number>();
  const candidate = buildCandidate(state, ns, request.changes ?? [], written);
  const validation = validateCandidate(state, ns, candidate.release, candidate.valid);
  const preview: ShipPreview = {
    base_version: ns.active,
    release_name: state.application.release_name,
    schema_version: state.application.schema_version,
    entries: candidate.entries,
    validation,
    warnings: [],
  };
  if (request.dry_run) {
    return {
      status: 200,
      body: { status: "preview", preview, parameters: [] } satisfies ShipResult,
    };
  }
  if (!validation.valid) {
    return {
      status: 200,
      body: {
        status: "rejected",
        preview,
        parameters: [],
        error: {
          code: "failed_precondition",
          message: "the candidate release is invalid",
          validation_errors: validation.errors,
        },
      } satisfies ShipResult,
    };
  }
  // Write values.
  const parameters: ShipResult["parameters"] = [];
  for (const change of request.changes ?? []) {
    if (change.value === undefined) continue;
    const field = state.application.contract.find((entry) => entry.alias === change.alias);
    let parameter = ns.parameters[change.alias];
    if (!parameter) {
      parameter = {
        key: change.alias,
        content_type: change.content_type ?? field?.content_type ?? "string",
        versions: [],
      };
      ns.parameters[change.alias] = parameter;
    }
    parameter.versions.push(change.value);
    state.revision += 1;
    written.set(change.alias, parameter.versions.length);
    parameters.push({
      alias: change.alias,
      key: change.alias,
      version: parameter.versions.length,
      revision: state.revision,
    });
  }
  const final = buildCandidate(state, ns, request.changes ?? [], written);
  ns.releases.push(final.release);
  preview.entries = final.entries;
  const release = {
    name: final.release.name,
    version: final.release.version,
    digest: final.release.digest,
  };
  const forced = state.shipOutcome?.(request, { env: ns.namespace.env }) ?? null;
  if (forced === "conflict") {
    return {
      status: 200,
      body: {
        status: "conflict",
        preview,
        parameters,
        release,
        error: {
          code: "aborted",
          message: "the active release changed while shipping",
          current_version: ns.active,
        },
      } satisfies ShipResult,
    };
  }
  if (forced === "release_created_not_activated") {
    return {
      status: 200,
      body: {
        status: "release_created_not_activated",
        preview,
        parameters,
        release,
        error: {
          code: "failed_precondition",
          message: "activation re-validation failed",
          validation_errors: [],
        },
      } satisfies ShipResult,
    };
  }
  const activation = activate(state, ns, final.release, "ship");
  return {
    status: 200,
    body: { status: "activated", preview, parameters, release, activation } satisfies ShipResult,
  };
}

// --- routing ------------------------------------------------------------------

function error(status: number, code: string, message: string): { status: number; body: unknown } {
  return { status, body: { error: { code, message } } };
}

function summaries(ns: FakeNamespace, name?: string): ReleaseSummary[] {
  return ns.releases
    .filter((release) => !name || release.name === name)
    .sort((a, b) => b.version - a.version)
    .map((release) => ({
      release,
      current: release.version === ns.active,
      previous: release.version === ns.previous && ns.previous !== ns.active,
      activation_revision: release.version === ns.active ? ns.activationRevision : 0,
    }));
}

function handle(
  state: ConsoleState,
  method: string,
  path: string,
  params: URLSearchParams,
  body: unknown,
): { status: number; body: unknown } {
  const env = params.get("env") ?? "";
  const app = params.get("app") ?? "";
  const ns = state.namespaces[env];
  const nsFor = (e: string) => state.namespaces[e];
  const b = (body ?? {}) as Record<string, unknown>;

  switch (`${method} ${path}`) {
    case "GET /whoami":
      return { status: 200, body: { ...state.identity, namespace: null } };
    case "GET /health":
      return {
        status: 200,
        body: {
          healthy: true,
          ready: true,
          version: "e2e",
          current_revision: state.revision,
          grpc_addr: "127.0.0.1:7443",
          tls_enabled: true,
          admin_client_cert_required: false,
          client_cert_presented: false,
        },
      };
    case "GET /namespaces":
      return {
        status: 200,
        body: {
          namespaces: Object.values(state.namespaces).map((entry) => entry.namespace),
          next_page_token: "",
        },
      };
    case "GET /applications":
      return {
        status: 200,
        body: {
          applications: [applicationOverview(state).application],
          next_page_token: "",
        },
      };
    case "GET /applications/get":
      return params.get("name") === state.application.name
        ? { status: 200, body: { application: applicationOverview(state).application } }
        : error(404, "not_found", "application not found");
    case "GET /applications/overview": {
      const name = params.get("name");
      if (!name) {
        const overview = applicationOverview(state);
        return {
          status: 200,
          body: {
            applications: [
              {
                application: overview.application,
                status: overview.status,
                environments: overview.environments.map((entry) => ({
                  env: entry.namespace.env,
                  status: entry.status,
                  production: entry.production,
                })),
              },
            ],
          },
        };
      }
      if (name !== state.application.name) return error(404, "not_found", "application not found");
      return { status: 200, body: applicationOverview(state) };
    }
    case "GET /applications/dashboard": {
      const overview = applicationOverview(state);
      return {
        status: 200,
        body: {
          application: overview.application,
          environments: overview.environments.map((entry) => entry.namespace),
          rows: overview.rows,
        },
      };
    }
    case "POST /applications/ship":
      return ship(state, b as unknown as ShipRequest);
    case "GET /parameters": {
      if (!ns) return error(404, "not_found", "namespace not found");
      return {
        status: 200,
        body: {
          parameters: Object.values(ns.parameters).map((parameter) => parameterOf(ns, parameter)),
          next_page_token: "",
        },
      };
    }
    case "GET /parameters/get": {
      const parameter = ns?.parameters[params.get("key") ?? ""];
      if (!ns || !parameter) return error(404, "not_found", "parameter not found");
      const version = Number(params.get("version") ?? parameter.versions.length);
      return { status: 200, body: { parameter: parameterOf(ns, parameter, version) } };
    }
    case "GET /secrets":
      if (!ns) return error(404, "not_found", "namespace not found");
      return {
        status: 200,
        body: {
          secrets: Object.values(ns.secrets).map((secret) => ({
            env,
            app,
            key: secret.key,
            content_type: "text/plain",
            bound: secret.bound,
            has_access_token: false,
            metadata_json: "{}",
            created_at_unix_ms: 1,
            updated_at_unix_ms: 1,
            labels: { current: secret.versionCount },
            versions: [],
          })),
          next_page_token: "",
        },
      };
    case "GET /releases":
      if (!ns) return error(404, "not_found", "namespace not found");
      return {
        status: 200,
        body: { releases: summaries(ns, params.get("name") ?? undefined), next_page_token: "" },
      };
    case "GET /releases/get": {
      const release = ns?.releases.find(
        (entry) =>
          entry.name === params.get("name") && entry.version === Number(params.get("version")),
      );
      return release
        ? { status: 200, body: { release } }
        : error(404, "not_found", "release not found");
    }
    case "GET /releases/active": {
      const release = ns ? activeRelease(ns) : null;
      if (!ns || !release || release.name !== params.get("name")) {
        return error(404, "not_found", "no active release");
      }
      return {
        status: 200,
        body: {
          release,
          activation_revision: ns.activationRevision,
          previous_version: ns.previous,
        },
      };
    }
    case "POST /releases/validate":
    case "POST /releases/activate":
    case "POST /releases/rollback": {
      const target = (b.namespace as { env: string; app: string } | undefined) ?? {
        env: String(b.env ?? ""),
        app: String(b.app ?? ""),
      };
      const space = nsFor(target.env);
      if (!space) return error(404, "not_found", "namespace not found");
      const name = String(b.name ?? "");
      if (path === "/releases/rollback") {
        const expected = b.expected_current_version as number | undefined;
        if (expected !== undefined && expected !== space.active) {
          return error(409, "aborted", `active version is ${space.active}, expected ${expected}`);
        }
        const previous = space.releases.find(
          (entry) => entry.name === name && entry.version === space.previous,
        );
        if (!previous) return error(412, "failed_precondition", "no previous release");
        const validation = validateCandidate(state, space, previous, true);
        if (!validation.valid) {
          return {
            status: 412,
            body: {
              error: {
                code: "failed_precondition",
                message: "the previous release is invalid",
                validation_errors: validation.errors,
              },
            },
          };
        }
        const from = space.active;
        const activation = activate(state, space, previous, "rollback");
        return {
          status: 200,
          body: {
            release: previous,
            activation_revision: activation.activation_revision,
            previous_version: activation.previous_version,
            rolled_back_from: from,
            changed: activation.changed,
          },
        };
      }
      const version = Number(b.version ?? 0);
      const release = space.releases.find(
        (entry) => entry.name === name && entry.version === version,
      );
      if (!release) return error(404, "not_found", "release not found");
      const validation = validateCandidate(state, space, release, true);
      if (path === "/releases/validate") return { status: 200, body: validation };
      if (!validation.valid) {
        return {
          status: 412,
          body: {
            error: {
              code: "failed_precondition",
              message: "release is invalid",
              validation_errors: validation.errors,
            },
          },
        };
      }
      const expected = b.expected_current_version as number | undefined;
      if (expected !== undefined && expected !== space.active) {
        return error(409, "aborted", `active version is ${space.active}, expected ${expected}`);
      }
      const activation = activate(state, space, release, "activate");
      return { status: 200, body: { release, ...activation } };
    }
    case "GET /release-subscribers":
      if (!ns) return error(404, "not_found", "namespace not found");
      return {
        status: 200,
        body: {
          subscribers: ns.subscribers.filter((row) => row.release_name === params.get("name")),
          current_revision: state.revision,
          next_page_token: "",
        },
      };
    case "GET /release-subscribers/stream":
      return error(404, "not_found", "no stream on this server");
    case "GET /configuration-schemas":
      return {
        status: 200,
        body: {
          schemas: state.application.schema_version
            ? [
                {
                  application: state.application.name,
                  release_name: state.application.release_name,
                  version: state.application.schema_version,
                  schema_json: (incidentJson as { schema_json?: string }).schema_json ?? "{}",
                  digest: "sha256:schema",
                  metadata_json: "{}",
                  created_by: "admin",
                  created_at_unix_ms: 1,
                },
              ]
            : [],
          next_page_token: "",
        },
      };
    case "GET /audit":
      return { status: 200, body: { events: [], next_page_token: "" } };
    case "GET /subscribers":
      return { status: 200, body: { subscribers: [], current_revision: state.revision } };
    case "GET /keys":
      return { status: 200, body: { keys: [] } };
    default:
      return error(404, "not_found", `no fake for ${method} ${path}`);
  }
}

function parameterOf(ns: FakeNamespace, parameter: FakeParameter, version?: number) {
  const v = version ?? parameter.versions.length;
  return {
    env: ns.namespace.env,
    app: ns.namespace.app,
    key: parameter.key,
    value: parameter.versions[v - 1] ?? "",
    content_type: parameter.content_type,
    version: v,
    metadata_json: "{}",
    created_by: "admin",
    created_at_unix_ms: 1,
    labels: { current: parameter.versions.length },
  };
}

/**
 * Installs the fake for every `/api/v1/**` request on `page` and signs the
 * console in as `state.identity`. `state` is shared by reference: mutate it
 * (or read `state.log`) from the test at any point.
 */
export async function mockConsole(page: Page, state: ConsoleState): Promise<ConsoleState> {
  await page.addInitScript(() => {
    sessionStorage.setItem("kms_token", "kms_e2e_token");
  });
  await page.route("**/api/v1/**", async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^.*\/api\/v1/, "");
    let body: unknown = null;
    const raw = request.postData();
    if (raw) {
      try {
        body = JSON.parse(raw);
      } catch {
        body = raw;
      }
    }
    state.log.push({ method: request.method(), path, body });
    const result = handle(state, request.method(), path, url.searchParams, body);
    await route.fulfill({
      status: result.status,
      contentType: "application/json",
      body: JSON.stringify(result.body),
    });
  });
  return state;
}

export { nsKey };
