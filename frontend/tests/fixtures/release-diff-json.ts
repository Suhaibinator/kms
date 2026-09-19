// A JSON parameter value pair with every kind of field change the release
// comparison renders: a changed number and duration, twelve changed array
// leaves, a key that moved under a new object, and an added object. Shared by
// the vitest view tests and the Playwright fake (`withFeaturesJson`), so the
// counts asserted in both come from one place. `tests/release-diff.test.ts`
// pins `FEATURES_EXPECTED` to `structuralDiff` so the fixture cannot drift.

const HOSTS = 24;

function hosts(weightFor: (index: number) => number) {
  return Array.from({ length: HOSTS }, (_, i) => ({
    name: `h${i}`,
    port: 8000 + i,
    weight: weightFor(i),
  }));
}

/** Before: a 50-connection pool, 24 evenly weighted hosts, a legacy endpoint at the root. */
export const FEATURES_BEFORE: string = JSON.stringify({
  pool: { max: 50, idle: 10, timeout: "30s" },
  hosts: hosts(() => 1),
  features: { read_replicas: true, sharding: false },
  legacy_endpoint: "https://old.internal:8443/api",
});

/**
 * After: `pool.max` and `pool.timeout` changed, the first twelve hosts
 * reweighted, `legacy_endpoint` moved to `endpoints.legacy` (inside a new
 * object) and a `tls` object added.
 */
export const FEATURES_AFTER: string = JSON.stringify({
  pool: { max: 5, idle: 10, timeout: "5s" },
  hosts: hosts((i) => (i < 12 ? 2 : 1)),
  features: { read_replicas: true, sharding: false },
  endpoints: { legacy: "https://old.internal:8443/api" },
  tls: { cert_file: "/etc/kms/tls.crt", key_file: "/etc/kms/tls.key", min_version: "1.3" },
});

/** What `fieldCounts(structuralDiff(FEATURES_BEFORE, FEATURES_AFTER))` reports. */
export const FEATURES_EXPECTED = { added: 1, removed: 0, changed: 14, moved: 1 };

/** Sum of `FEATURES_EXPECTED`: over the 12-field cap, so "Show all" appears. */
export const FEATURES_FIELD_TOTAL = 16;

/** The pretty-printed `tls` object as the added-subtree line count (`{`, three fields, `}`). */
export const FEATURES_TLS_LINES = 5;
