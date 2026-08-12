import type { ValidateReleaseManifest } from "../releases/loader.js";
import type { ReleaseManifest } from "../releases/types.js";
import { CandidateError } from "./errors.js";

export type ContractKind = "parameter" | "secret";

export interface ContractEntry {
  readonly alias: string;
  readonly kind: ContractKind;
  /** Exact for parameters; an empty secret content type is a wildcard. */
  readonly contentType?: string;
}

/** Validate and defensively copy the generated release alias contract. */
export function validateContract(contract: readonly ContractEntry[]): readonly ContractEntry[] {
  if (!Array.isArray(contract) || contract.length === 0) {
    throw new TypeError("configstore: contract must contain at least one entry");
  }
  const aliases = new Set<string>();
  const result: ContractEntry[] = [];
  for (const entry of contract) {
    if (!entry?.alias || entry.alias.trim() !== entry.alias) {
      throw new TypeError("configstore: contract aliases must be non-empty and canonical");
    }
    if (aliases.has(entry.alias)) {
      throw new TypeError("configstore: contract contains a duplicate alias");
    }
    aliases.add(entry.alias);
    if (entry.kind !== "parameter" && entry.kind !== "secret") {
      throw new TypeError("configstore: contract entry kind is invalid");
    }
    const contentType = entry.contentType ?? "";
    if (entry.kind === "parameter" && !contentType) {
      throw new TypeError("configstore: parameter contract entries require a content type");
    }
    result.push(Object.freeze({ alias: entry.alias, kind: entry.kind, contentType }));
  }
  return Object.freeze(result);
}

/** Build the exact, pre-resolution release manifest validation hook. */
export function createManifestValidator(
  contract: readonly ContractEntry[],
  onObserved?: (manifest: ReleaseManifest) => void,
): ValidateReleaseManifest {
  const entries = validateContract(contract);
  const expected = new Map(entries.map((entry) => [entry.alias, entry] as const));
  return (manifest, signal) => {
    throwIfAborted(signal);
    onObserved?.(manifest);
    const actual = manifest.entries();
    if (actual.size !== expected.size) throw contractMismatch();
    for (const [alias, wanted] of expected) {
      const entry = actual.get(alias);
      if (!entry || entry.alias !== alias || entry.kind !== wanted.kind) throw contractMismatch();
      if ((wanted.contentType ?? "") && entry.contentType !== wanted.contentType) {
        throw contractMismatch();
      }
    }
  };
}

function contractMismatch(): CandidateError {
  return new CandidateError(
    "config_contract_mismatch",
    new Error("configstore: release manifest does not match generated contract"),
  );
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) {
    throw signal.reason ?? new DOMException("Aborted", "AbortError");
  }
}
