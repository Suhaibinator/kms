import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api";
import ReleasesPage from "@/pages/releases";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  replace: vi.fn(async () => true),
  listReleases: vi.fn(),
  validateRelease: vi.fn(),
  getActiveRelease: vi.fn(),
  activateRelease: vi.fn(),
  releaseSubscribers: vi.fn(),
  subscriberStream: vi.fn(),
  getRelease: vi.fn(),
  rollbackRelease: vi.fn(),
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
      subscriberStream: mocks.subscriberStream,
      getRelease: mocks.getRelease,
      rollbackRelease: mocks.rollbackRelease,
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

const dashboardWithContract = {
  application: {
    name: "payments",
    description: "",
    release_name: "runtime",
    schema_id: "payments/runtime",
    schema_version: 3,
    contract: [{ alias: "runtime", kind: "parameter" as const, content_type: "json" }],
    created_by: "admin",
    created_at_unix_ms: 1,
    updated_at_unix_ms: 1,
    environment_count: 1,
  },
  environments: [],
  rows: [
    {
      key: "runtime",
      kind: "parameter" as const,
      environments: { prod: { present: true, content_type: "json", version: 4 } },
    },
  ],
};

const dashboardWithoutContract = {
  application: {
    ...dashboardWithContract.application,
    schema_id: "",
    schema_version: 0,
    contract: [],
  },
  environments: [],
  rows: [],
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
    // No stream endpoint: the rollout hook falls back to polling at once.
    mocks.subscriberStream.mockRejectedValue(new ApiError("unimplemented", "no stream", 404));
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
    expect(await within(dialog).findByText("api/api-1")).toBeVisible();
    expect(within(dialog).getByTestId("rollout-progress")).toHaveTextContent("1/1 applied");
    expect(mocks.releaseSubscribers).toHaveBeenCalledWith(
      releaseV2.namespace,
      "runtime",
      1000,
      undefined,
      expect.objectContaining({ signal: expect.anything() }),
    );
  });

  it("keeps the Rollout tab selected when an activation refreshes the list", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.listReleases
      .mockResolvedValueOnce({
        releases: [
          { release: releaseV2, current: true, previous: false, activation_revision: 8 },
          { release: releaseV1, current: false, previous: true, activation_revision: 7 },
        ],
        next_page_token: "",
      })
      // The refresh after activation returns equal data in fresh objects.
      .mockResolvedValue({
        releases: [
          { release: { ...releaseV1 }, current: true, previous: false, activation_revision: 9 },
          { release: { ...releaseV2 }, current: false, previous: true, activation_revision: 8 },
        ],
        next_page_token: "",
      });
    mocks.getActiveRelease.mockResolvedValue({ release: releaseV2, previous_version: 1 });
    mocks.activateRelease.mockResolvedValue({
      release: releaseV1,
      activation_revision: 9,
      previous_version: 2,
      changed: true,
    });

    render(<ReleasesPage />);
    expect((await screen.findAllByText("runtime@1"))[0]).toBeVisible();
    fireEvent.click(screen.getAllByRole("button", { name: "View" })[1]);
    const dialog = screen.getByRole("dialog", { name: "Release runtime@1" });
    fireEvent.click(within(dialog).getByRole("tab", { name: "Rollout status" }));
    await waitFor(() => expect(mocks.releaseSubscribers).toHaveBeenCalled());

    fireEvent.click(within(dialog).getByRole("button", { name: "Activate" }));
    const confirm = await screen.findByRole("dialog", { name: "Activate release?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Activate release" }));
    await waitFor(() => expect(mocks.listReleases).toHaveBeenCalledTimes(2));

    // The table now carries the refreshed summaries (v1 is current)...
    expect((await screen.findAllByText(/current · rev 9/))[0]).toBeInTheDocument();
    // ...and the workspace is still open on the same release and the same tab.
    const reopened = screen.getByRole("dialog", { name: "Release runtime@1" });
    expect(within(reopened).getByRole("tab", { name: "Rollout status" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("renders validation failures in the workspace violations table", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.listReleases.mockResolvedValue({
      releases: [{ release: releaseV2, current: false, previous: false, activation_revision: 0 }],
      next_page_token: "",
    });
    mocks.validateRelease.mockResolvedValue({
      valid: false,
      errors: [
        {
          alias: "runtime",
          code: "schema_violation",
          schema_pointer: "/properties/enabled",
          message: "expected boolean",
        },
      ],
    });

    render(<ReleasesPage />);
    expect((await screen.findAllByText("runtime@2"))[0]).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));

    const dialog = await screen.findByRole("dialog", { name: "Release runtime@2" });
    const panel = await within(dialog).findByRole("alert");
    expect(panel).toHaveTextContent("runtime@2 failed validation");
    expect(within(panel).getByText("expected boolean")).toBeVisible();
    expect(within(panel).getByText("/properties/enabled")).toBeVisible();
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("refreshes the list after creating a release that matches the active name filter", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.applicationDashboard.mockResolvedValue(dashboardWithContract);
    mocks.createRelease.mockResolvedValue({ release: releaseV2 });

    render(<ReleasesPage />);
    await screen.findByText("No releases found");
    expect(mocks.listReleases).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getAllByRole("button", { name: "New release" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "New release · prod/payments" });
    await within(dialog).findByRole("textbox", { name: "Release name" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create immutable version" }));

    await waitFor(() => expect(mocks.createRelease).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mocks.listReleases).toHaveBeenCalledTimes(2));
  });

  it("does not let a late URL update overwrite what the user typed into the name filter", async () => {
    mocks.query = { app: "payments", env: "prod" };
    const { rerender } = render(<ReleasesPage />);
    await screen.findByText("No releases found");

    const input = screen.getByRole("textbox", { name: "Release name" });
    fireEvent.change(input, { target: { value: "runtime" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
    expect(mocks.replace).toHaveBeenCalledWith(
      { pathname: "/releases", query: { app: "payments", env: "prod", name: "runtime" } },
      undefined,
      { shallow: true, scroll: false },
    );
    // The user keeps typing before the shallow replace lands in router.query.
    fireEvent.change(input, { target: { value: "runtime-canary" } });
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    rerender(<ReleasesPage />);

    expect(screen.getByRole("textbox", { name: "Release name" })).toHaveValue("runtime-canary");
  });

  it("pre-populates the guided builder from the application contract and submits its API shape", async () => {
    mocks.query = { app: "payments", env: "prod" };
    mocks.applicationDashboard.mockResolvedValue(dashboardWithContract);
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
    mocks.applicationDashboard.mockResolvedValue(dashboardWithoutContract);

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

  it("shows the guided blocking reason on click instead of an inert Create button", async () => {
    mocks.query = { app: "payments", env: "prod" };
    mocks.applicationDashboard.mockResolvedValue(dashboardWithoutContract);

    render(<ReleasesPage />);
    await screen.findByText("No releases found");
    fireEvent.click(screen.getAllByRole("button", { name: "New release" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "New release · prod/payments" });
    // No contract → one blank entry whose alias is empty, so the guided
    // definition is invalid before the user touches anything.
    await within(dialog).findByRole("textbox", { name: "Alias" });
    expect(within(dialog).queryByRole("alert")).toBeNull();

    const create = within(dialog).getByRole("button", { name: "Create immutable version" });
    expect(create).toBeEnabled();
    fireEvent.click(create);

    expect(within(dialog).getByRole("alert")).toHaveTextContent("Alias is required.");
    expect(mocks.createRelease).not.toHaveBeenCalled();
  });

  it("rejects an unknown entry kind in JSON mode", async () => {
    mocks.query = { app: "payments", env: "prod" };
    mocks.applicationDashboard.mockResolvedValue(dashboardWithoutContract);

    render(<ReleasesPage />);
    await screen.findByText("No releases found");
    fireEvent.click(screen.getAllByRole("button", { name: "New release" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "New release · prod/payments" });
    await within(dialog).findByRole("textbox", { name: "Alias" });
    fireEvent.click(within(dialog).getByRole("tab", { name: "JSON" }));
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Release definition" }), {
      target: {
        value: JSON.stringify({
          name: "runtime",
          entries: [{ alias: "runtime", kind: "banana", ref: { key: "runtime" } }],
        }),
      },
    });
    expect(within(dialog).getByText(/kind must be parameter or secret/)).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Create immutable version" })).toBeDisabled();
  });

  it("clears the form and offers Retry when the application contract fails to load", async () => {
    mocks.query = { app: "payments", env: "prod" };
    mocks.applicationDashboard
      .mockRejectedValueOnce(new Error("gateway timeout"))
      .mockResolvedValue(dashboardWithContract);

    render(<ReleasesPage />);
    await screen.findByText("No releases found");
    fireEvent.click(screen.getAllByRole("button", { name: "New release" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "New release · prod/payments" });
    const panel = await within(dialog).findByRole("alert");
    expect(panel).toHaveTextContent("gateway timeout");
    expect(within(dialog).queryByRole("textbox", { name: "Release name" })).toBeNull();
    expect(within(dialog).getByRole("button", { name: "Create immutable version" })).toBeDisabled();

    fireEvent.click(within(panel).getByRole("button", { name: "Retry" }));
    expect(await within(dialog).findByRole("textbox", { name: "Release name" })).toHaveValue(
      "runtime",
    );
  });

  it("resets the schema JSON when the register dialog is reopened", async () => {
    mocks.query = { tab: "schemas" };

    render(<ReleasesPage />);
    await screen.findByText("No schemas found");
    fireEvent.click(screen.getAllByRole("button", { name: "Register schema" })[0]);
    let dialog = await screen.findByRole("dialog", { name: "Register JSON Schema" });
    const editor = within(dialog).getByRole("textbox", { name: "JSON Schema definition" });
    fireEvent.change(editor, { target: { value: "{" } });
    expect(editor).toHaveValue("{");
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    fireEvent.click(screen.getAllByRole("button", { name: "Register schema" })[0]);
    dialog = await screen.findByRole("dialog", { name: "Register JSON Schema" });
    const reopened = within(dialog).getByRole("textbox", { name: "JSON Schema definition" });
    expect((reopened as HTMLTextAreaElement).value).toContain('"type": "object"');
  });
  it("renders release, schema and revision identifiers as typed chips with breadcrumbs", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.listReleases.mockResolvedValue({
      releases: [
        { release: releaseV2, current: true, previous: false, activation_revision: 8 },
        { release: releaseV1, current: false, previous: true, activation_revision: 7 },
      ],
      next_page_token: "",
    });

    render(<ReleasesPage />);
    await screen.findAllByText("runtime@2");
    const table = screen.getByRole("table");
    const chips = table.querySelectorAll(".ident.ident-release");
    expect(chips).toHaveLength(2);
    expect(chips[0]).toHaveTextContent("runtime@2");
    expect(table.querySelector(".ident.ident-schema")).toHaveTextContent("payments/runtime@1");

    const nav = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(nav).toHaveTextContent("Applications");
    expect(nav.querySelector(".ident-app")).toHaveTextContent("payments");
    expect(nav.querySelector(".ident-env")).toHaveTextContent("prod");
  });

  it("opens a ?release= deep link that is not in the loaded page by fetching it", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime", release: "runtime@1" };
    mocks.listReleases.mockResolvedValue({
      releases: [{ release: releaseV2, current: true, previous: false, activation_revision: 8 }],
      next_page_token: "",
    });
    mocks.getRelease.mockResolvedValue({ release: releaseV1 });
    mocks.getActiveRelease.mockResolvedValue({
      release: releaseV2,
      activation_revision: 8,
      previous_version: 1,
    });

    render(<ReleasesPage />);
    const dialog = await screen.findByRole("dialog", { name: "Release runtime@1" });
    expect(mocks.getRelease).toHaveBeenCalledWith({ env: "prod", app: "payments" }, "runtime", 1);
    expect(within(dialog).getByText("previous")).toBeVisible();

    // Closing writes the parameter back out of the URL.
    fireEvent.click(within(dialog).getByRole("button", { name: "Dismiss dialog" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Release runtime@1" })).toBeNull(),
    );
    expect(mocks.replace).toHaveBeenLastCalledWith(
      {
        pathname: "/releases",
        query: { app: "payments", env: "prod", name: "runtime" },
      },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("writes ?release= when a release is viewed from the table", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.listReleases.mockResolvedValue({
      releases: [{ release: releaseV2, current: true, previous: false, activation_revision: 8 }],
      next_page_token: "",
    });
    render(<ReleasesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "View" }));
    expect(mocks.replace).toHaveBeenLastCalledWith(
      {
        pathname: "/releases",
        query: { app: "payments", env: "prod", name: "runtime", release: "runtime@2" },
      },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("rolls back through the RollbackDialog with pre-validation and a CAS guard", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.listReleases.mockResolvedValue({
      releases: [
        { release: releaseV2, current: true, previous: false, activation_revision: 8 },
        { release: releaseV1, current: false, previous: true, activation_revision: 7 },
      ],
      next_page_token: "",
    });
    mocks.validateRelease.mockResolvedValue({ valid: true, errors: [] });
    mocks.rollbackRelease.mockResolvedValue({
      release: releaseV1,
      activation_revision: 9,
      previous_version: 2,
      rolled_back_from: 2,
      changed: true,
    });

    render(<ReleasesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Roll back to previous" }));
    const dialog = await screen.findByRole("dialog", { name: "Roll back release?" });
    await waitFor(() =>
      expect(mocks.validateRelease).toHaveBeenCalledWith(
        { env: "prod", app: "payments" },
        "runtime",
        1,
      ),
    );
    await within(dialog).findByText("is valid and can be activated.");
    const confirm = within(dialog).getByTestId("rollback-confirm");
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByTestId("rollback-confirm-env"), {
      target: { value: "prod" },
    });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);

    await waitFor(() =>
      expect(mocks.rollbackRelease).toHaveBeenCalledWith({
        env: "prod",
        app: "payments",
        name: "runtime",
        expected_current_version: 2,
      }),
    );
    // The legacy activate-based rollback is gone: no getActiveRelease/activateRelease round trip.
    expect(mocks.activateRelease).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.listReleases).toHaveBeenCalledTimes(2));
    expect(mocks.toast.success).toHaveBeenCalledWith("Rolled back runtime to version 1");
  });

  it("offers Roll back to previous inside the workspace of the current release", async () => {
    mocks.query = { app: "payments", env: "prod", name: "runtime" };
    mocks.listReleases.mockResolvedValue({
      releases: [
        { release: releaseV2, current: true, previous: false, activation_revision: 8 },
        { release: releaseV1, current: false, previous: true, activation_revision: 7 },
      ],
      next_page_token: "",
    });
    mocks.validateRelease.mockResolvedValue({ valid: true, errors: [] });

    render(<ReleasesPage />);
    fireEvent.click((await screen.findAllByRole("button", { name: "View" }))[0]);
    const workspace = screen.getByRole("dialog", { name: "Release runtime@2" });
    fireEvent.click(within(workspace).getByRole("button", { name: "Roll back to previous" }));
    expect(await screen.findByRole("dialog", { name: "Roll back release?" })).toBeVisible();
  });
});
