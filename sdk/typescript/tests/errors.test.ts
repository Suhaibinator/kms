import { status } from "@grpc/grpc-js";
import { describe, expect, it } from "vitest";

import {
  ConfigError,
  isKmsError,
  KmsError,
  mapGrpcError,
  NoNamespaceError,
  wrapError,
} from "../src/errors.js";

function grpcError(code: status, details: string): Error {
  return Object.assign(new Error(`${code} ${details}`), { code, details });
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
    expect(mapGrpcError(Object.assign(new Error("OK"), { code: status.OK }))).toBeUndefined();
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
