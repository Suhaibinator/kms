import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ReleasesPage from "@/pages/releases";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  replace: vi.fn(async () => true),
  listReleases: vi.fn(),
  validateRelease: vi.fn(),
  getActiveRelease: vi.fn(),
  activateRelease: vi.fn(),
  releaseSubscribers: vi.fn(),
  applicationDashboard: vi.fn(),
  parameterMetadata: vi.fn(),
  secretMetadata: vi.fn(),
  createRelease: vi.fn(),
  listSchemas: vi.fn(),
  createSchema: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    query: mocks.query,
    pathname: "/releases",
    isReady: true,
    replace: mocks.replace,
  }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/hooks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/hooks")>();
  return {
    ...actual,
    useNamespaces: () => ({
      namespaces: [
        {
          env: "prod",
          app: "payments",
          description: "",
          allowed_auth_methods: [],
          created_by: "admin",
          created_at_unix_ms: 1,
          parameter_count: 1,
          secret_count: 0,
        },
      ],
      error: null,
      loading: false,
      reload: vi.fn(),
    }),
  };
});
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    isAbortError: () => false,
    api: {
      ...actual.api,
      listReleases: mocks.listReleases,
      validateRelease: mocks.validateRelease,
      getActiveRelease: mocks.getActiveRelease,
      activateRelease: mocks.activateRelease,
      releaseSubscribers: mocks.releaseSubscribers,
      applicationDashboard: mocks.applicationDashboard,
      parameterMetadata: mocks.parameterMetadata,
      secretMetadata: mocks.secretMetadata,
      createRelease: mocks.createRelease,
      listSchemas: mocks.listSchemas,
      createSchema: mocks.createSchema,
    },
  };
});

const releaseV1 = {
  namespace: { env: "prod", app: "payments" },
  name: "runtime",
  version: 1,
  schema_id: "payments/runtime",
  schema_version: 1,
  entries: [
    {
      alias: "runtime",
      kind: "parameter" as const,
      ref: { namespace: { env: "prod", app: "payments" }, key: "runtime" },
      version: 1,
      content_type: "json",
      metadata_json: "{}",
      parameter_digest: "old-digest",
      client_bound: false,
      has_access_token: false,
    },
  ],
  digest: "11111111111111111111111111111111",
  metadata_json: "{}",
  created_by: "admin",
  created_at_unix_ms: 1,
};

const releaseV2 = {
  ...releaseV1,
  version: 2,
  digest: "22222222222222222222222222222222",
  entries: [{ ...releaseV1.entries[0], version: 2, parameter_digest: "new-digest" }],
};

