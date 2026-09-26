import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useApplicationActions } from "@/components/applications/useApplicationActions";
import type { ApplicationOverview, ApplicationConfigurationRow } from "@/lib/types";
import readyJson from "./fixtures/backend/overview-ready.json";

const mocks = vi.hoisted(() => ({ write: vi.fn(), reload: vi.fn(async () => undefined) }));
vi.mock("next/router", () => ({
  useRouter: () => ({ query: {}, pathname: "/applications", replace: vi.fn() }),
}));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: { name: "root", kind: "admin", namespace: null } }),
}));
vi.mock("@/context/ToastContext", () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn() }),
}));
vi.mock("@/lib/useSchemaRegistry", () => ({
  useSchemaRegistry: () => ({ schemas: [], reload: vi.fn() }),
}));
vi.mock("@/lib/api", async (original) => {
  const actual = await original<typeof import("@/lib/api")>();
  return { ...actual, api: { ...actual.api, putApplicationParameter: mocks.write } };
});
const overview = readyJson as unknown as ApplicationOverview;
const row: ApplicationConfigurationRow = {
  key: "banner",
  kind: "parameter",
  environments: {
    dev: { present: true, value: "dev value", content_type: "string", version: 1 },
    prod: { present: true, value: "prod value", content_type: "string", version: 1 },
  },
};
const emptyRow: ApplicationConfigurationRow = { key: "", kind: "parameter", environments: {} };
function Harness({ create = false }: { create?: boolean }) {
  const { actions, modals } = useApplicationActions({
    overview,
    reload: mocks.reload,
    pathname: "/applications",
  });
  return (
    <>
      <button type="button" onClick={() => actions.openWriteRow(create ? emptyRow : row)}>
        Edit values
      </button>
      {modals}
    </>
  );
}

describe("persistent application write outcomes", () => {
  beforeEach(() => {
    mocks.write.mockReset();
    mocks.reload.mockClear();
  });
  it("keeps a partial new-key retry tied to its original key and value", async () => {
    mocks.write
      .mockResolvedValueOnce({
        results: [
          { environment: "dev", version: 1, revision: 20 },
          { environment: "prod", version: 0, revision: 0, error: "Temporary failure" },
        ],
      })
      .mockResolvedValueOnce({ results: [{ environment: "prod", version: 1, revision: 21 }] });
    render(<Harness create />);
    fireEvent.click(screen.getByRole("button", { name: "Edit values" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Key" }), { target: { value: "key-a" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Value" }), {
      target: { value: "original value" },
    });
    fireEvent.click(screen.getByRole("checkbox", { name: "prod" }));
    fireEvent.click(screen.getByRole("button", { name: "Review 2 environments" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply to 2 environments" }));
    await screen.findByText("Temporary failure");
    const key = screen.getByRole("textbox", { name: "Key" });
    const value = screen.getByRole("textbox", { name: "Value" });
    expect(key).toBeDisabled();
    expect(value).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Content type" })).toBeDisabled();
    fireEvent.change(key, { target: { value: "key-b" } });
    fireEvent.change(value, { target: { value: "different value" } });
    fireEvent.click(screen.getByRole("button", { name: "Retry failed environments" }));
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(2));
    expect(mocks.write.mock.calls[1][0]).toMatchObject({
      key: "key-a",
      value: "original value",
      content_type: "string",
      environments: ["prod"],
    });
  });
  it("retains successes through a retry and never resubmits successful targets", async () => {
    mocks.write
      .mockResolvedValueOnce({
        results: [
          { environment: "dev", version: 2, revision: 20 },
          { environment: "prod", version: 0, revision: 0, error: "Temporary failure" },
        ],
      })
      .mockResolvedValueOnce({ results: [{ environment: "prod", version: 3, revision: 21 }] });
    render(<Harness />);
    fireEvent.click(screen.getByRole("button", { name: "Edit values" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "prod" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Value" }), {
      target: { value: "new value" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Review 2 environments" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply to 2 environments" }));
    await screen.findByText("Temporary failure");
    expect(screen.getByRole("region", { name: "Update results" })).toHaveTextContent(
      "dev: Saved v2",
    );
    fireEvent.click(screen.getByRole("button", { name: "Retry failed environments" }));
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(2));
    expect(mocks.write.mock.calls[1][0]).toMatchObject({
      environments: ["prod"],
      value: "new value",
    });
    await waitFor(() =>
      expect(screen.getByRole("region", { name: "Update results" })).toHaveTextContent(
        "prod: Saved v3",
      ),
    );
    expect(screen.getByRole("region", { name: "Update results" })).toHaveTextContent(
      "dev: Saved v2",
    );
    expect(screen.queryByRole("textbox", { name: "Value" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(mocks.reload).toHaveBeenCalledTimes(1);
  });
});
