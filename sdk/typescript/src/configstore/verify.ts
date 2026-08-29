import type {
  VerifyReleaseDefaultsOptions,
  VerifyReleaseDefaultsResult,
  VerifyVerdict,
} from "../releases/verify.js";
import { parameterHash } from "./canonical.js";
import type { ContractEntry } from "./contract.js";

/**
 * What a generated binding knows about its source-owned defaults: the
 * contract, the schema digest, and the canonical non-secret parameter group
 * documents keyed by alias (`encodeParameterGroups`).
 */
export interface VerifyInput {
  readonly schemaSha256: string;
  readonly contract: readonly ContractEntry[];
  readonly groups: Readonly<Record<string, string>>;
}

/** Addresses the release to compare against. */
export interface VerifyOptions {
  /** The "env/app" whose active release is compared. Required. */
  readonly namespace: string;
  /** Release name; omit to select the application's configured release name on the server. */
  readonly release?: string;
  /** Informational label sent with the request. */
  readonly profile?: string;
  readonly signal?: AbortSignal;
  readonly deadline?: Date;
}

/** The verdict for one parameter alias. */
export interface VerifyEntryResult {
  readonly alias: string;
  readonly contentType: string;
  readonly verdict: VerifyVerdict;
}

/** Structural KmsClient subset used by verifyDefaults; transport internals remain private. */
export interface VerifyClient {
  verifyReleaseDefaults(
    options: VerifyReleaseDefaultsOptions,
  ): Promise<VerifyReleaseDefaultsResult>;
}

/** Value-free outcome of verifyDefaults. */
export class VerifyResult {
  readonly namespace: string;
  readonly releaseName: string;
  readonly releaseVersion: bigint;
  readonly activationRevision: bigint;
  /** True when the server's pinned application schema digest equals the generated contract's digest. */
  readonly schemaMatches: boolean;
  readonly entries: readonly VerifyEntryResult[];
  /** Parameter aliases pinned by the release that the contract did not mention. */
  readonly unverified: number;

  /** @internal Use verifyDefaults. */
  constructor(init: {
    readonly namespace: string;
    readonly releaseName: string;
    readonly releaseVersion: bigint;
    readonly activationRevision: bigint;
    readonly schemaMatches: boolean;
    readonly entries: readonly VerifyEntryResult[];
    readonly unverified: number;
  }) {
    this.namespace = init.namespace;
    this.releaseName = init.releaseName;
    this.releaseVersion = init.releaseVersion;
    this.activationRevision = init.activationRevision;
    this.schemaMatches = init.schemaMatches;
    this.entries = Object.freeze(init.entries.map((entry) => Object.freeze({ ...entry })));
    this.unverified = init.unverified;
    Object.freeze(this);
  }

  /** Whether the schema matched and every alias matched. */
  passed(): boolean {
    return this.schemaMatches && this.entries.every((entry) => entry.verdict === "match");
  }

  /** Entries whose verdict is not match, sorted by alias. */
  failures(): VerifyEntryResult[] {
    return sortByAlias(this.entries.filter((entry) => entry.verdict !== "match"));
  }

  /** Human-readable, value-free summary suitable for CI logs. */
  report(): string {
    const lines: string[] = [];
    lines.push(
      `${this.namespace} ${this.releaseName}@${this.releaseVersion}#${this.activationRevision}  schema: ${this.schemaMatches ? "match" : "differs"}`,
    );
    const rows = [
      ["VERDICT", "ALIAS", "CONTENT_TYPE"],
      ...sortByAlias(this.entries).map((entry) => [entry.verdict, entry.alias, entry.contentType]),
    ];
    lines.push(...alignColumns(rows));
    const counts = new Map<VerifyVerdict, number>();
    for (const entry of this.entries)
      counts.set(entry.verdict, (counts.get(entry.verdict) ?? 0) + 1);
    const count = (verdict: VerifyVerdict): number => counts.get(verdict) ?? 0;
    lines.push(
      `summary: match=${count("match")} differs=${count("differs")} missing_in_release=${count("missing_in_release")} unknown_alias=${count("unknown_alias")} secret_alias=${count("secret_alias")} unsupported_content_type=${count("unsupported_content_type")} unverified=${this.unverified}`,
    );
    lines.push(
      this.passed()
        ? "result: active release matches source defaults"
        : "result: active release differs from source defaults",
    );
    return `${lines.join("\n")}\n`;
  }

