import {
  type handleBidiStreamingCall,
  type handleUnaryCall,
  Server,
  ServerCredentials,
  type ServerDuplexStream,
  type UntypedServiceImplementation,
} from "@grpc/grpc-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KmsClient, type WatchEvent } from "../src/client.js";
import {
  AdminServiceService,
  ConfigurationRelease,
  ConfigurationReleaseServiceService,
  type DeleteParameterRequest,
  type DeleteParameterResponse,
  type DeleteSecretRequest,
  type DeleteSecretResponse,
  type DestroySecretVersionRequest,
  type DestroySecretVersionResponse,
  type DisableSecretRequest,
  type DisableSecretResponse,
  type GetActiveReleaseRequest,
  type GetActiveReleaseResponse,
  type GetParameterMetadataRequest,
  type GetParameterMetadataResponse,
  type GetParameterRequest,
  type GetParameterResponse,
  type GetSecretMetadataRequest,
  type GetSecretMetadataResponse,
  type GetSecretRequest,
  type GetSecretResponse,
  type ListParametersRequest,
  type ListParametersResponse,
  type ListSecretsRequest,
  type ListSecretsResponse,
  ParameterServiceService,
  type PromoteSecretVersionRequest,
  type PromoteSecretVersionResponse,
  type PutParameterRequest,
  type PutParameterResponse,
  type SecretMetadata,
  SecretServiceService,
  type SubscribeEvent,
  type SubscribeRequest,
  type WatchReleaseEvent,
  type WatchReleaseRequest,
  WatchServiceService,
  type WhoAmIRequest,
  type WhoAmIResponse,
} from "../src/generated/kms.js";
import { deterministicReleaseDigest, sha256Hex } from "../src/releases/digest.js";

const namespace = { env: "prod", app: "api" } as const;
const initialParameter = wireParameter("settings", "initial", 7n);

afterEach(() => vi.restoreAllMocks());

