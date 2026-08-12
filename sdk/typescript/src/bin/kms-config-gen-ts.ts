#!/usr/bin/env node

import { open, realpath } from "node:fs/promises";
import { fileURLToPath } from "node:url";

import { assertDistinctPaths } from "../configgen/files.js";
import {
  generate,
  parseDescriptor,
  StaleArtifactsError,
  verifyArtifacts,
  writeArtifacts,
} from "../configgen/index.js";

const MAX_DESCRIPTOR_BYTES = 1024 * 1024;
const DESCRIPTOR_READ_CHUNK_BYTES = 64 * 1024;

export interface CliIO {
  readonly stdout: (message: string) => void;
  readonly stderr: (message: string) => void;
}

/** Public CLI entry point, exported to make exit behavior directly testable. */
export async function runCli(
  args: readonly string[],
  io: CliIO = {
    stdout: (message) => process.stdout.write(message),
    stderr: (message) => process.stderr.write(message),
  },
): Promise<number> {
  let flags: Flags;
  try {
    flags = parseFlags(args);
  } catch (error) {
    io.stderr(`${errorMessage(error)}\n`);
    io.stderr(usage());
    return 2;
  }
  if (flags.help) {
    io.stdout(usage());
    return 0;
  }
  try {
    await assertCliPathsDistinct(flags);
    const descriptor = await readDescriptor(flags.descriptor);
    const artifacts = generate(parseDescriptor(descriptor), {
      ...(flags.runtimeImport ? { runtimeImport: flags.runtimeImport } : {}),
      ...(flags.coreImport ? { coreImport: flags.coreImport } : {}),
    });
    const paths = {
      binding: flags.bindingOutput,
      schema: flags.schemaOutput,
      contract: flags.contractOutput,
    };
    if (flags.check) await verifyArtifacts(paths, artifacts);
    else await writeArtifacts(paths, artifacts);
    return 0;
  } catch (error) {
    io.stderr(`${errorMessage(error)}\n`);
    return error instanceof StaleArtifactsError ? 1 : 1;
  }
}

/** @internal Bounded even when stat reports zero or the file grows while open. */
export async function readDescriptor(path: string): Promise<string> {
  const handle = await open(path, "r");
  try {
    const info = await handle.stat({ bigint: true });
    if (info.size > BigInt(MAX_DESCRIPTOR_BYTES)) {
      throw oversizedDescriptor(info.size);
    }

    const chunks: Buffer[] = [];
    let byteLength = 0;
    while (byteLength <= MAX_DESCRIPTOR_BYTES) {
      const remaining = MAX_DESCRIPTOR_BYTES + 1 - byteLength;
      const chunk = Buffer.allocUnsafe(Math.min(DESCRIPTOR_READ_CHUNK_BYTES, remaining));
      const { bytesRead } = await handle.read(chunk, 0, chunk.byteLength, null);
      if (bytesRead === 0) break;
      chunks.push(chunk.subarray(0, bytesRead));
      byteLength += bytesRead;
    }
    if (byteLength > MAX_DESCRIPTOR_BYTES) {
      throw oversizedDescriptor();
    }
    return Buffer.concat(chunks, byteLength).toString("utf8");
  } finally {
    await handle.close();
  }
}

function oversizedDescriptor(size?: bigint): RangeError {
  const measured = size === undefined ? " exceeds" : ` is ${size} bytes;`;
  return new RangeError(
    `configgen: descriptor${measured} maximum is ${MAX_DESCRIPTOR_BYTES} bytes`,
  );
}

interface Flags {
  readonly descriptor: string;
  readonly bindingOutput: string;
  readonly schemaOutput: string;
  readonly contractOutput: string;
  readonly runtimeImport?: string;
  readonly coreImport?: string;
  readonly check: boolean;
  readonly help: boolean;
}

function parseFlags(args: readonly string[]): Flags {
  const values = new Map<string, string>();
  let check = false;
  let help = false;
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (!argument) continue;
    if (argument === "--check" || argument === "--verify") {
      check = true;
      continue;
    }
    if (argument === "--help" || argument === "-h") {
      help = true;
      continue;
    }
    if (!argument.startsWith("--")) {
      throw new TypeError("kms-config-gen-ts: positional arguments are not supported");
    }
    const separator = argument.indexOf("=");
    const name = separator === -1 ? argument : argument.slice(0, separator);
    const inline = separator === -1 ? undefined : argument.slice(separator + 1);
    if (!VALUE_FLAGS.has(name)) throw new TypeError(`kms-config-gen-ts: unknown option ${name}`);
    if (values.has(name)) throw new TypeError(`kms-config-gen-ts: duplicate option ${name}`);
    const value = inline ?? args[index + 1];
    if (inline === undefined) index += 1;
    if (!value || (inline === undefined && value.startsWith("--"))) {
      throw new TypeError(`kms-config-gen-ts: ${name} requires a value`);
    }
    values.set(name, value);
  }
  if (help) {
    return {
      descriptor: "",
      bindingOutput: "",
      schemaOutput: "",
      contractOutput: "",
      check,
      help,
    };
  }
  return {
    descriptor: required(values, "--descriptor"),
    bindingOutput: required(values, "--binding-output"),
    schemaOutput: required(values, "--schema-output"),
    contractOutput: required(values, "--contract-output"),
    ...(values.get("--runtime-import")
      ? { runtimeImport: values.get("--runtime-import") as string }
      : {}),
    ...(values.get("--core-import") ? { coreImport: values.get("--core-import") as string } : {}),
    check,
    help,
  };
}

const VALUE_FLAGS = new Set([
  "--descriptor",
  "--binding-output",
  "--schema-output",
  "--contract-output",
  "--runtime-import",
  "--core-import",
]);

function required(values: ReadonlyMap<string, string>, name: string): string {
  const value = values.get(name);
  if (!value) throw new TypeError(`kms-config-gen-ts: ${name} is required`);
  return value;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "configgen: generation failed";
}

async function assertCliPathsDistinct(flags: Flags): Promise<void> {
  await assertDistinctPaths([
    { name: "descriptor", path: flags.descriptor },
    { name: "binding output", path: flags.bindingOutput },
    { name: "schema output", path: flags.schemaOutput },
    { name: "contract output", path: flags.contractOutput },
  ]);
}

function usage(): string {
  return `Usage: kms-config-gen-ts \\
  --descriptor <config.kms.json> \\
  --binding-output <config.generated.ts> \\
  --schema-output <runtime.schema.json> \\
  --contract-output <runtime.contract.json> [options]\n\nOptions:\n  --runtime-import <specifier>  Generated configstore import\n  --core-import <specifier>     Generated core SDK import\n  --check, --verify             Compare outputs without writing\n  --help                        Show this help\n`;
}

/** @internal Exported for executable-boundary regression tests. */
export async function isExecutedEntry(entry: string | undefined): Promise<boolean> {
  if (!entry) return false;
  try {
    return (await realpath(entry)) === (await realpath(fileURLToPath(import.meta.url)));
  } catch {
    return false;
  }
}

if (await isExecutedEntry(process.argv[1])) {
  process.exitCode = await runCli(process.argv.slice(2));
}
