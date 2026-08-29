import type { AppliedReport, CandidateRejectionReport, DefaultMismatchReport } from "./errors.js";
import type { Callbacks } from "./manager.js";

/** Minimal structured logger accepted by consoleCallbacks (console, pino, winston adapters). */
export interface ConsoleLogger {
  info(message: string, attrs?: Record<string, unknown>): void;
  error(message: string, attrs?: Record<string, unknown>): void;
}

export interface ConsoleCallbacksOptions {
  /** Attached as `component` to every record when set. */
  readonly component?: string;
  /** Log one `kms config group` record per parameter group after the startup generation. Default true. */
  readonly startupSnapshot?: boolean;
  /** Log one `kms config field changed` record per changed field after a reload. Default true. */
  readonly reloadChanges?: boolean;
}

/**
 * Ready-made managed-store callbacks that render every report as structured
 * log records. Mirrors the Go SDK's SlogCallbacks: messages and attribute keys
 * are fixed and never carry secret values; parameter group documents are the
 * canonical non-secret encodings supplied by the generated binding. Logger
 * failures are isolated so they can never affect candidate admission.
 */
export function consoleCallbacks(
  logger: ConsoleLogger,
  options: ConsoleCallbacksOptions = {},
): Callbacks {
  if (!logger || typeof logger.info !== "function" || typeof logger.error !== "function") {
    throw new TypeError("configstore: consoleCallbacks requires a logger with info and error");
  }
  const component = options.component;
  const startupSnapshot = options.startupSnapshot ?? true;
  const reloadChanges = options.reloadChanges ?? true;
  const base = (): Record<string, unknown> => (component ? { component } : {});
  const releaseAttrs = (
    report: AppliedReport | DefaultMismatchReport,
  ): Record<string, unknown> => ({
    ...base(),
    phase: report.phase,
    release: String(report.release),
    release_version: report.release.version.toString(),
    activation_revision: report.release.activationRevision.toString(),
  });
  const info = (message: string, attrs: Record<string, unknown>): void => {
    try {
      logger.info(message, attrs);
    } catch {
      // Logging must never affect configuration lifecycle.
    }
  };
  const error = (message: string, attrs: Record<string, unknown>): void => {
    try {
      logger.error(message, attrs);
    } catch {
      // Logging must never affect configuration lifecycle.
    }
  };

  return Object.freeze({
    onDefaultMismatch(report: DefaultMismatchReport): void {
      error("kms config diverges from source defaults", {
        ...base(),
        phase: report.phase,
        release: String(report.release),
        fields: report.fields().map(({ path }) => path),
      });
    },
    onApplied(report: AppliedReport): void {
      if (report.phase === "startup") {
        info("kms config applied", {
          ...releaseAttrs(report),
          default_divergent: report.defaultDivergent,
        });
        if (!startupSnapshot) return;
        const groups = report.groups();
        for (const alias of Object.keys(groups).sort()) {
          info("kms config group", {
            ...base(),
            alias,
            values: parseDocument(groups[alias] ?? ""),
            release_version: report.release.version.toString(),
            activation_revision: report.release.activationRevision.toString(),
          });
        }
        return;
      }
      const changed = report.changed();
      info("kms config reloaded", {
        ...releaseAttrs(report),
        default_divergent: report.defaultDivergent,
        changed_count: changed.length,
      });
      if (!reloadChanges) return;
      for (const change of changed) {
        info("kms config field changed", {
          ...base(),
          path: change.path,
          previous: change.previous,
          current: change.current,
        });
      }
    },
    onCandidateRejected(report: CandidateRejectionReport): void {
      error("kms config candidate rejected", {
        ...base(),
        category: report.category,
        release: String(report.release),
        paths: report.paths(),
      });
    },
  });
}

function parseDocument(document: string): unknown {
  try {
    return JSON.parse(document);
  } catch {
    return document;
  }
}
