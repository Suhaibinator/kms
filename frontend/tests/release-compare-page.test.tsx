import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api";
import { links } from "@/lib/links";
import type { ApplicationOverview, ReleaseDiffResponse, ReleaseSummary } from "@/lib/types";
import ReleaseComparePage from "@/pages/releases/compare";
import overviewJson from "./fixtures/backend/overview-incident.json";
import diffJson from "./fixtures/backend/release-diff.json";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  isReady: true,
  push: vi.fn(async () => true),
  replace: vi.fn(async () => true),
  releaseDiff: vi.fn(),
  listReleases: vi.fn(),
  applicationOverview: vi.fn(),
  getActiveRelease: vi.fn(),
  validateRelease: vi.fn(),
  rollbackRelease: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn(), dismiss: vi.fn() },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    query: mocks.query,
    pathname: "/releases/compare",
    isReady: mocks.isReady,
    push: mocks.push,
    replace: mocks.replace,
  }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    isAbortError: () => false,
    api: {
      ...actual.api,
      releaseDiff: mocks.releaseDiff,
      listReleases: mocks.listReleases,
      applicationOverview: mocks.applicationOverview,
      getActiveRelease: mocks.getActiveRelease,
      validateRelease: mocks.validateRelease,
      rollbackRelease: mocks.rollbackRelease,
    },
  };
});

const diff = diffJson as unknown as ReleaseDiffResponse;
const overview = overviewJson as unknown as ApplicationOverview;

/** A `GET /releases` row for the fixture's runtime track. */
function summary(version: number, state: "current" | "previous" | "inactive"): ReleaseSummary {
  return {
    release: {
      namespace: { env: "prod", app: "gradethis" },
      name: "runtime",
      version,
      schema_version: 1,
      entries: [],
      metadata_json: "{}",
      digest: `digest-${version}`,
      created_by: "admin",
      created_at_unix_ms: 1755000000000,
    },
    current: state === "current",
    previous: state === "previous",
    activation_revision: state === "current" ? 12 : 0,
  };
}

const baseQuery = {
  app: "gradethis",
  env: "prod",
  name: "runtime",
  schema_version: "1",
  from: "1",
  to: "2",
};

