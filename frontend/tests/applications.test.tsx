import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ApplicationsPage from "@/pages/applications";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  push: vi.fn(async () => true),
  listApplications: vi.fn(),
  applicationDashboard: vi.fn(),
  createSecret: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({ query: mocks.query, isReady: true, push: mocks.push }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", () => ({
  isAbortError: () => false,
  api: {
    listApplications: mocks.listApplications,
    applicationDashboard: mocks.applicationDashboard,
    createSecret: mocks.createSecret,
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

describe("ApplicationsPage", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.push.mockClear();
    mocks.listApplications.mockReset();
    mocks.applicationDashboard.mockReset();
    mocks.createSecret.mockReset();
    mocks.toast.error.mockClear();
  });

  // Without this, each render leaks into the next and role queries match the
  // previous test's DOM as well as this one's.
  afterEach(cleanup);

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

  it("lists application-owned contracts", async () => {
    mocks.listApplications.mockResolvedValue({ applications: [application], next_page_token: "" });
    render(<ApplicationsPage />);
    expect(await screen.findByText("payments-api")).toBeVisible();
    expect(screen.getByText("payments/runtime@3")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Manage" }));
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith({
        pathname: "/applications",
        query: { app: "payments-api" },
      }),
    );
  });

  it("renders values and missing state across environments", async () => {
    mocks.query = { app: "payments-api" };
    mocks.applicationDashboard.mockResolvedValue({
      application,
      environments: [
        {
          env: "dev",
          app: "payments-api",
          description: "",
          allowed_auth_methods: ["mtls"],
          created_by: "admin",
          created_at_unix_ms: 1,
          parameter_count: 1,
          secret_count: 0,
        },
        {
          env: "prod-gcp",
          app: "payments-api",
          description: "",
          allowed_auth_methods: ["mtls"],
          created_by: "admin",
          created_at_unix_ms: 1,
          parameter_count: 0,
          secret_count: 0,
        },
      ],
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

  it("creates a standard secret without leaving the application workspace", async () => {
    mocks.query = { app: "payments-api" };
    const dashboard = {
      application,
      environments: [
        {
          env: "dev",
          app: "payments-api",
          description: "",
          allowed_auth_methods: ["mtls"],
          created_by: "admin",
          created_at_unix_ms: 1,
          parameter_count: 0,
          secret_count: 0,
        },
      ],
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
});
