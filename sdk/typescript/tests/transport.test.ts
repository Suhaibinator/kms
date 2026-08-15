import { status } from "@grpc/grpc-js";
import { describe, expect, it, vi } from "vitest";

const grpcState = vi.hoisted(() => ({
  cancelled: 0,
  onRequest: undefined as (() => void) | undefined,
}));

vi.mock("@grpc/grpc-js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@grpc/grpc-js")>();
  return {
    ...actual,
    Client: class {
      makeUnaryRequest(...args: unknown[]) {
        const callback = args.at(-1) as (error: Error | null, response?: unknown) => void;
        grpcState.onRequest?.();
        return {
          cancel() {
            grpcState.cancelled++;
            callback(
              Object.assign(new Error("cancelled"), {
                code: status.CANCELLED,
                details: "cancelled",
              }),
            );
          },
        };
      }

      close() {}
    },
  };
});

import { GrpcTransport, insecureCredentials, type UnaryMethod } from "../src/transport.js";

describe("GrpcTransport", () => {
  it("closes the abort race between starting a unary call and installing its listener", async () => {
    const controller = new AbortController();
    grpcState.cancelled = 0;
    grpcState.onRequest = () => controller.abort();
    const transport = new GrpcTransport({
      endpoint: "localhost:8443",
      credentials: insecureCredentials(),
    });
    const method: UnaryMethod<Record<string, never>, string> = {
      path: "/test.Service/Get",
      requestStream: false,
      responseStream: false,
      requestSerialize: () => Buffer.alloc(0),
      responseDeserialize: (value) => value.toString("utf8"),
    };

    await expect(transport.unary(method, {}, { signal: controller.signal })).rejects.toMatchObject({
      code: status.CANCELLED,
    });
    expect(grpcState.cancelled).toBe(1);
    transport.close();
  });
});
