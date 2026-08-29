import type { VerifyReleaseDefaultsResponse } from "../generated/kms.js";

/** Bounded verdicts returned by verifyReleaseDefaults for one alias. */
export const VERIFY_VERDICTS = [
  "match",
  "differs",
  "missing_in_release",
  "unknown_alias",
  "secret_alias",
  "unsupported_content_type",
] as const;
export type VerifyVerdict = (typeof VERIFY_VERDICTS)[number];

/**
 * One parameter alias with the lowercase hex SHA-256 of its canonical encoded
 * value (`parameterHash`). Secret aliases must not be sent; the server reports
 * them as `secret_alias` without reading them.
 */
export interface VerifyDefaultsEntry {
  readonly alias: string;
  readonly contentType: string;
  readonly sha256: string;
}

/** One value-free comparison of source-owned defaults against an active release. */
export interface VerifyReleaseDefaultsOptions {
  /** The "env/app" whose active release is compared. Required: verification identities are typically unbound. */
  readonly namespace: string;
  /** Release name; empty selects the application's configured release name. */
  readonly release?: string;
  /** Informational label carried with the request. */
  readonly profile?: string;
  /** Generated contract schema digest; empty skips the schema check and leaves schemaMatches false. */
  readonly schemaSha256?: string;
  readonly entries: readonly VerifyDefaultsEntry[];
  readonly signal?: AbortSignal;
  readonly deadline?: Date;
}

/** The server's bounded verdict for one alias. */
export interface VerifyDefaultsVerdict {
  readonly alias: string;
  readonly verdict: VerifyVerdict;
}

/** Validated, value-free server response. */
export interface VerifyReleaseDefaultsResult {
  readonly releaseName: string;
  readonly releaseVersion: bigint;
  readonly activationRevision: bigint;
  readonly schemaMatches: boolean;
  readonly entries: readonly VerifyDefaultsVerdict[];
  readonly matchCount: number;
  readonly differsCount: number;
  readonly missingCount: number;
  readonly unknownAliasCount: number;
  readonly secretAliasCount: number;
  readonly unsupportedCount: number;
  /** Parameter aliases the release pins that the request did not mention. */
  readonly unverifiedCount: number;
  /** True when the schema matched and every entry matched. */
  passed(): boolean;
}

export function isVerifyVerdict(value: string): value is VerifyVerdict {
  return (VERIFY_VERDICTS as readonly string[]).includes(value);
}

export function validLowerHex64(value: string): boolean {
  return /^[0-9a-f]{64}$/u.test(value);
}

/** @internal Validate the wire response against the request and freeze the result. */
export function verifyResultFromWire(
  response: VerifyReleaseDefaultsResponse,
  requested: ReadonlySet<string>,
  fail: (message: string) => never,
): VerifyReleaseDefaultsResult {
  if (response.entries.length !== requested.size) {
    fail(`verify response has ${response.entries.length} verdicts for ${requested.size} entries`);
  }
  const tally = new Map<VerifyVerdict, number>();
  const seen = new Set<string>();
  const entries: VerifyDefaultsVerdict[] = [];
  for (const [index, entry] of response.entries.entries()) {
    if (!entry) fail(`verify response entry ${index} is empty`);
    if (!requested.has(entry.alias) || seen.has(entry.alias)) {
      fail("verify response names an unknown or duplicated alias");
    }
    if (!isVerifyVerdict(entry.verdict)) fail("verify response entry has an invalid verdict");
    seen.add(entry.alias);
    tally.set(entry.verdict, (tally.get(entry.verdict) ?? 0) + 1);
    entries.push(Object.freeze({ alias: entry.alias, verdict: entry.verdict }));
  }
  const counts: readonly (readonly [VerifyVerdict, number])[] = [
    ["match", response.matchCount],
    ["differs", response.differsCount],
    ["missing_in_release", response.missingInReleaseCount],
    ["unknown_alias", response.unknownAliasCount],
    ["secret_alias", response.secretAliasCount],
    ["unsupported_content_type", response.unsupportedContentTypeCount],
  ];
  for (const [verdict, count] of counts) {
    if (!validCount(count) || count !== (tally.get(verdict) ?? 0)) {
      fail(`verify response ${verdict} count disagrees with its verdicts`);
    }
  }
  if (!validCount(response.unverifiedCount)) fail("verify response unverified count is invalid");
  const schemaMatches = response.schemaMatches === true;
  const frozenEntries = Object.freeze(entries);
  return Object.freeze({
    releaseName: response.name,
    releaseVersion: response.version,
    activationRevision: response.activationRevision,
    schemaMatches,
    entries: frozenEntries,
    matchCount: response.matchCount,
    differsCount: response.differsCount,
    missingCount: response.missingInReleaseCount,
    unknownAliasCount: response.unknownAliasCount,
    secretAliasCount: response.secretAliasCount,
    unsupportedCount: response.unsupportedContentTypeCount,
    unverifiedCount: response.unverifiedCount,
    passed: () => schemaMatches && frozenEntries.every((entry) => entry.verdict === "match"),
  });
}

function validCount(value: number): boolean {
  return Number.isInteger(value) && value >= 0;
}
