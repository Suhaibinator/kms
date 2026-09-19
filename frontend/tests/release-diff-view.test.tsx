// The release comparison view against the Go-generated fixture: the verdict
// band an operator reads first, groups with "Needs attention" ahead of
// everything else, the inline old → new text, filtering, lazily loaded values
// for unchanged rows, changed JSON rows open to their field list with the
// Fields / Unified / Split view chosen once in the toolbar, secrets rendered
// by version only, compact mode, the identical state and the plain-text
// export.
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ReleaseDiffView } from "@/components/releases/diff/ReleaseDiffView";
import type { ReleaseDiffPin, ReleaseDiffResponse, ReleaseDiffRow } from "@/lib/types";
import diffJson from "./fixtures/backend/release-diff.json";
import {
  FEATURES_AFTER,
  FEATURES_BEFORE,
  FEATURES_FIELD_TOTAL,
} from "./fixtures/release-diff-json";

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

/** A changed JSON row: two pool fields changed, `ssl` added, `host` and `port` unchanged. */
function databaseRow(): ReleaseDiffRow {
  return {
    alias: "database",
    kind: "parameter",
    change: "changed",
    reasons: ["value"],
    from: pin("database", 1, {
      content_type: "json",
      value: '{"pool":{"max":50,"idle":10},"host":"db","port":5432}',
    }),
    to: pin("database", 2, {
      content_type: "json",
      value: '{"pool":{"max":5,"idle":20},"host":"db","port":5432,"ssl":true}',
    }),
  };
}

/** The shared fixture: 16 field changes (one added subtree, 14 changed, one moved). */
function featuresRow(): ReleaseDiffRow {
  return {
    alias: "features",
    kind: "parameter",
    change: "changed",
    reasons: ["value"],
    from: pin("features", 1, { content_type: "json", value: FEATURES_BEFORE }),
    to: pin("features", 2, { content_type: "json", value: FEATURES_AFTER }),
  };
}

const fieldLines = (row: HTMLElement) => row.querySelectorAll<HTMLElement>(".release-diff-field");
const fieldWithPath = (row: HTMLElement, path: string) =>
  [...fieldLines(row)].find(
    (line) => line.querySelector(".release-diff-field-path")?.textContent === path,
  ) as HTMLElement;
const valueViewTab = (name: "Fields" | "Unified" | "Split") =>
  within(screen.getByRole("tablist", { name: "Value view" })).getByRole("tab", { name });

