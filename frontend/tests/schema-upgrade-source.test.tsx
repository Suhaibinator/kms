import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { SchemaUpgradeSource } from "@/components/applications/SchemaUpgradeSource";
import ready from "./fixtures/backend/overview-ready.json";
const mocks = vi.hoisted(() => ({ listSchemas: vi.fn(), applicationOverview: vi.fn() }));
vi.mock("@/lib/api", async (original) => ({
  ...(await original<typeof import("@/lib/api")>()),
  api: mocks,
}));
const overview = (schemaVersion: number) => {
  const result = structuredClone(ready);
  result.application.schema_version = schemaVersion;
  for (const environment of result.environments)
    if (environment.release.active) environment.release.active.schema_version = schemaVersion;
  return result;
};
beforeEach(() => {
  vi.clearAllMocks();
  mocks.listSchemas.mockResolvedValue({
    schemas: [{ version: 3 }, { version: 2 }, { version: 1 }],
    next_page_token: "",
  });
  mocks.applicationOverview.mockImplementation((_app, _env, _options, version) =>
    Promise.resolve(overview(version)),
  );
});
it("requires an explicit source and offers older tracks including schema-free", async () => {
  const onSelect = vi.fn();
  render(
    <SchemaUpgradeSource
      application="gradethis"
      destination={3}
      onSelect={onSelect}
      onCancel={vi.fn()}
    />,
  );
  const select = await screen.findByRole("combobox", { name: "Source track and environment" });
  expect(screen.getByRole("button", { name: "Continue upgrade" })).toBeDisabled();
  expect(screen.getByRole("option", { name: /schema v1 · dev/ })).toBeInTheDocument();
  fireEvent.change(select, { target: { value: JSON.stringify([0, "dev"]) } });
  fireEvent.click(screen.getByRole("button", { name: "Continue upgrade" }));
  expect(onSelect).toHaveBeenCalledWith(0, "dev");
  expect(mocks.applicationOverview.mock.calls.map((call) => call[3])).toEqual([2, 1, 0]);
});
it("discards source lookup results after the destination changes", async () => {
  let resolveOld!: (value: ReturnType<typeof overview>) => void;
  mocks.applicationOverview.mockReturnValueOnce(
    new Promise((resolve) => {
      resolveOld = resolve;
    }),
  );
  const props = { application: "gradethis", onSelect: vi.fn(), onCancel: vi.fn() };
  const view = render(<SchemaUpgradeSource {...props} destination={3} />);
  await waitFor(() => expect(mocks.applicationOverview).toHaveBeenCalledTimes(3));
  view.rerender(<SchemaUpgradeSource {...props} destination={2} />);
  await screen.findByRole("combobox", { name: "Source track and environment" });
  await act(async () => resolveOld(overview(2)));
  expect(screen.queryByRole("option", { name: /schema v2/ })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Continue upgrade" })).toBeDisabled();
});
