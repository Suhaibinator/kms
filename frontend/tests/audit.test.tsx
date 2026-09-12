import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { links } from "@/lib/links";
import type { AuditEvent, Namespace, ReleaseDiffResponse } from "@/lib/types";
import AuditPage from "@/pages/audit";
import releaseDiffJson from "./fixtures/backend/release-diff.json";

const mocks = vi.hoisted(() => ({
  namespaces: [] as Namespace[],
  namespacesLoading: false,
  namespacesError: null as unknown,
  query: {} as Record<string, string>,
  // Like the real router, a replace changes what the next render reads.
  replace: vi.fn(async (url: { query: Record<string, string> }) => {
    mocks.query = url.query;
    return true;
  }),
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    pathname: "/audit",
    query: mocks.query,
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
      namespaces: mocks.namespaces,
      loading: mocks.namespacesLoading,
      error: mocks.namespacesError,
      reload: vi.fn(),
    }),
  };
});

function event(id: number, overrides: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id,
    event_type: "secret.read",
    actor_identity: "billing-api",
    actor_type: "client",
    resource_type: "secret",
    resource_env: "prod",
    resource_app: "billing",
    resource_key: "db/password",
    resource_version: 1,
    resource_namespace_id: 1,
    decision: "allow",
    source_ip: "10.0.0.1",
    user_agent: "",
    request_id: "",
    created_at_unix_ms: 1_700_000_000_000,
    metadata_json: "",
    ...overrides,
  };
}

const replaced = () => mocks.replace.mock.calls.at(-1)?.[0];

beforeEach(() => {
  mocks.namespaces = [];
  mocks.namespacesLoading = false;
  mocks.namespacesError = null;
  mocks.query = {};
  mocks.replace.mockClear();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  vi.spyOn(api, "listAudit").mockResolvedValue({ events: [], next_page_token: "" });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("audit table", () => {
  const eventTypes = () =>
    [...document.querySelectorAll('table.data tbody td[data-label="Event"]')].map(
      (cell) => cell.textContent ?? "",
    );

  it("reorders the loaded page from a column header and records the sort in the URL", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({
      events: [event(1), event(2, { event_type: "parameter.write" })],
      next_page_token: "",
    });
    const { rerender } = render(<AuditPage />);
    await screen.findByText("secret.read");
    expect(eventTypes()).toEqual(["secret.read", "parameter.write"]);

    fireEvent.click(screen.getByRole("button", { name: "Event" }));
    expect(replaced()).toEqual({ pathname: "/audit", query: { sort: "event", dir: "asc" } });

    // The mock router lands on the new query, so a rerender reads it back.
    rerender(<AuditPage />);
    expect(eventTypes()).toEqual(["parameter.write", "secret.read"]);
    expect(screen.getByRole("button", { name: "Event" }).closest("th")).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
    expect(screen.getByTestId("table-summary")).toHaveTextContent(
      "Showing 2 of 2 events · Sorts the events loaded on this page",
    );
  });

  it("reserves the loaded table's column count while loading", async () => {
    let settle: (page: { events: AuditEvent[]; next_page_token: string }) => void = () => {};
    vi.mocked(api.listAudit).mockImplementation(
      () =>
        new Promise((resolve) => {
          settle = resolve;
        }),
    );
    render(<AuditPage />);
    const skeletonColumns = document.querySelectorAll("thead th").length;
    expect(skeletonColumns).toBeGreaterThan(0);

    settle({ events: [event(1)], next_page_token: "" });
    await screen.findByText("secret.read");
    // The loaded header adds a gutter for the expand control; a skeleton one
    // column short re-lays out every column the instant the data lands.
    expect(document.querySelectorAll("thead th")).toHaveLength(skeletonColumns);
  });

  it("counts the events on screen and the filters narrowing them", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({ events: [event(1)], next_page_token: "" });
    render(<AuditPage />);
    await screen.findByText("secret.read");
    const summary = screen.getByTestId("table-summary");
    expect(summary).toHaveTextContent("Showing 1 of 1 event");
    expect(summary).not.toHaveTextContent("filter");

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "deploy-bot" } });
    fireEvent.change(screen.getByLabelText("Event type"), { target: { value: "secret.read" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(screen.getByTestId("table-summary")).toHaveTextContent(
        "Showing 1 of 1 event · 2 filters active",
      ),
    );
  });
});

