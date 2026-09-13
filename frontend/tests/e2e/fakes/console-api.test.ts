import { describe, expect, it } from "vitest";
import type { ReleaseDiffResponse } from "../../../lib/types";
import {
  allApplied,
  type FakeSecret,
  handle,
  handleFakeConsoleRequest,
  incidentState,
  oneRejected,
} from "./console-api";

describe("console API fake application evidence", () => {
  it("keeps the rejected attempt separate from the last applied target and recovers on rollback", () => {
    const state = incidentState();
    const ns = state.namespaces.prod;
    const release = ns.releases.find((row) => row.version === ns.active);
    if (!release) throw new Error("Missing active fixture release");
    const previous = allApplied({
      env: "prod",
      release,
      revision: ns.activationRevision,
      kind: "activate",
      previous: ns.subscribers,
    });
    const candidate = { ...release, version: release.version + 1 };
    const rejected = oneRejected(
      previous[0].instance_id,
      "config_validation_failed",
      "invalid",
    )({
      env: "prod",
      release: candidate,
      revision: ns.activationRevision + 1,
      kind: "ship",
      previous,
    });
    expect(rejected[0]).toMatchObject({
      state: "rejected",
      release_version: candidate.version,
      desired_version: candidate.version,
      last_applied_version: release.version,
      last_applied_revision: ns.activationRevision,
      last_applied_sequence: previous[0].sequence,
    });
    const recovered = allApplied({
      env: "prod",
      release,
      revision: ns.activationRevision + 2,
      kind: "rollback",
      previous: rejected,
    });
    expect(recovered[0]).toMatchObject({
      state: "applied",
      release_version: release.version,
      last_applied_version: release.version,
      last_applied_revision: ns.activationRevision + 2,
      rejection_category: "",
      diagnostic: "",
    });
  });

  it("does not invent prior application from a rejected attempt", () => {
    const ns = incidentState().namespaces.prod;
    const previous = ns.subscribers.filter((row) => row.state === "rejected");
    const release = ns.releases.find((row) => row.version === ns.active);
    if (!release || previous.length !== 1) throw new Error("Missing rejected fixture");
    const result = oneRejected(
      previous[0].instance_id,
      "config_validation_failed",
      "invalid",
    )({
      env: "prod",
      release,
      revision: ns.activationRevision + 1,
      kind: "ship",
      previous,
    });
    expect(result[0].last_applied_version).toBe(0);
    expect(result[0].last_applied_revision).toBe(0);
  });
});

const keyA = "binding-key-a-0123456789-0123456789";
const keyB = "binding-key-b-0123456789-0123456789";

function version(number: number, bound: boolean, bindingKey: string | undefined) {
  return {
    version: number,
    state: "enabled" as const,
    bound,
    bindingKey,
    valueBase64: `value-${number}`,
    metadataJson: "{}",
    expiresAtUnixMs: 0,
    createdAtUnixMs: number,
  };
}

function installSecret(secret: FakeSecret) {
  const state = incidentState();
  state.namespaces.prod.secrets[secret.key] = secret;
  return state;
}

