import { inspect } from "node:util";

import { ClassifiedReleaseError } from "../releases/types.js";
import { cloneConfig, containsSecret } from "./clone.js";
import { configDecodeErrorPath } from "./codecs.js";
import type { ReleaseIdentity } from "./snapshot.js";

export const REJECTION_CATEGORIES = [
  "config_contract_mismatch",
  "config_decode_failed",
  "config_validation_failed",
  "default_mismatch",
  "restart_required",
  "internal",
] as const;
export type RejectionCategory = (typeof REJECTION_CATEGORIES)[number];

/** Whether a candidate was the initial generation or a reload. */
export type Phase = "startup" | "runtime";
/** Retained for callback code written against earlier SDK versions; identical to Phase. */
export type MismatchPhase = Phase;
/**
 * Default mismatches are applied and reported rather than refused, so the only
 * severity is "error". The type is retained so existing callback code compiles.
 */
export type MismatchSeverity = "error";

export interface FieldDifference {
  readonly path: string;
  readonly expected: unknown;
  readonly actual: unknown;
}

/**
 * One field that differs between the previously applied generation and a
 * newly applied one. Secret rotations are reported path-only with null
 * previous and current values.
 */
export interface FieldChange {
  readonly path: string;
  readonly previous: unknown;
  readonly current: unknown;
}

const candidatePaths = new WeakMap<CandidateError, readonly string[]>();

/** Classified candidate error with a deliberately redacted rendering surface. */
export class CandidateError extends ClassifiedReleaseError {
  readonly category: RejectionCategory;

  constructor(category: RejectionCategory, cause?: unknown, paths: readonly string[] = []) {
    const normalized = validRejectionCategory(category) ? category : "internal";
    super(normalized, `configstore: candidate rejected (${normalized})`, { cause });
    this.name = "CandidateError";
    this.category = normalized;
    candidatePaths.set(this, Object.freeze(sanitizeDiagnosticPaths(paths)));
  }

  override toString(): string {
    return `${this.name}: ${this.message}`;
  }

  toJSON(): Readonly<{ category: RejectionCategory }> {
    return Object.freeze({ category: this.category });
  }

  [inspect.custom](): string {
    return this.toString();
  }
}

/** Classify an application/runtime preparation error. */
export function reject(category: RejectionCategory, cause?: unknown): CandidateError {
  return new CandidateError(category, cause ?? new Error("configstore: candidate rejected"));
}

/** Classify a strict group decode error and translate only its safe generated path. */
export function rejectDecode(group: string, cause: unknown): CandidateError {
  const paths: string[] = [];
  if (validDiagnosticSegment(group)) {
    const path = configDecodeErrorPath(cause);
    if (path === "$") paths.push(group);
    else if (path?.startsWith("$.")) paths.push(`${group}${path.slice(1)}`);
  }
  return new CandidateError("config_decode_failed", cause, paths);
}

/** @internal Manager-only defensive diagnostic path copy. */
export function candidateErrorPaths(error: CandidateError): readonly string[] {
  return [...(candidatePaths.get(error) ?? [])];
}

/** Immutable, secret-aware default comparison report. */
export class DefaultMismatchReport {
  readonly phase: MismatchPhase;
  readonly severity: MismatchSeverity;
  readonly release: ReleaseIdentity;
  readonly #differences: readonly FieldDifference[];

  constructor(
    phase: MismatchPhase,
    severity: MismatchSeverity,
    release: ReleaseIdentity,
    differences: readonly FieldDifference[],
  ) {
    this.phase = phase;
    this.severity = severity;
    this.release = release;
    this.#differences = Object.freeze(cloneDifferences(differences));
    Object.freeze(this);
  }

  fields(): readonly FieldDifference[] {
    return cloneDifferences(this.#differences);
  }

  toString(): string {
    return `configstore: default mismatch (${this.phase}/${this.severity}) for ${this.release} fields=${this.#differences.map(({ path }) => path).join(",")}`;
  }

  toJSON(): Readonly<Record<string, unknown>> {
    return Object.freeze({
      phase: this.phase,
      severity: this.severity,
      release: this.release,
      differences: this.#differences.map(jsonDifference),
    });
  }

  [inspect.custom](): string {
    return this.toString();
  }
}

/**
 * Immutable view of one published generation. `changed()` and `groups()`
 * return fresh copies on every call; `groups()` is empty when the generated
 * binding did not supply parameter group documents.
 */
export class AppliedReport {
  readonly phase: Phase;
  readonly release: ReleaseIdentity;
  readonly defaultDivergent: boolean;
  readonly #changes: readonly FieldChange[];
  readonly #groups: Readonly<Record<string, string>>;

  constructor(
    phase: Phase,
    release: ReleaseIdentity,
    defaultDivergent: boolean,
    changes: readonly FieldChange[] = [],
    groups: Readonly<Record<string, string>> = {},
  ) {
    this.phase = phase;
    this.release = release;
    this.defaultDivergent = defaultDivergent;
    this.#changes = Object.freeze(cloneChanges(changes));
    this.#groups = Object.freeze(copyGroups(groups));
    Object.freeze(this);
  }

