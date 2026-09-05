import { inspect } from "node:util";
import { Metadata, status } from "@grpc/grpc-js";
import { describe, expect, it, vi } from "vitest";
import { type GetOptions, KmsClient } from "../src/client.js";
import {
  ConfigError,
  KmsError,
  PURGE_CLEANUP_PENDING_MESSAGE,
  PurgeCleanupPendingError,
} from "../src/errors.js";
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
            metadata: new Metadata(),
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

  it("keeps coalesced discovery independent from each caller's cancellation", async () => {
    let finishDiscovery!: () => void;
    const discoveryGate = new Promise<void>((resolve) => {
      finishDiscovery = resolve;
    });
    const transport = new FakeTransport(async (path, request) => {
      if (path.endsWith("/WhoAmI")) {
        await discoveryGate;
        return {
          name: "app",
          kind: "client",
          namespace: { env: "prod", app: "api" },
          authMethod: "token",
        };
      }
      expect(request).toMatchObject({
        ref: { namespace: { env: "prod", app: "api" } },
      });
      return {
        parameter: {
          ref: { namespace: { env: "prod", app: "api" }, key: "second" },
          value: "resolved",
          contentType: "string",
          version: 1n,
          metadataJson: "{}",
          createdBy: "test",
          createdAtUnixMs: 0n,
          labels: {},
        },
      };
    });
    const client = new KmsClient({ transport });
    const firstController = new AbortController();
    const first = client.getParameter("first", { signal: firstController.signal });
    const second = client.getParameter("second");
    await vi.waitFor(() =>
      expect(transport.calls.filter((call) => call.path.endsWith("/WhoAmI"))).toHaveLength(1),
    );
    firstController.abort(new DOMException("caller stopped", "AbortError"));
    await expect(first).rejects.toMatchObject({ name: "AbortError" });

    finishDiscovery();
    await expect(second).resolves.toBe("resolved");
    expect(transport.calls.filter((call) => call.path.endsWith("/WhoAmI"))).toHaveLength(1);
    await client.close();
  });

  it("does not start shared discovery for an already-aborted caller", async () => {
    const transport = new FakeTransport(() => Promise.reject(new Error("discovery failed")));
    const client = new KmsClient({ transport });
    const controller = new AbortController();
    controller.abort(new DOMException("caller stopped", "AbortError"));

    await expect(
      client.getParameter("cancelled", { signal: controller.signal }),
    ).rejects.toMatchObject({ name: "AbortError" });
    await Promise.resolve();
    expect(transport.calls).toHaveLength(0);
    await client.close();
  });

  it("applies an earlier call deadline while waiting for lazy discovery", async () => {
    let finishDiscovery!: () => void;
    const discoveryGate = new Promise<void>((resolve) => {
      finishDiscovery = resolve;
    });
    const transport = new FakeTransport(async (path) => {
      if (!path.endsWith("/WhoAmI")) throw new Error(`unexpected ${path}`);
      await discoveryGate;
      return {
        name: "app",
        kind: "client",
        namespace: { env: "prod", app: "api" },
        authMethod: "token",
      };
    });
    const client = new KmsClient({ transport, timeoutMs: 5_000 });

    await expect(
      client.getParameter("slow", { deadline: new Date(Date.now() + 10) }),
    ).rejects.toMatchObject({ code: "deadline_exceeded" });
    expect(transport.calls).toHaveLength(1);
    finishDiscovery();
    await Promise.resolve();
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

  it("never caches secret plaintext and forwards independent read credentials", async () => {
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
    const second = await client.getSecret("db/password");
    expect(second.text()).toBe("secret-2");
    expect(String(second)).toBe(REDACTED);
    expect(JSON.stringify(second)).toBe(`"${REDACTED}"`);

    const credentialed = await client.getSecret("db/password", {
      secretToken: "one-time",
      bindingKey: "operator-binding-key",
    });
    expect(credentialed.bindKey).toBe("");
    await client.getSecret("db/password", {
      secretToken: "one-time",
      bindingKey: "operator-binding-key",
    });
    expect(reads).toBe(4);
    expect(transport.calls.at(-1)?.options.metadata?.["x-kms-secret-token"]).toBeUndefined();
    expect(
      (transport.calls.at(-1)?.request as { secretToken?: string } | undefined)?.secretToken,
    ).toBe("one-time");
    expect(
      (transport.calls.at(-1)?.request as { bindingKey?: string } | undefined)?.bindingKey,
    ).toBe("operator-binding-key");
    await client.close();
  });

  it("never forwards parameter secret tokens and never promotes those reads into cache", async () => {
    let reads = 0;
    const transport = new FakeTransport((path, request, options) => {
      if (!path.endsWith("/GetParameter")) throw new Error(`unexpected ${path}`);
      reads += 1;
      const key = (request as { ref?: { key?: string } }).ref?.key ?? "missing";
      return {
        parameter: {
          ref: { namespace: { env: "prod", app: "api" }, key },
          value: `value-${reads}`,
          contentType: "string",
          version: BigInt(reads),
          metadataJson: "{}",
          createdBy: "test",
          createdAtUnixMs: 0n,
          labels: {},
          observedMetadata: options.metadata,
        },
      };
    });
    const client = new KmsClient({ transport, namespace: "prod/api", cacheTtlMs: 60_000 });

    await expect(client.getParameter("protected", { secretToken: "read-token" })).resolves.toBe(
      "value-1",
    );
    await expect(
      client.getParameterInfo("protected", { secretToken: "metadata-token" }),
    ).resolves.toMatchObject({ value: "value-2" });
    expect(transport.calls[0]?.options.metadata?.["x-kms-secret-token"]).toBeUndefined();
    expect(transport.calls[1]?.options.metadata?.["x-kms-secret-token"]).toBeUndefined();

    await expect(client.getParameter("protected")).resolves.toBe("value-3");
    await expect(client.getParameter("protected")).resolves.toBe("value-3");
    expect(reads).toBe(3);
    await client.close();
  });

  it("preserves secret RPC error codes while discarding reflected values and credentials", async () => {
    const plaintext = "plaintext-reflection-canary";
    const secretToken = "access-token-reflection-canary";
    const bindingKey = "binding-key-reflection-canary";
    const newBindingKey = "new-binding-key-reflection-canary";
    const reflected = [plaintext, secretToken, bindingKey, newBindingKey].join("|");
    const failure = Object.assign(new Error(reflected), {
      code: status.PERMISSION_DENIED,
      details: reflected,
      metadata: new Metadata(),
    });
    const transport = new FakeTransport(() => Promise.reject(failure));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const operations: readonly [string, () => Promise<unknown>][] = [
      ["get", () => client.getSecret("secret", { secretToken, bindingKey })],
      ["put", () => client.putSecret("secret", plaintext, { bindingKey })],
      ["bind", () => client.bindSecret("secret", { expectedCurrentVersion: 1n, bindingKey })],
      ["unbind", () => client.unbindSecret("secret", { expectedCurrentVersion: 1n, bindingKey })],
      ["preview", () => client.previewSecretBindingCohort("secret", { bindingKey })],
      [
        "rotate",
        () =>
          client.rotateSecretBindingKey("secret", {
            expectedCurrentVersion: 1n,
            bindingKey,
            newBindingKey,
          }),
      ],
      [
        "purge",
        () =>
          client.purgeSecretBindingCohort("secret", {
            bindingKey,
            expectedRevision: 1n,
            expectedAffectedVersions: [1n],
          }),
      ],
      ["preview-unbound", () => client.previewSecretUnboundVersions("secret")],
      [
        "purge-unbound",
        () =>
          client.purgeSecretUnboundVersions("secret", {
            expectedRevision: 1n,
            expectedAffectedVersions: [1n],
          }),
      ],
    ];
    for (const [operation, call] of operations) {
      const error = await call().catch((reason: unknown) => reason);
      expect(error, operation).toBeInstanceOf(KmsError);
      expect(error, operation).toMatchObject({
        code: "permission_denied",
        grpcCode: status.PERMISSION_DENIED,
        message: "KMS secret operation failed",
      });
      expect((error as Error & { cause?: unknown }).cause, operation).toBeUndefined();
      for (const rendered of [
        String(error),
        inspect(error, { depth: 10 }),
        JSON.stringify(error),
      ]) {
        expect(rendered, operation).not.toContain(plaintext);
        expect(rendered, operation).not.toContain(secretToken);
        expect(rendered, operation).not.toContain(bindingKey);
        expect(rendered, operation).not.toContain(newBindingKey);
      }
    }
    await client.close();
  });

  it("distinguishes a purge that committed with artifact cleanup pending", async () => {
    const bindingKey = "binding-key-must-not-leak";
    const transport = new FakeTransport((path) => {
      const details = path.endsWith("/PurgeSecretBindingCohort")
        ? PURGE_CLEANUP_PENDING_MESSAGE
        : `${PURGE_CLEANUP_PENDING_MESSAGE}: hostile suffix`;
      throw Object.assign(new Error(details), {
        code: status.UNAVAILABLE,
        details,
        metadata: new Metadata(),
      });
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });

    const purgeError = await client
      .purgeSecretBindingCohort("secret", {
        bindingKey,
        expectedRevision: 1n,
        expectedAffectedVersions: [1n],
      })
      .catch((reason: unknown) => reason);
    expect(purgeError).toBeInstanceOf(PurgeCleanupPendingError);
    expect(purgeError).toMatchObject({
      code: "purge_cleanup_pending",
      grpcCode: status.UNAVAILABLE,
      message: PURGE_CLEANUP_PENDING_MESSAGE,
    });
    expect((purgeError as Error & { cause?: unknown }).cause).toBeUndefined();

    const rotateError = await client
      .rotateSecretBindingKey("secret", {
        expectedCurrentVersion: 1n,
        bindingKey,
        newBindingKey: "replacement-binding-key",
      })
      .catch((reason: unknown) => reason);
    expect(rotateError).toMatchObject({
      code: "unavailable",
      message: "KMS secret operation failed",
    });
    for (const rendered of [String(purgeError), inspect(purgeError), String(rotateError)]) {
      expect(rendered).not.toContain(bindingKey);
      expect(rendered).not.toContain("hostile suffix");
    }
    await client.close();
  });

  it("invalidates writes and preserves one-time access tokens", async () => {
    const transport = new FakeTransport((path, request) => {
      if (path.endsWith("/PutSecretV03")) {
        expect(request).toMatchObject({
          bindingKey: "operator-binding-key",
          generateAccessToken: true,
        });
        expect(request).not.toHaveProperty("clientBound");
        expect(request).not.toHaveProperty("secretToken");
        return { version: 7n, revision: 10n, accessToken: "only-once" };
      }
      return { version: 2n, revision: 3n };
    });
    const client = new KmsClient({ transport, namespace: "dev/tool" });
    await expect(client.putParameter("flag", "on")).resolves.toEqual({ version: 2n, revision: 3n });
    await expect(
      client.putSecret("token", "value", {
        bindingKey: "operator-binding-key",
        generateAccessToken: true,
      }),
    ).resolves.toEqual({ version: 7n, revision: 10n, accessToken: "only-once" });
    await client.close();
  });

  it("maps binding lifecycle RPCs and freezes returned results", async () => {
    let revision = 20n;
    const transport = new FakeTransport((path) => {
      revision += 1n;
      if (
        path.endsWith("/BindSecret") ||
        path.endsWith("/UnbindSecret") ||
        path.endsWith("/RotateSecretBindingKey")
      ) {
        return { currentVersion: 9n, previousVersion: 8n, revision };
      }
      if (
        path.endsWith("/PreviewSecretUnboundVersions") ||
        path.endsWith("/PurgeSecretUnboundVersions")
      ) {
        return { affectedVersions: [3n, 5n], revision };
      }
      return { anchorVersion: 8n, affectedVersions: [7n, 8n], revision };
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });

    const previewed = await client.previewSecretBindingCohort("credential", {
      anchorVersion: 8n,
      bindingKey: "binding-key-a",
    });
    const bound = await client.bindSecret("credential", {
      expectedCurrentVersion: 7n,
      bindingKey: "binding-key-a",
    });
    const unbound = await client.unbindSecret("credential", {
      expectedCurrentVersion: 7n,
      bindingKey: "binding-key-a",
    });
    const rotated = await client.rotateSecretBindingKey("credential", {
      expectedCurrentVersion: 8n,
      bindingKey: "binding-key-a",
      newBindingKey: "binding-key-b",
    });
    const purged = await client.purgeSecretBindingCohort("credential", {
      anchorVersion: 8n,
      bindingKey: "binding-key-b",
      expectedRevision: 21n,
      expectedAffectedVersions: [7n, 8n],
    });
    const unboundPreview = await client.previewSecretUnboundVersions("credential");
    const unboundPurged = await client.purgeSecretUnboundVersions("credential", {
      expectedRevision: unboundPreview.revision,
      expectedAffectedVersions: unboundPreview.affectedVersions,
    });

    for (const result of [
      previewed,
      bound,
      unbound,
      rotated,
      purged,
      unboundPreview,
      unboundPurged,
    ]) {
      expect(Object.isFrozen(result)).toBe(true);
    }
    for (const result of [previewed, purged, unboundPreview, unboundPurged]) {
      expect(Object.isFrozen(result.affectedVersions)).toBe(true);
    }
    expect(transport.calls.map(({ request }) => request)).toEqual([
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
        anchorVersion: 8n,
        bindingKey: "binding-key-a",
      },
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
        expectedCurrentVersion: 7n,
        bindingKey: "binding-key-a",
      },
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
        expectedCurrentVersion: 7n,
        bindingKey: "binding-key-a",
      },
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
        expectedCurrentVersion: 8n,
        bindingKey: "binding-key-a",
        newBindingKey: "binding-key-b",
      },
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
        anchorVersion: 8n,
        bindingKey: "binding-key-b",
        expectedRevision: 21n,
        expectedAffectedVersions: [7n, 8n],
      },
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
      },
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
        expectedRevision: unboundPreview.revision,
        expectedAffectedVersions: [3n, 5n],
      },
    ]);
    await client.close();
  });

  it("requires and validates bound-purge guard sets locally", async () => {
    const transport = new FakeTransport(() => ({
      anchorVersion: 8n,
      affectedVersions: [7n, 8n],
      revision: 22n,
    }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    await client.purgeSecretBindingCohort("credential", {
      anchorVersion: 8n,
      bindingKey: "binding-key-b",
      expectedRevision: 21n,
      expectedAffectedVersions: Object.freeze([7n, 8n]),
    });

    expect(transport.calls.map(({ request }) => request)).toEqual([
      {
        ref: { namespace: { env: "prod", app: "api" }, key: "credential" },
        anchorVersion: 8n,
        bindingKey: "binding-key-b",
        expectedRevision: 21n,
        expectedAffectedVersions: [7n, 8n],
      },
    ]);

    const invalid = [
      {},
      { expectedRevision: 1n },
      { expectedAffectedVersions: [1n] },
      { expectedRevision: 0n, expectedAffectedVersions: [1n] },
      { expectedRevision: 1n, expectedAffectedVersions: [] },
      { expectedRevision: 1n, expectedAffectedVersions: [0n] },
      { expectedRevision: 1n, expectedAffectedVersions: [2n, 1n] },
      { expectedRevision: 1n, expectedAffectedVersions: [1n, 1n] },
    ] as const;
    for (const guards of invalid) {
      await expect(
        client.purgeSecretBindingCohort("credential", {
          anchorVersion: 8n,
          bindingKey: "binding-key-b",
          ...guards,
        } as never),
      ).rejects.toBeInstanceOf(ConfigError);
    }
    expect(transport.calls).toHaveLength(1);
    await client.close();
  });

  it("rejects mismatched parameter identities and versions without polluting the cache", async () => {
    const cases: readonly {
      readonly name: string;
      readonly options: GetOptions;
      readonly invalid: ReturnType<typeof wireParameter>;
    }[] = [
      {
        name: "resource reference",
        options: {},
        invalid: wireParameter("other", "poison", 1n),
      },
      {
        name: "namespace",
        options: {},
        invalid: wireParameter("target", "poison", 1n, { env: "prod", app: "other" }),
      },
      {
        name: "exact version",
        options: { version: 7n },
        invalid: wireParameter("target", "poison", 8n),
      },
      {
        name: "resolved version",
        options: { label: "previous" },
        invalid: wireParameter("target", "poison", 0n),
      },
      {
        name: "runtime version",
        options: {},
        invalid: wireParameter("target", "poison", 1 as never),
      },
    ];

    for (const testCase of cases) {
      let reads = 0;
      const transport = new FakeTransport((path) => {
        if (!path.endsWith("/GetParameter")) throw new Error(`unexpected ${path}`);
        reads++;
        return {
          parameter:
            reads === 1
              ? testCase.invalid
              : wireParameter("target", `safe-${testCase.name}`, testCase.options.version ?? 2n),
        };
      });
      const client = new KmsClient({
        transport,
        namespace: "prod/api",
        cacheTtlMs: 60_000,
      });

      await expect(client.getParameter("target", testCase.options)).rejects.toMatchObject({
        code: "internal",
      });
      await expect(client.getParameter("target", testCase.options)).resolves.toBe(
        `safe-${testCase.name}`,
      );
      expect(reads).toBe(2);
      await client.close();
    }
  });

  it("rejects missing or mismatched secret identities and versions without cache pollution", async () => {
    const cases: readonly {
      readonly name: string;
      readonly options: GetOptions;
      readonly invalid: ReturnType<typeof wireSecret>;
    }[] = [
      {
        name: "missing resource reference",
        options: {},
        invalid: wireSecret(undefined, "poison", 1n),
      },
      {
        name: "resource reference",
        options: {},
        invalid: wireSecret("other", "poison", 1n),
      },
      {
        name: "namespace",
        options: {},
        invalid: wireSecret("target", "poison", 1n, { env: "prod", app: "other" }),
      },
      {
        name: "exact version",
        options: { version: 7n },
        invalid: wireSecret("target", "poison", 8n),
      },
      {
        name: "resolved version",
        options: { label: "previous" },
        invalid: wireSecret("target", "poison", 0n),
      },
      {
        name: "runtime version",
        options: {},
        invalid: wireSecret("target", "poison", 1 as never),
      },
    ];

    for (const testCase of cases) {
      let reads = 0;
      const transport = new FakeTransport((path) => {
        if (!path.endsWith("/GetSecret")) throw new Error(`unexpected ${path}`);
        reads++;
        return reads === 1
          ? testCase.invalid
          : wireSecret("target", `safe-${testCase.name}`, testCase.options.version ?? 2n);
      });
      const client = new KmsClient({
        transport,
        namespace: "prod/api",
        cacheTtlMs: 60_000,
      });

      const error = await client
        .getSecret("target", testCase.options)
        .catch((reason: unknown) => reason);
      expect(error).toMatchObject({ code: "internal" });
      expect(String(error)).not.toContain("poison");
      await expect(client.getSecret("target", testCase.options)).resolves.toMatchObject({
        version: testCase.options.version ?? 2n,
      });
      expect((await client.getSecret("target", testCase.options)).text()).toBe(
        `safe-${testCase.name}`,
      );
      expect(reads).toBe(3);
      await client.close();
    }
  });

  it("rejects secret metadata returned for a different resource", async () => {
    const invalidResponses = [
      wireSecretMetadata("other"),
      wireSecretMetadata("target", { env: "prod", app: "other" }),
    ] as const;

    for (const invalid of invalidResponses) {
      const transport = new FakeTransport((path) => {
        if (!path.endsWith("/GetSecretMetadata")) throw new Error(`unexpected ${path}`);
        return { secret: invalid };
      });
      const client = new KmsClient({ transport, namespace: "prod/api" });

      await expect(client.getSecretMetadata("target")).rejects.toMatchObject({
        code: "internal",
        message: "KMS secret metadata response resource reference did not match request",
      });
      await client.close();
    }
  });

  it("does not let a parameter read repopulate the cache after a successful mutation", async () => {
    let finishFirstRead!: (response: unknown) => void;
    const firstRead = new Promise<unknown>((resolve) => {
      finishFirstRead = resolve;
    });
    let reads = 0;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetParameter")) {
        reads++;
        if (reads === 1) return firstRead;
        return { parameter: wireParameter("target", "fresh", 2n) };
      }
      if (path.endsWith("/PutParameter")) return { version: 2n, revision: 2n };
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({
      transport,
      namespace: "prod/api",
      cacheTtlMs: 60_000,
    });

    const stale = client.getParameter("target", { label: "current" });
    await vi.waitFor(() => expect(reads).toBe(1));
    await client.putParameter("target", "fresh");
    finishFirstRead({ parameter: wireParameter("target", "stale", 1n) });

    await expect(stale).resolves.toBe("stale");
    await expect(client.getParameter("target", { label: "current" })).resolves.toBe("fresh");
    expect(reads).toBe(2);
    await client.close();
  });

  it("does not let a secret read repopulate the cache after a successful mutation", async () => {
    let finishFirstRead!: (response: unknown) => void;
    const firstRead = new Promise<unknown>((resolve) => {
      finishFirstRead = resolve;
    });
    let reads = 0;
    const transport = new FakeTransport((path) => {
      if (path.endsWith("/GetSecret")) {
        reads++;
        if (reads === 1) return firstRead;
        return wireSecret("target", "fresh", 2n);
      }
      if (path.endsWith("/PutSecretV03")) {
        return { version: 2n, revision: 2n, accessToken: "" };
      }
      throw new Error(`unexpected ${path}`);
    });
    const client = new KmsClient({
      transport,
      namespace: "prod/api",
      cacheTtlMs: 60_000,
    });

    const stale = client.getSecret("target");
    await vi.waitFor(() => expect(reads).toBe(1));
    await client.putSecret("target", "fresh");
    finishFirstRead(wireSecret("target", "stale", 1n));

    expect((await stale).text()).toBe("stale");
    expect((await client.getSecret("target")).text()).toBe("fresh");
    expect(reads).toBe(2);
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

  it("rejects non-string secret credentials before making an RPC", async () => {
    const transport = new FakeTransport(() => {
      throw new Error("unexpected RPC");
    });
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const hostile = Object.freeze({
      toString() {
        throw new Error("credential-coercion-canary");
      },
    });
    const calls: readonly (() => Promise<unknown>)[] = [
      () => client.getSecret("secret", { bindingKey: hostile as never }),
      () => client.getSecret("secret", { secretToken: hostile as never }),
      () => client.putSecret("secret", "value", { bindingKey: hostile as never }),
      () =>
        client.bindSecret("secret", {
          expectedCurrentVersion: 1n,
          bindingKey: hostile as never,
        }),
      () =>
        client.unbindSecret("secret", {
          expectedCurrentVersion: 1n,
          bindingKey: hostile as never,
        }),
      () => client.previewSecretBindingCohort("secret", { bindingKey: hostile as never }),
      () =>
        client.rotateSecretBindingKey("secret", {
          expectedCurrentVersion: 1n,
          bindingKey: "old",
          newBindingKey: hostile as never,
        }),
      () =>
        client.purgeSecretBindingCohort("secret", {
          bindingKey: hostile as never,
          expectedRevision: 1n,
          expectedAffectedVersions: [1n],
        }),
    ];

    for (const call of calls) {
      const error = await call().catch((reason: unknown) => reason);
      expect(error).toBeInstanceOf(ConfigError);
      expect(String(error)).not.toContain("credential-coercion-canary");
    }
    expect(transport.calls).toHaveLength(0);
    await client.close();
  });

  it("sends an unchanged replacement binding key for ordered server validation", async () => {
    const transport = new FakeTransport(() => ({
      currentVersion: 2n,
      previousVersion: 1n,
      revision: 3n,
    }));
    const client = new KmsClient({ transport, namespace: "prod/api" });
    const bindingKey = "same-binding-key-0123456789abcdef";

    await expect(
      client.rotateSecretBindingKey("secret", {
        expectedCurrentVersion: 1n,
        bindingKey,
        newBindingKey: bindingKey,
      }),
    ).resolves.toMatchObject({
      currentVersion: 2n,
      previousVersion: 1n,
    });
    expect(transport.calls).toHaveLength(1);
    expect(transport.calls[0]?.request).toMatchObject({ bindingKey, newBindingKey: bindingKey });
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

  it("installs the shared close attempt before abort listeners can re-enter close", async () => {
    let client!: KmsClient;
    let reentrantClose: Promise<void> | undefined;
    let closeCalls = 0;
    let requestStarted!: () => void;
    const started = new Promise<void>((resolve) => {
      requestStarted = resolve;
    });
    const transport: RpcTransport = {
      unary: (_method, _request, options = {}) =>
        new Promise((_, reject) => {
          const abort = () => {
            reentrantClose = client.close();
            reject(new Error("transport aborted"));
          };
          options.signal?.addEventListener("abort", abort, { once: true });
          requestStarted();
        }),
      bidi: () => {
        throw new Error("unexpected stream");
      },
      close: () => {
        closeCalls++;
      },
    };
    client = new KmsClient({ transport, namespace: "prod/api" });
    const read = client.getParameter("pending").catch(() => undefined);
    await started;

    const close = client.close();

    expect(reentrantClose).toBe(close);
    await close;
    await read;
    expect(closeCalls).toBe(1);
  });
});

function wireParameter(
  key: string,
  value: string,
  version: bigint,
  namespace = { env: "prod", app: "api" },
) {
  return {
    ref: { namespace, key },
    value,
    contentType: "string",
    version,
    metadataJson: "{}",
    createdBy: "test",
    createdAtUnixMs: 0n,
    labels: {},
  };
}

function wireSecret(
  key: string | undefined,
  value: string,
  version: bigint,
  namespace = { env: "prod", app: "api" },
) {
  return {
    ...(key === undefined ? {} : { ref: { namespace, key } }),
    version,
    value: Buffer.from(value),
    contentType: "text/plain",
    metadataJson: "{}",
    createdAtUnixMs: 0n,
  };
}

function wireSecretMetadata(key: string, namespace = { env: "prod", app: "api" }) {
  return {
    ref: { namespace, key },
    contentType: "text/plain",
    bound: false,
    hasAccessToken: false,
    metadataJson: "{}",
    createdAtUnixMs: 0n,
    updatedAtUnixMs: 0n,
    labels: {},
    versions: [],
  };
}
