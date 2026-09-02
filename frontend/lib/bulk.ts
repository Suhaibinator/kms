// Running one destructive action over a selection. There is no bulk endpoint:
// the console calls the same per-item API the row menus call, one at a time, and
// reports what happened to each. A partial result is never silent.

import { countNoun } from "./format";

export interface BulkFailure {
  /** The item's display name — what the operator selected it by. */
  name: string;
  message: string;
}

export interface BulkResult {
  /** Items that succeeded. */
  succeeded: string[];
  failures: BulkFailure[];
}

/** The message an error carries, whatever kind of error it is. */
export function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  return "Something went wrong.";
}

/**
 * Applies `action` to each name in order, collecting failures instead of
 * stopping at the first one — a half-applied selection the operator cannot see
 * is worse than a slow one. `onProgress` receives the number finished so far.
 */
export async function runBulk(
  names: readonly string[],
  action: (name: string) => Promise<unknown>,
  onProgress?: (completed: number) => void,
): Promise<BulkResult> {
  const result: BulkResult = { succeeded: [], failures: [] };
  let completed = 0;
  for (const name of names) {
    try {
      await action(name);
      result.succeeded.push(name);
    } catch (error) {
      result.failures.push({ name, message: errorMessage(error) });
    }
    completed += 1;
    onProgress?.(completed);
  }
  return result;
}

export interface BulkSummary {
  ok: boolean;
  title: string;
  /** Names on success; "name: message" per failure otherwise. */
  detail: string;
}

/**
 * The single toast a run ends with. Failures are named with their own error
 * message, so "3 of 5" never leaves the operator guessing which two.
 */
export function bulkSummary(result: BulkResult, verbPast: string, plural: string): BulkSummary {
  const total = result.succeeded.length + result.failures.length;
  if (result.failures.length === 0) {
    return {
      ok: true,
      title: `${verbPast} ${total} ${countNoun(total, plural)}`,
      detail: result.succeeded.join(", "),
    };
  }
  return {
    ok: false,
    title: `${verbPast} ${result.succeeded.length} of ${total} ${countNoun(total, plural)}`,
    detail: result.failures.map((failure) => `${failure.name}: ${failure.message}`).join(" · "),
  };
}
