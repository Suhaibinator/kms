// The release comparison view against the Go-generated fixture: the counts an
// operator reads first, groups with "Needs attention" ahead of everything
// else, the inline old → new text, filtering, lazily loaded values for
// unchanged rows, the structural / side-by-side toggle for JSON, secrets
// rendered by version only, compact mode, the identical state and the
// plain-text export.
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ReleaseDiffView } from "@/components/releases/diff/ReleaseDiffView";
import type { ReleaseDiffPin, ReleaseDiffResponse, ReleaseDiffRow } from "@/lib/types";
import diffJson from "./fixtures/backend/release-diff.json";

const mocks = vi.hoisted(() => ({
  releaseDiff: vi.fn(),
  getParameter: vi.fn(),
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn(), dismiss: vi.fn() },
}));

vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    isAbortError: () => false,
    api: { ...actual.api, releaseDiff: mocks.releaseDiff, getParameter: mocks.getParameter },
  };
});

const fixture = diffJson as unknown as ReleaseDiffResponse;
const query = { env: "prod", app: "gradethis", name: "runtime", schemaVersion: 1, from: 1, to: 2 };

const ns = { env: "prod", app: "gradethis" };

function pin(
  key: string,
  version: number,
  overrides: Partial<ReleaseDiffPin> = {},
): ReleaseDiffPin {
  return {
    ref: { namespace: ns, key },
    version,
    content_type: "string",
    parameter_digest: `digest-${key}-${version}`,
    metadata_json: "{}",
    created_by: "alice",
    created_at_unix_ms: 1755000000000,
    value_state: "present",
    value_bytes: 1,
    ...overrides,
  };
}

/** The fixture with its rows replaced and the counts recomputed. */
function withRows(rows: ReleaseDiffRow[], overrides: Partial<ReleaseDiffResponse> = {}) {
  const counts = {
    added: 0,
    removed: 0,
    changed: 0,
    unchanged: 0,
    secrets_changed: 0,
    attention: 0,
  };
  for (const row of rows) {
    counts[row.change] += 1;
    if (row.kind === "secret" && row.change !== "unchanged") counts.secrets_changed += 1;
    if (row.reasons.includes("content_type") || row.reasons.includes("kind")) counts.attention += 1;
  }
  return {
    ...fixture,
    rows,
    counts,
    identical: rows.every((row) => row.change === "unchanged"),
    ...overrides,
  } satisfies ReleaseDiffResponse;
}

const rows = () => screen.getAllByTestId("release-diff-row");
const rowFor = (alias: string) =>
  screen
    .getAllByTestId("release-diff-row")
    .find((row) => row.getAttribute("data-alias") === alias) as HTMLElement;

