import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import { formatUnixMs } from "@/lib/format";
import { links } from "@/lib/links";
import type { AuditEvent, Namespace } from "@/lib/types";
import AuditPage from "@/pages/audit";

const mocks = vi.hoisted(() => ({
  namespaces: [] as Namespace[],
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
      loading: false,
      error: null,
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
  mocks.query = {};
  mocks.replace.mockClear();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  vi.spyOn(api, "listAudit").mockResolvedValue({ events: [], next_page_token: "" });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("audit filters", () => {
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
});
