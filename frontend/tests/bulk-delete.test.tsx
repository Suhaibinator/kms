import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "@/lib/api";
import { bulkSummary, errorMessage, runBulk } from "@/lib/bulk";
import type { Identity, Namespace, Parameter } from "@/lib/types";
import IdentitiesPage from "@/pages/identities";
import ParametersPage from "@/pages/parameters/index";

const mocks = vi.hoisted(() => ({
  router: {
    isReady: true,
    pathname: "/parameters",
    query: {} as Record<string, string>,
    push: vi.fn(),
    replace: vi.fn(),
    events: { on: vi.fn(), off: vi.fn() },
  },
  identity: { name: "root", kind: "admin", namespace: null } as Identity,
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
}));

vi.mock("next/router", () => ({ useRouter: () => mocks.router }));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/context/AuthContext", () => ({ useAuth: () => ({ identity: mocks.identity }) }));

const NAMESPACE: Namespace = {
  env: "prod",
  app: "billing",
  description: "",
  allowed_auth_methods: ["mtls"],
  created_by: "admin",
  created_at_unix_ms: 1,
  parameter_count: 3,
  secret_count: 0,
};

vi.mock("@/lib/hooks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/hooks")>();
  return {
    ...actual,
    useNamespaces: () => ({
      namespaces: [
        {
          env: "prod",
          app: "billing",
          description: "",
          allowed_auth_methods: ["mtls"],
          created_by: "admin",
          created_at_unix_ms: 1,
          parameter_count: 3,
          secret_count: 0,
        },
      ],
      loading: false,
      error: null,
      reload: vi.fn(),
    }),
  };
});

function parameter(key: string): Parameter {
  return {
    env: NAMESPACE.env,
    app: NAMESPACE.app,
    key,
    value: "1",
    content_type: "integer",
    version: 1,
    metadata_json: "{}",
    created_by: "admin",
    created_at_unix_ms: 1,
    labels: { current: 1 },
  };
}

function identity(name: string, overrides: Partial<Identity> = {}): Identity {
  return {
    name,
    kind: "client",
    namespace: { env: "prod", app: "billing" },
    has_token: false,
    certs: [],
    ...overrides,
  };
}

beforeEach(() => {
  mocks.router.isReady = true;
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
  mocks.router.replace.mockReset();
  mocks.identity = { name: "root", kind: "admin", namespace: null };
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

/** Ticks the named rows and opens the bulk confirmation. */
function selectAndConfirm(names: string[], action: string): HTMLElement {
  for (const name of names) {
    fireEvent.click(screen.getByRole("checkbox", { name: `Select ${name}` }));
  }
  const bar = screen.getByRole("region", { name: "Bulk actions" });
  expect(bar).toHaveTextContent(`${names.length} `);
  fireEvent.click(within(bar).getByRole("button", { name: action }));
  return screen.getByRole("dialog");
}

describe("bulk delete on the parameters list", () => {
  it("deletes each selected key with the per-row API and reports one summary", async () => {
    vi.spyOn(api, "listParameters")
      .mockResolvedValueOnce({
        parameters: [parameter("alpha"), parameter("beta"), parameter("gamma")],
        next_page_token: "",
      })
      .mockResolvedValue({ parameters: [parameter("gamma")], next_page_token: "" });
    const deleteParameter = vi.spyOn(api, "deleteParameter").mockResolvedValue({ revision: 2 });

    render(<ParametersPage />);
    expect(await screen.findByText("alpha")).toBeVisible();

    const dialog = selectAndConfirm(["alpha", "beta"], "Delete selected");
    expect(dialog).toHaveAccessibleName("Delete 2 parameters?");
    // A production namespace is called out before anything is destroyed.
    expect(dialog).toHaveTextContent("prod/billing is a production environment.");
    expect(within(dialog).getByText("alpha")).toBeVisible();
    expect(within(dialog).getByText("beta")).toBeVisible();

    // The console's destructive pattern is type-to-confirm; bulk asks for the count.
    const confirm = within(dialog).getByRole("button", { name: "Delete 2 parameters" });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "2" } });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);

    await waitFor(() => expect(deleteParameter).toHaveBeenCalledTimes(2));
    expect(deleteParameter).toHaveBeenNthCalledWith(1, {
      env: "prod",
      app: "billing",
      key: "alpha",
    });
    expect(deleteParameter).toHaveBeenNthCalledWith(2, {
      env: "prod",
      app: "billing",
      key: "beta",
    });
    expect(mocks.toast.success).toHaveBeenCalledWith("Deleted 2 parameters", "alpha, beta");
    expect(mocks.toast.error).not.toHaveBeenCalled();
    // Selection clears once the run finishes, so the bar goes with it.
    await waitFor(() => expect(screen.queryByRole("region", { name: "Bulk actions" })).toBeNull());
  });

  it("names the failures with their own messages instead of reporting success", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [parameter("alpha"), parameter("beta")],
      next_page_token: "",
    });
    const deleteParameter = vi
      .spyOn(api, "deleteParameter")
      .mockResolvedValueOnce({ revision: 2 })
      .mockRejectedValueOnce(
        new ApiError("permission_denied", "policy denies parameter:delete", 403),
      );

    render(<ParametersPage />);
    expect(await screen.findByText("alpha")).toBeVisible();

    const dialog = selectAndConfirm(["alpha", "beta"], "Delete selected");
    fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete 2 parameters" }));

    // One failure must not stop the rest, and must not be swallowed either.
    await waitFor(() => expect(deleteParameter).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledTimes(1));
    expect(mocks.toast.error).toHaveBeenCalledWith(
      expect.objectContaining({ message: "beta: policy denies parameter:delete" }),
      "Deleted 1 of 2 parameters",
    );
    expect(mocks.toast.success).not.toHaveBeenCalled();
  });

  it("drops the selection when the filter changes the list under it", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [parameter("alpha"), parameter("beta")],
      next_page_token: "",
    });
    render(<ParametersPage />);
    expect(await screen.findByText("alpha")).toBeVisible();

    fireEvent.click(screen.getByRole("checkbox", { name: "Select alpha" }));
    expect(screen.getByRole("region", { name: "Bulk actions" })).toHaveTextContent(
      "1 parameter selected",
    );

    fireEvent.change(screen.getByLabelText("Key prefix"), { target: { value: "be" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(screen.queryByRole("region", { name: "Bulk actions" })).toBeNull());
  });

  it("offers no checkboxes to a client identity", async () => {
    mocks.identity = { name: "billing-api", kind: "client", namespace: NAMESPACE };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [parameter("alpha")],
      next_page_token: "",
    });
    render(<ParametersPage />);
    expect(await screen.findByText("alpha")).toBeVisible();
    expect(screen.queryAllByRole("checkbox")).toEqual([]);
    expect(screen.queryByRole("region", { name: "Bulk actions" })).toBeNull();
  });
});

