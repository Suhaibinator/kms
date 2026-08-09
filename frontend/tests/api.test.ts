import { afterEach, describe, expect, it, vi } from "vitest";
import { apiFetch, clearToken, setToken } from "@/lib/api";

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
});

describe("apiFetch", () => {
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
});