describe("audit filters", () => {
  it("disables namespace filters when the namespace list has no choices", async () => {
    render(<AuditPage />);
    await screen.findByText("No audit events have been recorded yet.");

    const app = screen.getByRole("combobox", { name: "Application" });
    const env = screen.getByRole("combobox", { name: "Environment" });
    expect(app).toBeDisabled();
    expect(env).toBeDisabled();
    expect(app).toHaveTextContent("No applications available");
    expect(env).toHaveTextContent("No environments available");
  });

  it("distinguishes a loading or failed namespace list", async () => {
    mocks.namespacesLoading = true;
    const { rerender } = render(<AuditPage />);
    await screen.findByText("No audit events have been recorded yet.");
    expect(screen.getByRole("combobox", { name: "Application" })).toHaveTextContent(
      "Loading applications…",
    );
    expect(screen.getByRole("combobox", { name: "Environment" })).toHaveTextContent(
      "Loading environments…",
    );

    mocks.namespacesLoading = false;
    mocks.namespacesError = new Error("unavailable");
    rerender(<AuditPage />);
    expect(screen.getByRole("combobox", { name: "Application" })).toHaveTextContent(
      "Applications unavailable",
    );
    expect(screen.getByRole("combobox", { name: "Environment" })).toHaveTextContent(
      "Environments unavailable",
    );
  });

  it("keeps stale URL filters visible and disables only an optionless environment", async () => {
    mocks.namespaces = [
      {
        app: "billing",
        env: "prod",
        description: "",
        allowed_auth_methods: ["mtls"],
        created_by: "admin",
        created_at_unix_ms: 1,
        parameter_count: 0,
        secret_count: 0,
      },
    ];
    mocks.query = { app: "ghost" };
    render(<AuditPage />);
    await screen.findByText("No events match the current filters.");

    const app = screen.getByRole("combobox", { name: "Application" });
    const env = screen.getByRole("combobox", { name: "Environment" });
    expect(app).toBeEnabled();
    expect(app).toHaveTextContent("ghost (not found)");
    expect(env).toBeDisabled();
    expect(env).toHaveTextContent("No environments available");
  });

  it("offers Clear filters only once a filter is applied", async () => {
    const listAudit = vi.mocked(api.listAudit);
    render(<AuditPage />);
    await screen.findByText("No audit events have been recorded yet.");
    expect(screen.queryByRole("button", { name: "Clear filters" })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "deploy-bot" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await screen.findByText("No events match the current filters.");
    expect(listAudit).toHaveBeenLastCalledWith(
      expect.objectContaining({ actor: "deploy-bot", page_token: undefined }),
      expect.anything(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await screen.findByText("No audit events have been recorded yet.");
    expect(screen.getByLabelText("Actor")).toHaveValue("");
  });

  it("rejects a malformed key prefix inline and disables Apply", async () => {
    render(<AuditPage />);
    await screen.findByText("No audit events have been recorded yet.");
    const prefix = screen.getByLabelText("Key prefix");
    const apply = screen.getByRole("button", { name: "Apply" });

    fireEvent.change(prefix, { target: { value: "/billing" } });
    expect(apply).toBeDisabled();
    // The message waits for blur so a half-typed prefix is not called wrong.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    fireEvent.blur(prefix);
    expect(screen.getByText("Key must not start or end with '/'.")).toBeVisible();
    expect(prefix).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(prefix, { target: { value: "billing" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(apply).toBeEnabled();
  });

  it("rejects an end before the start and explains the exclusive end", async () => {
    const listAudit = vi.mocked(api.listAudit);
    render(<AuditPage />);
    await screen.findByText("No audit events have been recorded yet.");
    const to = screen.getByLabelText("To");
    expect(screen.getByText("End is exclusive")).toBeVisible();
    expect(to).toHaveAttribute("aria-describedby", expect.stringContaining("hint"));

    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-08-22T10:00" } });
    fireEvent.change(to, { target: { value: "2026-08-22T09:00" } });
    expect(screen.getByText("End must be after start.")).toBeVisible();
    expect(to).toHaveAttribute("aria-invalid", "true");
    const apply = screen.getByRole("button", { name: "Apply" });
    expect(apply).toBeDisabled();
    fireEvent.submit(apply.closest("form") as HTMLFormElement);
    expect(listAudit).toHaveBeenCalledTimes(1);

    fireEvent.change(to, { target: { value: "2026-08-22T11:00" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(apply).toBeEnabled();
  });

  it("returns to page 1 when a filter is applied", async () => {
    const listAudit = vi
      .mocked(api.listAudit)
      .mockImplementation(async (filters) =>
        filters.page_token === "page-2"
          ? { events: [event(2)], next_page_token: "" }
          : { events: [event(1)], next_page_token: filters.actor ? "" : "page-2" },
      );
    render(<AuditPage />);
    await screen.findByText("10.0.0.1");
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByText("Page 2");

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "billing-api" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(listAudit).toHaveBeenLastCalledWith(
        expect.objectContaining({ actor: "billing-api", page_token: undefined }),
        expect.anything(),
      ),
    );
    await waitFor(() => expect(screen.queryByText("Page 2")).not.toBeInTheDocument());
  });

  it("keeps the applied filters and the cursor in the URL", async () => {
    vi.mocked(api.listAudit).mockImplementation(async (filters) => ({
      events: [event(filters.page_token === "page-2" ? 2 : 1)],
      next_page_token: filters.page_token === "page-2" ? "" : "page-2",
    }));
    render(<AuditPage />);
    await screen.findByText("10.0.0.1");
    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "billing-api" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(replaced()).toEqual({ pathname: "/audit", query: { actor: "billing-api" } });

    fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
    expect(replaced()?.query).toEqual({ actor: "billing-api", page_token: "page-2", page: "2" });
    await screen.findByText("Page 2");
    fireEvent.click(await screen.findByRole("button", { name: "Previous page" }));
    expect(replaced()?.query).toEqual({ actor: "billing-api" });
    await screen.findByText("Page 1");

    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(replaced()).toEqual({ pathname: "/audit", query: {} });
  });

  it("restores an investigation from the URL, including its page", async () => {
    mocks.query = {
      actor: "deploy-bot",
      event_type: "release.activate",
      page_token: "tok-3",
      page: "3",
    };
    const listAudit = vi.mocked(api.listAudit).mockResolvedValue({
      events: [event(3)],
      next_page_token: "",
    });
    render(<AuditPage />);
    await screen.findByText("10.0.0.1");
    expect(listAudit).toHaveBeenCalledTimes(1);
    expect(listAudit).toHaveBeenCalledWith(
      expect.objectContaining({
        actor: "deploy-bot",
        event_type: "release.activate",
        page_token: "tok-3",
      }),
      expect.anything(),
    );
    expect(screen.getByLabelText("Actor")).toHaveValue("deploy-bot");
    expect(screen.getByLabelText("Event type")).toHaveValue("release.activate");
    expect(screen.getByText("Page 3")).toBeVisible();
    expect(screen.getByText("1 event")).toBeVisible();
    // The pages before a restored token are unknown, so the way back is First page.
    expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "First page" }));
    expect(replaced()?.query).toEqual({ actor: "deploy-bot", event_type: "release.activate" });
    await waitFor(() =>
      expect(listAudit).toHaveBeenLastCalledWith(
        expect.objectContaining({ actor: "deploy-bot", page_token: undefined }),
        expect.anything(),
      ),
    );
  });

  it("keeps cursor history and an unapplied draft across its own pagination acknowledgements", async () => {
    const listAudit = vi.mocked(api.listAudit).mockImplementation(async (filters) => ({
      events: [event(filters.page_token === "page-3" ? 3 : filters.page_token ? 2 : 1)],
      next_page_token:
        filters.page_token === "page-3" ? "" : filters.page_token ? "page-3" : "page-2",
    }));
    const { rerender } = render(<AuditPage />);
    await screen.findByText("10.0.0.1");

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "unapplied-draft" } });
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    rerender(<AuditPage />);
    await screen.findByText("Page 2");
    expect(screen.getByLabelText("Actor")).toHaveValue("unapplied-draft");

    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    rerender(<AuditPage />);
    await screen.findByText("Page 3");
    expect(screen.getByRole("button", { name: "Previous page" })).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Previous page" }));
    rerender(<AuditPage />);
    await screen.findByText("Page 2");
    await waitFor(() =>
      expect(listAudit).toHaveBeenLastCalledWith(
        expect.objectContaining({ page_token: "page-2" }),
        expect.anything(),
      ),
    );
    expect(screen.getByLabelText("Actor")).toHaveValue("unapplied-draft");
  });

  it("does not let a no-op filter replace mask later browser history", async () => {
    mocks.query = { actor: "actor-a" };
    const { rerender } = render(<AuditPage />);
    await screen.findByText("No events match the current filters.");

    // Applying the URL's existing filters is a no-op and must not leave an
    // acknowledgement marker that can be mistaken for history returning here.
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(mocks.replace).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "actor-b" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    rerender(<AuditPage />);
    await waitFor(() => expect(screen.getByLabelText("Actor")).toHaveValue("actor-b"));

    mocks.query = { actor: "actor-a" };
    rerender(<AuditPage />);
    await waitFor(() => expect(screen.getByLabelText("Actor")).toHaveValue("actor-a"));
  });

  it("does not retain an acknowledgement after a cancelled URL replacement", async () => {
    mocks.query = { actor: "actor-a" };
    mocks.replace.mockResolvedValueOnce(false);
    const { rerender } = render(<AuditPage />);
    await screen.findByText("No events match the current filters.");

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "actor-b" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledTimes(1));
    // Let the rejected acknowledgement clear before an unrelated history entry
    // reaches the page, then retain a local draft that an external entry must replace.
    await new Promise((resolve) => setTimeout(resolve, 0));
    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "local-draft" } });

    mocks.query = { actor: "actor-b" };
    rerender(<AuditPage />);
    await waitFor(() => expect(screen.getByLabelText("Actor")).toHaveValue("actor-b"));
  });

  it("retries a cancelled replacement from the last acknowledged URL", async () => {
    mocks.query = { actor: "actor-a" };
    mocks.replace.mockResolvedValueOnce(false);
    render(<AuditPage />);
    await screen.findByText("No events match the current filters.");

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "actor-b" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledTimes(1));
    await new Promise((resolve) => setTimeout(resolve, 0));

    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledTimes(2));
  });

  it("restores a cursor from same-filter browser history", async () => {
    const listAudit = vi.mocked(api.listAudit).mockImplementation(async (filters) => ({
      events: [event(filters.page_token ? 3 : 1)],
      next_page_token: "",
    }));
    mocks.query = { actor: "actor-a" };
    const { rerender } = render(<AuditPage />);
    await screen.findByText("secret.read");
    expect(screen.getByLabelText("Actor")).toHaveValue("actor-a");

    mocks.query = { actor: "actor-a", page_token: "cursor-b", page: "3" };
    rerender(<AuditPage />);

    await waitFor(() =>
      expect(listAudit).toHaveBeenLastCalledWith(
        expect.objectContaining({ actor: "actor-a", page_token: "cursor-b" }),
        expect.anything(),
      ),
    );
    expect(screen.getByLabelText("Actor")).toHaveValue("actor-a");
    expect(screen.getByText("Page 3")).toBeVisible();
    // The middle cursors were never loaded in this component instance, so
    // history restoration exposes First page instead of inventing Previous.
    expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
    expect(screen.getByRole("button", { name: "First page" })).toBeVisible();
  });
});

