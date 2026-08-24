import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  api,
  clearToken,
  getToken,
  isConflict,
  setToken,
  UNAUTHORIZED_EVENT,
} from "@/lib/api";
import type { SubscriberStreamSnapshot } from "@/lib/types";

const fetchMock = vi.fn();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function sseResponse(text: string, status = 200): Response {
  return new Response(text, { status, headers: { "Content-Type": "text/event-stream" } });
}

const ns = { env: "prod", app: "gradethis" };

describe("console api additions", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    setToken("tok-1");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    clearToken();
  });

  it("getApplication / applicationOverview / fleetOverview hit the documented paths", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse({ applications: [] })));
    await api.getApplication("gradethis");
    await api.applicationOverview("gradethis");
    await api.applicationOverview("gradethis", ["prod", "dev"]);
    await api.applicationOverview("gradethis", []);
    await api.fleetOverview();
    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "/api/v1/applications/get?name=gradethis",
      "/api/v1/applications/overview?name=gradethis",
      "/api/v1/applications/overview?name=gradethis&env=prod%2Cdev",
      "/api/v1/applications/overview?name=gradethis",
      "/api/v1/applications/overview",
    ]);
    expect(fetchMock.mock.calls[0]?.[1].headers.Authorization).toBe("Bearer tok-1");
  });

  it("ship / cloneEnvironment / rollbackRelease POST their request bodies verbatim", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse({ status: "preview" })));
    const ship = {
      application: "gradethis",
      environment: "prod",
      changes: [{ alias: "rate_limits", value: "{}", content_type: "json" }],
      dry_run: true,
      expected_active_version: 7,
    };
    await api.ship(ship);
    const clone = {
      application: "gradethis",
      source_env: "dev",
      target_env: "prod",
      copy_values: true,
    };
    await api.cloneEnvironment(clone);
    const rollback = { ...ns, name: "runtime", expected_current_version: 8 };
    await api.rollbackRelease(rollback);
    expect(
      fetchMock.mock.calls.map((call) => [call[0], call[1].method, JSON.parse(call[1].body)]),
    ).toEqual([
      ["/api/v1/applications/ship", "POST", ship],
      ["/api/v1/applications/environments/clone", "POST", clone],
      ["/api/v1/releases/rollback", "POST", rollback],
    ]);
  });

  it("isConflict recognises a 409 whatever its code", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { code: "failed_precondition", message: "moved" } }, 409),
    );
    const err = await api.rollbackRelease({ ...ns, name: "runtime" }).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(isConflict(err)).toBe(true);
    expect(isConflict(new ApiError("already_exists", "dup", 400))).toBe(false);
    expect(isConflict(new Error("409"))).toBe(false);
  });

  describe("subscriberStream", () => {
    it("sends the bearer header, parses snapshot frames and resolves on end", async () => {
      const snapshot = { subscribers: [], current_revision: 41, server_time_unix_ms: 1 };
      fetchMock.mockResolvedValueOnce(
        sseResponse(
          `: keep-alive\n\nevent: snapshot\ndata: ${JSON.stringify(snapshot)}\n\nevent: end\ndata: {}\n\nevent: snapshot\ndata: {"never":true}\n\n`,
        ),
      );
      const seen: SubscriberStreamSnapshot[] = [];
      await api.subscriberStream(ns, "runtime", { onSnapshot: (s) => void seen.push(s) });
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/release-subscribers/stream?env=prod&app=gradethis&name=runtime",
        expect.objectContaining({
          headers: { Accept: "text/event-stream", Authorization: "Bearer tok-1" },
          cache: "no-store",
        }),
      );
      expect(seen).toEqual([snapshot]);
    });

    it.each([404, 405, 501])("maps a %s to unimplemented", async (status) => {
      fetchMock.mockResolvedValueOnce(
        jsonResponse({ error: { code: "not_found", message: "x" } }, status),
      );
      const err = await api
        .subscriberStream(ns, "runtime", { onSnapshot: () => {} })
        .catch((e) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect(err.code).toBe("unimplemented");
      expect(err.status).toBe(status);
    });

    it("maps a non-SSE 200 to unimplemented and other errors through the envelope", async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ subscribers: [] }));
      await expect(
        api.subscriberStream(ns, "runtime", { onSnapshot: () => {} }),
      ).rejects.toMatchObject({ code: "unimplemented", status: 200 });

      fetchMock.mockResolvedValueOnce(
        jsonResponse({ error: { code: "permission_denied", message: "nope" } }, 403),
      );
      await expect(
        api.subscriberStream(ns, "runtime", { onSnapshot: () => {} }),
      ).rejects.toMatchObject({ code: "permission_denied", message: "nope", status: 403 });

      fetchMock.mockRejectedValueOnce(new TypeError("network down"));
      await expect(
        api.subscriberStream(ns, "runtime", { onSnapshot: () => {} }),
      ).rejects.toMatchObject({ code: "unavailable", status: 0 });
    });

    it("clears the session on 401 like apiFetch does", async () => {
      const onUnauthorized = vi.fn();
      window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
      fetchMock.mockResolvedValueOnce(new Response("", { status: 401 }));
      await expect(
        api.subscriberStream(ns, "runtime", { onSnapshot: () => {} }),
      ).rejects.toMatchObject({ code: "unauthenticated", status: 401 });
      expect(getToken()).toBeNull();
      expect(onUnauthorized).toHaveBeenCalledTimes(1);
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    });

    it("propagates the caller's abort as an AbortError", async () => {
      const controller = new AbortController();
      fetchMock.mockImplementationOnce(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal?.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      );
      const done = api.subscriberStream(ns, "runtime", {
        signal: controller.signal,
        onSnapshot: () => {},
      });
      controller.abort();
      await expect(done).rejects.toMatchObject({ name: "AbortError" });
    });
  });
});