describe("release compare page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.query = { ...baseQuery };
    mocks.isReady = true;
    mocks.releaseDiff.mockResolvedValue(diff);
    mocks.listReleases.mockResolvedValue({
      releases: [summary(3, "inactive"), summary(2, "current"), summary(1, "previous")],
      next_page_token: "",
    });
    mocks.applicationOverview.mockResolvedValue(overview);
  });

  it("renders the header chips, the breadcrumb trail and the changed row from the diff", async () => {
    render(<ReleaseComparePage />);
    expect(mocks.releaseDiff).toHaveBeenCalledWith(
      {
        env: "prod",
        app: "gradethis",
        name: "runtime",
        schemaVersion: 1,
        from: 1,
        to: 2,
        toEnv: undefined,
        toSchemaVersion: undefined,
      },
      expect.objectContaining({ signal: expect.anything() }),
    );
    const row = await screen.findByTestId("release-diff-row");
    expect(row).toHaveAttribute("data-alias", "rate_limits");
    expect(row).toHaveAttribute("data-change", "changed");
    expect(row).toHaveTextContent("7");
    expect(row).toHaveTextContent("12");

    const heading = screen.getByRole("heading", { level: 1 });
    expect(within(heading).getAllByText("runtime@1")[0]).toBeVisible();
    expect(within(heading).getByText("runtime@2")).toBeVisible();
    // The `to` side is current and the `from` side is the previous label.
    expect(screen.getByText("current · rev 12")).toBeVisible();
    expect(screen.getByText("previous")).toBeVisible();
    const trail = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(trail).getByRole("link", { name: "Releases" })).toHaveAttribute(
      "href",
      links.releases({ app: "gradethis", env: "prod", name: "runtime", schemaVersion: 1 }),
    );
    expect(within(trail).getByText("Compare v1 → v2")).toBeVisible();
    // Rows link to the resource each alias pins.
    expect(screen.getByRole("link", { name: "Open parameter rate_limits" })).toHaveAttribute(
      "href",
      links.parameterDetail({ env: "prod", app: "gradethis", key: "rate_limits" }),
    );
    // Numeric selectors need no URL rewrite.
    expect(mocks.replace).not.toHaveBeenCalled();
  });

  it("rewrites the current/previous labels to the versions the server resolved", async () => {
    mocks.query = { ...baseQuery, from: "previous", to: "current" };
    render(<ReleaseComparePage />);
    expect(mocks.releaseDiff).toHaveBeenCalledWith(
      expect.objectContaining({ from: "previous", to: "current" }),
      expect.anything(),
    );
    await screen.findByTestId("release-diff-row");
    await waitFor(() =>
      expect(mocks.replace).toHaveBeenCalledWith(
        {
          pathname: "/releases/compare",
          query: { ...baseQuery, from: "1", to: "2" },
        },
        undefined,
        { shallow: true, scroll: false },
      ),
    );
  });

  it("swaps the two sides and steps to the adjacent version through the URL", async () => {
    render(<ReleaseComparePage />);
    await screen.findByTestId("release-diff-row");
    fireEvent.click(screen.getByTestId("release-diff-swap"));
    expect(mocks.replace).toHaveBeenLastCalledWith(
      { pathname: "/releases/compare", query: { ...baseQuery, from: "2", to: "1" } },
      undefined,
      { shallow: true, scroll: false },
    );
    // v2 sits between v3 and v1 in the loaded track.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Compare with the newer version" })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Compare with the newer version" }));
    expect(mocks.replace).toHaveBeenLastCalledWith(
      { pathname: "/releases/compare", query: { ...baseQuery, to: "3" } },
      undefined,
      { shallow: true, scroll: false },
    );
    fireEvent.click(screen.getByRole("button", { name: "Compare with the older version" }));
    expect(mocks.replace).toHaveBeenLastCalledWith(
      { pathname: "/releases/compare", query: { ...baseQuery, to: "1" } },
      undefined,
      { shallow: true, scroll: false },
    );
    // The pickers list the track's versions with their labels.
    const to = screen.getByRole("combobox", { name: "To" });
    expect(to).toHaveTextContent("runtime@1:2 · current");
    fireEvent.click(to);
    expect(await screen.findByRole("option", { name: "runtime@1:1 · previous" })).toBeVisible();
  });

  it("shows the rollout cell only when the `to` side is the current release", async () => {
    render(<ReleaseComparePage />);
    expect(await screen.findByTestId("release-diff-rollout")).toBeVisible();
    expect(mocks.applicationOverview).toHaveBeenCalledWith(
      "gradethis",
      ["prod"],
      expect.objectContaining({ signal: expect.anything() }),
      1,
    );
    // The fixture's prod rollout: 3 instances, 1 rejected.
    expect(screen.getByTestId("release-diff-rollout")).toHaveTextContent("1 rejected");
    // Rolling back is offered from the header because `to` is current.
    expect(screen.getByRole("button", { name: "Roll back to v1" })).toBeVisible();
  });

  it("skips the rollout fetch and the rollback action for an inactive `to` side", async () => {
    mocks.releaseDiff.mockResolvedValue({
      ...diff,
      to: { ...diff.to, current: false, activation_revision: 0 },
    });
    render(<ReleaseComparePage />);
    await screen.findByTestId("release-diff-row");
    expect(screen.queryByTestId("release-diff-rollout")).toBeNull();
    expect(mocks.applicationOverview).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /Roll back/ })).toBeNull();
  });

  it("says so loudly when the two releases are identical", async () => {
    mocks.releaseDiff.mockResolvedValue({
      ...diff,
      identical: true,
      counts: { ...diff.counts, changed: 0, unchanged: 3 },
      rows: diff.rows.map((row) => ({ ...row, change: "unchanged", reasons: [] })),
    });
    render(<ReleaseComparePage />);
    expect(await screen.findByTestId("release-diff-identical")).toHaveTextContent("No differences");
    expect(screen.queryByTestId("release-diff-row")).toBeNull();
  });

  it("refuses to compare a version with itself before asking the server", () => {
    mocks.query = { ...baseQuery, from: "2", to: "2" };
    render(<ReleaseComparePage />);
    expect(screen.getByText("Pick two different versions.")).toBeVisible();
    expect(mocks.releaseDiff).not.toHaveBeenCalled();
  });

  it("explains a pruned release and links to the audit log", async () => {
    mocks.releaseDiff.mockRejectedValue(
      new ApiError("not_found", "from release runtime@1:1 not found", 404),
    );
    render(<ReleaseComparePage />);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("from release runtime@1:1 not found");
    expect(alert).toHaveTextContent("watch.release_retain_versions");
    expect(within(alert).getByRole("link", { name: /audit log/ })).toHaveAttribute(
      "href",
      expect.stringContaining("/audit?env=prod&app=gradethis&key_prefix=runtime"),
    );
  });

  it("explains a first activation when there is no previous release", async () => {
    mocks.query = { ...baseQuery, from: "previous", to: "current" };
    mocks.releaseDiff.mockRejectedValue(
      new ApiError("failed_precondition", "no previous release", 412),
    );
    render(<ReleaseComparePage />);
    expect(await screen.findByRole("status")).toHaveTextContent(
      /has no previous release in\s*env\s*prod/,
    );
    // Nothing to rewrite: the labels never resolved.
    expect(mocks.replace).not.toHaveBeenCalled();
  });

  it("asks for the missing selectors instead of fetching", () => {
    mocks.query = { app: "gradethis", env: "prod" };
    render(<ReleaseComparePage />);
    expect(screen.getByText("Nothing to compare", { selector: "h1" })).toBeVisible();
    expect(mocks.releaseDiff).not.toHaveBeenCalled();

    mocks.query = { ...baseQuery, schema_version: "x" };
    render(<ReleaseComparePage />);
    expect(screen.getByText("Invalid schema version", { selector: "h1" })).toBeVisible();
    expect(mocks.releaseDiff).not.toHaveBeenCalled();
  });

  it("keeps the filter and unchanged toggle in the URL", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      render(<ReleaseComparePage />);
      await screen.findByTestId("release-diff-row");
      fireEvent.click(screen.getByRole("checkbox", { name: /Show unchanged/ }));
      expect(mocks.replace).toHaveBeenLastCalledWith(
        { pathname: "/releases/compare", query: { ...baseQuery, view: "all" } },
        undefined,
        { shallow: true, scroll: false },
      );
      // Unchanged rows appear at once; the URL write is what the mock records.
      await waitFor(() => expect(screen.getAllByTestId("release-diff-row")).toHaveLength(3));

      fireEvent.change(screen.getByRole("searchbox"), { target: { value: "rate" } });
      await waitFor(() => expect(screen.getAllByTestId("release-diff-row")).toHaveLength(1));
      vi.advanceTimersByTime(300);
      expect(mocks.replace).toHaveBeenLastCalledWith(
        { pathname: "/releases/compare", query: { ...baseQuery, q: "rate" } },
        undefined,
        { shallow: true, scroll: false },
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("compares across environments with a banner and no version steps", async () => {
    mocks.query = { ...baseQuery, from: "2", to: "1", to_env: "dev" };
    mocks.releaseDiff.mockResolvedValue({
      ...diff,
      cross_environment: true,
      to: { ...diff.to, namespace: { env: "dev", app: "gradethis" }, version: 1, current: true },
      from: { ...diff.from, version: 2, current: true, previous: false },
    });
    render(<ReleaseComparePage />);
    await screen.findByTestId("release-diff-row");
    expect(mocks.releaseDiff).toHaveBeenCalledWith(
      expect.objectContaining({ toEnv: "dev", from: 2, to: 1 }),
      expect.anything(),
    );
    expect(screen.getByText(/Entries are matched by alias/)).toBeVisible();
    expect(screen.queryByRole("button", { name: /newer version/ })).toBeNull();
    // Swapping sides swaps environments too.
    fireEvent.click(screen.getByTestId("release-diff-swap"));
    expect(mocks.replace).toHaveBeenLastCalledWith(
      {
        pathname: "/releases/compare",
        query: {
          ...baseQuery,
          env: "dev",
          to_env: "prod",
          schema_version: "1",
          to_schema_version: "1",
          from: "1",
          to: "2",
        },
      },
      undefined,
      { shallow: true, scroll: false },
    );
    // Rollout belongs to one environment; across two it is not shown.
    expect(screen.queryByTestId("release-diff-rollout")).toBeNull();
  });
});