  /** Fields that differ from the previously applied generation; empty at startup. */
  changed(): FieldChange[] {
    return cloneChanges(this.#changes);
  }

  /** Canonical non-secret parameter group documents keyed by alias. */
  groups(): Readonly<Record<string, string>> {
    return Object.freeze(copyGroups(this.#groups));
  }

  /** Lists only canonical field paths; values remain available through changed(). */
  toString(): string {
    return `configstore: applied (${this.phase}) ${this.release} divergent=${this.defaultDivergent} changed=${this.#changes.map(({ path }) => path).join(",")}`;
  }

  toJSON(): Readonly<Record<string, unknown>> {
    return Object.freeze({
      phase: this.phase,
      release: this.release,
      defaultDivergent: this.defaultDivergent,
      changed: this.#changes.map(jsonChange),
    });
  }

  [inspect.custom](): string {
    return this.toString();
  }
}

/** Value-free local report for a classified candidate rejection. */
export class CandidateRejectionReport {
  readonly category: RejectionCategory;
  readonly release: ReleaseIdentity;
  readonly #paths: readonly string[];

  constructor(
    category: RejectionCategory,
    release: ReleaseIdentity,
    paths: readonly string[] = [],
  ) {
    this.category = validRejectionCategory(category) ? category : "internal";
    this.release = release;
    this.#paths = Object.freeze(sanitizeDiagnosticPaths(paths));
    Object.freeze(this);
  }

  paths(): readonly string[] {
    return [...this.#paths];
  }

  toString(): string {
    return `configstore: candidate rejection (${this.category}) for ${this.release} fields=${this.#paths.join(",")}`;
  }

  toJSON(): Readonly<Record<string, unknown>> {
    return Object.freeze({ category: this.category, release: this.release, paths: this.paths() });
  }

  [inspect.custom](): string {
    return this.toString();
  }
}

function cloneDifferences(differences: readonly FieldDifference[]): FieldDifference[] {
  return differences.map((difference) => ({
    path: validDiagnosticPath(difference.path) ? difference.path : "invalid_path",
    expected: cloneReportValue(difference.expected),
    actual: cloneReportValue(difference.actual),
  }));
}

function cloneChanges(changes: readonly FieldChange[]): FieldChange[] {
  return changes.map((change) => ({
    path: validDiagnosticPath(change.path) ? change.path : "invalid_path",
    previous: cloneReportValue(change.previous),
    current: cloneReportValue(change.current),
  }));
}

function copyGroups(groups: Readonly<Record<string, string>>): Record<string, string> {
  const result: Record<string, string> = Object.create(null) as Record<string, string>;
  for (const alias of Object.keys(groups).sort()) {
    const document = groups[alias];
    if (typeof document === "string") result[alias] = document;
  }
  return result;
}

function cloneReportValue(value: unknown): unknown {
  return containsSecret(value) ? "[REDACTED]" : cloneConfig(value);
}

function jsonChange(change: FieldChange): FieldChange {
  return {
    path: change.path,
    previous: jsonReportValue(change.previous, new Set<object>()),
    current: jsonReportValue(change.current, new Set<object>()),
  };
}

function jsonDifference(difference: FieldDifference): FieldDifference {
  return {
    path: difference.path,
    expected: jsonReportValue(difference.expected, new Set<object>()),
    actual: jsonReportValue(difference.actual, new Set<object>()),
  };
}

/** Convert cloned report values without invoking caller-provided toJSON methods. */
function jsonReportValue(value: unknown, ancestors: Set<object>): unknown {
  if (typeof value === "bigint") return value.toString();
  if (typeof value === "function" || typeof value === "symbol") return undefined;
  if (value === null || typeof value !== "object") return value;
  if (ancestors.has(value)) return "[Circular]";

  ancestors.add(value);
  try {
    if (value instanceof Date) {
      const milliseconds = value.getTime();
      return Number.isFinite(milliseconds) ? value.toISOString() : null;
    }
    if (value instanceof Uint8Array) return [...value];
    if (value instanceof Map || value instanceof Set) return {};
    if (Array.isArray(value)) {
      return Array.from({ length: value.length }, (_, index) => {
        const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
        return descriptor && "value" in descriptor
          ? jsonReportValue(descriptor.value, ancestors)
          : undefined;
      });
    }

    const result: Record<string, unknown> = Object.create(null) as Record<string, unknown>;
    for (const key of Object.keys(value)) {
      const descriptor = Object.getOwnPropertyDescriptor(value, key);
      if (!descriptor || !("value" in descriptor)) continue;
      result[key] = jsonReportValue(descriptor.value, ancestors);
    }
    return result;
  } finally {
    ancestors.delete(value);
  }
}

function validRejectionCategory(category: string): category is RejectionCategory {
  return (REJECTION_CATEGORIES as readonly string[]).includes(category);
}

export function sanitizeDiagnosticPaths(paths: readonly string[]): string[] {
  const result: string[] = [];
  const seen = new Set<string>();
  for (const path of paths) {
    if (!validDiagnosticPath(path) || seen.has(path)) continue;
    result.push(path);
    seen.add(path);
  }
  return result;
}

export function validDiagnosticPath(path: string): boolean {
  if (!path || path.length > 512) return false;
  const segments = path.split(".");
  if (segments.length > 32) return false;
  return segments.every((segment) => {
    let base = segment;
    while (base.endsWith("[]") || base.endsWith("[*]")) {
      base = base.endsWith("[]") ? base.slice(0, -2) : base.slice(0, -3);
    }
    return validDiagnosticSegment(base);
  });
}

export function validDiagnosticSegment(segment: string): boolean {
  return /^[A-Za-z][A-Za-z0-9_-]{0,63}$/u.test(segment);
}
