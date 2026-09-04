import { Metadata, status } from "@grpc/grpc-js";
import { describe, expect, it } from "vitest";

import { KmsClient } from "../src/client.js";
import {
  ConfigError,
  isKmsError,
  KmsError,
  mapGrpcError,
  mapSecretGrpcError,
  NoNamespaceError,
  wrapError,
} from "../src/errors.js";
import { FakeTransport } from "./helpers/fake-transport.js";

function grpcError(code: status, details: string): Error {
  return Object.assign(new Error(`${code} ${details}`), {
    code,
    details,
    metadata: new Metadata(),
  });
}

describe("gRPC error normalization", () => {
  it.each([
    [status.NOT_FOUND, "not_found"],
    [status.PERMISSION_DENIED, "permission_denied"],
    [status.UNAUTHENTICATED, "unauthenticated"],
    [status.FAILED_PRECONDITION, "failed_precondition"],
    [status.DEADLINE_EXCEEDED, "deadline_exceeded"],
    [status.UNAVAILABLE, "unavailable"],
  ] as const)("maps status %s to %s", (grpcCode, code) => {
    const source = grpcError(grpcCode, "safe server diagnostic");
    const mapped = mapGrpcError(source);
    expect(mapped).toBeInstanceOf(KmsError);
    expect(mapped).toMatchObject({ code, grpcCode, message: "safe server diagnostic" });
    expect((mapped as Error).cause).toBe(source);
  });

  it("preserves ordinary and already normalized errors", () => {
    const ordinary = new Error("socket setup failed");
    const normalized = new KmsError("not_found", "missing");
    expect(mapGrpcError(ordinary)).toBe(ordinary);
    expect(mapGrpcError(normalized)).toBe(normalized);
    expect(mapGrpcError(grpcError(status.OK, "OK"))).toBeUndefined();
  });

  it("does not mistake DOM cancellation or ordinary numeric codes for gRPC status", () => {
    const cancellation = new DOMException("caller cancelled", "AbortError");
    const applicationError = Object.assign(new Error("application lookup failed"), {
      code: status.NOT_FOUND,
      details: "not a service error",
    });

    expect(typeof cancellation.code).toBe("number");
    expect(mapGrpcError(cancellation)).toBe(cancellation);
    expect(mapGrpcError(applicationError)).toBe(applicationError);
  });

  it("maps genuine gRPC-shaped errors across duplicated grpc-js module instances", () => {
    const source = Object.assign(new Error("5 NOT_FOUND: missing"), {
      code: status.NOT_FOUND,
      details: "missing",
      // A second installed grpc-js copy has a distinct Metadata constructor,
      // but retains the stable public Metadata API.
      metadata: { getMap: () => ({}) },
    });

    const mapped = mapGrpcError(source);
    expect(mapped).toBeInstanceOf(KmsError);
    expect(mapped).toMatchObject({ code: "not_found", grpcCode: status.NOT_FOUND });
    expect((mapped as Error).cause).toBe(source);
  });

  it("maps secret RPC errors from status alone without reading or retaining hostile details", () => {
    const canary = "reflected-secret-credential";
    const source = new Error(canary);
    let detailsReads = 0;
    Object.defineProperties(source, {
      code: { value: status.PERMISSION_DENIED },
      details: {
        get() {
          detailsReads += 1;
          throw new Error(canary);
        },
      },
    });

    const mapped = mapSecretGrpcError(source);
    expect(detailsReads).toBe(0);
    expect(mapped).toMatchObject({
      code: "permission_denied",
      grpcCode: status.PERMISSION_DENIED,
      message: "KMS secret operation failed",
    });
    expect(mapped?.cause).toBeUndefined();
    expect(String(mapped)).not.toContain(canary);
  });

  it("preserves an injected DOM cancellation through the client RPC boundary", async () => {
    const cancellation = new DOMException("caller cancelled", "AbortError");
    const transport = new FakeTransport(() => Promise.reject(cancellation));
    const client = new KmsClient({ transport, namespace: "prod/api" });

    try {
      await expect(client.getParameter("flag")).rejects.toBe(cancellation);
    } finally {
      await client.close();
    }
  });

  it("provides code-aware narrowing and wrapping", () => {
    const source = new KmsError("not_found", "missing");
    const wrapped = wrapError('resolve parameter "rate"', source);
    expect(isKmsError(wrapped, "not_found")).toBe(true);
    expect(wrapped.message).toContain("rate");
    expect(wrapped.cause).toBe(source);
  });
});

describe("configuration errors", () => {
  it("use stable codes and name the offending relative key", () => {
    expect(new ConfigError("bad endpoint")).toMatchObject({ code: "invalid_argument" });
    const error = new NoNamespaceError("billing/key");
    expect(error.code).toBe("no_namespace");
    expect(error.message).toContain("billing/key");
  });
});
