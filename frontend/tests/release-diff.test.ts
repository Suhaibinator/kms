import { describe, expect, it } from "vitest";
import {
  aliasPrefix,
  buildRows,
  describeInline,
  elide,
  filterRows,
  groupRows,
  releaseDiffAsText,
  resolveDurationFormat,
  sideKey,
} from "@/lib/release-diff";
import type {
  ReleaseDiffPin,
  ReleaseDiffResponse,
  ReleaseDiffRow,
  ReleaseDiffSide,
} from "@/lib/types";
import { describeChange } from "@/lib/value-diff";

const ns = { env: "prod", app: "gradethis" };

function side(version: number, extra: Partial<ReleaseDiffSide> = {}): ReleaseDiffSide {
  return {
    namespace: ns,
    name: "runtime",
    version,
    schema_version: 1,
    digest: `digest-${version}`,
    created_by: version === 9 ? "alice" : "bob",
    created_at_unix_ms: Date.UTC(2026, 8, 12, 2, 41),
    current: version === 9,
    previous: version === 7,
    activation_revision: version === 9 ? 53 : 0,
    previous_version: version === 9 ? 7 : 0,
    ...extra,
  };
}

function pin(key: string, version: number, extra: Partial<ReleaseDiffPin> = {}): ReleaseDiffPin {
  return {
    ref: { namespace: ns, key },
    version,
    content_type: "integer",
    parameter_digest: `d${version}`,
    metadata_json: "",
    created_by: version % 2 ? "bob" : "alice",
    created_at_unix_ms: Date.UTC(2026, 8, 10),
    value_state: "present",
    value: String(version * 10),
    value_bytes: 2,
    ...extra,
  };
}

function response(
  rows: ReleaseDiffRow[],
  extra: Partial<ReleaseDiffResponse> = {},
): ReleaseDiffResponse {
  const counts = {
    added: rows.filter((r) => r.change === "added").length,
    removed: rows.filter((r) => r.change === "removed").length,
    changed: rows.filter((r) => r.change === "changed").length,
    unchanged: rows.filter((r) => r.change === "unchanged").length,
    secrets_changed: rows.filter((r) => r.kind === "secret" && r.change !== "unchanged").length,
    attention: 0,
  };
  return {
    from: side(7),
    to: side(9),
    identical: rows.every((r) => r.change === "unchanged"),
    schema_changed: false,
    cross_environment: false,
    counts,
    rows: [...rows].sort((a, b) => a.alias.localeCompare(b.alias)),
    value_cap_bytes: 262144,
    values_included: true,
    ...extra,
  };
}

const rateLimits: ReleaseDiffRow = {
  alias: "rate_limits",
  kind: "parameter",
  change: "changed",
  reasons: ["value"],
  from: pin("rate_limits", 3, { value: "100" }),
  to: pin("rate_limits", 4, { value: "20" }),
};
const database: ReleaseDiffRow = {
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
    value: '{"pool":{"max":5,"idle":2},"host":"db","ssl":true}',
  }),
};
const featureFlags: ReleaseDiffRow = {
  alias: "feature_flags",
  kind: "parameter",
  change: "added",
  reasons: [],
  to: pin("feature_flags", 1, { content_type: "json", value: '{"beta":true}' }),
};
const dbPassword: ReleaseDiffRow = {
  alias: "db_password",
  kind: "secret",
  change: "changed",
  reasons: ["pin"],
  from: pin("db_password", 2, {
    content_type: "",
    parameter_digest: "",
    value_state: "secret",
    value: undefined,
    secret_state: "enabled",
    bound: true,
  }),
  to: pin("db_password", 3, {
    content_type: "",
    parameter_digest: "",
    value_state: "secret",
    value: undefined,
    secret_state: "enabled",
    bound: true,
  }),
};
const timeout: ReleaseDiffRow = {
  alias: "timeout",
  kind: "parameter",
  change: "unchanged",
  reasons: [],
  from: pin("timeout", 1, {
    content_type: "string",
    value_state: "omitted_unchanged",
    value: undefined,
  }),
  to: pin("timeout", 1, {
    content_type: "string",
    value_state: "omitted_unchanged",
    value: undefined,
  }),
};

