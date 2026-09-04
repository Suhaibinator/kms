import { describe, expect, it } from "vitest";
import { type FakeSecret, handleFakeConsoleRequest, incidentState } from "./console-api";

const keyA = "binding-key-a-0123456789-0123456789";
const keyB = "binding-key-b-0123456789-0123456789";

function version(
  number: number,
  bound: boolean,
  bindingKey: string | undefined,
  hasAccessToken: boolean,
) {
  return {
    version: number,
    state: "enabled" as const,
    bound,
    bindingKey,
    hasAccessToken,
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
      versions: [version(1, true, keyA, false)],
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

  it("retains the secret token credential when purging current but historical gated versions survive", () => {
    const state = installSecret({
      key: "purge-token",
      versionCount: 2,
      currentVersion: 2,
      previousVersion: 1,
      bound: false,
      hasAccessToken: true,
      versions: [version(1, true, keyA, true), version(2, false, undefined, true)],
    });
    const request = { env: "prod", app: "gradethis", key: "purge-token" };
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
    const secret = state.namespaces.prod.secrets["purge-token"];
    expect(secret.hasAccessToken).toBe(true);
    expect(secret.versions?.[0]).toMatchObject({ state: "enabled", hasAccessToken: true });
    expect(secret.versions?.[1]).toMatchObject({ state: "destroyed", hasAccessToken: false });

    const put = handleFakeConsoleRequest(state, "POST", "/secrets", {
      ...request,
      value_base64: "replacement",
      content_type: "text/plain",
    });
    expect(put).toMatchObject({ status: 200, body: { version: 3 } });
    expect(secret.hasAccessToken).toBe(true);
    expect(secret.versions?.[2]).toMatchObject({ state: "enabled", hasAccessToken: true });
  });

  it("requires an exact bound-cohort preview guard and aborts stale guards atomically", () => {
    const state = installSecret({
      key: "purge-cas",
      versionCount: 2,
      currentVersion: 2,
      previousVersion: 1,
      bound: true,
      bindingKey: keyA,
      versions: [version(1, true, keyA, false), version(2, true, keyA, false)],
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
      version(1, true, keyA, false),
      version(2, true, keyA, false),
    ]);
  });

  it("keeps the persistent token state when transitioning an ungated current version", () => {
    const state = installSecret({
      key: "transition-token",
      versionCount: 1,
      currentVersion: 1,
      bound: false,
      hasAccessToken: true,
      versions: [version(1, false, undefined, false)],
    });

    const result = handleFakeConsoleRequest(state, "POST", "/secrets/bind", {
      env: "prod",
      app: "gradethis",
      key: "transition-token",
      expected_current_version: 1,
      binding_key: keyA,
    });

    expect(result).toMatchObject({
      status: 200,
      body: { current_version: 2, previous_version: 1 },
    });
    const secret = state.namespaces.prod.secrets["transition-token"];
    expect(secret.hasAccessToken).toBe(true);
    expect(secret.versions?.[1]).toMatchObject({ bound: true, hasAccessToken: false });
  });
});
