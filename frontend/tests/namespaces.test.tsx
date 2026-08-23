import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Namespace } from "@/lib/types";
import NamespacesPage from "@/pages/namespaces";

const mocks = vi.hoisted(() => ({
  namespaces: {
    namespaces: [] as Namespace[],
    loading: false,
    error: null as unknown,
    reload: vi.fn(),
  },
  updateNamespace: vi.fn(),
  deleteNamespace: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", () => ({
  api: {
    updateNamespace: mocks.updateNamespace,
    deleteNamespace: mocks.deleteNamespace,
  },
}));
vi.mock("@/lib/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks")>()),
  useNamespaces: () => mocks.namespaces,
}));

function namespace(env: string, counts: { parameters?: number; secrets?: number } = {}): Namespace {
  return {
    env,
    app: "payments-api",
    description: "",
    allowed_auth_methods: ["mtls"],
    created_by: "admin",
    created_at_unix_ms: 1,
    parameter_count: counts.parameters ?? 0,
    secret_count: counts.secrets ?? 0,
  };
}

describe("NamespacesPage", () => {
  beforeEach(() => {
    mocks.namespaces = { namespaces: [], loading: false, error: null, reload: vi.fn() };
    mocks.updateNamespace.mockReset();
    mocks.deleteNamespace.mockReset();
    mocks.toast.error.mockClear();
    mocks.toast.success.mockClear();
  });

  it("renders a load failure as an error state with a retry, not as an empty list", () => {
    mocks.namespaces.error = new Error("offline");
    render(<NamespacesPage />);
    expect(screen.getByText("Could not load environments")).toBeVisible();
    expect(screen.queryByText("No application environments yet")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(mocks.namespaces.reload).toHaveBeenCalledTimes(1);
    expect(mocks.toast.error).toHaveBeenCalledTimes(1);
  });

  it("shows the skeleton only while there is nothing to show yet", () => {
    mocks.namespaces.loading = true;
    const { rerender } = render(<NamespacesPage />);
    expect(screen.getByText("Loading…")).toBeInTheDocument();

    // A same-session reload keeps the previous list; the table stays put.
    mocks.namespaces = { ...mocks.namespaces, namespaces: [namespace("dev")] };
    rerender(<NamespacesPage />);
    expect(screen.queryByText("Loading…")).toBeNull();
    expect(screen.getByText("dev")).toBeVisible();
    expect(screen.getByText("dev").closest("[aria-busy]")).toHaveAttribute("aria-busy", "true");
  });

  it("links each namespace to its parameters and secrets", () => {
    mocks.namespaces.namespaces = [namespace("dev", { parameters: 3, secrets: 2 })];
    render(<NamespacesPage />);
    expect(screen.getByRole("link", { name: "3" })).toHaveAttribute(
      "href",
      "/parameters?env=dev&app=payments-api",
    );
    expect(screen.getByRole("link", { name: "2" })).toHaveAttribute(
      "href",
      "/secrets?env=dev&app=payments-api",
    );
  });

  it("explains why a non-empty namespace cannot be deleted", () => {
    mocks.namespaces.namespaces = [namespace("dev", { parameters: 3, secrets: 2 })];
    render(<NamespacesPage />);
    const remove = screen.getByRole("button", { name: "Delete" });
    expect(remove).toBeDisabled();
    expect(remove).toHaveAccessibleDescription(/holds 3 parameter\(s\) and 2 secret\(s\)/);
  });

  it("requires at least one auth method inline before saving", async () => {
    mocks.namespaces.namespaces = [namespace("dev")];
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog");
    const save = within(modal).getByRole("button", { name: "Save changes" });
    expect(save).toBeEnabled();

    fireEvent.click(within(modal).getByRole("checkbox", { name: /mTLS/ }));
    expect(within(modal).getByRole("alert").textContent).toContain(
      "Select at least one allowed auth method.",
    );
    expect(save).toBeDisabled();
    expect(mocks.toast.error).not.toHaveBeenCalled();

    fireEvent.click(within(modal).getByRole("checkbox", { name: /Token/ }));
    expect(within(modal).queryByRole("alert")).toBeNull();
    expect(save).toBeEnabled();
  });
});
