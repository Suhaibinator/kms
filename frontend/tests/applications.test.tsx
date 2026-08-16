import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ApplicationsPage from "@/pages/applications";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  push: vi.fn(async () => true),
  listApplications: vi.fn(),
  applicationDashboard: vi.fn(),
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
    mocks.toast.error.mockClear();
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
});
