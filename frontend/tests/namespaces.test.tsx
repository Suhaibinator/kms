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
  router: { isReady: true, query: {} as Record<string, string>, replace: vi.fn() },
  updateNamespace: vi.fn(),
  deleteNamespace: vi.fn(),
  listIdentities: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

// The table's sort state lives in the URL, so the page reads and writes a router.
vi.mock("next/router", () => ({ useRouter: () => mocks.router }));
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
    identities?: number;
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
    identity_count: counts.identities ?? 0,
  };
}

describe("NamespacesPage", () => {
  beforeEach(() => {
    mocks.namespaces = { namespaces: [], loading: false, error: null, reload: vi.fn() };
    mocks.router.query = {};
    mocks.router.replace.mockReset();
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

  it("reserves the loaded table's column count while loading", () => {
    mocks.namespaces.loading = true;
    const { rerender } = render(<NamespacesPage />);
    const skeletonColumns = document.querySelectorAll("thead th").length;

    mocks.namespaces = { ...mocks.namespaces, loading: false, namespaces: [namespace("dev")] };
    rerender(<NamespacesPage />);
    // The loaded header adds an actions gutter the skeleton must stand in for,
    // or every column shifts the moment the data arrives.
    expect(document.querySelectorAll("thead th")).toHaveLength(skeletonColumns);
  });

  it("reserves the group heading, the toolbar and the summary the loaded list has", () => {
    mocks.namespaces.loading = true;
    render(<NamespacesPage />);

    // The loaded page is one .ns-group per application, with a heading block
    // above its table, a mobile toolbar inside the card and a summary caption
    // below it. A bare table skeleton left all three out.
    expect(document.querySelector(".ns-group")).not.toBeNull();
    expect(document.querySelector(".ns-group-title")).not.toBeNull();
    expect(document.querySelector(".mobile-list-toolbar")).not.toBeNull();
    expect(document.querySelector("caption.table-summary")).not.toBeNull();
    // And the loaded table's own class, which carries its column widths.
    expect(document.querySelector("table.data")).toHaveClass("namespace-table");
  });

  it("reorders every environment table from a column header and records it in the URL", () => {
    mocks.namespaces.namespaces = [
      namespace("dev", { parameters: 9 }),
      namespace("prod", { parameters: 1 }),
    ];
    const environments = () =>
      [...document.querySelectorAll('table.data tbody td[data-label="Environment"]')].map(
        (cell) => cell.textContent ?? "",
      );

    const { rerender } = render(<NamespacesPage />);
    expect(environments()).toEqual(["dev", "prod"]);
    // The whole list is loaded here, so the footer's total is the real one.
    expect(screen.getByTestId("table-summary")).toHaveTextContent("Showing 2 of 2 environments");

    fireEvent.click(screen.getByRole("button", { name: "Parameters" }));
    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      { pathname: "/namespaces", query: { sort: "parameters", dir: "asc" } },
      undefined,
      { shallow: true, scroll: false },
    );

    // The URL is the source of truth, so land the router on what the click asked for.
    mocks.router.query = { sort: "parameters", dir: "asc" };
    rerender(<NamespacesPage />);
    expect(environments()).toEqual(["prod", "dev"]);
    expect(screen.getByRole("button", { name: "Parameters" }).closest("th")).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
  });

  it("links each namespace to its parameters and secrets", () => {
    mocks.namespaces.namespaces = [namespace("dev", { parameters: 3, secrets: 2 })];
    render(<NamespacesPage />);
    expect(screen.getByRole("link", { name: "Manage payments-api/dev" })).toHaveAttribute(
      "href",
      "/applications?app=payments-api&env=dev",
    );
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
    expect(remove).toHaveTextContent(
      /holds 3 parameter\(s\), 2 secret\(s\), and 0 bound identities/,
    );
    expect(mocks.deleteNamespace).not.toHaveBeenCalled();
  });

  it("blocks deletion for bound identities and links to the place that resolves them", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { identities: 2 })];
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "More for dev/payments-api" }));

    expect(await screen.findByRole("menuitem", { name: /Delete environment/ })).toHaveTextContent(
      "2 bound identities",
    );
    expect(screen.getByRole("menuitem", { name: "Manage bound identities" })).toHaveAttribute(
      "href",
      "/identities?env=dev&app=payments-api",
    );
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

  it("shows the namespace being edited as read-only, not as a disabled control", async () => {
    mocks.namespaces.namespaces = [namespace("dev")];
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog");

    // It is a display of what is being edited, so it has to stay legible and
    // selectable; `disabled` greyed it out and took it off the tab order.
    const field = within(modal).getByLabelText("Namespace");
    expect(field).toHaveValue("dev/payments-api");
    expect(field).toHaveAttribute("readonly");
    expect(field).toBeEnabled();
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
    expect(within(modal).getByText(/additional impact is unknown/i)).toBeVisible();

    fireEvent.click(within(modal).getByRole("button", { name: "Save changes" }));
    const confirm = await screen.findByRole("dialog", { name: "Remove authentication method?" });
    expect(mocks.updateNamespace).not.toHaveBeenCalled();
    expect(confirm).toHaveTextContent(/policy-granted access/i);
    expect(confirm).not.toHaveTextContent(/1 identity stops/i);
    fireEvent.click(within(confirm).getByRole("button", { name: "Save with unknown impact" }));
    await waitFor(() =>
      expect(mocks.updateNamespace).toHaveBeenCalledWith({
        env: "dev",
        app: "payments-api",
        description: "",
        allowed_auth_methods: ["mtls"],
      }),
    );
  });

  it("treats a live unbound credential as unknown policy impact", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { methods: ["mtls", "token"] })];
    mocks.listIdentities.mockResolvedValue({
      identities: [
        {
          name: "automation",
          kind: "client",
          namespace: null,
          has_token: true,
          certs: [],
        },
      ],
      next_page_token: "",
    });
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog", { name: "Edit dev/payments-api" });
    await waitFor(() => expect(mocks.listIdentities).toHaveBeenCalled());
    fireEvent.click(within(modal).getByRole("checkbox", { name: /Token/ }));
    fireEvent.click(within(modal).getByRole("button", { name: "Save changes" }));

    const confirm = await screen.findByRole("dialog", { name: "Remove authentication method?" });
    expect(confirm).toHaveTextContent(/number of credentials this disables is unknown/i);
    expect(within(confirm).getByRole("button", { name: "Save with unknown impact" })).toBeEnabled();
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

  it("confirms unknown impact while the identity check is still loading", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { methods: ["mtls", "token"] })];
    mocks.listIdentities.mockReturnValue(new Promise(() => {}));
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog", { name: "Edit dev/payments-api" });
    fireEvent.click(within(modal).getByRole("checkbox", { name: /Token/ }));
    expect(within(modal).getByText(/impact is unknown until this finishes/i)).toBeVisible();

    fireEvent.click(within(modal).getByRole("button", { name: "Save changes" }));
    const confirm = await screen.findByRole("dialog", { name: "Remove authentication method?" });
    expect(confirm).toHaveTextContent(/identity check is still running/i);
    expect(within(confirm).getByRole("button", { name: "Save with unknown impact" })).toBeEnabled();
    expect(mocks.updateNamespace).not.toHaveBeenCalled();
  });

  it("keeps failed identity impact unknown and lets the user retry", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { methods: ["mtls", "token"] })];
    mocks.listIdentities
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({ identities: [], next_page_token: "" });
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog", { name: "Edit dev/payments-api" });
    fireEvent.click(within(modal).getByRole("checkbox", { name: /Token/ }));

    expect(await within(modal).findByText(/impact is unknown/i)).toBeVisible();
    fireEvent.click(within(modal).getByRole("button", { name: "Save changes" }));
    expect(
      await screen.findByRole("dialog", { name: "Remove authentication method?" }),
    ).toHaveTextContent(/identity check failed/i);
    fireEvent.click(
      within(screen.getByRole("dialog", { name: "Remove authentication method?" })).getByRole(
        "button",
        { name: "Cancel" },
      ),
    );
    fireEvent.click(within(modal).getByRole("button", { name: "Retry identity check" }));
    await waitFor(() => expect(mocks.listIdentities).toHaveBeenCalledTimes(2));
  });

  it("treats the 2,000-identity scan cap as incomplete impact", async () => {
    mocks.namespaces.namespaces = [namespace("dev", { methods: ["mtls", "token"] })];
    mocks.listIdentities.mockImplementation(async (_limit, token) => ({
      identities: [],
      next_page_token: token ? `${token}-next` : "page-2",
    }));
    render(<NamespacesPage />);
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const modal = await screen.findByRole("dialog", { name: "Edit dev/payments-api" });
    fireEvent.click(within(modal).getByRole("checkbox", { name: /Token/ }));

    expect(await within(modal).findByText(/first 2,000 identities/i)).toBeVisible();
    expect(mocks.listIdentities).toHaveBeenCalledTimes(10);
    expect(within(modal).getByRole("link", { name: "Review bound identities" })).toHaveAttribute(
      "href",
      "/identities?env=dev&app=payments-api",
    );
    fireEvent.click(within(modal).getByRole("button", { name: "Save changes" }));
    const confirm = await screen.findByRole("dialog", { name: "Remove authentication method?" });
    expect(confirm).toHaveTextContent(/More than 2,000 identities exist/i);
    expect(within(confirm).getByRole("button", { name: "Save with unknown impact" })).toBeEnabled();
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
