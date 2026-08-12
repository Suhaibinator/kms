import { spawn } from "node:child_process";
import { readdir, readFile, rm } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const sdkDirectory = dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const nextExecutable = resolve(sdkDirectory, "node_modules/next/dist/bin/next");
const validFixture = resolve(sdkDirectory, "tests/fixtures/next-boundary");
const invalidFixture = resolve(sdkDirectory, "tests/fixtures/next-invalid-client-import");

await clean(validFixture);
await clean(invalidFixture);

const valid = await build(validFixture);
if (valid.code !== 0) {
  process.stderr.write(valid.output);
  throw new Error("valid Next.js adapter fixture failed to build");
}

const chunksDirectory = resolve(validFixture, ".next/static/chunks");
const chunks = await javascriptFiles(chunksDirectory);
const browserBundle = (await Promise.all(chunks.map((path) => readFile(path, "utf8")))).join("\n");
for (const forbidden of [
  "@grpc/grpc-js",
  "node:tls",
  "KMS_SERVER_CREDENTIAL_MUST_NOT_REACH_CLIENT",
  "Next KMS adapter is closed",
]) {
  if (browserBundle.includes(forbidden)) {
    throw new Error(`client chunks contain forbidden server marker: ${forbidden}`);
  }
}
if (!browserBundle.includes("kms-public-config-")) {
  throw new Error("client hook marker is missing from the built browser chunks");
}

const invalid = await build(invalidFixture);
if (invalid.code === 0) {
  throw new Error("Next.js accepted a Client Component import of next/server");
}
if (!/(server-only|unsupported-browser|not exported|Node\.js-only)/iu.test(invalid.output)) {
  process.stderr.write(invalid.output);
  throw new Error("invalid Next.js build failed for an unexpected reason");
}

await verifySignalShutdown();

await clean(validFixture);
await clean(invalidFixture);

async function build(directory) {
  return new Promise((resolveBuild, rejectBuild) => {
    const child = spawn(process.execPath, [nextExecutable, "build", directory], {
      cwd: sdkDirectory,
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
    child.once("error", rejectBuild);
    child.once("close", (code) => resolveBuild({ code: code ?? 1, output }));
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

async function clean(directory) {
  await Promise.all([
    rm(resolve(directory, ".next"), { recursive: true, force: true }),
    rm(resolve(directory, "next-env.d.ts"), { force: true }),
  ]);
}

async function verifySignalShutdown() {
  const serverUrl = pathToFileURL(resolve(sdkDirectory, "dist/next/server.js")).href;
  const publishingUrl = pathToFileURL(resolve(sdkDirectory, "dist/publishing.js")).href;
  const source = `
import { createNextKms } from ${JSON.stringify(serverUrl)};
import { definePublicProjection } from ${JSON.stringify(publishingUrl)};
const adapter = createNextKms({
  initialize: () => ({
    source: { current: () => ({ revision: 1n, value: { value: 1 } }) },
    close: async () => { process.stdout.write("closed\\n"); },
  }),
  projection: definePublicProjection({ value: (policy) => policy.value }),
  validate: () => ({ valid: true }),
});
await adapter.start();
adapter.installProcessShutdown({
  signals: ["SIGTERM"],
  onCleanupComplete: () => {
    process.stdout.write("complete\\n");
    process.exit(143);
  },
});
process.stdout.write("ready\\n");
setInterval(() => undefined, 60_000);
`;
  const child = spawn(
    process.execPath,
    ["--conditions=react-server", "--input-type=module", "--eval", source],
    {
      cwd: sdkDirectory,
      env: { ...process.env, NEXT_RUNTIME: "nodejs" },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let output = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    output += chunk;
  });
  child.stderr.on("data", (chunk) => {
    output += chunk;
  });
  try {
    await waitUntil(() => output.includes("ready\n") || child.exitCode !== null, 5_000);
    if (!output.includes("ready\n")) throw new Error(`shutdown fixture exited early:\n${output}`);
    child.kill("SIGTERM");
    const code = await Promise.race([
      new Promise((resolveExit) => child.once("close", resolveExit)),
      new Promise((_, rejectTimeout) =>
        setTimeout(() => rejectTimeout(new Error("shutdown fixture did not exit")), 5_000),
      ),
    ]);
    if (code !== 143 || !output.includes("closed\ncomplete\n")) {
      throw new Error(`shutdown fixture violated cleanup/exit order (code ${code}):\n${output}`);
    }
  } finally {
    if (child.exitCode === null) child.kill("SIGKILL");
  }
}

async function waitUntil(predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("timed out waiting for child process state");
    await new Promise((resolveWait) => setTimeout(resolveWait, 10));
  }
}