  toJSON(): Readonly<Record<string, unknown>> {
    return Object.freeze({
      namespace: this.namespace,
      releaseName: this.releaseName,
      releaseVersion: this.releaseVersion.toString(),
      activationRevision: this.activationRevision.toString(),
      schemaMatches: this.schemaMatches,
      entries: this.entries,
      unverified: this.unverified,
      passed: this.passed(),
    });
  }
}

/**
 * Hash every parameter group in `input.groups` with `parameterHash` and ask
 * the server which aliases of the active release differ. Secret contract
 * entries are never sent. The result carries verdicts only; a `RateLimitedError`
 * means the per-identity verify budget is spent and the call should not be
 * retried until the window resets.
 */
export async function verifyDefaults(
  client: VerifyClient,
  input: VerifyInput,
  options: VerifyOptions,
): Promise<VerifyResult> {
  if (!client || typeof client.verifyReleaseDefaults !== "function") {
    throw new TypeError("configstore: verify requires a KmsClient-compatible client");
  }
  const namespace = options?.namespace?.trim();
  if (!namespace) throw new TypeError("configstore: verify requires options.namespace");
  if (!input || !Array.isArray(input.contract)) {
    throw new TypeError("configstore: verify requires a contract array");
  }
  if (typeof input.groups !== "object" || input.groups === null) {
    throw new TypeError("configstore: verify requires encoded parameter groups");
  }
  const entries: { alias: string; contentType: string; sha256: string }[] = [];
  const contentTypes = new Map<string, string>();
  for (const entry of input.contract) {
    if (entry.kind !== "parameter") continue;
    const document = Object.hasOwn(input.groups, entry.alias)
      ? input.groups[entry.alias]
      : undefined;
    if (typeof document !== "string") {
      throw new Error(`configstore: verify: missing encoded parameter group ${entry.alias}`);
    }
    const contentType = entry.contentType ?? "";
    let sha256: string;
    try {
      sha256 = parameterHash(contentType, document);
    } catch (cause) {
      throw new Error(`configstore: verify: hash parameter group ${entry.alias}`, { cause });
    }
    entries.push({ alias: entry.alias, contentType, sha256 });
    contentTypes.set(entry.alias, contentType);
  }
  const response = await client.verifyReleaseDefaults({
    namespace,
    ...(options.release ? { release: options.release } : {}),
    ...(options.profile ? { profile: options.profile } : {}),
    ...(input.schemaSha256 ? { schemaSha256: input.schemaSha256 } : {}),
    entries,
    ...(options.signal ? { signal: options.signal } : {}),
    ...(options.deadline ? { deadline: options.deadline } : {}),
  });
  return new VerifyResult({
    namespace,
    releaseName: response.releaseName,
    releaseVersion: response.releaseVersion,
    activationRevision: response.activationRevision,
    schemaMatches: response.schemaMatches,
    entries: response.entries.map((verdict) => ({
      alias: verdict.alias,
      contentType: contentTypes.get(verdict.alias) ?? "",
      verdict: verdict.verdict,
    })),
    unverified: response.unverifiedCount,
  });
}

function sortByAlias(entries: readonly VerifyEntryResult[]): VerifyEntryResult[] {
  return [...entries].sort((left, right) =>
    Buffer.compare(Buffer.from(left.alias, "utf8"), Buffer.from(right.alias, "utf8")),
  );
}

/** Pad every column but the last to its widest cell plus two spaces, like Go's tabwriter. */
function alignColumns(rows: readonly (readonly string[])[]): string[] {
  const widths: number[] = [];
  for (const row of rows) {
    for (const [index, cell] of row.entries()) {
      if (index === row.length - 1) continue;
      widths[index] = Math.max(widths[index] ?? 0, cell.length + 2);
    }
  }
  return rows.map((row) =>
    row
      .map((cell, index) => (index === row.length - 1 ? cell : cell.padEnd(widths[index] ?? 0)))
      .join(""),
  );
}
