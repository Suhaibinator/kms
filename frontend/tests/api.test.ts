import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  api,
  apiFetch,
  clearToken,
  getToken,
  isUnreachableError,
  setToken,
  UNAUTHORIZED_EVENT,
} from "@/lib/api";

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
});

describe("apiFetch", () => {
  it("sends BOM and invalid UTF-8 artifact bytes as the exact raw request body", async () => {
    const artifact = Uint8Array.from([
      0xef,
      0xbb,
      0xbf, // UTF-8 BOM
      0x7b,
      0x22,
      0xc3,
      0x28, // invalid UTF-8 sequence
      0xff, // invalid UTF-8 byte
      0x22,
      0x7d,
    ]).buffer;
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          profile: "dév",
          schema_sha256: "abc",
          artifact_digest: "def",
          plan_digest: "plan",
          entries: [],
          missing_secrets: [],
          executed: false,
          definition_changed: false,
          definition_updated: false,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );

    await api.importApplicationDefaults({
      env: "prod",
      app: "grades",
      artifact,
      overwrite: true,
      updateDefinition: true,
      execute: true,
      planDigest: "plan",
    });

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/applications/defaults?env=prod&app=grades&overwrite=true&update_definition=true&execute=true&plan_digest=plan",
      expect.objectContaining({
        method: "POST",
        body: expect.any(ArrayBuffer),
        headers: expect.objectContaining({ "Content-Type": "application/json" }),
      }),
    );
    const sent = fetchMock.mock.calls[0]?.[1]?.body;
    expect(sent).toBe(artifact);
    expect(Array.from(new Uint8Array(sent as ArrayBuffer))).toEqual(
      Array.from(new Uint8Array(artifact)),
    );
  });

  it("sends authenticated API requests with browser caching disabled", async () => {
    setToken("kms_test_token");
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(apiFetch<{ ok: boolean }>("/test")).resolves.toEqual({ ok: true });

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/test",
      expect.objectContaining({
        cache: "no-store",
        headers: expect.objectContaining({ Authorization: "Bearer kms_test_token" }),
      }),
    );
  });

  it("sends a reveal token in the body and never creates a custom secret header", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          env: "prod",
          app: "billing",
          key: "api-key",
          version: 2,
          value_base64: "dmFsdWU=",
          content_type: "text/plain",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );

    await api.revealSecret(
      { env: "prod", app: "billing", key: "api-key" },
      2,
      "",
      "kmss_version_token",
    );

    const init = fetchMock.mock.calls[0]?.[1];
    expect(init?.headers).not.toEqual(
      expect.objectContaining({ "X-KMS-Secret-Token": expect.any(String) }),
    );
    expect(JSON.parse(String(init?.body))).toMatchObject({
      env: "prod",
      app: "billing",
      key: "api-key",
      version: 2,
      secret_token: "kmss_version_token",
    });
  });

  it("sends an update token in the secret request body", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ version: 2, revision: 2 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await api.createSecret({
      env: "prod",
      app: "billing",
      key: "api-key",
      value_base64: "dmFsdWU=",
      content_type: "text/plain",
      metadata_json: "{}",
      client_bound: true,
      generate_access_token: false,
      expires_at_unix_ms: 0,
      secret_token: "kmss_current_token",
    });

    const init = fetchMock.mock.calls[0]?.[1];
    expect(init?.headers).not.toEqual(
      expect.objectContaining({ "X-KMS-Secret-Token": expect.any(String) }),
    );
    expect(JSON.parse(String(init?.body))).toMatchObject({
      secret_token: "kmss_current_token",
    });
  });

  it("turns a request timeout into a useful unavailable error", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((_input, init) => {
      return new Promise((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          reject(new DOMException("Aborted", "AbortError"));
        });
      });
    });

    const request = apiFetch("/slow", { timeoutMs: 1 });

    await expect(request).rejects.toMatchObject({
      code: "unavailable",
      status: 0,
      message: "The server took too long to respond.",
    });
  });

  it("preserves caller cancellation instead of reporting an offline error", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation((_input, init) => {
      return new Promise((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          reject(new DOMException("Aborted", "AbortError"));
        });
      });
    });
    const controller = new AbortController();
    const request = apiFetch("/cancelled", { signal: controller.signal });

    controller.abort();

    await expect(request).rejects.toMatchObject({ name: "AbortError" });
  });

  it("rejects a 2xx body it cannot parse instead of resolving null", async () => {
    // A proxy or an error page served with a 200 used to resolve as `null`,
    // which blew up as a TypeError deep inside whichever page asked for it.
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("<!doctype html><title>Gateway</title>", {
        status: 200,
        headers: { "Content-Type": "text/html" },
      }),
    );

    await expect(apiFetch("/html")).rejects.toMatchObject({
      code: "internal",
      status: 200,
    });
  });

  it("resolves an empty 204 body as null", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 204 }));

    await expect(apiFetch("/deleted")).resolves.toBeNull();
  });

  it("carries the error envelope's code, message and validation errors", async () => {
    const validationErrors = [
      {
        alias: "runtime",
        code: "invalid_type",
        schema_pointer: "/properties/timeout",
        message: "must be an integer",
      },
    ];
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: "already_exists",
            message: "key exists",
            validation_errors: validationErrors,
          },
        }),
        { status: 409, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(apiFetch("/conflict")).rejects.toMatchObject({
      code: "already_exists",
      message: "key exists",
      status: 409,
      validationErrors,
    });
  });

  it("falls back to a readable message when the body and statusText are empty", async () => {
    // HTTP/2 responses always carry an empty statusText, so `??` would have
    // produced an error with a blank message.
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(null, { status: 500, statusText: "" }),
    );

    await expect(apiFetch("/boom")).rejects.toMatchObject({
      code: "internal",
      message: "Request failed",
      status: 500,
    });
  });

  it("reports a 401 on an unauthenticated request as bad credentials", async () => {
    setToken("kms_existing_session");
    const onUnauthorized = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "unauthenticated", message: "bad token" } }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      }),
    );

    try {
      // The login form has no session to lose; a rejected token is a sign-in
      // failure, not an expired session.
      await expect(apiFetch("/auth/login", { method: "POST", auth: false })).rejects.toMatchObject({
        code: "invalid_credentials",
        message: "bad token",
        status: 401,
      });
      expect(getToken()).toBe("kms_existing_session");
      expect(onUnauthorized).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    }
  });

  it("clears the token and announces a 401 on an authenticated request", async () => {
    setToken("kms_dying_session");
    const onUnauthorized = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "unauthenticated", message: "expired" } }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      }),
    );

    try {
      await expect(apiFetch("/whoami")).rejects.toMatchObject({
        code: "unauthenticated",
        status: 401,
      });
      expect(getToken()).toBeNull();
      expect(onUnauthorized).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    }
  });
});

describe("isUnreachableError", () => {
  it("is true only for a status-0 unavailable error", () => {
    expect(isUnreachableError(new ApiError("unavailable", "offline", 0))).toBe(true);
    expect(isUnreachableError(new ApiError("unavailable", "maintenance", 503))).toBe(false);
    expect(isUnreachableError(new ApiError("internal", "boom", 0))).toBe(false);
    expect(isUnreachableError(new Error("offline"))).toBe(false);
    expect(isUnreachableError(undefined)).toBe(false);
  });

  it("classifies what apiFetch throws when fetch rejects", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(apiFetch("/v1/health")).rejects.toSatisfy(isUnreachableError);
  });
});
