import { randomUUID } from "node:crypto";
import { mkdir, open, readFile, rename, stat, unlink } from "node:fs/promises";
import { dirname, resolve } from "node:path";

import type { GeneratedArtifacts } from "./artifacts.js";
import { compareText } from "./model.js";

export interface OutputPaths {
  readonly binding: string;
  readonly schema: string;
  readonly contract: string;
}

export class StaleArtifactsError extends Error {
  readonly paths: readonly string[];

  constructor(paths: readonly string[]) {
    const sorted = Object.freeze([...paths].sort(compareText));
    super(`configgen: generated configuration artifacts are stale: ${sorted.join(", ")}`);
    this.name = "StaleArtifactsError";
    this.paths = sorted;
  }
}

/** Compare all outputs without modifying files, directories, or timestamps. */
export async function verifyArtifacts(
  paths: OutputPaths,
  artifacts: GeneratedArtifacts,
): Promise<void> {
  const outputs = validatedOutputs(paths, artifacts);
  const stale: string[] = [];
  for (const output of outputs) {
    try {
      const current = await readFile(output.path, "utf8");
      if (current !== output.data) stale.push(output.path);
    } catch (error) {
      if (isNotFound(error)) stale.push(output.path);
      else throw fileError(`read ${output.name} output`, output.path, error);
    }
  }
  if (stale.length > 0) throw new StaleArtifactsError(stale);
}

/**
 * Stage every changed output beside its destination, fsync it, then atomically
 * replace each destination. Matching files are not touched. A staging failure
 * cannot change an existing artifact.
 */
export async function writeArtifacts(
  paths: OutputPaths,
  artifacts: GeneratedArtifacts,
): Promise<void> {
  const outputs = validatedOutputs(paths, artifacts);
  const changed: StagedOutput[] = [];
  try {
    for (const output of outputs) {
      const current = await readOptional(output.path);
      if (current?.data === output.data) continue;
      const directory = dirname(output.path);
      await mkdir(directory, { recursive: true, mode: 0o755 });
      const temporary = `${directory}/.kms-config-gen-ts-${process.pid}-${randomUUID()}`;
      const mode = current?.mode ?? 0o644;
      const handle = await open(temporary, "wx", mode);
      try {
        await handle.writeFile(output.data, "utf8");
        await handle.sync();
        await handle.chmod(mode);
      } finally {
        await handle.close();
      }
      changed.push({ ...output, temporary });
    }
    for (const output of changed) {
      await rename(output.temporary, output.path);
      output.committed = true;
    }
  } catch (error) {
    throw fileError("write generated output", firstUncommitted(changed)?.path ?? "", error);
  } finally {
    await Promise.all(
      changed
        .filter((output) => !output.committed)
        .map(async (output) => {
          try {
            await unlink(output.temporary);
          } catch (error) {
            if (!isNotFound(error)) void error;
          }
        }),
    );
  }
}

interface Output {
  readonly name: "binding" | "schema" | "contract";
  readonly path: string;
  readonly data: string;
}

interface StagedOutput extends Output {
  readonly temporary: string;
  committed?: boolean;
}

function validatedOutputs(paths: OutputPaths, artifacts: GeneratedArtifacts): readonly Output[] {
  const outputs: Output[] = [
    { name: "binding", path: requiredPath(paths.binding, "binding"), data: artifacts.binding },
    { name: "schema", path: requiredPath(paths.schema, "schema"), data: artifacts.schema },
    { name: "contract", path: requiredPath(paths.contract, "contract"), data: artifacts.contract },
  ];
  const seen = new Map<string, string>();
  for (const output of outputs) {
    const absolute = resolve(output.path);
    const previous = seen.get(absolute);
    if (previous) {
      throw new TypeError(
        `configgen: ${previous} and ${output.name} outputs resolve to the same path ${JSON.stringify(absolute)}`,
      );
    }
    seen.set(absolute, output.name);
  }
  return outputs;
}

function requiredPath(value: string, name: string): string {
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new TypeError(`configgen: ${name} output path is required`);
  }
  return value;
}

async function readOptional(
  path: string,
): Promise<{ readonly data: string; readonly mode: number } | undefined> {
  try {
    const [data, info] = await Promise.all([readFile(path, "utf8"), stat(path)]);
    return { data, mode: info.mode & 0o777 };
  } catch (error) {
    if (isNotFound(error)) return undefined;
    throw fileError("read generated output", path, error);
  }
}

function firstUncommitted(outputs: readonly StagedOutput[]): StagedOutput | undefined {
  return outputs.find((output) => !output.committed);
}

function isNotFound(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    (error as NodeJS.ErrnoException).code === "ENOENT"
  );
}

function fileError(action: string, path: string, cause: unknown): Error {
  return new Error(`configgen: ${action}${path ? ` ${JSON.stringify(path)}` : ""}`, { cause });
}
