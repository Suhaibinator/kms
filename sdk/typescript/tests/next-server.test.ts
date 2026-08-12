import { describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

import { createNextKms, createPublicConfigGET } from "../src/next/server.js";
import { definePublicProjection, formatRevision } from "../src/publishing.js";

type Policy = { minLength: number; privateValue: string };
type PublicPolicy = { readonly minLength: number };

const projection = definePublicProjection<
  Policy,
  {
    minLength: (value: Readonly<Policy>) => number;
  }
>({ minLength: (value) => value.minLength });

describe("Next.js server adapter", () => {
  it("coalesces concurrent starts and closes once", async () => {
    let starts = 0;
    let closes = 0;
    const adapter = createNextKms<Policy, PublicPolicy, string, readonly string[]>({
      initialize: async () => {
        starts++;
        await Promise.resolve();
        return {
          source: {
            current: () => ({ revision: 4n, value: { minLength: 12, privateValue: "hidden" } }),
          },
          close: () => {
            closes++;
          },
        };
      },
      projection,
      validate: (policy, value) =>
        value.length >= policy.minLength
          ? { valid: true }
          : { valid: false, errors: ["too_short"] },
    });
    await Promise.all(Array.from({ length: 100 }, () => adapter.start()));
    expect(starts).toBe(1);
    expect(await adapter.readPublicPolicy()).toEqual({ revision: "4", config: { minLength: 12 } });
    await Promise.all([adapter.close(), adapter.close()]);
    expect(closes).toBe(1);
  });

  it("retries failed initialization and cleans malformed resources", async () => {
    let starts = 0;
    let malformedCloses = 0;
    const adapter = createNextKms<Policy, PublicPolicy, string, readonly string[]>({
      initialize: () => {
        starts++;
        if (starts === 1) throw new Error("transient startup failure");
        if (starts === 2) {
          return {
            source: {} as never,
            close: () => {
              malformedCloses++;
            },
          };
        }
        return {
          source: {
            current: () => ({ revision: 1n, value: { minLength: 8, privateValue: "hidden" } }),
          },
        };
      },
      projection,
      validate: () => ({ valid: true }),
    });

    await expect(adapter.start()).rejects.toThrow("transient startup failure");
    await expect(adapter.start()).rejects.toThrow("invalid resource");
    expect(malformedCloses).toBe(1);
    await expect(adapter.start()).resolves.toBeUndefined();
    expect(starts).toBe(3);
    await adapter.close();
  });

  it("closes a resource exactly once when close races initialization", async () => {
    let releaseInitialization!: () => void;
    const gate = new Promise<void>((resolve) => {
      releaseInitialization = resolve;
    });
    let closes = 0;
    const adapter = createNextKms<Policy, PublicPolicy, string, readonly string[]>({
      initialize: async () => {
        await gate;
        return {
          source: {
            current: () => ({ revision: 1n, value: { minLength: 8, privateValue: "hidden" } }),
          },
          close: () => {
            closes++;
          },
        };
      },
      projection,
      validate: () => ({ valid: true }),
    });

    const start = adapter.start();
    const close = adapter.close();
    releaseInitialization();
    await expect(start).rejects.toThrow(/closed/);
    await close;
    expect(closes).toBe(1);
  });

  it("removes installed process signal listeners during close", async () => {
    const baseline = process.listenerCount("SIGTERM");
    const adapter = createNextKms<Policy, PublicPolicy, string, readonly string[]>({
      initialize: () => ({
        source: {
          current: () => ({ revision: 1n, value: { minLength: 8, privateValue: "hidden" } }),
        },
      }),
      projection,
      validate: () => ({ valid: true }),
    });
    adapter.installProcessShutdown({ signals: ["SIGTERM"] });
    expect(process.listenerCount("SIGTERM")).toBe(baseline + 1);

    await adapter.close();
    expect(process.listenerCount("SIGTERM")).toBe(baseline);
  });

  it("emits exact 200, weak/list 304, and redacted 503 contracts", async () => {
    const get = createPublicConfigGET(async () => ({
      revision: formatRevision(18_446_744_073_709_551_615n),
      config: { minLength: 16 },
    }));
    const response = await get(new Request("http://local/policy"));
    expect(response.status).toBe(200);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(await response.json()).toEqual({
      revision: "18446744073709551615",
      config: { minLength: 16 },
    });
    const etag = response.headers.get("etag") as string;
    const notModified = await get(
      new Request("http://local/policy", { headers: { "If-None-Match": `"other", W/${etag}` } }),
    );
    expect(notModified.status).toBe(304);
    expect(await notModified.text()).toBe("");
    const malformedWildcard = await get(
      new Request("http://local/policy", { headers: { "If-None-Match": "*garbage" } }),
    );
    expect(malformedWildcard.status).toBe(200);

    const unavailable = createPublicConfigGET(async () => undefined);
    const failure = await unavailable(new Request("http://local/policy"));
    expect(failure.status).toBe(503);
    expect(failure.headers.get("retry-after")).toBe("1");
    expect(await failure.json()).toEqual({ status: "unavailable" });
  });

  it("rejects the Edge runtime", async () => {
    vi.stubEnv("NEXT_RUNTIME", "edge");
    expect(() => createPublicConfigGET(async () => undefined)).toThrow(/Node runtime/);
    vi.unstubAllEnvs();
  });
});