describe("ReleasesPage", () => {
  beforeEach(() => {
    mocks.query = {};
    for (const mock of Object.values(mocks)) {
      if (typeof mock === "function" && "mockReset" in mock) mock.mockReset();
    }
    mocks.listReleases.mockResolvedValue({ releases: [], next_page_token: "" });
    mocks.listSchemas.mockResolvedValue({ schemas: [], next_page_token: "" });
    mocks.releaseSubscribers.mockResolvedValue({
      subscribers: [],
      current_revision: 0,
      next_page_token: "",
    });
  });

  it("loads the URL-selected schema tab lazily and keeps schema JSON out of the table", async () => {
    mocks.query = { tab: "schemas" };
    mocks.listSchemas.mockResolvedValue({
      schemas: [
        {
          id: "payments/runtime",
          version: 3,
          schema_json: '{"type":"object","properties":{"enabled":{"type":"boolean"}}}',
          digest: "abcdef0123456789abcdef0123456789",
          metadata_json: "{}",
          created_by: "admin",
          created_at_unix_ms: 1,
        },
      ],
      next_page_token: "",
    });

    render(<ReleasesPage />);
    expect(await screen.findByText("payments/runtime@3")).toBeVisible();
    expect(mocks.listReleases).not.toHaveBeenCalled();
    expect(screen.queryByText(/properties/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "View" }));
    const dialog = screen.getByRole("dialog", { name: "Schema payments/runtime@3" });
    expect(within(dialog).getByText(/"enabled":/)).toBeVisible();
    expect(within(dialog).getByRole("checkbox", { name: "Wrap lines" })).toBeChecked();
  });

  it("opens release details, compares same-name versions, and loads rollout state on demand", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.listReleases.mockResolvedValue({
      releases: [
        { release: releaseV2, current: true, previous: false, activation_revision: 8 },
        { release: releaseV1, current: false, previous: true, activation_revision: 7 },
      ],
      next_page_token: "",
    });
    mocks.releaseSubscribers.mockResolvedValue({
      subscribers: [
        {
          namespace: releaseV2.namespace,
          release_name: "runtime",
          client_name: "api",
          instance_id: "api-1",
          identity: "payments-client",
          state: "applied",
          release_version: 2,
          activation_revision: 8,
          rejection_category: "",
          diagnostic: "",
          client_timestamp_unix_ms: 1,
          server_timestamp_unix_ms: 1,
          connected: true,
        },
      ],
      current_revision: 8,
      next_page_token: "",
    });

    render(<ReleasesPage />);
    expect((await screen.findAllByText("runtime@2"))[0]).toBeVisible();
    fireEvent.click(screen.getAllByRole("button", { name: "View" })[0]);
    const dialog = screen.getByRole("dialog", { name: "Release runtime@2" });

    fireEvent.click(within(dialog).getByRole("tab", { name: "Compare" }));
    expect(await within(dialog).findByText(/old-digest/)).toBeVisible();
    expect(within(dialog).getByText(/new-digest/)).toBeVisible();

    fireEvent.click(within(dialog).getByRole("tab", { name: "Rollout status" }));
    expect(await within(dialog).findByText("api-1")).toBeVisible();
    expect(mocks.releaseSubscribers).toHaveBeenCalledWith(
      releaseV2.namespace,
      "runtime",
      1000,
      undefined,
    );
  });

  it("pre-populates the guided builder from the application contract and submits its API shape", async () => {
    mocks.query = { app: "payments", env: "prod" };
    mocks.applicationDashboard.mockResolvedValue({
      application: {
        name: "payments",
        description: "",
        release_name: "runtime",
        schema_id: "payments/runtime",
        schema_version: 3,
        contract: [{ alias: "runtime", kind: "parameter", content_type: "json" }],
        created_by: "admin",
        created_at_unix_ms: 1,
        updated_at_unix_ms: 1,
        environment_count: 1,
      },
      environments: [],
      rows: [
        {
          key: "runtime",
          kind: "parameter",
          environments: { prod: { present: true, content_type: "json", version: 4 } },
        },
      ],
    });
    mocks.createRelease.mockResolvedValue({ release: releaseV2 });

    render(<ReleasesPage />);
    await screen.findByText("No releases found");
    fireEvent.click(screen.getAllByRole("button", { name: "New release" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "New release · prod/payments" });
    expect(await within(dialog).findByRole("textbox", { name: "Release name" })).toBeDisabled();
    expect(within(dialog).getByRole("textbox", { name: "Schema ID" })).toBeDisabled();

    fireEvent.click(within(dialog).getByRole("button", { name: "Create immutable version" }));
    await waitFor(() =>
      expect(mocks.createRelease).toHaveBeenCalledWith({
        namespace: { env: "prod", app: "payments" },
        name: "runtime",
        schema_id: "payments/runtime",
        schema_version: 3,
        entries: [
          {
            alias: "runtime",
            kind: "parameter",
            ref: {
              namespace: { env: "prod", app: "payments" },
              key: "runtime",
            },
          },
        ],
        metadata_json: "{}",
      }),
    );
  });

  it("keeps invalid advanced JSON in place and blocks returning to Guided mode", async () => {
    mocks.query = { app: "payments", env: "prod" };
    mocks.applicationDashboard.mockResolvedValue({
      application: {
        name: "payments",
        description: "",
        release_name: "runtime",
        schema_id: "",
        schema_version: 0,
        contract: [],
        created_by: "admin",
        created_at_unix_ms: 1,
        updated_at_unix_ms: 1,
        environment_count: 1,
      },
      environments: [],
      rows: [],
    });

    render(<ReleasesPage />);
    await screen.findByText("No releases found");
    fireEvent.click(screen.getAllByRole("button", { name: "New release" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "New release · prod/payments" });
    fireEvent.click(within(dialog).getByRole("tab", { name: "JSON" }));
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Release definition" }), {
      target: { value: "{" },
    });
    expect(within(dialog).getByText(/Definition must be valid JSON/)).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Create immutable version" })).toBeDisabled();

    fireEvent.click(within(dialog).getByRole("tab", { name: "Guided" }));
    expect(
      within(dialog).getByText("Fix the JSON definition before returning to Guided mode."),
    ).toBeVisible();
    expect(within(dialog).getByRole("textbox", { name: "Release definition" })).toHaveValue("{");
  });
});
