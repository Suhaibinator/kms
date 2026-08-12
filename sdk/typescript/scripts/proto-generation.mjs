import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { appendFile, mkdir, readFile } from "node:fs/promises";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const sdkDirectory = fileURLToPath(new URL("..", import.meta.url));
export const protoDirectory = fileURLToPath(new URL("../../../proto/kms/v1", import.meta.url));
export const protoFile = fileURLToPath(new URL("../../../proto/kms/v1/kms.proto", import.meta.url));
export const trackedGeneratedFile = fileURLToPath(
  new URL("../src/generated/kms.ts", import.meta.url),
);

const protocLauncher = fileURLToPath(new URL("../node_modules/protoc/protoc.cjs", import.meta.url));
const protocPackage = fileURLToPath(
  new URL("../node_modules/protoc/package.json", import.meta.url),
);
const tsProtoPlugin = fileURLToPath(
  new URL(
    `../node_modules/.bin/protoc-gen-ts_proto${process.platform === "win32" ? ".cmd" : ""}`,
    import.meta.url,
  ),
);
const tsProtoPackage = fileURLToPath(
  new URL("../node_modules/ts-proto/package.json", import.meta.url),
);

const tsProtoOptions = Object.freeze([
  "env=node",
  "outputServices=grpc-js",
  "outputClientImpl=false",
  "useExactTypes=false",
  "oneof=unions-value",
  "forceLong=bigint",
  "esModuleInterop=true",
  "importSuffix=.js",
]);

async function readPackageVersion(path) {
  const parsed = JSON.parse(await readFile(path, "utf8"));
  if (typeof parsed.version !== "string" || parsed.version.length === 0) {
    throw new Error(`protobuf generator package has no version: ${path}`);
  }
  return parsed.version;
}

/**
 * Generate the TypeScript protobuf binding into an explicit output directory.
 *
 * Both the compiler and plugin are resolved from the package lock. All input,
 * include, output, plugin, and option paths are explicit, so neither a system
 * `protoc` nor user-level configuration can affect the result.
 */
export async function generateProtobuf(outputDirectory) {
  await mkdir(outputDirectory, { recursive: true });

  const [protocVersion, tsProtoVersion] = await Promise.all([
    readPackageVersion(protocPackage),
    readPackageVersion(tsProtoPackage),
  ]);
  const canonicalGenerationConfig = {
    compiler: { package: "protoc", version: protocVersion },
    plugin: { package: "ts-proto", version: tsProtoVersion },
    template: {
      input: "kms.proto",
      output: "kms.ts",
      plugin: "ts-proto",
      options: tsProtoOptions,
    },
  };
  const childEnvironment = { ...process.env };
  const pathKey =
    Object.keys(childEnvironment).find((key) => key.toLowerCase() === "path") ?? "PATH";
  childEnvironment[pathKey] = [dirname(process.execPath), childEnvironment[pathKey] ?? ""].join(
    delimiter,
  );

  const result = spawnSync(
    process.execPath,
    [
      protocLauncher,
      `--plugin=protoc-gen-ts_proto=${tsProtoPlugin}`,
      `--proto_path=${protoDirectory}`,
      `--ts_proto_out=${outputDirectory}`,
      `--ts_proto_opt=${tsProtoOptions.join(",")}`,
      protoFile,
    ],
    {
      cwd: sdkDirectory,
      encoding: "utf8",
      env: childEnvironment,
      stdio: "inherit",
    },
  );

  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`protobuf generation failed with exit code ${result.status ?? "unknown"}`);
  }

  const generatedFile = join(outputDirectory, "kms.ts");
  const source = await readFile(protoFile);
  const sourceDigest = createHash("sha256").update(source).digest("hex");
  const generationDigest = createHash("sha256")
    .update(JSON.stringify(canonicalGenerationConfig))
    .digest("hex");
  await appendFile(
    generatedFile,
    `\n// source-sha256: ${sourceDigest}\n// generation-sha256: ${generationDigest}\n`,
  );

  return generatedFile;
}
