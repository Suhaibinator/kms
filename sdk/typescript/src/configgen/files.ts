import { randomUUID } from "node:crypto";
import { mkdir, open, readFile, realpath, rename, stat, unlink } from "node:fs/promises";
import { basename, dirname, join, resolve } from "node:path";

import type { GeneratedArtifacts } from "./artifacts.js";
import { compareText } from "./model.js";

export interface OutputPaths {
  readonly binding: string;
  readonly schema: string;
  readonly contract: string;
}

/** @internal Filesystem-identity guard shared with the public CLI. */
interface NamedPath {
  readonly name: string;
  readonly path: string;
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
  const outputs = await validatedOutputs(paths, artifacts);
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
 *
 * Each destination replacement is atomic. No portable filesystem primitive can
 * atomically replace three arbitrary paths as one transaction, so a process or
 * filesystem failure between renames, or interleaved independent writers, can
 * leave a mixed set. `verifyArtifacts` detects every member that differs from
 * the requested generation; callers must not run concurrent writers.
 */
export async function writeArtifacts(
  paths: OutputPaths,
  artifacts: GeneratedArtifacts,
): Promise<void> {
  const outputs = await validatedOutputs(paths, artifacts);
  const changed: StagedOutput[] = [];
  try {
    for (const output of outputs) {
      const current = await readOptional(output.path);
      if (current?.data === output.data) continue;
      const directory = dirname(output.path);
      await mkdir(directory, { recursive: true, mode: 0o755 });
      const temporary = `${directory}/.kms-config-gen-ts-${process.pid}-${randomUUID()}`;
      const mode = current?.mode ?? 0o644;
      try {
        const handle = await open(temporary, "wx", mode);
        try {
          await handle.writeFile(output.data, "utf8");
          await handle.sync();
          await handle.chmod(mode);
        } finally {
          await handle.close();
        }
      } catch (error) {
        try {
          await unlink(temporary);
        } catch (cleanupError) {
          if (!isNotFound(cleanupError)) void cleanupError;
        }
        throw error;
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

async function validatedOutputs(
  paths: OutputPaths,
  artifacts: GeneratedArtifacts,
): Promise<readonly Output[]> {
  const outputs: Output[] = [
    {
      name: "binding",
      path: resolve(requiredPath(paths.binding, "binding")),
      data: artifacts.binding,
    },
    {
      name: "schema",
      path: resolve(requiredPath(paths.schema, "schema")),
      data: artifacts.schema,
    },
    {
      name: "contract",
      path: resolve(requiredPath(paths.contract, "contract")),
      data: artifacts.contract,
    },
  ];
  await assertDistinctPaths(
    outputs.map((output) => ({ name: `${output.name} output`, path: output.path })),
  );
  return outputs;
}

/**
 * Reject paths that name the same filesystem object or the same not-yet-created
 * entry. Existing symlinks and hardlinks are compared by target identity;
 * missing suffixes are resolved below their deepest canonical existing parent.
 */
/** @internal */
export async function assertDistinctPaths(paths: readonly NamedPath[]): Promise<void> {
  const identities = await Promise.all(
    paths.map(async ({ name, path }) => ({
      name,
      identity: await pathIdentity(requiredPath(path, name)),
    })),
  );
  for (let rightIndex = 1; rightIndex < identities.length; rightIndex += 1) {
    const right = identities[rightIndex];
    if (!right) continue;
    for (let leftIndex = 0; leftIndex < rightIndex; leftIndex += 1) {
      const left = identities[leftIndex];
      if (!left) continue;
      if (samePathIdentity(left.identity, right.identity)) {
        throw new TypeError(
          `configgen: ${left.name} and ${right.name} resolve to the same filesystem object ${JSON.stringify(right.identity.absolute)}`,
        );
      }
    }
  }
}

interface PathIdentity {
  readonly absolute: string;
  readonly canonicalKey: string;
  readonly device?: bigint;
  readonly inode?: bigint;
}

async function pathIdentity(path: string): Promise<PathIdentity> {
  const absolute = resolve(path);
  const suffix: string[] = [];
  let existing = absolute;
  while (true) {
    try {
      const [canonical, info] = await Promise.all([
        realpath(existing),
        stat(existing, { bigint: true }),
      ]);
      if (suffix.length === 0) {
        return {
          absolute,
          canonicalKey: canonical,
          device: info.dev,
          inode: info.ino,
        };
      }
      const caseInsensitive = await isCaseInsensitive(canonical, info.dev, info.ino);
      const unresolved = caseInsensitive
        ? suffix.map((part) => part.normalize("NFC").toLowerCase())
        : suffix;
      return { absolute, canonicalKey: join(canonical, ...unresolved) };
    } catch (error) {
      if (!isMissingPath(error)) throw fileError("resolve path", absolute, error);
      const parent = dirname(existing);
      if (parent === existing) throw fileError("resolve path", absolute, error);
      suffix.unshift(basename(existing));
      existing = parent;
    }
  }
}

async function isCaseInsensitive(path: string, device: bigint, inode: bigint): Promise<boolean> {
  let current = path;
  let currentDevice = device;
  let currentInode = inode;
  while (true) {
    const parent = dirname(current);
    if (parent === current) return process.platform === "win32";
    const name = basename(current);
    const toggled = toggleAsciiCase(name);
    if (toggled !== name) {
      try {
        const alternate = await stat(join(parent, toggled), { bigint: true });
        return alternate.dev === currentDevice && alternate.ino === currentInode;
      } catch (error) {
        if (isMissingPath(error)) return false;
        throw fileError("inspect filesystem case behavior", current, error);
      }
    }
    current = parent;
    const info = await stat(current, { bigint: true });
    currentDevice = info.dev;
    currentInode = info.ino;
  }
}

function toggleAsciiCase(value: string): string {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 65 && code <= 90) {
      return `${value.slice(0, index)}${String.fromCharCode(code + 32)}${value.slice(index + 1)}`;
    }
    if (code >= 97 && code <= 122) {
      return `${value.slice(0, index)}${String.fromCharCode(code - 32)}${value.slice(index + 1)}`;
    }
  }
  return value;
}

function samePathIdentity(left: PathIdentity, right: PathIdentity): boolean {
  if (left.canonicalKey === right.canonicalKey) return true;
  return (
    left.device !== undefined &&
    left.inode !== undefined &&
    right.device !== undefined &&
    right.inode !== undefined &&
    left.device === right.device &&
    left.inode === right.inode
  );
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

function isMissingPath(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    ((error as NodeJS.ErrnoException).code === "ENOENT" ||
      (error as NodeJS.ErrnoException).code === "ENOTDIR")
  );
}

function fileError(action: string, path: string, cause: unknown): Error {
  return new Error(`configgen: ${action}${path ? ` ${JSON.stringify(path)}` : ""}`, { cause });
}