describe("buildRows", () => {
  it("describes each row, names the group-worthy reasons and builds a search haystack", () => {
    const rows = buildRows(response([rateLimits, database, featureFlags, dbPassword, timeout]));
    expect(rows.map((row) => row.alias)).toEqual([
      "database",
      "db_password",
      "feature_flags",
      "rate_limits",
      "timeout",
    ]);
    const rate = rows.find((row) => row.alias === "rate_limits");
    expect(rate?.summary).toBe("100 → 20 (−80, −80 %)");
    expect(rate?.attention).toBe(false);
    expect(rate?.prefix).toBe("rate");
    expect(rate?.searchText).toContain("rate_limits");
    expect(rate?.searchText).toContain("100 → 20");

    const db = rows.find((row) => row.alias === "database");
    expect(db?.summary).toBe("pool.idle 10 → 2, pool.max 50 → 5, +1 more");
    expect(db?.searchText).toContain("pool.max");

    expect(rows.find((row) => row.alias === "feature_flags")?.summary).toBe('{"beta":true}');
    expect(rows.find((row) => row.alias === "db_password")?.summary).toBe(
      "v2 → v3 (binding key → binding key)",
    );
    expect(rows.find((row) => row.alias === "db_password")?.description).toBeNull();
    expect(rows.find((row) => row.alias === "timeout")?.summary).toBe("unchanged");
  });

  it("flags attention for kind/type changes, unhealthy or expired secrets, unreadable values and one-track aliases", () => {
    const now = Date.UTC(2026, 8, 12);
    const rows = buildRows(
      response(
        [
          { ...rateLimits, reasons: ["value", "content_type"] },
          {
            ...dbPassword,
            alias: "api_key",
            to: { ...(dbPassword.to as ReleaseDiffPin), secret_state: "disabled" },
          },
          {
            ...dbPassword,
            alias: "old_key",
            to: { ...(dbPassword.to as ReleaseDiffPin), expires_at_unix_ms: now - 1 },
          },
          {
            ...database,
            alias: "blob",
            to: {
              ...(database.to as ReleaseDiffPin),
              value_state: "unavailable",
              value: undefined,
            },
            from: {
              ...(database.from as ReleaseDiffPin),
              value_state: "unavailable",
              value: undefined,
            },
          },
          { ...featureFlags, alias: "only_new" },
        ],
        { schema_changed: true },
      ),
      { now },
    );
    const byAlias = new Map(rows.map((row) => [row.alias, row]));
    expect(byAlias.get("rate_limits")?.attentionReasons).toEqual(["content type changed"]);
    expect(byAlias.get("api_key")?.attentionReasons).toEqual(["secret disabled"]);
    expect(byAlias.get("old_key")?.attentionReasons).toEqual(["secret expired"]);
    expect(byAlias.get("blob")?.attentionReasons).toEqual(["value not readable"]);
    expect(byAlias.get("only_new")?.attentionReasons).toEqual(["only on one schema track"]);
    expect(rows.every((row) => row.attention)).toBe(true);
  });

  it("summarises entry-only responses by version and reason without touching values", () => {
    const rows = buildRows(
      response(
        [
          {
            ...rateLimits,
            from: {
              ...(rateLimits.from as ReleaseDiffPin),
              value_state: "omitted_request",
              value: undefined,
            },
            to: {
              ...(rateLimits.to as ReleaseDiffPin),
              value_state: "omitted_request",
              value: undefined,
            },
          },
        ],
        { values_included: false },
      ),
    );
    expect(rows[0]?.summary).toBe("v3 → v4 (value)");
    expect(rows[0]?.description).toBeNull();
    expect(rows[0]?.attention).toBe(false);
  });

  it("reports an oversize omitted value with its size and promotes schema durations", () => {
    const rows = buildRows(
      response([
        {
          ...rateLimits,
          alias: "big",
          from: {
            ...(rateLimits.from as ReleaseDiffPin),
            value_state: "omitted_size",
            value: undefined,
            value_bytes: 1_500_000,
          },
          to: {
            ...(rateLimits.to as ReleaseDiffPin),
            value_state: "omitted_size",
            value: undefined,
            value_bytes: 1_500_000,
          },
        },
        {
          ...rateLimits,
          alias: "grace",
          from: { ...(rateLimits.from as ReleaseDiffPin), content_type: "string", value: "3s" },
          to: { ...(rateLimits.to as ReleaseDiffPin), content_type: "string", value: "30s" },
        },
      ]),
      {
        schemaJson: JSON.stringify({
          properties: {
            grace: { anyOf: [{ type: "string", format: "go-duration" }, { type: "null" }] },
          },
        }),
      },
    );
    expect(rows.find((row) => row.alias === "big")?.summary).toMatch(
      /^too large to compare inline \(1\.4 MiB\)$/,
    );
    expect(rows.find((row) => row.alias === "big")?.attentionReasons).toEqual([
      "value too large to compare",
    ]);
    expect(rows.find((row) => row.alias === "grace")?.summary).toBe("3s → 30s (×10)");
  });
});

