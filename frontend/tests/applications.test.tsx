import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ApplicationsPage from "@/pages/applications";

const mocks = vi.hoisted(() => {
  class ApiError extends Error {
    readonly code: string;
    readonly status: number;
    constructor(code: string, message: string, status: number) {
      super(message);
      this.name = "ApiError";
      this.code = code;
      this.status = status;
    }
  }
  return {
    ApiError,
    query: {} as Record<string, string>,
    isReady: true,
    push: vi.fn(async () => true),
    listApplications: vi.fn(),
    applicationDashboard: vi.fn(),
    createApplication: vi.fn(),
    createSecret: vi.fn(),
    putApplicationParameter: vi.fn(),
    toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
  };
});

vi.mock("next/router", () => ({
  useRouter: () => ({ query: mocks.query, isReady: mocks.isReady, push: mocks.push }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", () => ({
  ApiError: mocks.ApiError,
  isAbortError: () => false,
  api: {
    listApplications: mocks.listApplications,
    applicationDashboard: mocks.applicationDashboard,
    createApplication: mocks.createApplication,
    createSecret: mocks.createSecret,
    putApplicationParameter: mocks.putApplicationParameter,
  },
}));

const application = {
  name: "payments-api",
  description: "Payments",
  release_name: "runtime",
  schema_id: "payments/runtime",
  schema_version: 3,
  contract: [{ alias: "runtime", kind: "parameter" as const, content_type: "json" }],
  created_by: "admin",
  created_at_unix_ms: 1,
  updated_at_unix_ms: 1,
  environment_count: 2,
};

function environment(env: string, parameterCount = 0) {
  return {
    env,
    app: "payments-api",
    description: "",
    allowed_auth_methods: ["mtls"],
    created_by: "admin",
    created_at_unix_ms: 1,
    parameter_count: parameterCount,
    secret_count: 0,
  };
}

describe("ApplicationsPage", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.isReady = true;
    mocks.push.mockClear();
    mocks.listApplications.mockReset();
    mocks.applicationDashboard.mockReset();
    mocks.createApplication.mockReset();
    mocks.createSecret.mockReset();
    mocks.putApplicationParameter.mockReset();
    mocks.toast.error.mockClear();
    mocks.toast.success.mockClear();
  });

  /** Opens the "New application" modal and returns it. */
  async function openCreateModal(): Promise<HTMLElement> {
    mocks.listApplications.mockResolvedValue({ applications: [application], next_page_token: "" });
    render(<ApplicationsPage />);
    expect(await screen.findByText("payments-api")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "New application" }));
    return screen.getByRole("dialog");
  }

  it("blocks an application name that breaks the label rule", async () => {
    const modal = await openCreateModal();
    const name = within(modal).getByLabelText("Application name");
    // Uppercase and underscores are legal in a release alias but not in the
    // app half of a namespace, which is the rule this field follows.
    fireEvent.change(name, { target: { value: "Payments_API" } });
    fireEvent.blur(name);
    expect(within(modal).getByRole("alert").textContent).toContain("lowercase");
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(within(modal).getByRole("button", { name: /Create application/ })).toBeDisabled();

    fireEvent.change(name, { target: { value: "payments-api" } });
    expect(within(modal).queryByRole("alert")).toBeNull();
    expect(within(modal).getByRole("button", { name: /Create application/ })).toBeEnabled();
  });

  it("catches a duplicate contract alias before the server does", async () => {
    const modal = await openCreateModal();
    const contract = within(modal).getByLabelText("Shared release contract");
    fireEvent.change(contract, {
      target: {
        value: JSON.stringify([
          { alias: "runtime", kind: "parameter", content_type: "json" },
          { alias: "runtime", kind: "secret" },
        ]),
      },
    });
    fireEvent.blur(contract);
    expect(within(modal).getByRole("alert").textContent).toContain("Duplicate contract alias");
    expect(within(modal).getByRole("button", { name: /Create application/ })).toBeDisabled();
  });

  it("rejects a contract parameter with no content type", async () => {
    const modal = await openCreateModal();
    const contract = within(modal).getByLabelText("Shared release contract");
    // A parameter entry must pin a content type; only secrets may omit one.
    fireEvent.change(contract, {
      target: { value: JSON.stringify([{ alias: "runtime", kind: "parameter" }]) },
    });
    fireEvent.blur(contract);
    expect(within(modal).getByRole("alert").textContent).toContain("Content type");
    expect(within(modal).getByRole("button", { name: /Create application/ })).toBeDisabled();
  });

  it("marks both schema pin inputs invalid when only one half is set", async () => {
    const modal = await openCreateModal();
    const schemaId = within(modal).getByLabelText("Schema ID");
    fireEvent.change(schemaId, { target: { value: "payments/runtime" } });
    fireEvent.blur(schemaId);
    expect(within(modal).getByRole("alert")).toBeVisible();
    expect(schemaId).toHaveAttribute("aria-invalid", "true");
    expect(within(modal).getByLabelText("Schema version")).toHaveAttribute("aria-invalid", "true");
  });

  it("submits the New-application form from the keyboard", async () => {
    mocks.createApplication.mockResolvedValue({ application: { ...application, name: "orders" } });
    const modal = await openCreateModal();
    fireEvent.change(within(modal).getByLabelText("Application name"), {
      target: { value: "orders" },
    });
    const form = modal.querySelector("form");
    expect(form).not.toBeNull();
    fireEvent.submit(form as HTMLFormElement);
    await waitFor(() => expect(mocks.createApplication).toHaveBeenCalledTimes(1));
    expect(mocks.createApplication.mock.calls[0][0]).toMatchObject({ name: "orders" });
  });

  it("lists application-owned contracts", async () => {
    mocks.listApplications.mockResolvedValue({ applications: [application], next_page_token: "" });
    render(<ApplicationsPage />);
    expect(await screen.findByText("payments-api")).toBeVisible();
    expect(screen.getByText("payments/runtime@3")).toBeVisible();
    const applicationLink = screen.getByRole("link", { name: "Manage payments-api" });
    expect(applicationLink).toHaveAttribute("href", "/applications?app=payments-api");
    expect(applicationLink.closest("tr")).toHaveClass("application-row");
    expect(screen.queryByRole("button", { name: "Manage" })).toBeNull();
  });

  it("shows a skeleton instead of the list until the router has hydrated", () => {
    mocks.isReady = false;
    mocks.query = { app: "payments-api" };
    render(<ApplicationsPage />);
    expect(screen.queryByRole("button", { name: "New application" })).toBeNull();
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    expect(mocks.listApplications).not.toHaveBeenCalled();
    expect(mocks.applicationDashboard).not.toHaveBeenCalled();
  });

  it("renders values and missing state across environments", async () => {
    mocks.query = { app: "payments-api" };
    mocks.applicationDashboard.mockResolvedValue({
      application,
      environments: [environment("dev", 1), environment("prod-gcp")],
      rows: [
        {
          key: "rate-limit",
          kind: "parameter",
          environments: {
            dev: { present: true, value: "100", content_type: "integer", version: 2 },
          },
        },
      ],
    });
    render(<ApplicationsPage />);
    expect(await screen.findByText("rate-limit")).toBeVisible();
    expect(screen.getByText("100")).toBeVisible();
    expect(screen.getByText("missing")).toBeVisible();
    expect(screen.getByRole("button", { name: "Edit contract" })).toBeEnabled();
  });

  it("never renders a dashboard that arrives after the app has changed", async () => {
    let resolveFirst: (value: unknown) => void = () => undefined;
    mocks.applicationDashboard.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve;
        }),
    );
    mocks.query = { app: "payments-api" };
    const { rerender } = render(<ApplicationsPage />);
    expect(mocks.applicationDashboard).toHaveBeenCalledWith("payments-api", expect.anything());

    mocks.applicationDashboard.mockResolvedValueOnce({
      application: { ...application, name: "orders" },
      environments: [environment("dev")],
      rows: [{ key: "orders-key", kind: "parameter", environments: {} }],
    });
    mocks.query = { app: "orders" };
    rerender(<ApplicationsPage />);
    expect(await screen.findByText("orders-key")).toBeVisible();

    // The first request was aborted when the second began.
    const firstSignal = mocks.applicationDashboard.mock.calls[0][1].signal as AbortSignal;
    expect(firstSignal.aborted).toBe(true);

    resolveFirst({
      application,
      environments: [environment("dev")],
      rows: [{ key: "payments-key", kind: "parameter", environments: {} }],
    });
    await waitFor(() => expect(screen.getByText("orders-key")).toBeVisible());
    expect(screen.queryByText("payments-key")).toBeNull();
  });

  it("explains an unknown application instead of offering to add environments", async () => {
    mocks.query = { app: "ghost" };
    mocks.applicationDashboard.mockRejectedValue(
      new mocks.ApiError("not_found", "application not found", 404),
    );
    render(<ApplicationsPage />);
    expect(await screen.findByRole("heading", { name: "Application not found" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Add environment" })).toBeNull();
    expect(screen.getByRole("link", { name: /Back to applications/ })).toHaveAttribute(
      "href",
      "/applications",
    );
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("offers a retry when the dashboard cannot be loaded", async () => {
    mocks.query = { app: "payments-api" };
    mocks.applicationDashboard.mockRejectedValueOnce(new Error("offline"));
    render(<ApplicationsPage />);
    expect(
      await screen.findByRole("heading", { name: "Could not load application" }),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "Add environment" })).toBeNull();

    mocks.applicationDashboard.mockResolvedValueOnce({
      application,
      environments: [environment("dev")],
      rows: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No parameters or secrets have been created.")).toBeVisible();
  });

  it("resets the Add-environment form between openings", async () => {
    mocks.query = { app: "payments-api" };
    mocks.applicationDashboard.mockResolvedValue({
      application,
      environments: [environment("dev")],
      rows: [],
    });
    render(<ApplicationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Add environment" }));
    let modal = screen.getByRole("dialog");
    fireEvent.change(within(modal).getByLabelText("Environment"), {
      target: { value: "prod-gcp" },
    });
    fireEvent.change(within(modal).getByLabelText("Description"), {
      target: { value: "GCP production" },
    });
    fireEvent.click(within(modal).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Add environment" }));
    modal = await screen.findByRole("dialog");
    expect(within(modal).getByLabelText("Environment")).toHaveValue("");
    expect(within(modal).getByLabelText("Description")).toHaveValue("");
    expect(within(modal).queryByRole("alert")).toBeNull();
  });

  it("creates a standard secret without leaving the application workspace", async () => {
    mocks.query = { app: "payments-api" };
    const dashboard = {
      application,
      environments: [environment("dev")],
      rows: [],
    };
    mocks.applicationDashboard.mockResolvedValue(dashboard);
    mocks.createSecret.mockResolvedValue({ version: 1, revision: 4 });

    render(<ApplicationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "New secret" }));
    const modal = screen.getByRole("dialog");
    expect(within(modal).getByLabelText("Application")).toHaveValue("payments-api");
    expect(within(modal).getByLabelText("Environment")).toHaveTextContent("dev");

    fireEvent.change(within(modal).getByLabelText("Secret key"), {
      target: { value: "stripe-api-key" },
    });
    fireEvent.change(within(modal).getByLabelText("Secret value"), {
      target: { value: "super-secret" },
    });
    // A blank content type is the server's default, not a validation error.
    fireEvent.change(within(modal).getByLabelText("Content type"), { target: { value: "  " } });
    fireEvent.click(within(modal).getByRole("button", { name: "Create secret" }));

    await waitFor(() =>
      expect(mocks.createSecret).toHaveBeenCalledWith({
        env: "dev",
        app: "payments-api",
        key: "stripe-api-key",
        value_base64: "c3VwZXItc2VjcmV0",
        content_type: "text/plain",
        metadata_json: "{}",
        client_bound: false,
        generate_access_token: false,
        expires_at_unix_ms: 0,
      }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it("asks for an environment inline when several are available", async () => {
    mocks.query = { app: "payments-api" };
    mocks.applicationDashboard.mockResolvedValue({
      application,
      environments: [environment("dev"), environment("prod")],
      rows: [],
    });
    render(<ApplicationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "New secret" }));
    const modal = screen.getByRole("dialog");
    fireEvent.change(within(modal).getByLabelText("Secret key"), { target: { value: "k" } });
    fireEvent.change(within(modal).getByLabelText("Secret value"), { target: { value: "v" } });
    fireEvent.submit(modal.querySelector("form") as HTMLFormElement);
    expect(within(modal).getByRole("alert").textContent).toContain("Choose an environment.");
    expect(mocks.createSecret).not.toHaveBeenCalled();
  });

  it("keeps the bulk edit open and narrows the targets after a partial failure", async () => {
    mocks.query = { app: "payments-api" };
    mocks.applicationDashboard.mockResolvedValue({
      application,
      environments: [environment("dev", 1), environment("prod-gcp", 1), environment("non-prod", 1)],
      rows: [
        {
          key: "rate-limit",
          kind: "parameter",
          environments: {
            dev: { present: true, value: "100", content_type: "integer", version: 2 },
            "prod-gcp": { present: true, value: "100", content_type: "integer", version: 1 },
            "non-prod": { present: true, value: "100", content_type: "integer", version: 1 },
          },
        },
      ],
    });
    mocks.putApplicationParameter.mockResolvedValue({
      results: [
        { environment: "dev", version: 3 },
        { environment: "prod-gcp", error: "permission denied" },
        { environment: "non-prod", version: 2 },
      ],
    });
    render(<ApplicationsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const modal = screen.getByRole("dialog");
    // Only names that start with prod (or are exactly production) are flagged.
    expect(within(modal).getAllByText("production")).toHaveLength(1);
    expect(
      within(modal).getByRole("checkbox", { name: "prod-gcp" }).parentElement,
    ).toContainElement(within(modal).getByText("production"));

    fireEvent.change(within(modal).getByLabelText("Value"), { target: { value: "250" } });
    fireEvent.click(within(modal).getByRole("button", { name: "Apply to 3 environment(s)" }));
    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledTimes(1));
    expect(mocks.putApplicationParameter.mock.calls[0][0]).toMatchObject({
      environments: ["dev", "prod-gcp", "non-prod"],
    });

    expect(screen.getByRole("dialog")).toBeVisible();
    expect(within(modal).getByLabelText("Value")).toHaveValue("250");
    expect(within(modal).getByRole("checkbox", { name: "dev" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(within(modal).getByRole("checkbox", { name: "non-prod" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(within(modal).getByRole("checkbox", { name: "prod-gcp" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(within(modal).getByRole("button", { name: "Apply to 1 environment(s)" })).toBeEnabled();
    // The matrix is not reloaded underneath an edit that is still in progress.
    expect(mocks.applicationDashboard).toHaveBeenCalledTimes(1);
  });
});
