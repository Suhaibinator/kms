import { spawn } from "node:child_process";
import { mkdtemp, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const sdkDirectory = dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const temporaryDirectory = await mkdtemp(join(tmpdir(), "kms-typescript-minimum-"));

try {
  const packed = await command(
    "npm",
    ["pack", "--ignore-scripts", "--pack-destination", temporaryDirectory],
    sdkDirectory,
  );
  if (packed.code !== 0) fail("SDK package failed to pack for TypeScript 5.2", packed.output);
  const archives = (await readdir(temporaryDirectory)).filter((entry) => entry.endsWith(".tgz"));
  const archive = archives[0];
  if (archives.length !== 1 || archive === undefined) {
    throw new Error(`expected one packed SDK archive, found ${archives.length}`);
  }

  await writeFile(
    resolve(temporaryDirectory, "package.json"),
    `${JSON.stringify(
      {
        private: true,
        type: "module",
        dependencies: {
          "@suhaibinator/kms": `file:./${archive}`,
          react: "18.3.1",
        },
        devDependencies: {
          "@types/node": "20.14.12",
          "@types/react": "18.3.31",
          typescript: "5.2.2",
        },
      },
      null,
      2,
    )}\n`,
  );
  await writeFile(
    resolve(temporaryDirectory, "consumer.ts"),
    `import { createClient, formatRevision, type WatchStatus } from "@suhaibinator/kms";
import { codecs, type ValueCodec } from "@suhaibinator/kms/configstore";
import { generate, type ConfigDescriptor } from "@suhaibinator/kms/configgen";
import { createNextKms } from "@suhaibinator/kms/next/server";
import { usePublicConfig } from "@suhaibinator/kms/next/client";

declare const status: WatchStatus;
const revision: string = formatRevision(status.currentRevision);
const bytes: ValueCodec<Uint8Array | null> = codecs.bytes;
void [createClient, generate, createNextKms, usePublicConfig, revision, bytes];
const descriptor: ConfigDescriptor | undefined = undefined;
void descriptor;
`,
  );
  await writeFile(
    resolve(temporaryDirectory, "tsconfig.json"),
    `${JSON.stringify(
      {
        compilerOptions: {
          target: "ES2022",
          module: "NodeNext",
          moduleResolution: "NodeNext",
          strict: true,
          noEmit: true,
          skipLibCheck: false,
          types: ["node", "react"],
        },
        include: ["consumer.ts"],
      },
      null,
      2,
    )}\n`,
  );

  const install = await command("npm", ["install", "--no-audit", "--no-fund"]);
  if (install.code !== 0)
    fail("TypeScript 5.2 consumer dependencies failed to install", install.output);
  const compile = await command(process.execPath, [
    resolve(temporaryDirectory, "node_modules/typescript/bin/tsc"),
    "-p",
    "tsconfig.json",
    "--pretty",
    "false",
  ]);
  if (compile.code !== 0) fail("TypeScript 5.2 rejected the packed declarations", compile.output);
} finally {
  await rm(temporaryDirectory, { recursive: true, force: true });
}

function command(executable, arguments_, cwd = temporaryDirectory) {
  return new Promise((resolveCommand, rejectCommand) => {
    const child = spawn(executable, arguments_, {
      cwd,
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let output = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      output += chunk;
    });
    child.stderr.on("data", (chunk) => {
      output += chunk;
    });
    child.once("error", rejectCommand);
    child.once("close", (code) => resolveCommand({ code: code ?? 1, output }));
  });
}

function fail(message, output) {
  process.stderr.write(output);
  throw new Error(message);
}