describe("ReleaseDiffView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.releaseDiff.mockResolvedValue(fixture);
  });

  it("shows the verdict band and only the changed row by default, with the old → new values", async () => {
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

    // The band is one sentence of facts; separators are CSS, so each fact's
    // text is exact and the zero facts are marked faint.
    const band = screen.getByTestId("release-diff-strip");
    expect(band).toHaveClass("release-diff-verdict");
    expect(band).not.toHaveAttribute("role");
    expect(screen.getByTestId("release-diff-count-changed")).toHaveTextContent(
      /^1 parameter changed$/,
    );
    expect(screen.getByTestId("release-diff-count-added")).toHaveTextContent(/^0 added$/);
    expect(screen.getByTestId("release-diff-count-added")).toHaveAttribute("data-zero");
    expect(screen.getByTestId("release-diff-count-removed")).toHaveTextContent(/^0 removed$/);
    expect(screen.getByTestId("release-diff-count-secrets")).toHaveTextContent(
      /^no secrets repinned$/,
    );
    expect(screen.getByTestId("release-diff-schema")).toHaveTextContent(/^schema v1 unchanged$/);
    // No JSON row has a structural diff, so no field total.
    expect(screen.queryByTestId("release-diff-fields-total")).toBeNull();
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
    expect(screen.getByTestId("release-diff-count-secrets")).toHaveTextContent(
      /^1 secret repinned$/,
    );
    // `counts.changed` includes the secret, so the noun is "entries".
    expect(screen.getByTestId("release-diff-count-changed")).toHaveTextContent(
      /^3 entries changed$/,
    );
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

  it("opens a changed JSON row to its field list without a click, with count chips in the head", async () => {
    mocks.releaseDiff.mockResolvedValue(withRows([databaseRow()]));
    render(<ReleaseDiffView query={query} />);
    const row = await screen.findByTestId("release-diff-row");
    // Open by default: the chevron offers to collapse, and the field list is there.
    expect(within(row).getByRole("button", { name: "Collapse database" })).toBeInTheDocument();
    const fields = within(row).getByTestId("release-diff-fields");
    expect(fields.querySelector("ol.release-diff-field-list")).not.toBeNull();
    expect(fieldLines(row)).toHaveLength(3);
    // A changed number carries its typed delta.
    const max = fieldWithPath(row, "pool.max");
    expect(max).toHaveAttribute("data-change", "changed");
    expect(max.querySelector(".release-diff-field-gutter")).toHaveTextContent("~");
    expect(max.querySelector(".release-diff-old")).toHaveTextContent("50");
    expect(max.querySelector(".release-diff-new")).toHaveTextContent("5");
    expect(max.querySelector(".release-diff-delta")).toHaveTextContent("(−45, −90 %)");
    // An added leaf: value only, the gutter says which side.
    const ssl = fieldWithPath(row, "ssl");
    expect(ssl).toHaveAttribute("data-change", "added");
    expect(ssl.querySelector(".release-diff-field-gutter")).toHaveTextContent("+");
    expect(ssl.querySelector(".release-diff-field-values")).toHaveTextContent("true");
    expect(ssl.querySelector(".release-diff-old")).toBeNull();
    // Unchanged leaves are counted, not listed.
    expect(fields).not.toHaveTextContent('"db"');
    expect(fields).toHaveTextContent("2 unchanged fields not listed");
    // The head carries the counts as chips instead of a `changed` badge.
    const head = row.querySelector(".release-diff-row-head") as HTMLElement;
    expect(head.querySelector('.release-diff-chip[data-tone="changed"]')).toHaveTextContent("~2");
    expect(head.querySelector('.release-diff-chip[data-tone="added"]')).toHaveTextContent("+1");
    expect(head.querySelector('.release-diff-chip[data-tone="removed"]')).toBeNull();
    expect(head.textContent).not.toMatch(/\bchanged\b/);
    // The band totals the fields across rows.
    expect(screen.getByTestId("release-diff-fields-total")).toHaveTextContent(
      /^3 fields \(\+1 ~2\)$/,
    );
    // The chevron collapses to head and meta only.
    fireEvent.click(within(row).getByRole("button", { name: "Collapse database" }));
    expect(within(row).queryByTestId("release-diff-fields")).toBeNull();
    expect(within(row).getByRole("button", { name: "Expand database" })).toBeInTheDocument();
    fireEvent.click(within(row).getByRole("button", { name: "Expand database" }));
    expect(within(row).getByTestId("release-diff-fields")).toBeInTheDocument();
  });

  it("switches the value view from the toolbar and remembers it, migrating the old key", async () => {
    mocks.releaseDiff.mockResolvedValue(withRows([databaseRow()]));
    const first = render(<ReleaseDiffView query={query} />);
    const row = await screen.findByTestId("release-diff-row");
    expect(valueViewTab("Fields")).toHaveAttribute("aria-selected", "true");
    // No per-row tabs any more: the choice is made once.
    expect(within(row).queryByRole("tab")).toBeNull();

    fireEvent.click(valueViewTab("Split"));
    await waitFor(() =>
      expect(within(row).getByTestId("json-diff")).toHaveAttribute("data-layout", "split"),
    );
    expect(within(row).queryByTestId("release-diff-fields")).toBeNull();
    expect(window.localStorage.getItem("kms-release-diff-mode")).toBe("split");

    fireEvent.click(valueViewTab("Unified"));
    await waitFor(() =>
      expect(within(row).getByTestId("json-diff")).toHaveAttribute("data-layout", "unified"),
    );
    // The removed line's sign cell is the rail.
    expect(row.querySelector('.json-diff-sign[data-op="del"]')).not.toBeNull();
    expect(window.localStorage.getItem("kms-release-diff-mode")).toBe("unified");

    fireEvent.click(valueViewTab("Fields"));
    await waitFor(() => expect(within(row).getByTestId("release-diff-fields")).toBeVisible());
    expect(within(row).queryByTestId("json-diff")).toBeNull();
    expect(window.localStorage.getItem("kms-release-diff-mode")).toBe("fields");
    first.unmount();

    // The previous pass stored `side` / `structural`; both migrate and are rewritten.
    window.localStorage.setItem("kms-release-diff-mode", "side");
    const second = render(<ReleaseDiffView query={query} />);
    const rowAgain = await screen.findByTestId("release-diff-row");
    await waitFor(() => expect(valueViewTab("Split")).toHaveAttribute("aria-selected", "true"));
    expect(within(rowAgain).getByTestId("json-diff")).toHaveAttribute("data-layout", "split");
    expect(window.localStorage.getItem("kms-release-diff-mode")).toBe("split");
    second.unmount();

    window.localStorage.setItem("kms-release-diff-mode", "structural");
    render(<ReleaseDiffView query={query} />);
    const rowThird = await screen.findByTestId("release-diff-row");
    await waitFor(() => expect(valueViewTab("Fields")).toHaveAttribute("aria-selected", "true"));
    expect(within(rowThird).getByTestId("release-diff-fields")).toBeInTheDocument();
    expect(window.localStorage.getItem("kms-release-diff-mode")).toBe("fields");
  });

  it("caps the field list at twelve, marks a moved key and prints an added object in full", async () => {
    mocks.releaseDiff.mockResolvedValue(withRows([featuresRow()]));
    render(<ReleaseDiffView query={query} />);
    const row = await screen.findByTestId("release-diff-row");
    expect(fieldLines(row)).toHaveLength(12);
    // The move reads as one line, old path → new path, value once in a neutral tint.
    const moved = row.querySelectorAll('.release-diff-field[data-change="moved"]');
    expect(moved).toHaveLength(1);
    const movedLine = moved[0] as HTMLElement;
    expect(movedLine.querySelector(".release-diff-field-gutter")).toHaveTextContent("↷");
    expect(movedLine.querySelector(".release-diff-field-path")).toHaveTextContent(
      "legacy_endpoint → endpoints.legacy",
    );
    expect(movedLine.querySelector(".release-diff-moved")).toHaveTextContent(
      "https://old.internal:8443/api",
    );
    expect(movedLine.querySelector(".release-diff-old")).toBeNull();
    expect(movedLine.querySelector(".release-diff-new")).toBeNull();
    // Keys sort, so `pool.*` sits past the cap until Show all.
    expect(fieldWithPath(row, "pool.timeout")).toBeUndefined();
    // Chips and band agree with the fixture's counts.
    expect(row.querySelector('.release-diff-chip[data-tone="changed"]')).toHaveTextContent("~14");
    expect(row.querySelector('.release-diff-chip[data-tone="added"]')).toHaveTextContent("+1");
    expect(row.querySelector('.release-diff-chip[data-tone="moved"]')).toHaveTextContent("↷1");
    expect(screen.getByTestId("release-diff-fields-total")).toHaveTextContent(
      /^16 fields \(\+1 ~14 ↷1\)$/,
    );
    // Show all reveals the rest, including the added object printed line by line.
    fireEvent.click(
      within(row).getByRole("button", { name: `Show all ${FEATURES_FIELD_TOTAL} fields` }),
    );
    expect(fieldLines(row)).toHaveLength(FEATURES_FIELD_TOTAL);
    const tls = fieldWithPath(row, "tls");
    expect(tls).toHaveAttribute("data-change", "added");
    expect(tls).toHaveAttribute("data-subtree", "true");
    const code = tls.querySelectorAll(".release-diff-field-code");
    expect(code).toHaveLength(5);
    expect(code[0]).toHaveAttribute("data-op");
    expect(tls).toHaveTextContent("/etc/kms/tls.crt");
    // A Go duration reads with its ratio.
    const timeout = fieldWithPath(row, "pool.timeout");
    expect(timeout).toHaveAttribute("data-change", "changed");
    expect(timeout.querySelector(".release-diff-old")).toHaveTextContent("30s");
    expect(timeout.querySelector(".release-diff-new")).toHaveTextContent("5s");
    expect(timeout.querySelector(".release-diff-delta")).toHaveTextContent("×0.17");
    expect(within(row).queryByRole("button", { name: /Show all/ })).toBeNull();
    // The unchanged leaves stay counted, never listed.
    expect(within(row).getByTestId("release-diff-fields")).toHaveTextContent(
      /\d+ unchanged fields not listed/,
    );
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
