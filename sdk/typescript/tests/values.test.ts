import { inspect } from "node:util";

import { afterEach, describe, expect, it, vi } from "vitest";

import { ConfigError, KmsError, NotInitializedError } from "../src/errors.js";
import { displayPath, type ResourceRef, resolveRef } from "../src/refs.js";
import { REDACTED, Secret } from "../src/secret.js";
import {
  collectDeclarativeValues,
  type ParameterUpdateHandler,
  ParameterValue,
  ResolutionError,
  resolveValues,
  type SecretReadOptions,
  SecretValue,
  type ValueReadOptions,
  type ValueResolver,
} from "../src/values.js";

class FakeResolver implements ValueResolver {
  readonly parameters = new Map<string, string>();
  readonly secrets = new Map<string, Secret>();
  readonly parameterCalls: Array<{ key: string; options: ValueReadOptions | undefined }> = [];
  readonly secretCalls: Array<{ key: string; options: SecretReadOptions | undefined }> = [];
  readonly handlers = new Map<string, ParameterUpdateHandler>();
  parameterError: Error | undefined;
  secretError: Error | undefined;
  fallbackToDefaultsOnError = false;
  registrationCount = 0;
  disposalCount = 0;
  callbackQueue: Array<() => void> | undefined;

  async getParameter(key: string, options?: ValueReadOptions): Promise<string> {
    this.parameterCalls.push({ key, options });
    if (this.parameterError !== undefined) throw this.parameterError;
    const value = this.parameters.get(key);
    if (value === undefined) throw new KmsError("not_found", "parameter absent");
    return value;
  }

  async getSecret(key: string, options?: SecretReadOptions): Promise<Secret> {
    this.secretCalls.push({ key, options });
    if (this.secretError !== undefined) throw this.secretError;
    const value = this.secrets.get(key);
    if (value === undefined) throw new KmsError("not_found", "secret absent");
    return value.clone();
  }

  _resolveRef(key: string): ResourceRef {
    return resolveRef(key, "prod/app");
  }

  _registerParameter(
    ref: ResourceRef,
    _initialValue: string,
    handler: ParameterUpdateHandler,
  ): () => void {
    this.registrationCount++;
    const path = displayPath(ref);
    this.handlers.set(path, handler);
    return () => {
      this.disposalCount++;
      this.handlers.delete(path);
    };
  }

  _enqueueCallback(callback: () => void): void {
    if (this.callbackQueue === undefined) callback();
    else this.callbackQueue.push(callback);
  }

  update(key: string, value: string, present = true): void {
    this.handlers.get(displayPath(resolveRef(key, "prod/app")))?.(value, present);
  }
}

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("SecretValue", () => {
  it("uses non-empty env override before store and redacts inspection", async () => {
    vi.stubEnv("SDK_TEST_SECRET", "from-env");
    const client = new FakeResolver();
    client.secrets.set("secret/key", new Secret("from-store"));
    const value = new SecretValue("secret/key", {
      envVar: "SDK_TEST_SECRET",
      token: "access-token-must-not-render",
      default: "default-must-not-render",
    });

    await value.init(client);

    expect(value.value()).toBe("from-env");
    expect(client.secretCalls).toHaveLength(0);
    for (const rendered of [String(value), inspect(value), JSON.stringify(value)]) {
      expect(rendered).toContain(REDACTED);
      expect(rendered).not.toContain("from-env");
      expect(rendered).not.toContain("access-token-must-not-render");
      expect(rendered).not.toContain("default-must-not-render");
    }
    expect(Object.keys(value)).toEqual([]);
  });

  it("fetches the store and forwards token and cancellation options", async () => {
    const client = new FakeResolver();
    client.secrets.set("secret/key", new Secret("from-store", { version: 7n }));
    const value = new SecretValue({ key: "secret/key", token: "token" });
    const controller = new AbortController();

    const deadline = new Date(Date.now() + 1_000);
    await value.init(client, { signal: controller.signal, deadline });

    expect(value.text()).toBe("from-store");
    expect(value.bytes()).toEqual(new TextEncoder().encode("from-store"));
    expect(value.secret()).not.toBe(client.secrets.get("secret/key"));
    expect(client.secretCalls[0]).toEqual({
      key: "secret/key",
      options: { signal: controller.signal, deadline, secretToken: "token" },
    });
  });

  it("uses a default for not-found but not for hard errors by default", async () => {
    const missingClient = new FakeResolver();
    const missing = new SecretValue("absent", { default: "development" });
    await missing.init(missingClient);
    expect(missing.value()).toBe("development");

    const unavailableClient = new FakeResolver();
    unavailableClient.secretError = new KmsError("unavailable", "store down");
    const unavailable = new SecretValue("flaky", { default: "development" });
    await expect(unavailable.init(unavailableClient)).rejects.toMatchObject({
      code: "unavailable",
    });
    expect(unavailable.initialized).toBe(false);
  });

  it("allows broad fallback only when opted in and never hides config errors", async () => {
    const client = new FakeResolver();
    client.fallbackToDefaultsOnError = true;
    client.secretError = new KmsError("unavailable", "store down");
    const flaky = new SecretValue("flaky", { default: "development" });
    await flaky.init(client);
    expect(flaky.value()).toBe("development");

    client.secretError = new KmsError("no_namespace", "unbound");
    const unbound = new SecretValue("relative", { default: "development" });
    await expect(unbound.init(client)).rejects.toMatchObject({ code: "no_namespace" });
  });

  it("throws a typed error before initialization and is idempotent", async () => {
    const client = new FakeResolver();
    client.secrets.set("key", new Secret("v1"));
    const value = new SecretValue("key");
    expect(() => value.value()).toThrow(NotInitializedError);
    await value.init(client);
    client.secrets.set("key", new Secret("v2"));
    await value.init(client);
    expect(value.value()).toBe("v1");
    expect(client.secretCalls).toHaveLength(1);
    expect(value.isInitialized()).toBe(true);
  });

  it("coalesces concurrent initialization", async () => {
    let release: ((secret: Secret) => void) | undefined;
    const pending = new Promise<Secret>((resolve) => {
      release = resolve;
    });
    let calls = 0;
    const client: ValueResolver = {
      getParameter: async () => "unused",
      getSecret: async () => {
        calls++;
        return pending;
      },
    };
    const value = new SecretValue("key");
    const first = value.init(client);
    const second = value.init(client);
    release?.(new Secret("resolved"));
    await Promise.all([first, second]);
    expect(calls).toBe(1);
    expect(value.value()).toBe("resolved");
  });

  it("rejects an entirely unconfigured or empty-default secret", async () => {
    const client = new FakeResolver();
    await expect(new SecretValue().init(client)).rejects.toBeInstanceOf(ConfigError);
    await expect(new SecretValue({ default: "" }).init(client)).rejects.toBeInstanceOf(ConfigError);
  });
});

