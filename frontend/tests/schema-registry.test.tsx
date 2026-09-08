import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { useSchemaRegistry } from "@/lib/useSchemaRegistry";

const { listSchemas } = vi.hoisted(() => ({ listSchemas: vi.fn() }));
vi.mock("@/lib/api", () => ({ api: { listSchemas }, isAbortError: () => false }));
beforeEach(() => listSchemas.mockReset());

it("finds the newest schema across all registry pages", async () => {
  listSchemas.mockResolvedValueOnce({ schemas: [{ version: 2 }], next_page_token: "next" });
  listSchemas.mockResolvedValueOnce({
    schemas: [{ version: 5 }, { version: 1 }],
    next_page_token: "",
  });
  const { result } = renderHook(() => useSchemaRegistry("payments", "runtime"));
  await waitFor(() =>
    expect(result.current.schemas?.map((schema) => schema.version)).toEqual([5, 2, 1]),
  );
  expect(listSchemas).toHaveBeenNthCalledWith(2, "payments", "runtime", "next", expect.anything());
});

it("ignores a previous application's late page even when abort is ignored", async () => {
  let finish: (value: unknown) => void = () => undefined;
  listSchemas.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  listSchemas.mockResolvedValueOnce({ schemas: [{ version: 3 }], next_page_token: "" });
  const { result, rerender } = renderHook(({ app }) => useSchemaRegistry(app), {
    initialProps: { app: "old" },
  });
  rerender({ app: "new" });
  await waitFor(() => expect(result.current.schemas?.[0].version).toBe(3));
  await act(async () => finish({ schemas: [{ version: 99 }], next_page_token: "stale" }));
  expect(result.current.schemas?.[0].version).toBe(3);
  expect(listSchemas).toHaveBeenCalledTimes(2);
});

it("refreshes available tracks after registration without changing the application", async () => {
  listSchemas.mockResolvedValueOnce({ schemas: [{ version: 1 }], next_page_token: "" });
  listSchemas.mockResolvedValueOnce({
    schemas: [{ version: 2 }, { version: 1 }],
    next_page_token: "",
  });
  const { result } = renderHook(() => useSchemaRegistry("payments"));
  await waitFor(() => expect(result.current.schemas?.[0].version).toBe(1));
  act(() => result.current.reload());
  await waitFor(() => expect(result.current.schemas?.[0].version).toBe(2));
});