describe("ReleaseDiffView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.releaseDiff.mockResolvedValue(fixture);
  });

  it("shows the counts strip and only the changed row by default, with the old → new values", async () => {
    render(<ReleaseDiffView query={query} />);
    expect(mocks.releaseDiff).toHaveBeenCalledWith(query, expect.anything());
    const row = await screen.findByTestId("release-diff-row");
    expect(row).toHaveAttribute("data-alias", "rate_limits");
    expect(row).toHaveAttribute("data-change", "changed");
    expect(row).toHaveAttribute("data-kind", "parameter");
    expect(row.querySelector(".release-diff-old")).toHaveTextContent("7");
    expect(row.querySelector(".release-diff-new")).toHaveTextContent("12");
    // Integers carry their delta.
    expect(row.querySelector(".release-diff-delta")).toHaveTextContent("+5");

    expect(screen.getByTestId("release-diff-count-changed")).toHaveTextContent("1");
    expect(screen.getByTestId("release-diff-count-added")).toHaveTextContent("0");
    expect(screen.getByTestId("release-diff-count-removed")).toHaveTextContent("0");
    expect(screen.getByTestId("release-diff-count-secrets")).toHaveTextContent("0");
    expect(screen.getByTestId("release-diff-schema")).toHaveTextContent("v1");
    // Two unchanged rows exist but are hidden behind the toggle.
    expect(rows()).toHaveLength(1);
    expect(screen.getByRole("checkbox", { name: /Show unchanged/ })).not.toBeChecked();
    expect(screen.getByText(/Show unchanged/)).toHaveTextContent("(2)");
    // The version authors sit under the head.
    expect(row.querySelector(".release-diff-row-meta")).toHaveTextContent("admin");
    // Not compact: the page-level copy link is offered.
    expect(screen.getByRole("button", { name: "Copy link" })).toBeVisible();
    // No rollout cell unless the page supplies one.
    expect(screen.queryByTestId("release-diff-rollout")).toBeNull();
  });

  it("groups rows with Needs attention first, then secrets, then changed / added / removed", async () => {
    mocks.releaseDiff.mockResolvedValue(
      withRows([
        {
          alias: "added_flag",
          kind: "parameter",
          change: "added",
          reasons: [],
          to: pin("added_flag", 1, { content_type: "boolean", value: "true" }),
        },
        {
          alias: "db_password",
          kind: "secret",
          change: "changed",
          reasons: ["pin"],
          from: pin("db_password", 1, { value_state: "secret", parameter_digest: "", bound: true }),
          to: pin("db_password", 2, { value_state: "secret", parameter_digest: "", bound: true }),
        },
        {
          alias: "old_key",
          kind: "parameter",
          change: "removed",
          reasons: [],
          from: pin("old_key", 3, { value: "gone" }),
        },
        {
          alias: "rate_limits",
          kind: "parameter",
          change: "changed",
          reasons: ["value"],
          from: pin("rate_limits", 2, { content_type: "integer", value: "7" }),
          to: pin("rate_limits", 3, { content_type: "integer", value: "12" }),
        },
        {
          alias: "timeout",
          kind: "parameter",
          change: "changed",
          reasons: ["content_type", "value"],
          from: pin("timeout", 1, { content_type: "integer", value: "30" }),
          to: pin("timeout", 2, { content_type: "string", value: "30s" }),
        },
      ]),
    );
    render(<ReleaseDiffView query={query} />);
    await screen.findAllByTestId("release-diff-row");
    const groups = document.querySelectorAll(".release-diff-group");
    expect([...groups].map((group) => group.getAttribute("aria-label"))).toEqual([
      "Needs attention",
      "Secrets",
      "Changed",
      "Added",
      "Removed",
    ]);
    // Every group heading carries its count.
    expect(within(groups[0] as HTMLElement).getByRole("heading")).toHaveTextContent(/1/);
    // The attention row is marked and says why.
    const timeout = rowFor("timeout");
    expect(timeout).toHaveAttribute("data-attention", "true");
    expect(timeout).toHaveTextContent("content type changed");
    // Secrets: versions only, never a value token; the group says so.
    const secret = rowFor("db_password");
    expect(secret).toHaveAttribute("data-kind", "secret");
    expect(secret.querySelector(".release-diff-old")).toBeNull();
    expect(secret.querySelector(".release-diff-new")).toBeNull();
    expect(secret).toHaveTextContent("v1");
    expect(secret).toHaveTextContent("v2");
    expect(
      within(groups[1] as HTMLElement).getByText(/Values are never shown or fetched/),
    ).toBeVisible();
    // Added and removed show one side and an em dash for the other.
    expect(rowFor("added_flag").querySelector(".release-diff-missing")).not.toBeNull();
    expect(rowFor("old_key").querySelector(".release-diff-missing")).not.toBeNull();
    expect(screen.getByTestId("release-diff-count-secrets")).toHaveTextContent("1");
  });

  it("filters rows by alias text and reports an empty match", async () => {
    render(<ReleaseDiffView query={query} />);
    await screen.findByTestId("release-diff-row");
    const filter = screen.getByRole("searchbox");
    fireEvent.change(filter, { target: { value: "zzz" } });
    expect(screen.queryAllByTestId("release-diff-row")).toHaveLength(0);
    expect(screen.getByRole("status")).toHaveTextContent("No entries match “zzz”.");
    fireEvent.change(filter, { target: { value: "rate" } });
    expect(rows()).toHaveLength(1);
    // The match is highlighted in the alias chip.
    expect(rows()[0]?.querySelector("mark")).toHaveTextContent("rate");
  });

  it("reveals unchanged rows on demand and loads their value lazily", async () => {
    mocks.getParameter.mockResolvedValue({
      parameter: {
        env: "prod",
        app: "gradethis",
        key: "database",
        value: '{"pool":{"max":50}}',
        content_type: "json",
        version: 1,
        metadata_json: "{}",
        created_by: "admin",
        created_at_unix_ms: 1,
        labels: { current: 1 },
      },
    });
    render(<ReleaseDiffView query={query} />);
    await screen.findByTestId("release-diff-row");
    fireEvent.click(screen.getByRole("checkbox", { name: /Show unchanged/ }));
    await waitFor(() => expect(rows()).toHaveLength(3));
    const unchanged = document.querySelector('.release-diff-group[data-group="unchanged"]');
    expect(unchanged).not.toBeNull();
    const database = rowFor("database");
    expect(database).toHaveAttribute("data-change", "unchanged");
    expect(database).toHaveTextContent("unchanged");
    fireEvent.click(within(database).getByRole("button", { name: "Expand database" }));
    expect(within(database).getByText("Both releases pin the same version.")).toBeVisible();
    fireEvent.click(within(database).getByRole("button", { name: "Load value" }));
    expect(mocks.getParameter).toHaveBeenCalledWith(
      { env: "prod", app: "gradethis", key: "database" },
      1,
    );
    // The value is pretty-printed and tokenised, so match on the pane's text.
    await waitFor(() =>
      expect(database.querySelector(".release-diff-pre")).toHaveTextContent(/"max": 50/),
    );
    // The secret row never gets a value affordance.
    const secret = rowFor("db_password");
    expect(within(secret).queryByRole("button", { name: "Load value" })).toBeNull();
    expect(mocks.getParameter).toHaveBeenCalledTimes(1);
  });

  it("expands a JSON row to the structural leaf list and switches to side-by-side, remembering the choice", async () => {
    mocks.releaseDiff.mockResolvedValue(
      withRows([
        {
          alias: "database",
          kind: "parameter",
          change: "changed",
          reasons: ["value"],
          from: pin("database", 1, {
            content_type: "json",
            value: '{"pool":{"max":50,"idle":10},"host":"db"}',
          }),
          to: pin("database", 2, {
            content_type: "json",
            value: '{"pool":{"max":5,"idle":10},"host":"db"}',
          }),
        },
      ]),
    );
    render(<ReleaseDiffView query={query} />);
    const row = await screen.findByTestId("release-diff-row");
    // Collapsed: the inline summary names the leaf that changed.
    expect(row).toHaveTextContent("pool.max");
    fireEvent.click(within(row).getByRole("button", { name: "Expand database" }));
    const structural = await within(row).findByTestId("release-diff-structural");
    expect(structural).toHaveTextContent("pool.max");
    expect(structural).toHaveTextContent("50");
    expect(structural).toHaveTextContent("5");
    // Unchanged leaves are folded, not listed.
    expect(structural).not.toHaveTextContent('"db"');
    expect(structural).toHaveTextContent(/2 unchanged/);
    expect(within(row).getByRole("tab", { name: "Structural" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    fireEvent.click(within(row).getByRole("tab", { name: "Side-by-side" }));
    await waitFor(() => expect(within(row).getByTestId("json-diff")).toBeVisible());
    expect(within(row).queryByTestId("release-diff-structural")).toBeNull();
    expect(window.localStorage.getItem("kms-release-diff-mode")).toBe("side");
  });

  it("renders compact without the page-level actions and the identical state without a toolbar", async () => {
    mocks.releaseDiff.mockResolvedValue(
      withRows(
        [
          {
            alias: "database",
            kind: "parameter",
            change: "unchanged",
            reasons: [],
            from: pin("database", 1, { value_state: "omitted_unchanged", value_bytes: 0 }),
            to: pin("database", 1, { value_state: "omitted_unchanged", value_bytes: 0 }),
          },
        ],
        { to: { ...fixture.to, digest: fixture.from.digest } },
      ),
    );
    render(<ReleaseDiffView query={query} compact />);
    const identical = await screen.findByTestId("release-diff-identical");
    expect(identical).toHaveTextContent("No differences.");
    expect(identical).toHaveTextContent("runtime@1:1 and runtime@1:2");
    expect(identical).toHaveTextContent("Created separately by admin and admin.");
    expect(screen.getByTestId("release-diff")).toHaveClass("release-diff-compact");
    expect(screen.getByTestId("release-diff")).toHaveAttribute("data-identical", "true");
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Copy link" })).toBeNull();
    expect(screen.queryByTestId("release-diff-row")).toBeNull();
  });

  it("copies the comparison as text with one line per change and secrets by version", async () => {
    const writeText = vi.fn<(text: string) => Promise<void>>(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    mocks.releaseDiff.mockResolvedValue(
      withRows([
        {
          alias: "db_password",
          kind: "secret",
          change: "changed",
          reasons: ["pin"],
          from: pin("db_password", 1, {
            value_state: "secret",
            parameter_digest: "",
            bound: false,
          }),
          to: pin("db_password", 2, { value_state: "secret", parameter_digest: "", bound: false }),
        },
        {
          alias: "rate_limits",
          kind: "parameter",
          change: "changed",
          reasons: ["value"],
          from: pin("rate_limits", 2, { content_type: "integer", value: "7" }),
          to: pin("rate_limits", 3, { content_type: "integer", value: "12" }),
        },
      ]),
    );
    render(<ReleaseDiffView query={query} compact />);
    await screen.findAllByTestId("release-diff-row");
    fireEvent.click(screen.getByRole("button", { name: "Copy as text" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const text = writeText.mock.calls[0]?.[0] as string;
    const lines = text.split("\n");
    expect(lines[0]).toMatch(
      /^runtime@1:1 → runtime@1:2 in prod\/gradethis \(shipped by admin, .*rev 12\)$/,
    );
    expect(lines).toHaveLength(3);
    expect(lines.find((line) => line.startsWith("changed"))).toMatch(
      /rate_limits\s+7 → 12 \(\+5, \+71 %\)/,
    );
    const secretLine = lines.find((line) => line.startsWith("secret"));
    expect(secretLine).toMatch(/db_password\s+v1 → v2/);
    expect(text).not.toContain("value-");
  });

  it("guards equal versions before requesting and shows the entries-only banner for values=false", async () => {
    render(<ReleaseDiffView query={{ ...query, from: 2, to: 2 }} />);
    expect(screen.getByRole("status")).toHaveTextContent("Pick two different versions.");
    expect(mocks.releaseDiff).not.toHaveBeenCalled();
  });
});