describe("ParameterValue", () => {
  it("returns empty before initialization, then reads and hot-reloads", async () => {
    const client = new FakeResolver();
    client.parameters.set("rate", "10");
    const value = new ParameterValue("rate", { default: "5" });
    const changes: Array<[string, string]> = [];
    value.onChange((oldValue, newValue) => changes.push([oldValue, newValue]));

    expect(value.get()).toBe("");
    await value.init(client);
    expect(value.get()).toBe("10");
    expect(value.initialized).toBe(true);
    expect(client.registrationCount).toBe(1);

    client.update("rate", "20");
    client.update("rate", "20");
    expect(value.get()).toBe("20");
    expect(changes).toEqual([["10", "20"]]);

    client.update("rate", "deleted-value-is-ignored", false);
    expect(value.get()).toBe("5");
    expect(changes.at(-1)).toEqual(["20", "5"]);
  });

  it("preserves duplicate callback registrations like the Go SDK", async () => {
    const client = new FakeResolver();
    client.parameters.set("rate", "10");
    const value = new ParameterValue("rate");
    const callback = vi.fn();
    const removeFirst = value.onChange(callback);
    value.onChange(callback);
    await value.init(client);
    client.update("rate", "20");
    expect(callback).toHaveBeenCalledTimes(2);
    removeFirst();
    client.update("rate", "30");
    expect(callback).toHaveBeenCalledTimes(3);
  });

  it("fences queued callbacks after unsubscribe and disposal", async () => {
    const client = new FakeResolver();
    client.parameters.set("rate", "10");
    client.callbackQueue = [];
    const value = new ParameterValue("rate");
    const first = vi.fn();
    const removeFirst = value.onChange(first);
    await value.init(client);

    client.update("rate", "20");
    expect(client.callbackQueue).toHaveLength(1);
    removeFirst();
    client.callbackQueue.shift()?.();
    expect(first).not.toHaveBeenCalled();

    const second = vi.fn();
    value.onChange(second);
    client.update("rate", "30");
    expect(client.callbackQueue).toHaveLength(1);
    await value.dispose();
    client.callbackQueue.shift()?.();
    expect(second).not.toHaveBeenCalled();
  });

  it("retains last-known-good after deletion when no default exists", async () => {
    const client = new FakeResolver();
    client.parameters.set("rate", "10");
    const value = new ParameterValue("rate");
    await value.init(client);
    client.update("rate", "", false);
    expect(value.get()).toBe("10");
  });

  it("uses env override without fetching or subscribing", async () => {
    vi.stubEnv("SDK_TEST_PARAMETER", "99");
    const client = new FakeResolver();
    const value = new ParameterValue("rate", { envVar: "SDK_TEST_PARAMETER" });
    await value.init(client);
    expect(value.get()).toBe("99");
    expect(client.parameterCalls).toHaveLength(0);
    expect(client.registrationCount).toBe(0);
  });

  it("honors static opt-out and disposes an owned subscription", async () => {
    const client = new FakeResolver();
    client.parameters.set("static", "one");
    const staticValue = new ParameterValue("static", { static: true });
    await staticValue.init(client);
    expect(client.registrationCount).toBe(0);

    client.parameters.set("dynamic", "one");
    const dynamic = new ParameterValue("dynamic");
    await dynamic.init(client);
    await dynamic.dispose();
    expect(client.disposalCount).toBe(1);
  });

  it("releases a subscription whose registration finishes after disposal starts", async () => {
    let finishRef: ((ref: ResourceRef) => void) | undefined;
    const refPending = new Promise<ResourceRef>((resolve) => {
      finishRef = resolve;
    });
    let registered = 0;
    let disposed = 0;
    let resolveCalls = 0;
    const resolver: ValueResolver = {
      getParameter: async () => "initial",
      getSecret: async () => new Secret("unused"),
      resolveResourceRef: async () => {
        resolveCalls++;
        return refPending;
      },
      _registerParameter: () => {
        registered++;
        return () => {
          disposed++;
        };
      },
    };
    const value = new ParameterValue("race");

    const initializing = value.init(resolver);
    await vi.waitFor(() => expect(resolveCalls).toBe(1));
    const disposing = value.dispose();
    let disposeFinished = false;
    void disposing.then(() => {
      disposeFinished = true;
    });
    await Promise.resolve();
    expect(disposeFinished).toBe(false);

    finishRef?.(resolveRef("race", "prod/app"));
    await Promise.all([initializing, disposing]);

    expect(registered).toBe(1);
    expect(disposed).toBe(1);
    expect(value.get()).toBe("initial");
    value.applyUpdate("late");
    expect(value.get()).toBe("initial");
    await value.dispose();
    expect(disposed).toBe(1);
  });

  it("uses default for missing store value and still subscribes", async () => {
    const client = new FakeResolver();
    const value = new ParameterValue("created-later", { default: "fallback" });
    await value.init(client);
    expect(value.get()).toBe("fallback");
    expect(client.registrationCount).toBe(1);
    client.update("created-later", "live");
    expect(value.get()).toBe("live");
  });

  it("does not mask a hard error unless broad fallback is enabled", async () => {
    const client = new FakeResolver();
    client.parameterError = new KmsError("permission_denied", "denied");
    const strict = new ParameterValue("protected", { default: "fallback", static: true });
    await expect(strict.init(client)).rejects.toMatchObject({ code: "permission_denied" });

    client.fallbackToDefaultsOnError = true;
    const permissive = new ParameterValue("protected", { default: "fallback", static: true });
    await permissive.init(client);
    expect(permissive.get()).toBe("fallback");
  });
});

