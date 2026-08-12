import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { appendFile, mkdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const sdkDir = fileURLToPath(new URL("..", import.meta.url));
const protoDir = fileURLToPath(new URL("../../../proto/kms/v1", import.meta.url));
const protoFile = fileURLToPath(new URL("../../../proto/kms/v1/kms.proto", import.meta.url));
const outputDir = fileURLToPath(new URL("../src/generated", import.meta.url));
const generatedFile = new URL("../src/generated/kms.ts", import.meta.url);
const plugin = fileURLToPath(new URL("../node_modules/.bin/protoc-gen-ts_proto", import.meta.url));

await mkdir(outputDir, { recursive: true });

const result = spawnSync(
  "protoc",
  [
    `--plugin=protoc-gen-ts_proto=${plugin}`,
    `--proto_path=${protoDir}`,
    `--ts_proto_out=${outputDir}`,
    "--ts_proto_opt=env=node,outputServices=grpc-js,outputClientImpl=false,useExactTypes=false,oneof=unions-value,forceLong=bigint,esModuleInterop=true,importSuffix=.js",
    protoFile,
  ],
  { cwd: sdkDir, encoding: "utf8", stdio: "inherit" },
);

if (result.error) throw result.error;
if (result.status !== 0) process.exit(result.status ?? 1);

const source = await readFile(protoFile);
const digest = createHash("sha256").update(source).digest("hex");
await appendFile(generatedFile, `\n// source-sha256: ${digest}\n`);
