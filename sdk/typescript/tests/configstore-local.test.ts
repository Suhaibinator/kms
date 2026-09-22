import assert from "node:assert/strict";
import { describe, expect, it, vi } from "vitest";
import {
  CandidateError,
  type ConfigManager,
  type ManagedConfigManager,
} from "../src/configstore/index.js";
import { Secret } from "../src/secret.js";
import { createLocalStore } from "./fixtures/configgen/config.generated.js";
import type { Config } from "./fixtures/configgen/config.js";

function config(): Config {
  return {
    enabled: true,
    limit: 10,
    epoch: 0n,
    payload: new Uint8Array([1, 2]),
    labels: { region: "west" },
    endpoint: { host: "localhost", ports: [5432], zones: ["west", "east"] },
    password: new Secret("secret-canary", { bindKey: "binding-canary" }),
  };
}
const managedLifecycle = (manager: ManagedConfigManager): ConfigManager => manager;
void managedLifecycle;

describe("local generated stores", () => {
  it("validates asynchronously and isolates input, validator and output references", async () => {
    const input = config();
    let retained: Config | undefined;
    const { store, manager } = await createLocalStore(input, async (candidate) => {
      await Promise.resolve();
      expect(candidate.password.bindKey).toBe("");
      retained = candidate;
      candidate.limit = 12;
      candidate.password = new Secret("validated-secret", { bindKey: "validator-key" });
    });
    assert(input.labels && input.payload && retained?.labels);
    input.labels.region = "changed";
    input.payload[0] = 9;
    retained.labels.region = "changed";
    retained.password = new Secret("changed");
    const snapshot = store.current();
    const returned = snapshot.config();
    expect(returned.limit).toBe(12);
    expect(returned.labels).toEqual({ region: "west" });
    expect(returned.payload).toEqual(new Uint8Array([1, 2]));
    expect(returned.password.text()).toBe("validated-secret");
    expect(returned.password.bindKey).toBe("");
    assert(returned.labels && returned.payload);
    returned.labels.region = "changed";
    returned.payload[0] = 8;
    expect(snapshot.config().labels?.region).toBe("west");
    expect(snapshot.config().payload?.[0]).toBe(1);
    expect(snapshot.databaseHealth().limit).toBe(12);
    expect(snapshot.release.isZero).toBe(true);
    const lifecycle: ConfigManager = manager;
    await lifecycle.waitUntilReady();
    expect(lifecycle.status()).toMatchObject({
      source: "local",
      state: "applied",
      ready: true,
      defaultDivergent: false,
      reconnects: 0n,
    });
    expect(lifecycle.status().applied.isZero).toBe(true);
    expect(lifecycle.stats()).toEqual({
      candidates: 1n,
      applied: 1n,
      rejected: {},
      reconnects: 0n,
      defaultDivergent: false,
      appliedReleaseVersion: 0n,
      appliedActivationRevision: 0n,
    });
    for (let i = 0; i < 2; i++) {
      lifecycle.stop();
      await lifecycle.wait();
    }
    expect(store.current().config().limit).toBe(12);
    const client = { createReleaseLoader: vi.fn() };
    await expect(
      store.start(client, { release: "runtime", onDefaultMismatch: () => undefined }),
    ).rejects.toThrow(/only be called once/);
    expect(client.createReleaseLoader).not.toHaveBeenCalled();
    expect(String(snapshot)).not.toContain("validated-secret");
  });
  it("rejects invalid codecs and validator results with classified, redacted errors", async () => {
    await expect(
      createLocalStore({ ...config(), limit: 1.5 }, () => undefined),
    ).rejects.toBeInstanceOf(CandidateError);
    await expect(
      createLocalStore(config(), (candidate) => {
        candidate.epoch = -1n;
      }),
    ).rejects.toBeInstanceOf(CandidateError);
    await expect(
      createLocalStore(config(), () => {
        throw new Error("secret-canary");
      }),
    ).rejects.toThrow("configstore: candidate rejected (config_validation_failed)");
    await expect(
      createLocalStore(config(), (candidate) => {
        candidate.password = "bad" as unknown as Secret;
      }),
    ).rejects.toBeInstanceOf(CandidateError);
  });
  it("leaves optional and required empty-secret policy to application validation", async () => {
    const { store } = await createLocalStore(
      { ...config(), password: new Secret() },
      () => undefined,
    );
    expect(store.current().config().password.isEmpty).toBe(true);
    await expect(
      createLocalStore({ ...config(), password: new Secret() }, (candidate) => {
        if (candidate.password.isEmpty) throw new Error("token required");
      }),
    ).rejects.toBeInstanceOf(CandidateError);
  });
});
