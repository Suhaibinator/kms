// @vitest-environment happy-dom

import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { usePublicConfig } from "../src/next/client.js";
import type { DecimalRevision, PublicConfigWire, PublicJsonObject } from "../src/publishing.js";

interface Config extends PublicJsonObject {
  readonly minLength: number;
}

function wire(revision: string, minLength: number): PublicConfigWire<Config> {
  return {
    revision: revision as DecimalRevision,
    config: { minLength },
  };
}

function isConfig(value: unknown): value is Config {
  return (
    value !== null &&
    typeof value === "object" &&
    "minLength" in value &&
    typeof value.minLength === "number"
  );
}

describe("usePublicConfig", () => {
  it("refreshes with an ETag and installs a validated newer policy", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify(wire("9007199254740999", 16)), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const { result } = renderHook(() =>
      usePublicConfig(wire("9007199254740993", 12), {
        endpoint: "/policy",
        fetcher,
        validateConfig: isConfig,
        refreshOnMount: false,
        refreshOnFocus: false,
      }),
    );

    await act(async () => result.current.refresh());

    expect(result.current.revision).toBe(9_007_199_254_740_999n);
    expect(result.current.config).toEqual({ minLength: 16 });
    expect(Object.isFrozen(result.current.config)).toBe(true);
    expect(result.current.error).toBeNull();
    const request = fetcher.mock.calls[0];
    expect(request?.[0]).toBe("/policy");
    expect(new Headers(request?.[1]?.headers).get("if-none-match")).toBe(
      '"kms-public-config-9007199254740993"',
    );
  });

  it("retains last-known-good policy on 304 and invalid responses", async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(null, { status: 304 }))
      .mockResolvedValueOnce(
        new Response('{"revision":"2","config":{"minLength":"bad"}}', {
          status: 200,
        }),
      );
    const { result } = renderHook(() =>
      usePublicConfig(wire("1", 10), {
        fetcher,
        validateConfig: isConfig,
        refreshOnMount: false,
        refreshOnFocus: false,
      }),
    );

    await act(async () => result.current.refresh());
    expect(result.current.config).toEqual({ minLength: 10 });
    expect(result.current.error).toBeNull();

    await act(async () => result.current.refresh());
    expect(result.current.config).toEqual({ minLength: 10 });
    expect(result.current.revision).toBe(1n);
    expect(result.current.error).toBeInstanceOf(Error);
  });

  it("aborts and fences out-of-order refreshes", async () => {
    const pending: Array<(response: Response) => void> = [];
    const signals: AbortSignal[] = [];
    const fetcher = vi.fn<typeof fetch>((_input, init) => {
      if (init?.signal instanceof AbortSignal) {
        signals.push(init.signal);
      }
      return new Promise<Response>((resolve) => pending.push(resolve));
    });
    const { result } = renderHook(() =>
      usePublicConfig(wire("1", 8), {
        fetcher,
        validateConfig: isConfig,
        refreshOnMount: false,
        refreshOnFocus: false,
      }),
    );

    let older!: Promise<void>;
    let newer!: Promise<void>;
    act(() => {
      older = result.current.refresh();
      newer = result.current.refresh();
    });
    expect(signals[0]?.aborted).toBe(true);

    pending[1]?.(
      new Response(JSON.stringify(wire("3", 14)), {
        status: 200,
      }),
    );
    await act(async () => newer);
    pending[0]?.(
      new Response(JSON.stringify(wire("2", 9)), {
        status: 200,
      }),
    );
    await act(async () => older);

    expect(result.current.revision).toBe(3n);
    expect(result.current.config).toEqual({ minLength: 14 });
  });

  it("recovers from policy_changed and rejects malformed recovery payloads", async () => {
    const { result } = renderHook(() =>
      usePublicConfig(wire("4", 10), {
        validateConfig: isConfig,
        refreshOnMount: false,
        refreshOnFocus: false,
      }),
    );

    act(() => {
      expect(
        result.current.applyServerResult({
          status: "policy_changed",
          current: wire("5", 18),
        }),
      ).toBe(true);
    });
    expect(result.current.revision).toBe(5n);
    expect(result.current.config).toEqual({ minLength: 18 });

    act(() => {
      expect(
        result.current.applyServerResult({
          status: "policy_changed",
          current: { revision: "06", config: { minLength: 20 } },
        }),
      ).toBe(false);
    });
    expect(result.current.revision).toBe(5n);
    expect(result.current.config).toEqual({ minLength: 18 });
    expect(result.current.error).toBeInstanceOf(Error);
  });

  it("refreshes on focus when enabled", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 304 }));
    renderHook(() =>
      usePublicConfig(wire("1", 10), {
        fetcher,
        refreshOnMount: false,
        refreshOnFocus: true,
      }),
    );

    act(() => window.dispatchEvent(new Event("focus")));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  });
});
