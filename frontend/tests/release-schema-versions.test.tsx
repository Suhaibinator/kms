import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { useReleaseSchemaVersions } from "@/lib/useReleaseSchemaVersions";

const { releaseSchemaVersions } = vi.hoisted(() => ({ releaseSchemaVersions: vi.fn() }));
vi.mock("@/lib/api", () => ({ api: { releaseSchemaVersions }, isAbortError: () => false }));
beforeEach(() => releaseSchemaVersions.mockReset());

it("deduplicates registered versions across lineages and pages, newest first", async () => {
  releaseSchemaVersions.mockResolvedValueOnce({ schema_versions: [2, 1], next_page_token: "next" });
  releaseSchemaVersions.mockResolvedValueOnce({ schema_versions: [5, 2], next_page_token: "" });
  const { result } = renderHook(() => useReleaseSchemaVersions("prod", "payments", ""));
  await waitFor(() => expect(result.current.versions).toEqual([5, 2, 1]));
  expect(releaseSchemaVersions).toHaveBeenNthCalledWith(
    2,
    { env: "prod", app: "payments" },
    undefined,
    "next",
    expect.anything(),
  );
});

it.each([
  ["dev", "payments", "runtime"],
  ["prod", "other", "runtime"],
  ["prod", "payments", "other"],
])("discards old pages when scope changes to %s/%s/%s", async (env, app, name) => {
  let finish: (value: unknown) => void = () => undefined;
  releaseSchemaVersions.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  releaseSchemaVersions.mockResolvedValueOnce({ schema_versions: [3], next_page_token: "" });
  const { result, rerender } = renderHook(
    (scope) => useReleaseSchemaVersions(scope.env, scope.app, scope.name),
    { initialProps: { env: "prod", app: "payments", name: "runtime" } },
  );
  rerender({ env, app, name });
  await waitFor(() => expect(result.current.versions).toEqual([3]));
  await act(async () => finish({ schema_versions: [99], next_page_token: "stale" }));
  expect(result.current.versions).toEqual([3]);
  expect(releaseSchemaVersions).toHaveBeenCalledTimes(2);
});

it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
  "rejects invalid registered version %s",
  async (version) => {
    releaseSchemaVersions.mockResolvedValue({ schema_versions: [version], next_page_token: "" });
    const { result } = renderHook(() => useReleaseSchemaVersions("prod", "payments", "runtime"));
    await waitFor(() => expect(result.current.error).toBeInstanceOf(Error));
    expect(result.current.versions).toBeNull();
  },
);
