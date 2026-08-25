import { randomUUID } from "node:crypto";
import { mkdir, open, rename, stat, unlink } from "node:fs/promises";
import { dirname, resolve } from "node:path";

import {
  parseDefaultsArtifact,
  validateDefaultsProfile,
} from "../configstore/defaults-artifact.js";

const MAX_ERROR_BYTES = 512;

export interface DefaultsExporterIO {
  readonly stdout: (message: string) => void;
  readonly stderr: (message: string) => void;
}

export type DefaultsProvider<T, P extends string = string> = (profile: P) => T | Promise<T>;
export type DefaultsEncoder<T, P extends string = string> = (
  profile: P,
  defaults: T,
) => string | Promise<string>;

/**
 * Run a complete defaults exporter. Applications only supply their existing
 * profile provider and generated encoder.
 */
export async function runDefaultsExporter<P extends string, T>(
  args: readonly string[],
  provider: DefaultsProvider<T, P>,
  encoder: DefaultsEncoder<T, P>,
  io: DefaultsExporterIO = {
    stdout: (message) => process.stdout.write(message),
    stderr: (message) => process.stderr.write(message),
  },
): Promise<number> {
  let flags: ExporterFlags;
  try {
    flags = parseExporterFlags(args);
  } catch (error) {
    io.stderr(`${boundedMessage(error, "defaults exporter: invalid arguments")}\n`);
    io.stderr(exporterUsage());
    return 2;
  }
  if (flags.help) {
    io.stdout(exporterUsage());
    return 0;
  }
  if (typeof provider !== "function" || typeof encoder !== "function") {
    io.stderr("defaults exporter: provider and encoder are required\n");
    return 2;
  }

  let defaults: T;
  const profile = flags.profile as P;
  try {
    defaults = await provider(profile);
    if (defaults === null || defaults === undefined) {
      throw new TypeError("provider returned no defaults");
    }
  } catch {
    io.stderr("defaults exporter: loading profile defaults failed\n");
    return 1;
  }

  let artifact: string;
  try {
    artifact = await encoder(profile, defaults);
    if (typeof artifact !== "string") throw new TypeError("encoder returned a non-string");
    const parsed = parseDefaultsArtifact(artifact);
    if (parsed.profile !== profile) throw new TypeError("encoded profile does not match");
  } catch {
    io.stderr("defaults exporter: encoding defaults artifact failed\n");
    return 1;
  }

  try {
    if (flags.output === "-") io.stdout(artifact);
    else await writeArtifact(flags.output, artifact);
    return 0;
  } catch {
    io.stderr("defaults exporter: writing defaults artifact failed\n");
    return 1;
  }
}

interface ExporterFlags {
  readonly profile: string;
  readonly output: string;
  readonly help: boolean;
}

function parseExporterFlags(args: readonly string[]): ExporterFlags {
  const values = new Map<string, string>();
  let help = false;
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (!argument) continue;
    if (argument === "--help" || argument === "-h") {
      help = true;
      continue;
    }
    if (!argument.startsWith("--")) {
      throw new TypeError("defaults exporter: positional arguments are not supported");
    }
    const separator = argument.indexOf("=");
    const name = separator === -1 ? argument : argument.slice(0, separator);
    const inline = separator === -1 ? undefined : argument.slice(separator + 1);
    if (name !== "--profile" && name !== "--output") {
      throw new TypeError(`defaults exporter: unknown option ${boundedOption(name)}`);
    }
    if (values.has(name)) throw new TypeError(`defaults exporter: duplicate option ${name}`);
    const value = inline ?? args[index + 1];
    if (inline === undefined) index += 1;
    if (!value || (inline === undefined && value.startsWith("--"))) {
      throw new TypeError(`defaults exporter: ${name} requires a value`);
    }
    values.set(name, value);
  }
  if (help) return { profile: "", output: "", help: true };
  const profile = values.get("--profile");
  if (!profile) throw new TypeError("defaults exporter: --profile is required");
  validateDefaultsProfile(profile);
  const output = values.get("--output") ?? "-";
  if (output.trim() !== output) {
    throw new TypeError("defaults exporter: --output must be canonical");
  }
  return { profile, output, help: false };
}

async function writeArtifact(path: string, artifact: string): Promise<void> {
  const destination = resolve(path);
  const directory = dirname(destination);
  await mkdir(directory, { recursive: true, mode: 0o755 });
  const temporary = `${directory}/.kms-defaults-${process.pid}-${randomUUID()}`;
  let mode = 0o600;
  try {
    mode = (await stat(destination)).mode & 0o777;
  } catch (error) {
    if (!isNotFound(error)) throw error;
  }
  let committed = false;
  try {
    const handle = await open(temporary, "wx", mode);
    try {
      await handle.writeFile(artifact, "utf8");
      await handle.sync();
    } finally {
      await handle.close();
    }
    await rename(temporary, destination);
    committed = true;
  } finally {
    if (!committed) {
      try {
        await unlink(temporary);
      } catch {
        // Preserve the primary write error.
      }
    }
  }
}

function boundedMessage(error: unknown, fallback: string): string {
  const message = error instanceof Error ? error.message : fallback;
  return Buffer.from(message.replace(/[\r\n\t]/gu, " "), "utf8")
    .subarray(0, MAX_ERROR_BYTES)
    .toString("utf8");
}

function boundedOption(option: string): string {
  return JSON.stringify(option.slice(0, 80));
}

function isNotFound(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    (error as { code?: unknown }).code === "ENOENT"
  );
}

function exporterUsage(): string {
  return `Usage: defaults-exporter --profile <profile> [--output <file|->]\n\nOptions:\n  --profile <profile>  Application defaults profile\n  --output <file|->    Artifact destination; defaults to stdout\n  --help               Show this help\n`;
}