describe("audit events", () => {
  it("links each row to its resource and shows a relative time with the absolute title", async () => {
    const created = Date.now() - 5 * 60_000;
    vi.mocked(api.listAudit).mockResolvedValue({
      events: [
        event(1, { created_at_unix_ms: created }),
        event(2, {
          resource_type: "parameter",
          resource_key: "rate_limits",
          created_at_unix_ms: created,
        }),
        event(3, { resource_type: "policy", resource_key: "admins", created_at_unix_ms: created }),
      ],
      next_page_token: "",
    });
    render(<AuditPage />);
    expect(await screen.findByRole("link", { name: "/prod/billing/db/password" })).toHaveAttribute(
      "href",
      links.secretDetail({ env: "prod", app: "billing", key: "db/password" }),
    );
    expect(screen.getByRole("link", { name: "/prod/billing/rate_limits" })).toHaveAttribute(
      "href",
      links.parameterDetail({ env: "prod", app: "billing", key: "rate_limits" }),
    );
    // No console page for a policy resource: plain text, not a dead link.
    expect(screen.queryByRole("link", { name: "/prod/billing/admins" })).toBeNull();
    expect(screen.getByText("/prod/billing/admins")).toBeVisible();
    const times = screen.getAllByText("5m ago");
    expect(times).toHaveLength(3);
    expect(times[0]).toHaveAttribute("title", formatUnixMs(created));
    expect(screen.getByText("3 events")).toBeVisible();
  });

  it("marks the details toggle expanded and links it to the row it controls", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({
      events: [event(7, { metadata_json: '{"reason":"policy"}', request_id: "req-1" })],
      next_page_token: "",
    });
    render(<AuditPage />);
    const toggle = await screen.findByRole("button", { name: "Details" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(toggle).toHaveAttribute("aria-controls", "audit-meta-7");
    expect(document.getElementById("audit-meta-7")).toBeNull();

    fireEvent.click(toggle);
    const hide = screen.getByRole("button", { name: "Hide" });
    expect(hide).toHaveAttribute("aria-expanded", "true");
    expect(document.getElementById("audit-meta-7")).not.toBeNull();
    expect(screen.getByText("req-1")).toBeVisible();

    fireEvent.click(hide);
    expect(screen.getByRole("button", { name: "Details" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(document.getElementById("audit-meta-7")).toBeNull();
  });

  it("re-runs the current query from the Refresh button", async () => {
    const listAudit = vi.mocked(api.listAudit);
    render(<AuditPage />);
    await screen.findByText("No audit events have been recorded yet.");
    expect(listAudit).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(listAudit).toHaveBeenCalledTimes(2));
    expect(listAudit.mock.calls[1][0]).toEqual(listAudit.mock.calls[0][0]);
  });

  it("does not present old events as results for a failed new filter", async () => {
    vi.mocked(api.listAudit)
      .mockResolvedValueOnce({ events: [event(1)], next_page_token: "" })
      .mockRejectedValueOnce(new Error("audit offline"));
    render(<AuditPage />);
    expect(await screen.findByText("secret.read")).toBeVisible();

    fireEvent.change(screen.getByLabelText("Actor"), { target: { value: "other-client" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText(/No results are available for the current query/)).toBeVisible();
    expect(screen.queryByText("secret.read")).toBeNull();
    expect(screen.queryByText("No events match the current filters.")).toBeNull();
  });

  it("retains same-query events with an explicit stale marker after refresh fails", async () => {
    vi.mocked(api.listAudit)
      .mockResolvedValueOnce({ events: [event(1)], next_page_token: "" })
      .mockRejectedValueOnce(new Error("audit offline"));
    render(<AuditPage />);
    expect(await screen.findByText("secret.read")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText(/last successful results for this query/)).toBeVisible();
    expect(screen.getByText("secret.read")).toBeVisible();
  });
});

it("labels and links duplicate release versions by their recorded schema tracks", async () => {
  vi.mocked(api.listAudit).mockResolvedValue({
    events: [0, 1, 2, 999].map((schemaVersion) =>
      event(schemaVersion + 1, {
        event_type: "configuration_release.activate",
        resource_type: "configuration_release",
        resource_key: "runtime",
        resource_version: 1,
        metadata_json: JSON.stringify({
          schema_version: String(schemaVersion),
          activation_revision: "123",
        }),
      }),
    ),
    next_page_token: "",
  });
  render(<AuditPage />);
  for (const schemaVersion of [0, 1, 2, 999]) {
    const link = await screen.findByRole("link", {
      name: `/prod/billing/runtime · schema v${schemaVersion} · v1`,
    });
    const url = new URL(link.getAttribute("href")!, "https://kms.example");
    expect(url.searchParams.get("schema_version")).toBe(String(schemaVersion));
    expect(url.searchParams.get("release")).toBe(`runtime@${schemaVersion}:1`);
  }
});

it("renders malformed and legacy release audit metadata without guessing a track", async () => {
  vi.mocked(api.listAudit).mockResolvedValue({
    events: ["{", "{}", '{"schema_version":"9007199254740992"}', '{"schema_version":null}'].map(
      (metadata_json, index) =>
        event(index + 1, {
          event_type: "configuration_release.create",
          resource_type: "configuration_release",
          resource_key: `runtime-${index}`,
          metadata_json,
        }),
    ),
    next_page_token: "",
  });
  render(<AuditPage />);
  for (let index = 0; index < 4; index++) {
    const link = await screen.findByRole("link", { name: `/prod/billing/runtime-${index} · v1` });
    expect(link).toHaveAttribute("href", `/releases?app=billing&env=prod&name=runtime-${index}`);
  }
});

describe("release comparison links", () => {
  const activation = (overrides: Partial<AuditEvent> = {}) =>
    event(41, {
      event_type: "configuration_release.activate",
      resource_type: "configuration_release",
      resource_env: "prod",
      resource_app: "gradethis",
      resource_key: "runtime",
      resource_version: 9,
      metadata_json: JSON.stringify({
        schema_version: "1",
        activation_revision: "53",
        previous_version: "7",
      }),
      ...overrides,
    });

  it("links an activation to the comparison of the pair it swapped", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({ events: [activation()], next_page_token: "" });
    render(<AuditPage />);
    expect(await screen.findByRole("link", { name: "What changed (v7 → v9)" })).toHaveAttribute(
      "href",
      links.releaseCompare({
        app: "gradethis",
        env: "prod",
        name: "runtime",
        schemaVersion: 1,
        from: 7,
        to: 9,
      }),
    );
  });

  it("reads a rollback as newer → older and skips first activations", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({
      events: [
        activation({
          id: 42,
          event_type: "configuration_release.rollback",
          resource_version: 7,
          metadata_json: JSON.stringify({ schema_version: "1", previous_version: "9" }),
        }),
        activation({
          id: 43,
          resource_version: 1,
          metadata_json: JSON.stringify({ schema_version: "1", previous_version: "0" }),
        }),
      ],
      next_page_token: "",
    });
    render(<AuditPage />);
    expect(await screen.findByRole("link", { name: "What changed (v9 → v7)" })).toHaveAttribute(
      "href",
      links.releaseCompare({
        app: "gradethis",
        env: "prod",
        name: "runtime",
        schemaVersion: 1,
        from: 9,
        to: 7,
      }),
    );
    expect(screen.getAllByRole("link", { name: /^What changed/ })).toHaveLength(1);
  });

  it("links an activated ship event from previous_version to release_version", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({
      events: [
        event(45, {
          event_type: "application.ship",
          resource_type: "application",
          resource_env: "prod",
          resource_app: "gradethis",
          resource_key: "gradethis",
          resource_version: 0,
          metadata_json: JSON.stringify({
            schema_version: "1",
            environment: "prod",
            release_name: "runtime",
            aliases: "rate_limits",
            activated: "true",
            previous_version: "7",
            release_version: "9",
          }),
        }),
      ],
      next_page_token: "",
    });
    render(<AuditPage />);
    expect(await screen.findByRole("link", { name: "What changed (v7 → v9)" })).toHaveAttribute(
      "href",
      links.releaseCompare({
        app: "gradethis",
        env: "prod",
        name: "runtime",
        schemaVersion: 1,
        from: 7,
        to: 9,
      }),
    );
  });

  it("does not guess a release name for a ship event that did not record one", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({
      events: [
        event(44, {
          event_type: "application.ship",
          resource_type: "application",
          resource_env: "prod",
          resource_app: "gradethis",
          resource_key: "gradethis",
          resource_version: 0,
          metadata_json: JSON.stringify({
            schema_version: "1",
            environment: "prod",
            activated: "true",
            previous_version: "7",
            release_version: "9",
          }),
        }),
      ],
      next_page_token: "",
    });
    render(<AuditPage />);
    expect(await screen.findByText("application.ship")).toBeVisible();
    expect(screen.queryByRole("link", { name: /^What changed/ })).toBeNull();
  });

  it("summarises the diff under the expanded metadata without fetching values", async () => {
    vi.mocked(api.listAudit).mockResolvedValue({ events: [activation()], next_page_token: "" });
    const releaseDiff = vi
      .spyOn(api, "releaseDiff")
      .mockResolvedValue(releaseDiffJson as unknown as ReleaseDiffResponse);
    render(<AuditPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Details" }));
    const summary = await screen.findByTestId("release-diff-summary");
    await waitFor(() => expect(summary).toHaveTextContent("1 changed"));
    expect(releaseDiff).toHaveBeenCalledWith(
      expect.objectContaining({ name: "runtime", from: 7, to: 9, values: false }),
      expect.anything(),
    );
    expect(within(summary).getByRole("link", { name: "See all →" })).toHaveAttribute(
      "href",
      links.releaseCompare({
        app: "gradethis",
        env: "prod",
        name: "runtime",
        schemaVersion: 1,
        from: 7,
        to: 9,
      }),
    );
  });
});