describe("resolveValues", () => {
  it("discovers nested objects and arrays, handles cycles, and skips maps", async () => {
    const client = new FakeResolver();
    client.parameters.set("nested", "parameter");
    client.secrets.set("array-secret", new Secret("secret"));
    const skipped = new SecretValue("map-secret");
    const config: {
      nested: { parameter: ParameterValue };
      array: Array<{ secret: SecretValue }>;
      map: Map<string, SecretValue>;
      self?: unknown;
    } = {
      nested: { parameter: new ParameterValue("nested", { static: true }) },
      array: [{ secret: new SecretValue("array-secret") }],
      map: new Map([["skip", skipped]]),
    };
    config.self = config;

    expect(collectDeclarativeValues(config)).toHaveLength(2);
    await resolveValues(config, client);
    expect(config.nested.parameter.get()).toBe("parameter");
    expect(config.array[0]?.secret.value()).toBe("secret");
    expect(skipped.initialized).toBe(false);
  });

  it("settles all fields, preserves successes, and reports every failure", async () => {
    const client: ValueResolver = {
      getSecret: async (key) => {
        if (key === "ok") return new Secret("resolved");
        throw new KmsError("not_found", `${key} absent`);
      },
      getParameter: async () => {
        throw new KmsError("unavailable", "down");
      },
    };
    const config = {
      ok: new SecretValue("ok"),
      missing: new SecretValue("missing"),
      unavailable: new ParameterValue("unavailable", { static: true }),
    };

    const failure = await resolveValues(config, client).catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(ResolutionError);
    expect((failure as ResolutionError).errors).toHaveLength(2);
    expect(config.ok.value()).toBe("resolved");
    expect(config.missing.initialized).toBe(false);
  });
});
