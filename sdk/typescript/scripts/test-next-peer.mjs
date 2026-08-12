import { spawn } from "node:child_process";
import { cp, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const [nextVersion, reactVersion] = process.argv.slice(2);
if (!/^\d+\.\d+\.\d+$/.test(nextVersion ?? "") || !/^\d+\.\d+\.\d+$/.test(reactVersion ?? "")) {
  throw new TypeError("usage: test-next-peer.mjs <exact-next-version> <exact-react-version>");
}

const sdkDirectory = dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const temporaryDirectory = await mkdtemp(join(tmpdir(), "kms-next-peer-"));
const validFixture = resolve(temporaryDirectory, "valid");
const invalidFixture = resolve(temporaryDirectory, "invalid");
const reactMajor = Number.parseInt(reactVersion, 10);
const reactTypes = reactMajor === 18 ? "18.3.27" : "19.1.17";
const reactDomTypes = reactMajor === 18 ? "18.3.7" : "19.1.7";

try {
  await Promise.all([
    cp(resolve(sdkDirectory, "tests/fixtures/next-boundary"), validFixture, {
      recursive: true,
      filter: (source) => !source.endsWith("/.next") && !source.endsWith("/next-env.d.ts"),
    }),
    cp(resolve(sdkDirectory, "tests/fixtures/next-invalid-client-import"), invalidFixture, {
      recursive: true,
      filter: (source) => !source.endsWith("/.next") && !source.endsWith("/next-env.d.ts"),
    }),
  ]);
  await writeFile(
    resolve(temporaryDirectory, "package.json"),
    `${JSON.stringify(
      {
        private: true,
        type: "module",
        dependencies: {
          "@suhaibinator/kms": `file:${sdkDirectory}`,
          next: nextVersion,
          react: reactVersion,
          "react-dom": reactVersion,
        },
        devDependencies: {
          "@types/node": "22.19.15",
          "@types/react": reactTypes,
          "@types/react-dom": reactDomTypes,
          typescript: "5.9.3",
        },
      },
      null,
      2,
    )}\n`,
  );

  const install = await command("npm", ["install", "--no-audit", "--no-fund"], temporaryDirectory);
  if (install.code !== 0) fail("isolated peer dependencies failed to install", install.output);
  const nextExecutable = resolve(temporaryDirectory, "node_modules/next/dist/bin/next");

  const valid = await command(process.execPath, [nextExecutable, "build", validFixture]);
  if (valid.code !== 0) fail("valid peer-major fixture failed to build", valid.output);

  const chunks = await javascriptFiles(resolve(validFixture, ".next/static/chunks"));
  const browserBundle = (await Promise.all(chunks.map((path) => readFile(path, "utf8")))).join(
    "\n",
  );
  for (const forbidden of [
    "@grpc/grpc-js",
    "node:tls",
    "KMS_SERVER_CREDENTIAL_MUST_NOT_REACH_CLIENT",
  ]) {
    if (browserBundle.includes(forbidden)) {
      throw new Error(`peer-major browser chunks contain server marker: ${forbidden}`);
    }
  }
  if (!browserBundle.includes("kms-public-config-")) {
    throw new Error("peer-major browser chunks omit the public-config client hook");
  }

  const invalid = await command(process.execPath, [nextExecutable, "build", invalidFixture]);
  if (invalid.code === 0)
    throw new Error("peer-major Next accepted a client import of next/server");
  if (!/(server-only|unsupported-browser|not exported|Node\.js-only)/iu.test(invalid.output)) {
    fail("invalid peer-major fixture failed for an unexpected reason", invalid.output);
  }
} finally {
  await rm(temporaryDirectory, { recursive: true, force: true });
}

function command(executable, arguments_, cwd = sdkDirectory) {
  return new Promise((resolveCommand, rejectCommand) => {
    const child = spawn(executable, arguments_, {
      cwd,
      env: { ...process.env, NEXT_TELEMETRY_DISABLED: "1" },
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

async function javascriptFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) files.push(...(await javascriptFiles(path)));
    else if (entry.isFile() && path.endsWith(".js")) files.push(path);
  }
  return files;
}

function fail(message, output) {
  process.stderr.write(output);
  throw new Error(message);
}