describe("protocol-faithful gRPC integration", () => {
  it("exercises auth, discovery, unary calls, watch resume, and atomic releases", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    const release = ConfigurationRelease.create({
      namespace,
      name: "runtime",
      version: 3n,
      entries: [
        {
          alias: "settings",
          kind: "parameter",
          ref: initialParameter.ref,
          version: initialParameter.version,
          contentType: initialParameter.contentType,
          parameterDigest: sha256Hex(initialParameter.value),
        },
      ],
      metadataJson: "{}",
    });
    release.digest = deterministicReleaseDigest(release);

    let currentParameter = initialParameter;
    const exactParameters = new Map([[initialParameter.version, initialParameter]]);
    const watchRegistrations: SubscribeRequest[] = [];
    const releaseRequests: WatchReleaseRequest[] = [];
    const server = new Server();

    const whoAmI: handleUnaryCall<WhoAmIRequest, WhoAmIResponse> = (call, callback) => {
      expect(call.metadata.get("authorization")).toEqual(["Bearer integration-token"]);
      callback(null, {
        name: "typescript-sdk",
        kind: "client",
        namespace,
        authMethod: "token",
      });
    };
    server.addService(AdminServiceService, { whoAmI } as UntypedServiceImplementation);

    const getParameter: handleUnaryCall<GetParameterRequest, GetParameterResponse> = (
      call,
      callback,
    ) => {
      const selected =
        call.request.version === 0n ? currentParameter : exactParameters.get(call.request.version);
      callback(null, { parameter: selected });
    };
    const putParameter: handleUnaryCall<PutParameterRequest, PutParameterResponse> = (
      call,
      callback,
    ) => {
      currentParameter = wireParameter(call.request.ref?.key ?? "", call.request.value, 8n);
      exactParameters.set(8n, currentParameter);
      callback(null, { version: 8n, revision: 9_007_199_254_740_993n });
    };
    const listParameters: handleUnaryCall<ListParametersRequest, ListParametersResponse> = (
      _call,
      callback,
    ) => callback(null, { parameters: [currentParameter], nextPageToken: "" });
    server.addService(ParameterServiceService, {
      getParameter,
      putParameter,
      listParameters,
    } as UntypedServiceImplementation);

    const getSecret: handleUnaryCall<GetSecretRequest, GetSecretResponse> = (call, callback) => {
      expect(call.metadata.get("x-kms-secret-token")).toEqual(["secret-token"]);
      callback(null, {
        ref: call.request.ref,
        version: 2n,
        value: Buffer.from("secret-value"),
        contentType: "text/plain",
        metadataJson: "{}",
        createdAtUnixMs: 1n,
      });
    };
    server.addService(SecretServiceService, { getSecret } as UntypedServiceImplementation);

    const subscribe: handleBidiStreamingCall<SubscribeRequest, SubscribeEvent> = (call) => {
      call.on("data", (request: SubscribeRequest) => {
        if (request.namespaces.length === 0) return;
        watchRegistrations.push(request);
        const streamNumber = watchRegistrations.length;
        if (streamNumber === 1) {
          call.write(parameterChange("stream-one", 5n));
          call.write({
            event: { $case: "heartbeat", value: { serverTimeUnixMs: 1n } },
            revision: 6n,
          });
          setImmediate(() => call.end());
        } else if (streamNumber === 2) {
          call.write(parameterChange("stream-two", 7n));
        }
      });
      closeBidiOnClientEnd(call);
    };
    server.addService(WatchServiceService, { subscribe } as UntypedServiceImplementation);

    const getActiveRelease: handleUnaryCall<GetActiveReleaseRequest, GetActiveReleaseResponse> = (
      _call,
      callback,
    ) => callback(null, { release, activationRevision: 11n, previousVersion: 0n });
    const watchRelease: handleBidiStreamingCall<WatchReleaseRequest, WatchReleaseEvent> = (
      call,
    ) => {
      call.on("data", (request: WatchReleaseRequest) => releaseRequests.push(request));
      closeBidiOnClientEnd(call);
    };
    server.addService(ConfigurationReleaseServiceService, {
      getActiveRelease,
      watchRelease,
    } as UntypedServiceImplementation);

    const port = await bind(server);
    const client = new KmsClient({
      endpoint: `127.0.0.1:${port}`,
      token: "integration-token",
      insecure: true,
      clientName: "integration-client",
      timeoutMs: 2_000,
    });
    const loaderController = new AbortController();
    let stopWatch: (() => void) | undefined;
    let loaderRun: Promise<void> | undefined;

    try {
      await expect(client.whoAmI()).resolves.toEqual({
        identity: "typescript-sdk",
        kind: "client",
        namespace: "prod/api",
        authMethod: "token",
      });
      await expect(client.getParameter("settings")).resolves.toBe("initial");
      await expect(client.putParameter("settings", "updated")).resolves.toEqual({
        version: 8n,
        revision: 9_007_199_254_740_993n,
      });
      await expect(client.listParameters()).resolves.toMatchObject({
        items: [{ key: "settings", value: "updated", version: 8n }],
        nextPageToken: "",
      });
      await expect(
        client.getSecret("password", { secretToken: "secret-token" }).then((value) => value.text()),
      ).resolves.toBe("secret-value");

      const events: WatchEvent[] = [];
      stopWatch = await client.watch((event) => events.push(event));
      await waitFor(() => watchRegistrations.length === 2 && events.length === 2);
      expect(watchRegistrations.map((registration) => registration.lastSeenRevision)).toEqual([
        0n,
        6n,
      ]);
      expect(events.map((event) => (event.type === "put" ? event.value : event.type))).toEqual([
        "stream-one",
        "stream-two",
      ]);

      const loader = await client.createReleaseLoader({ name: "runtime" });
      let committed = false;
      loaderRun = loader.run((snapshot) => {
        expect(snapshot.parameter("settings")?.value()).toBe("initial");
        return {
          commit: () => {
            committed = true;
          },
          abort: () => undefined,
        };
      }, loaderController.signal);
      await waitFor(
        () =>
          committed &&
          releaseRequests.some(
            (request) =>
              request.request?.$case === "acknowledgement" &&
              request.request.value.state === "applied",
          ),
      );
      const registration = releaseRequests.find((request) => request.request?.$case === "register");
      expect(registration).toMatchObject({
        request: {
          $case: "register",
          value: {
            namespace,
            name: "runtime",
            clientName: "integration-client",
            lastSeenRevision: 11n,
          },
        },
      });
    } finally {
      stopWatch?.();
      loaderController.abort();
      if (loaderRun) await expect(loaderRun).rejects.toMatchObject({ name: "AbortError" });
      await client.close();
      await shutdown(server);
    }
  }, 15_000);

  it("maps metadata and lifecycle mutations without losing bigint precision or exposing plaintext", async () => {
    const firstExactInteger = 9_007_199_254_740_993n;
    const parameterMetadataRequests: GetParameterMetadataRequest[] = [];
    const deleteParameterRequests: DeleteParameterRequest[] = [];
    const listSecretsRequests: ListSecretsRequest[] = [];
    const secretMetadataRequests: GetSecretMetadataRequest[] = [];
    const deleteSecretRequests: DeleteSecretRequest[] = [];
    const disableSecretRequests: DisableSecretRequest[] = [];
    const destroySecretRequests: DestroySecretVersionRequest[] = [];
    const promoteSecretRequests: PromoteSecretVersionRequest[] = [];
    const observedMetadata: Array<{
      readonly rpc: string;
      readonly authorization: readonly unknown[];
      readonly secretToken: readonly unknown[];
    }> = [];
    const observeMetadata = (
      rpc: string,
      call: { readonly metadata: { get(name: string): unknown[] } },
    ): void => {
      observedMetadata.push({
        rpc,
        authorization: call.metadata.get("authorization"),
        secretToken: call.metadata.get("x-kms-secret-token"),
      });
    };

    const parameterMetadata: GetParameterMetadataResponse = {
      ref: { namespace, key: "settings" },
      contentType: "application/json",
      metadataJson: '{"owner":"integration"}',
      createdAtUnixMs: firstExactInteger,
      updatedAtUnixMs: firstExactInteger + 1n,
      labels: { current: firstExactInteger + 2n },
      versions: [
        {
          version: firstExactInteger + 2n,
          contentType: "application/json",
          state: "enabled",
          createdBy: "integration",
          createdAtUnixMs: firstExactInteger + 3n,
          metadataJson: '{"revision":"exact"}',
        },
      ],
    };
    const secretMetadata = wireSecretMetadata("password", firstExactInteger + 10n);
    const server = new Server();

    const getParameterMetadata: handleUnaryCall<
      GetParameterMetadataRequest,
      GetParameterMetadataResponse
    > = (call, callback) => {
      parameterMetadataRequests.push(call.request);
      observeMetadata("getParameterMetadata", call);
      callback(null, parameterMetadata);
    };
    const deleteParameter: handleUnaryCall<DeleteParameterRequest, DeleteParameterResponse> = (
      call,
      callback,
    ) => {
      deleteParameterRequests.push(call.request);
      observeMetadata("deleteParameter", call);
      callback(null, { revision: firstExactInteger + 4n });
    };
    server.addService(ParameterServiceService, {
      getParameterMetadata,
      deleteParameter,
    } as UntypedServiceImplementation);

    const listSecrets: handleUnaryCall<ListSecretsRequest, ListSecretsResponse> = (
      call,
      callback,
    ) => {
      listSecretsRequests.push(call.request);
      observeMetadata("listSecrets", call);
      callback(null, { secrets: [secretMetadata], nextPageToken: "next-secret-page" });
    };
    const getSecretMetadata: handleUnaryCall<
      GetSecretMetadataRequest,
      GetSecretMetadataResponse
    > = (call, callback) => {
      secretMetadataRequests.push(call.request);
      observeMetadata("getSecretMetadata", call);
      callback(null, { secret: secretMetadata });
    };
    const deleteSecret: handleUnaryCall<DeleteSecretRequest, DeleteSecretResponse> = (
      call,
      callback,
    ) => {
      deleteSecretRequests.push(call.request);
      observeMetadata("deleteSecret", call);
      callback(null, { revision: firstExactInteger + 20n });
    };
    let disableResponse = 0n;
    const disableSecret: handleUnaryCall<DisableSecretRequest, DisableSecretResponse> = (
      call,
      callback,
    ) => {
      disableSecretRequests.push(call.request);
      observeMetadata("disableSecret", call);
      disableResponse += 1n;
      callback(null, { revision: firstExactInteger + 20n + disableResponse });
    };
    const destroySecretVersion: handleUnaryCall<
      DestroySecretVersionRequest,
      DestroySecretVersionResponse
    > = (call, callback) => {
      destroySecretRequests.push(call.request);
      observeMetadata("destroySecretVersion", call);
      callback(null, { revision: firstExactInteger + 23n });
    };
    const promoteSecretVersion: handleUnaryCall<
      PromoteSecretVersionRequest,
      PromoteSecretVersionResponse
    > = (call, callback) => {
      promoteSecretRequests.push(call.request);
      observeMetadata("promoteSecretVersion", call);
      callback(null, {
        currentVersion: call.request.version,
        previousVersion: firstExactInteger + 10n,
        revision: firstExactInteger + 24n,
      });
    };
    server.addService(SecretServiceService, {
      listSecrets,
      getSecretMetadata,
      deleteSecret,
      disableSecret,
      destroySecretVersion,
      promoteSecretVersion,
    } as UntypedServiceImplementation);

    const port = await bind(server);
    const client = new KmsClient({
      endpoint: `127.0.0.1:${port}`,
      namespace: "prod/api",
      token: "integration-token",
      insecure: true,
      timeoutMs: 2_000,
    });

    try {
      const metadata = await client.getParameterMetadata("settings");
      expect(metadata).toEqual({
        ref: { namespace, key: "settings" },
        contentType: "application/json",
        metadataJson: '{"owner":"integration"}',
        createdAtUnixMs: firstExactInteger,
        updatedAtUnixMs: firstExactInteger + 1n,
        labels: { current: firstExactInteger + 2n },
        versions: [
          {
            version: firstExactInteger + 2n,
            contentType: "application/json",
            state: "enabled",
            createdBy: "integration",
            createdAtUnixMs: firstExactInteger + 3n,
            metadataJson: '{"revision":"exact"}',
          },
        ],
      });
      expect(Object.isFrozen(metadata)).toBe(true);
      expect(Object.isFrozen(metadata.ref)).toBe(true);
      expect(Object.isFrozen(metadata.ref.namespace)).toBe(true);
      expect(Object.isFrozen(metadata.labels)).toBe(true);
      expect(Object.isFrozen(metadata.versions)).toBe(true);
      expect(Object.isFrozen(metadata.versions[0])).toBe(true);
      await expect(client.deleteParameter("obsolete")).resolves.toBe(firstExactInteger + 4n);

      const secrets = await client.listSecrets("prod/api", {
        keyPrefix: "pass",
        pageSize: 17,
        pageToken: "secret-page",
      });
      expect(secrets).toEqual({
        items: [
          {
            env: "prod",
            app: "api",
            key: "password",
            contentType: "application/octet-stream",
            clientBound: true,
            hasAccessToken: true,
            metadataJson: '{"classification":"metadata-only"}',
            createdAtUnixMs: firstExactInteger + 10n,
            updatedAtUnixMs: firstExactInteger + 11n,
            namespace: "prod/api",
            path: "/prod/api/password",
            labels: { current: firstExactInteger + 10n },
            versions: [
              {
                version: firstExactInteger + 10n,
                state: "enabled",
                createdBy: "integration",
                createdAtUnixMs: firstExactInteger + 11n,
                destroyedAtUnixMs: 0n,
                expiresAtUnixMs: firstExactInteger + 12n,
                metadataJson: '{"source":"loopback"}',
              },
            ],
          },
        ],
        nextPageToken: "next-secret-page",
      });
      const listedSecret = secrets.items[0];
      expect(listedSecret).toBeDefined();
      expect(Object.isFrozen(secrets)).toBe(true);
      expect(Object.isFrozen(secrets.items)).toBe(true);
      expect(Object.isFrozen(listedSecret)).toBe(true);
      expect(Object.isFrozen(listedSecret?.labels)).toBe(true);
      expect(Object.isFrozen(listedSecret?.versions)).toBe(true);
      expect(Object.isFrozen(listedSecret?.versions[0])).toBe(true);
      expect(listedSecret).not.toHaveProperty("value");
      expect(listedSecret).not.toHaveProperty("accessToken");
      expect(listedSecret?.versions[0]).not.toHaveProperty("value");

      const fetchedMetadata = await client.getSecretMetadata("password");
      expect(fetchedMetadata).toEqual(listedSecret);
      expect(fetchedMetadata).not.toBe(listedSecret);
      expect(Object.isFrozen(fetchedMetadata)).toBe(true);
      expect(Object.isFrozen(fetchedMetadata.labels)).toBe(true);
      expect(Object.isFrozen(fetchedMetadata.versions)).toBe(true);
      expect(Object.isFrozen(fetchedMetadata.versions[0])).toBe(true);
      expect(fetchedMetadata).not.toHaveProperty("value");
      expect(fetchedMetadata).not.toHaveProperty("accessToken");
      await expect(client.deleteSecret("retired")).resolves.toBe(firstExactInteger + 20n);
      await expect(
        client.setSecretEnabled("password", false, {
          version: firstExactInteger + 10n,
          secretToken: "disable-token",
        }),
      ).resolves.toBe(firstExactInteger + 21n);
      await expect(
        client.setSecretEnabled("password", true, { secretToken: "enable-token" }),
      ).resolves.toBe(firstExactInteger + 22n);
      await expect(
        client.destroySecretVersion("password", firstExactInteger + 10n, {
          secretToken: "destroy-token",
        }),
      ).resolves.toBe(firstExactInteger + 23n);
      const promoted = await client.promoteSecretVersion("password", firstExactInteger + 12n, {
        secretToken: "promote-token",
      });
      expect(promoted).toEqual({
        currentVersion: firstExactInteger + 12n,
        previousVersion: firstExactInteger + 10n,
        revision: firstExactInteger + 24n,
      });
      expect(Object.isFrozen(promoted)).toBe(true);

      expect(parameterMetadataRequests).toEqual([{ ref: { namespace, key: "settings" } }]);
      expect(deleteParameterRequests).toEqual([{ ref: { namespace, key: "obsolete" } }]);
      expect(listSecretsRequests).toEqual([
        {
          namespace,
          keyPrefix: "pass",
          pageSize: 17,
          pageToken: "secret-page",
        },
      ]);
      expect(secretMetadataRequests).toEqual([{ ref: { namespace, key: "password" } }]);
      expect(deleteSecretRequests).toEqual([{ ref: { namespace, key: "retired" } }]);
      expect(disableSecretRequests).toEqual([
        {
          ref: { namespace, key: "password" },
          version: firstExactInteger + 10n,
          enable: false,
        },
        { ref: { namespace, key: "password" }, version: 0n, enable: true },
      ]);
      expect(destroySecretRequests).toEqual([
        { ref: { namespace, key: "password" }, version: firstExactInteger + 10n },
      ]);
      expect(promoteSecretRequests).toEqual([
        { ref: { namespace, key: "password" }, version: firstExactInteger + 12n },
      ]);
      expect(observedMetadata).toEqual([
        {
          rpc: "getParameterMetadata",
          authorization: ["Bearer integration-token"],
          secretToken: [],
        },
        {
          rpc: "deleteParameter",
          authorization: ["Bearer integration-token"],
          secretToken: [],
        },
        {
          rpc: "listSecrets",
          authorization: ["Bearer integration-token"],
          secretToken: [],
        },
        {
          rpc: "getSecretMetadata",
          authorization: ["Bearer integration-token"],
          secretToken: [],
        },
        {
          rpc: "deleteSecret",
          authorization: ["Bearer integration-token"],
          secretToken: [],
        },
        {
          rpc: "disableSecret",
          authorization: ["Bearer integration-token"],
          secretToken: ["disable-token"],
        },
        {
          rpc: "disableSecret",
          authorization: ["Bearer integration-token"],
          secretToken: ["enable-token"],
        },
        {
          rpc: "destroySecretVersion",
          authorization: ["Bearer integration-token"],
          secretToken: ["destroy-token"],
        },
        {
          rpc: "promoteSecretVersion",
          authorization: ["Bearer integration-token"],
          secretToken: ["promote-token"],
        },
      ]);
    } finally {
      await client.close();
      await shutdown(server);
    }
  }, 15_000);
});