describe("bulk revoke on the identities list", () => {
  it("revokes each selected identity with the per-row API", async () => {
    mocks.router.query = {};
    vi.spyOn(api, "listIdentities").mockResolvedValue({
      identities: [identity("alpha"), identity("beta"), identity("gone", { disabled: true })],
      next_page_token: "",
    });
    const revokeIdentity = vi.spyOn(api, "revokeIdentity").mockResolvedValue({});

    render(<IdentitiesPage />);
    expect(await screen.findByText("alpha")).toBeVisible();
    // A revoked identity has nothing left to revoke. Base UI renders the
    // checkbox as a span, so the disabled state is on aria-disabled.
    expect(screen.getByRole("checkbox", { name: "Select gone" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );

    const dialog = selectAndConfirm(["alpha", "beta"], "Revoke selected");
    expect(dialog).toHaveAccessibleName("Revoke 2 identities?");
    const confirm = within(dialog).getByRole("button", { name: "Revoke 2 identities" });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "2" } });
    fireEvent.click(confirm);

    await waitFor(() => expect(revokeIdentity).toHaveBeenCalledTimes(2));
    expect(revokeIdentity).toHaveBeenNthCalledWith(1, "alpha");
    expect(revokeIdentity).toHaveBeenNthCalledWith(2, "beta");
    expect(mocks.toast.success).toHaveBeenCalledWith("Revoked 2 identities", "alpha, beta");
  });

  it("selects and clears every selectable row from the header checkbox", async () => {
    mocks.router.query = {};
    vi.spyOn(api, "listIdentities").mockResolvedValue({
      identities: [identity("alpha"), identity("beta"), identity("gone", { disabled: true })],
      next_page_token: "",
    });
    render(<IdentitiesPage />);
    expect(await screen.findByText("alpha")).toBeVisible();

    const all = screen.getByRole("checkbox", { name: "Select all active identities on this page" });
    fireEvent.click(all);
    expect(screen.getByRole("region", { name: "Bulk actions" })).toHaveTextContent(
      "2 identities selected",
    );
    expect(screen.getByRole("checkbox", { name: "Select gone" })).not.toBeChecked();

    fireEvent.click(all);
    expect(screen.queryByRole("region", { name: "Bulk actions" })).toBeNull();
  });
});

describe("runBulk", () => {
  it("keeps going past a failure and reports progress as it lands", async () => {
    const seen: number[] = [];
    const result = await runBulk(
      ["a", "b", "c"],
      async (name) => {
        if (name === "b") throw new Error("nope");
        return name;
      },
      (completed) => seen.push(completed),
    );
    expect(result.succeeded).toEqual(["a", "c"]);
    expect(result.failures).toEqual([{ name: "b", message: "nope" }]);
    expect(seen).toEqual([1, 2, 3]);
  });

  it("runs one at a time, in the order given", async () => {
    const order: string[] = [];
    let running = 0;
    await runBulk(["a", "b", "c"], async (name) => {
      running += 1;
      expect(running).toBe(1);
      await Promise.resolve();
      order.push(name);
      running -= 1;
    });
    expect(order).toEqual(["a", "b", "c"]);
  });

  it("summarises a clean run and a partial one differently", () => {
    expect(bulkSummary({ succeeded: ["a"], failures: [] }, "Deleted", "parameters")).toEqual({
      ok: true,
      title: "Deleted 1 parameter",
      detail: "a",
    });
    expect(
      bulkSummary(
        { succeeded: ["a"], failures: [{ name: "b", message: "denied" }] },
        "Deleted",
        "parameters",
      ),
    ).toEqual({ ok: false, title: "Deleted 1 of 2 parameters", detail: "b: denied" });
  });

  it("reads a message off anything that was thrown", () => {
    expect(errorMessage(new ApiError("conflict", "already gone", 409))).toBe("already gone");
    expect(errorMessage("plain")).toBe("plain");
    expect(errorMessage(undefined)).toBe("Something went wrong.");
  });
});