describe("console API fake binding fidelity", () => {
  it("proves the old rotation key before rejecting an identical replacement", () => {
    const state = installSecret({
      key: "rotate-order",
      versionCount: 1,
      currentVersion: 1,
      bound: true,
      bindingKey: keyA,
      versions: [version(1, true, keyA)],
    });
    const request = {
      env: "prod",
      app: "gradethis",
      key: "rotate-order",
      expected_current_version: 1,
    };

    const wrong = handleFakeConsoleRequest(state, "POST", "/secrets/binding-key/rotate", {
      ...request,
      binding_key: keyB,
      new_binding_key: keyB,
    });
    expect(wrong).toMatchObject({ status: 500, body: { error: { code: "internal" } } });
    expect(state.namespaces.prod.secrets["rotate-order"].versionCount).toBe(1);

    const unchanged = handleFakeConsoleRequest(state, "POST", "/secrets/binding-key/rotate", {
      ...request,
      binding_key: keyA,
      new_binding_key: keyA,
    });
    expect(unchanged).toMatchObject({
      status: 400,
      body: { error: { code: "invalid_argument" } },
    });
    expect(state.namespaces.prod.secrets["rotate-order"].versionCount).toBe(1);
  });

  it("preserves historical bound versions when purging the unbound current version", () => {
    const state = installSecret({
      key: "purge-current",
      versionCount: 2,
      currentVersion: 2,
      previousVersion: 1,
      bound: false,
      versions: [version(1, true, keyA), version(2, false, undefined)],
    });
    const request = { env: "prod", app: "gradethis", key: "purge-current" };
    const preview = handleFakeConsoleRequest(
      state,
      "POST",
      "/secrets/unbound-versions/preview",
      request,
    );
    expect(preview).toMatchObject({
      status: 200,
      body: { affected_versions: [2], revision: state.revision },
    });
    const revision = (preview.body as { revision: number }).revision;

    const purged = handleFakeConsoleRequest(state, "POST", "/secrets/unbound-versions/purge", {
      ...request,
      expected_revision: revision,
      expected_affected_versions: [2],
    });
    expect(purged.status).toBe(200);
    const secret = state.namespaces.prod.secrets["purge-current"];
    expect(secret.versions?.[0]).toMatchObject({ state: "enabled", bound: true });
    expect(secret.versions?.[1]).toMatchObject({ state: "destroyed", bound: false });

    const put = handleFakeConsoleRequest(state, "POST", "/secrets", {
      ...request,
      value_base64: "replacement",
      content_type: "text/plain",
    });
    expect(put).toMatchObject({ status: 200, body: { version: 3 } });
  });

  it("requires an exact bound-cohort preview guard and aborts stale guards atomically", () => {
    const state = installSecret({
      key: "purge-cas",
      versionCount: 2,
      currentVersion: 2,
      previousVersion: 1,
      bound: true,
      bindingKey: keyA,
      versions: [version(1, true, keyA), version(2, true, keyA)],
    });
    const request = {
      env: "prod",
      app: "gradethis",
      key: "purge-cas",
      anchor_version: 2,
      binding_key: keyA,
    };

    const missing = handleFakeConsoleRequest(
      state,
      "POST",
      "/secrets/binding-cohort/purge",
      request,
    );
    expect(missing).toMatchObject({
      status: 400,
      body: { error: { code: "invalid_argument" } },
    });

    const preview = handleFakeConsoleRequest(
      state,
      "POST",
      "/secrets/binding-cohort/preview",
      request,
    );
    const revision = (preview.body as { revision: number }).revision;
    state.revision += 1;
    const stale = handleFakeConsoleRequest(state, "POST", "/secrets/binding-cohort/purge", {
      ...request,
      expected_revision: revision,
      expected_affected_versions: [1, 2],
    });
    expect(stale).toMatchObject({ status: 409, body: { error: { code: "aborted" } } });
    expect(state.namespaces.prod.secrets["purge-cas"].versions).toEqual([
      version(1, true, keyA),
      version(2, true, keyA),
    ]);
  });

  it("creates a bound version when binding the unbound current version", () => {
    const state = installSecret({
      key: "transition-binding",
      versionCount: 1,
      currentVersion: 1,
      bound: false,
      versions: [version(1, false, undefined)],
    });

    const result = handleFakeConsoleRequest(state, "POST", "/secrets/bind", {
      env: "prod",
      app: "gradethis",
      key: "transition-binding",
      expected_current_version: 1,
      binding_key: keyA,
    });

    expect(result).toMatchObject({
      status: 200,
      body: { current_version: 2, previous_version: 1 },
    });
    const secret = state.namespaces.prod.secrets["transition-binding"];
    expect(secret.versions?.[1]).toMatchObject({ bound: true });
  });
});

it("isolates application contract mutations between console fixtures", () => {
  const first = incidentState();
  const original = structuredClone(first.application.contract);
  first.application.contract.push({
    alias: "test_only",
    kind: "parameter",
    content_type: "string",
  });
  first.application.contract[0].alias = "changed";
  expect(incidentState().application.contract).toEqual(original);
});