function wireParameter(key: string, value: string, version: bigint) {
  return {
    ref: { namespace, key },
    value,
    contentType: "text/plain",
    version,
    metadataJson: "{}",
    createdBy: "integration",
    createdAtUnixMs: 1n,
    labels: { current: version },
  };
}

function wireSecretMetadata(key: string, version: bigint): SecretMetadata {
  return {
    ref: { namespace, key },
    contentType: "application/octet-stream",
    clientBound: true,
    hasAccessToken: true,
    metadataJson: '{"classification":"metadata-only"}',
    createdAtUnixMs: version,
    updatedAtUnixMs: version + 1n,
    labels: { current: version },
    versions: [
      {
        version,
        state: "enabled",
        createdBy: "integration",
        createdAtUnixMs: version + 1n,
        destroyedAtUnixMs: 0n,
        expiresAtUnixMs: version + 2n,
        metadataJson: '{"source":"loopback"}',
      },
    ],
  };
}

function parameterChange(value: string, revision: bigint): SubscribeEvent {
  return {
    event: {
      $case: "change",
      value: {
        ref: { namespace, key: "watched" },
        changeType: "put",
        value,
        contentType: "text/plain",
        version: revision,
        label: "",
      },
    },
    revision,
  };
}

function closeBidiOnClientEnd<Request, Response>(
  call: ServerDuplexStream<Request, Response>,
): void {
  call.on("end", () => call.end());
}

function bind(server: Server): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    server.bindAsync("127.0.0.1:0", ServerCredentials.createInsecure(), (error, port) => {
      if (error) reject(error);
      else resolve(port);
    });
  });
}

function shutdown(server: Server): Promise<void> {
  return new Promise<void>((resolve) => {
    server.tryShutdown((error) => {
      if (error) server.forceShutdown();
      resolve();
    });
  });
}

async function waitFor(predicate: () => boolean, timeoutMs = 3_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("condition was not met before timeout");
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}
