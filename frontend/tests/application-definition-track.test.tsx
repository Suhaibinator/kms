import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { ApplicationDefinitionModal } from "@/components/applications/ApplicationDefinitionModal";
import type { Application } from "@/lib/types";
import ready from "./fixtures/backend/overview-ready.json";

const mocks = vi.hoisted(() => ({
  getApplication: vi.fn(),
  updateApplication: vi.fn(),
  toast: { success: vi.fn(), error: vi.fn() },
}));
vi.mock("@/lib/api", () => ({
  api: { getApplication: mocks.getApplication, updateApplication: mocks.updateApplication },
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
beforeEach(() => vi.clearAllMocks());
it.each([
  [2, 1],
  [1, 0],
  [0, 2],
])(
  "keeps selected track %i read-only and preserves stored default %i",
  async (selectedVersion, storedVersion) => {
    const stored: Application = {
      ...(ready.application as Application),
      schema_version: storedVersion,
      contract: [],
    };
    const selected = {
      ...stored,
      schema_version: selectedVersion,
      contract: [{ alias: "extra", kind: "parameter" as const, content_type: "string" }],
    };
    mocks.getApplication.mockResolvedValue({ application: stored });
    mocks.updateApplication.mockResolvedValue({
      application: { ...stored, description: "updated description" },
    });
    render(
      <ApplicationDefinitionModal
        open
        application={selected}
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: "Add alias" })).toBeNull();
    expect(screen.queryByRole("textbox", { name: /Alias/ })).toBeNull();
    expect(screen.getByText("extra")).toBeVisible();
    expect(screen.getByText(/Established contracts are immutable/)).toBeVisible();
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "updated description" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save definition" }));
    await waitFor(() =>
      expect(mocks.updateApplication).toHaveBeenCalledWith({
        name: stored.name,
        release_name: stored.release_name,
        schema_version: stored.schema_version,
        contract: stored.contract,
        description: "updated description",
      }),
    );
  },
);
