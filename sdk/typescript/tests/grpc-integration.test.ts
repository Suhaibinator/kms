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
  type GetActiveReleaseRequest,
  type GetActiveReleaseResponse,
  type GetParameterRequest,
  type GetParameterResponse,
  type GetSecretRequest,
  type GetSecretResponse,
  type ListParametersRequest,
  type ListParametersResponse,
  ParameterServiceService,
  type PutParameterRequest,
  type PutParameterResponse,
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