describe("groupRows and filterRows", () => {
  const diff = response([rateLimits, database, featureFlags, dbPassword, timeout]);
  const rows = buildRows(diff);

  it("groups by kind with attention first and drops empty groups", () => {
    const groups = groupRows(filterRows(rows, "", "all", "all"), "kind");
    expect(groups.map((group) => [group.id, group.rows.map((row) => row.alias)])).toEqual([
      ["secrets", ["db_password"]],
      ["changed", ["database", "rate_limits"]],
      ["added", ["feature_flags"]],
      ["unchanged", ["timeout"]],
    ]);
    const attention = groupRows(
      buildRows(response([{ ...rateLimits, reasons: ["value", "kind"] }, database])),
      "kind",
    );
    expect(attention[0]?.id).toBe("attention");
    expect(attention[0]?.rows.map((row) => row.alias)).toEqual(["rate_limits"]);
  });

  it("groups by alias prefix alphabetically", () => {
    const groups = groupRows(rows, "prefix");
    expect(groups.map((group) => group.title)).toEqual([
      "database",
      "db",
      "feature",
      "rate",
      "timeout",
    ]);
    expect(groups.every((group) => group.tone === "prefix")).toBe(true);
    expect(aliasPrefix("db-password")).toBe("db");
    expect(aliasPrefix("plain")).toBe("plain");
  });

  it("hides unchanged rows unless asked, filters by kind and by every token", () => {
    expect(filterRows(rows, "").map((row) => row.alias)).not.toContain("timeout");
    expect(filterRows(rows, "", "all", "all").map((row) => row.alias)).toContain("timeout");
    expect(filterRows(rows, "", "secret").map((row) => row.alias)).toEqual(["db_password"]);
    expect(filterRows(rows, "rat").map((row) => row.alias)).toEqual(["rate_limits"]);
    expect(filterRows(rows, "pool.max").map((row) => row.alias)).toEqual(["database"]);
    expect(filterRows(rows, "RATE 20").map((row) => row.alias)).toEqual(["rate_limits"]);
    expect(filterRows(rows, "rate 999")).toEqual([]);
  });
});

describe("releaseDiffAsText", () => {
  it("prints the header, then one padded line per changed alias, secrets by version only", () => {
    const text = releaseDiffAsText(
      response([rateLimits, database, featureFlags, dbPassword, timeout]),
    );
    expect(text.split("\n")).toEqual([
      "runtime@1:7 → runtime@1:9 in prod/gradethis (shipped by alice, 2026-09-12 02:41 UTC, rev 53)",
      "changed  database      pool.idle 10 → 2, pool.max 50 → 5, +1 more",
      "secret   db_password   v2 → v3 (binding key → binding key)",
      'added    feature_flags {"beta":true}',
      "changed  rate_limits   100 → 20 (−80, −80 %)",
    ]);
    expect(text).not.toContain("hunter2");
  });

  it("says so when there is nothing to report and names both namespaces across environments", () => {
    expect(releaseDiffAsText(response([timeout]))).toBe(
      "runtime@1:7 → runtime@1:9 in prod/gradethis (shipped by alice, 2026-09-12 02:41 UTC, rev 53)\nno differences",
    );
    const cross = releaseDiffAsText(
      response([rateLimits], {
        cross_environment: true,
        to: side(9, {
          namespace: { env: "staging", app: "gradethis" },
          current: false,
          activation_revision: 0,
        }),
      }),
    );
    expect(cross.split("\n")[0]).toBe(
      "runtime@1:7 → runtime@1:9 in prod/gradethis → staging/gradethis (shipped by alice, 2026-09-12 02:41 UTC)",
    );
  });
});

describe("helpers", () => {
  it("elides long scalars, keys sides and reads a schema format through anyOf", () => {
    expect(elide("x".repeat(121))).toHaveLength(120);
    expect(elide("short")).toBe("short");
    expect(sideKey(side(7))).toBe("runtime@1:7");
    expect(resolveDurationFormat(null, "a")).toBeUndefined();
    expect(resolveDurationFormat("{", "a")).toBeUndefined();
    expect(resolveDurationFormat('{"properties":{"a":{"format":"go-duration"}}}', "a")).toBe(
      "go-duration",
    );
    expect(resolveDurationFormat('{"properties":{"a":{"format":"email"}}}', "a")).toBeUndefined();
    expect(describeInline(describeChange("true", "false", "boolean"))).toBe("true → false");
    expect(describeInline(describeChange("abcd", "abcdef", "binary"))).toBe("4 bytes → 6 bytes");
  });
});
