import { status } from "@grpc/grpc-js";
import { describe, expect, it, vi } from "vitest";
import { KmsClient } from "../src/client.js";
import { ConfigError, KmsError } from "../src/errors.js";
import { REDACTED } from "../src/secret.js";
import type { RpcTransport } from "../src/transport.js";
import { FakeTransport } from "./helpers/fake-transport.js";

describe("KmsClient", () => {
  it("fails closed unless security is explicit", () => {
    expect(() => new KmsClient({ endpoint: "localhost:8443" })).toThrow(ConfigError);
    expect(() => new KmsClient({ endpoint: "localhost:8080", insecure: true })).not.toThrow();
  });

  it("discovers a namespace once, preserves bigint, and attaches bearer metadata", async () => {
    const max = 18_446_744_073_709_551_615n;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/WhoAmI")) {
        return {
          name: "app",
          kind: "client",
          namespace: { env: "prod", app: "api" },
          authMethod: "token",
        };
      }
      if (path.endsWith("/GetParameter")) {
        return {
          parameter: {
            ref: { namespace: { env: "prod", app: "api" }, key: "limits/max" },
            value: "42",
            contentType: "integer",
            version: max,
            metadataJson: "{}",
            createdBy: "test",
            createdAtUnixMs: 1n,
            labels: { current: max },
          },
        };
      }
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({ transport, token: "identity-token", cacheTtlMs: 1_000 });

    const [a, b] = await Promise.all([
      client.getParameterInfo("limits/max", { version: max }),
      client.getParameterInfo("limits/max", { version: max }),
    ]);

    expect(a.version).toBe(max);
    expect(b.labels.current).toBe(max);
    expect(transport.calls.filter((call) => call.path.endsWith("/WhoAmI"))).toHaveLength(1);
    expect(
      transport.calls.every(
        (call) => call.options.metadata?.authorization === "Bearer identity-token",
      ),
    ).toBe(true);
    await client.close();
    await client.close();
    expect(transport.closeCount).toBe(1);
  });

  it("retries namespace discovery after a transient WhoAmI failure", async () => {
    let discoveries = 0;
    const transport = new FakeTransport((path, request) => {
      if (path.endsWith("/WhoAmI")) {
        discoveries += 1;
        if (discoveries === 1) {
          throw Object.assign(new Error("temporarily unavailable"), {
            code: status.UNAVAILABLE,
            details: "temporarily unavailable",
          });
        }
        return {
          name: "app",
          kind: "client",
          namespace: { env: "prod", app: "api" },
          authMethod: "token",
        };
      }
      expect(request).toMatchObject({
        ref: { namespace: { env: "prod", app: "api" }, key: "limits/max" },
      });
      return {
        parameter: {
          ref: { namespace: { env: "prod", app: "api" }, key: "limits/max" },
          value: "42",
          contentType: "integer",
          version: 1n,
          metadataJson: "{}",
          createdBy: "test",
          createdAtUnixMs: 0n,
          labels: {},
        },
      };
    });
    const client = new KmsClient({ transport });

    await expect(client.getParameter("limits/max")).rejects.toMatchObject({ code: "unavailable" });
    await expect(client.getParameter("limits/max")).resolves.toBe("42");
    await expect(client.getParameter("limits/max")).resolves.toBe("42");

    expect(discoveries).toBe(2);
    await client.close();
  });

  it("caches an unbound identity and returns no_namespace without repeat discovery", async () => {
    let discoveries = 0;
    const transport = new FakeTransport((path) => {
      if (!path.endsWith("/WhoAmI")) throw new Error(`unexpected ${path}`);
      discoveries += 1;
      return {
        name: "admin-tool",
        kind: "client",
        authMethod: "token",
      };
    });
    const client = new KmsClient({ transport });

    for (const key of ["first", "second"]) {
      await expect(client.getParameter(key)).rejects.toMatchObject({ code: "no_namespace" });
    }

    expect(discoveries).toBe(1);
    expect(transport.calls).toHaveLength(1);
    await client.close();
  });

  it("splits absolute paths without namespace discovery", async () => {
    const transport = new FakeTransport((_path, request) => {
      expect(request).toMatchObject({
        ref: { namespace: { env: "other", app: "worker" }, key: "nested/key" },
      });
      return {
        parameter: {
          ref: { namespace: { env: "other", app: "worker" }, key: "nested/key" },
          value: "ok",
          contentType: "string",
          version: 1n,
          metadataJson: "{}",
          createdBy: "",
          createdAtUnixMs: 0n,
          labels: {},
        },
      };
    });
    const client = new KmsClient({ transport });
    await expect(client.getParameter("/other/worker/nested/key")).resolves.toBe("ok");
    expect(transport.calls.some((call) => call.path.endsWith("/WhoAmI"))).toBe(false);
    await client.close();
  });

  it("defensively caches tokenless secrets and bypasses cache for token-gated reads", async () => {
    let reads = 0;
    const transport = new FakeTransport((_path, _request, options) => {
      reads++;
      return {
        ref: { namespace: { env: "prod", app: "api" }, key: "db/password" },
        version: 9n,
        value: Buffer.from(`secret-${reads}`),
        contentType: "text/plain",
        metadataJson: "{}",
        createdAtUnixMs: 0n,
        metadata: options.metadata,
      };
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });

    const first = await client.getSecret("db/password");
    const leaked = first.bytes();
    leaked.fill(0);
    const cached = await client.getSecret("db/password");
    expect(cached.text()).toBe("secret-1");
    expect(String(cached)).toBe(REDACTED);
    expect(JSON.stringify(cached)).toBe(`"${REDACTED}"`);

    await client.getSecret("db/password", { secretToken: "one-time" });
    await client.getSecret("db/password", { secretToken: "one-time" });
    expect(reads).toBe(3);
    expect(transport.calls.at(-1)?.options.metadata?.["x-kms-secret-token"]).toBe("one-time");
    await client.close();
  });

  it("maps typed gRPC errors without including secret writes", async () => {
    const failure = Object.assign(new Error("denied"), {
      code: status.PERMISSION_DENIED,
      details: "denied",
    });
    const transport = new FakeTransport(() => Promise.reject(failure));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const plaintext = "do-not-render";
    const error = await client.putSecret("secret", plaintext).catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(KmsError);
    expect((error as KmsError).code).toBe("permission_denied");
    expect(String(error)).not.toContain(plaintext);
    await client.close();
  });

  it("invalidates writes and preserves one-time access tokens", async () => {
    const transport = new FakeTransport((path, request) => {
      if (path.endsWith("/PutSecret")) {
        expect(request).toMatchObject({ clientBound: true, generateAccessToken: true });
        return { version: 7n, revision: 10n, accessToken: "only-once" };
      }
      return { version: 2n, revision: 3n };
    });
    const client = new KmsClient({ transport, namespace: "dev/tool" });
    await expect(client.putParameter("flag", "on")).resolves.toEqual({ version: 2n, revision: 3n });
    await expect(
      client.putSecret("token", "value", { clientBound: true, generateAccessToken: true }),
    ).resolves.toEqual({ version: 7n, revision: 10n, accessToken: "only-once" });
    await client.close();
  });

  it("rejects out-of-range protobuf integers before making an RPC", async () => {
    const transport = new FakeTransport(() => {
      throw new Error("unexpected RPC");
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const uint64Overflow = 1n << 64n;
    const int64Overflow = 1n << 63n;

    await expect(client.setSecretEnabled("secret", false, { version: -1n })).rejects.toThrow(
      ConfigError,
    );
    await expect(client.destroySecretVersion("secret", uint64Overflow)).rejects.toThrow(
      ConfigError,
    );
    await expect(client.promoteSecretVersion("secret", 0n)).rejects.toThrow(ConfigError);
    await expect(
      client.putSecret("secret", "value", { expiresAtUnixMs: int64Overflow }),
    ).rejects.toThrow(ConfigError);
    await expect(client.putSecret("secret", 42 as never)).rejects.toThrow(ConfigError);

    expect(transport.calls).toHaveLength(0);
    await client.close();
  });

  it("coalesces concurrent close calls until transport cleanup completes", async () => {
    const base = new FakeTransport(() => ({}));
    let finishClose!: () => void;
    const closeGate = new Promise<void>((resolve) => {
      finishClose = resolve;
    });
    let closeCalls = 0;
    const transport: RpcTransport = {
      unary: base.unary.bind(base),
      bidi: base.bidi.bind(base),
      close: () => {
        closeCalls++;
        return closeGate;
      },
    };
    const client = new KmsClient({ transport, namespace: "prod/api" });

    const first = client.close();
    const second = client.close();
    expect(second).toBe(first);
    await vi.waitFor(() => expect(closeCalls).toBe(1));

    let completed = false;
    void second.then(() => {
      completed = true;
    });
    await Promise.resolve();
    expect(completed).toBe(false);

    finishClose();
    await Promise.all([first, second]);
    expect(completed).toBe(true);
  });
});
