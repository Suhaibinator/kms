import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Identity, Namespace } from "@/lib/types";
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
  listIdentities: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", () => ({
  api: {
    updateNamespace: mocks.updateNamespace,
    deleteNamespace: mocks.deleteNamespace,
    listIdentities: mocks.listIdentities,
  },
}));
vi.mock("@/lib/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks")>()),
  useNamespaces: () => mocks.namespaces,
}));

function namespace(
  env: string,
  counts: {
    parameters?: number;
    secrets?: number;
    methods?: Namespace["allowed_auth_methods"];
  } = {},
): Namespace {
  return {
    env,
    app: "payments-api",
    description: "",
    allowed_auth_methods: counts.methods ?? ["mtls"],
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
    mocks.listIdentities.mockReset().mockResolvedValue({ identities: [], next_page_token: "" });
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

  it("explains why a non-empty namespace cannot be deleted", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { parameters: 3, secrets: 2 })];
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "More for dev/payments-api" }));
    const remove = await screen.findByRole("menuitem", { name: /Delete environment/ });
    expect(remove).toHaveAttribute("aria-disabled", "true");
    expect(remove).toHaveTextContent(/holds 3 parameter\(s\) and 2 secret\(s\)/);
    expect(mocks.deleteNamespace).not.toHaveBeenCalled();
  });

  it("deletes an empty namespace from the row menu after a named confirmation", async () => {
    mocks.namespaces.namespaces = [namespace("dev")];
    mocks.deleteNamespace.mockResolvedValue({});
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "More for dev/payments-api" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete environment" }));
    const confirm = await screen.findByRole("dialog", { name: "Delete namespace?" });
    expect(confirm).toHaveTextContent("dev/payments-api");
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete namespace" }));
    await waitFor(() =>
      expect(mocks.deleteNamespace).toHaveBeenCalledWith({ env: "dev", app: "payments-api" }),
    );
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

  it("warns which identities an auth-method removal breaks and confirms before saving", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { methods: ["mtls", "token"] })];
    const bound: Identity[] = [
      {
        name: "payments-worker",
        kind: "client",
        namespace: { env: "dev", app: "payments-api" },
        has_token: true,
        certs: [],
      },
      {
        name: "payments-cert-only",
        kind: "client",
        namespace: { env: "dev", app: "payments-api" },
        has_token: false,
        certs: [],
      },
      {
        name: "elsewhere",
        kind: "client",
        namespace: { env: "prod", app: "payments-api" },
        has_token: true,
        certs: [],
      },
      { name: "root", kind: "admin", namespace: null, has_token: true, certs: [] },
    ];
    mocks.listIdentities.mockResolvedValue({ identities: bound, next_page_token: "" });
    mocks.updateNamespace.mockResolvedValue({});
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog", { name: "Edit dev/payments-api" });
    await waitFor(() => expect(mocks.listIdentities).toHaveBeenCalled());
    expect(within(modal).queryByText(/breaks/)).toBeNull();

    fireEvent.click(within(modal).getByRole("checkbox", { name: /Token/ }));
    const warning = await within(modal).findByText(/Removing token authentication breaks/);
    expect(warning.closest(".warn-panel")).toHaveTextContent("1 identity: payments-worker.");
    expect(warning.closest(".warn-panel")).not.toHaveTextContent("elsewhere");
    expect(warning.closest(".warn-panel")).not.toHaveTextContent("root");

    fireEvent.click(within(modal).getByRole("button", { name: "Save changes" }));
    const confirm = await screen.findByRole("dialog", { name: "Remove authentication method?" });
    expect(mocks.updateNamespace).not.toHaveBeenCalled();
    expect(confirm).toHaveTextContent("payments-worker");
    fireEvent.click(within(confirm).getByRole("button", { name: "Save and break 1 identity" }));
    await waitFor(() =>
      expect(mocks.updateNamespace).toHaveBeenCalledWith({
        env: "dev",
        app: "payments-api",
        description: "",
        allowed_auth_methods: ["mtls"],
      }),
    );
  });

  it("saves directly when the removed method has no dependants", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { methods: ["mtls", "token"] })];
    mocks.updateNamespace.mockResolvedValue({});
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog");
    await waitFor(() => expect(mocks.listIdentities).toHaveBeenCalled());
    fireEvent.click(within(modal).getByRole("checkbox", { name: /Token/ }));
    fireEvent.click(within(modal).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(mocks.updateNamespace).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("dialog", { name: "Remove authentication method?" })).toBeNull();
  });

  it("asks before discarding an edited namespace", async () => {
    mocks.namespaces.namespaces = [namespace("dev")];
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog");
    await waitFor(() => expect(within(modal).getByLabelText("Description")).toHaveFocus());
    fireEvent.change(within(modal).getByLabelText("Description"), {
      target: { value: "Primary" },
    });
    fireEvent.click(within(modal).getByRole("button", { name: "Cancel", hidden: true }));
    const confirm = await screen.findByRole("dialog", { name: "Discard changes?", hidden: true });
    fireEvent.click(within(confirm).getByRole("button", { name: "Discard", hidden: true }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(mocks.updateNamespace).not.toHaveBeenCalled();
  });
});