describe("console API fake release diff", () => {
  const diff = (state: ReturnType<typeof incidentState>, query: string) =>
    handle(state, "GET", "/releases/diff", new URLSearchParams(query), null);

  it("compares previous to current with values on the changed row and none on the secret", () => {
    const state = incidentState();
    const result = diff(
      state,
      "env=prod&app=gradethis&name=runtime&schema_version=1&from=previous&to=current",
    );
    expect(result.status).toBe(200);
    const body = result.body as ReleaseDiffResponse;
    expect(body.from).toMatchObject({ version: 1, previous: true, current: false });
    expect(body.to).toMatchObject({ version: 2, current: true, activation_revision: 12 });
    expect(body.identical).toBe(false);
    expect(body.cross_environment).toBe(false);
    expect(body.values_included).toBe(true);
    expect(body.rows.map((row) => row.alias)).toEqual(["database", "db_password", "rate_limits"]);

    const changed = body.rows.find((row) => row.alias === "rate_limits");
    expect(changed).toMatchObject({ change: "changed", kind: "parameter", reasons: ["value"] });
    // v1 pins rate_limits@2, v2 pins rate_limits@3: the fake's sample values.
    expect(changed?.from).toMatchObject({ version: 2, value_state: "present", value: "200" });
    expect(changed?.to).toMatchObject({ version: 3, value_state: "present", value: "300" });
    expect(changed?.from?.parameter_digest).not.toBe(changed?.to?.parameter_digest);

    const secret = body.rows.find((row) => row.alias === "db_password");
    expect(secret).toMatchObject({ change: "unchanged", kind: "secret" });
    expect(secret?.to).toMatchObject({
      value_state: "secret",
      bound: false,
      secret_state: "enabled",
    });
    expect(secret?.to).not.toHaveProperty("value");

    const unchanged = body.rows.find((row) => row.alias === "database");
    expect(unchanged?.to).toMatchObject({ value_state: "omitted_unchanged", value_bytes: 0 });
    expect(body.counts).toEqual({
      added: 0,
      removed: 0,
      changed: 1,
      unchanged: 2,
      secrets_changed: 0,
      attention: 0,
    });
  });

  it("returns entries only for values=0 and rejects other values", () => {
    const state = incidentState();
    const result = diff(
      state,
      "env=prod&app=gradethis&name=runtime&schema_version=1&from=1&to=2&values=0",
    );
    const body = result.body as ReleaseDiffResponse;
    expect(body.values_included).toBe(false);
    const changed = body.rows.find((row) => row.alias === "rate_limits");
    expect(changed?.to).toMatchObject({ value_state: "omitted_request" });
    expect(changed?.to).not.toHaveProperty("value");
    expect(
      diff(state, "env=prod&app=gradethis&name=runtime&schema_version=1&from=1&to=2&values=2")
        .status,
    ).toBe(400);
  });

  it("names the missing side, fails the previous label before any rollback, and refuses equal sides", () => {
    const state = incidentState();
    const missing = diff(state, "env=prod&app=gradethis&name=runtime&schema_version=1&from=9&to=2");
    expect(missing).toMatchObject({
      status: 404,
      body: { error: { code: "not_found", message: "from release runtime@1:9 not found" } },
    });
    const noPrevious = diff(
      state,
      "env=dev&app=gradethis&name=runtime&schema_version=1&from=previous&to=current",
    );
    expect(noPrevious).toMatchObject({
      status: 412,
      body: { error: { code: "failed_precondition", message: "no previous release" } },
    });
    expect(
      diff(state, "env=prod&app=gradethis&name=runtime&schema_version=1&from=2&to=2").status,
    ).toBe(400);
    expect(diff(state, "env=prod&app=gradethis&name=runtime&from=1&to=2").status).toBe(400);
  });

  it("compares across environments by alias and does not count equal-digest repins", () => {
    const state = incidentState();
    // dev's active v1 pins rate_limits@2 with a different stored value than prod's v2 pins.
    const result = diff(
      state,
      "env=prod&app=gradethis&name=runtime&schema_version=1&from=current&to=current&to_env=dev",
    );
    expect(result.status).toBe(200);
    const body = result.body as ReleaseDiffResponse;
    expect(body.cross_environment).toBe(true);
    expect(body.to.namespace).toEqual({ env: "dev", app: "gradethis" });
    const rate = body.rows.find((row) => row.alias === "rate_limits");
    expect(rate).toMatchObject({ change: "changed", reasons: ["value"] });
    // Same pinned versions and stored values on both sides: not a change.
    const database = body.rows.find((row) => row.alias === "database");
    expect(database?.change).toBe("unchanged");
    const secret = body.rows.find((row) => row.alias === "db_password");
    expect(secret?.change).toBe("unchanged");
  });

  it("omits a value over the cap and reports its size", () => {
    const state = incidentState();
    const prod = state.namespaces.prod;
    prod.parameters.rate_limits.versions[2] = "x".repeat(300 * 1024);
    const result = diff(state, "env=prod&app=gradethis&name=runtime&schema_version=1&from=1&to=2");
    const body = result.body as ReleaseDiffResponse;
    const changed = body.rows.find((row) => row.alias === "rate_limits");
    expect(changed?.to).toMatchObject({ value_state: "omitted_size", value_bytes: 300 * 1024 });
    expect(changed?.to).not.toHaveProperty("value");
    expect(changed?.from).toMatchObject({ value_state: "present" });
  });
});
