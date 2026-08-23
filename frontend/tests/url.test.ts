import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { queryValue, useQueryReplace } from "@/lib/url";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string | string[]>,
  replace: vi.fn(async () => true),
}));

vi.mock("next/router", () => ({
  useRouter: () => ({ query: mocks.query, isReady: true, replace: mocks.replace }),
}));

describe("queryValue", () => {
  it("normalises string, array and missing entries", () => {
    expect(queryValue("prod")).toBe("prod");
    expect(queryValue(["prod", "dev"])).toBe("prod");
    expect(queryValue([])).toBe("");
    expect(queryValue(undefined)).toBe("");
  });
});

describe("useQueryReplace", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.replace.mockClear();
  });

  it("merges the patch into the current query with a shallow, non-scrolling replace", () => {
    mocks.query = { app: "payments", env: ["prod", "dev"] };
    const { result } = renderHook(() => useQueryReplace("/releases"));
    result.current({ name: "runtime" });

    expect(mocks.replace).toHaveBeenCalledTimes(1);
    expect(mocks.replace).toHaveBeenCalledWith(
      { pathname: "/releases", query: { app: "payments", env: "prod", name: "runtime" } },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("deletes a key when the patch value is an empty string", () => {
    mocks.query = { app: "payments", tab: "schemas" };
    const { result } = renderHook(() => useQueryReplace("/releases"));
    result.current({ tab: "" });

    expect(mocks.replace).toHaveBeenCalledWith(
      { pathname: "/releases", query: { app: "payments" } },
      undefined,
      { shallow: true, scroll: false },
    );
  });
});
