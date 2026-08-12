import { strict as assert } from "node:assert";
import { spawn } from "node:child_process";
import { rm } from "node:fs/promises";
import { createServer } from "node:net";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const sdkDirectory = dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const fixture = resolve(sdkDirectory, "tests/fixtures/next-boundary");
const nextExecutable = resolve(sdkDirectory, "node_modules/next/dist/bin/next");

await clean();
const build = await run([nextExecutable, "build", fixture]);
if (build.code !== 0) {
  process.stderr.write(build.output);
  throw new Error("browser fixture failed to build");
}

const port = await availablePort();
const server = spawn(process.execPath, [nextExecutable, "start", fixture, "-p", String(port)], {
  cwd: sdkDirectory,
  env: { ...process.env, NEXT_TELEMETRY_DISABLED: "1" },
  stdio: ["ignore", "pipe", "pipe"],
});
let serverOutput = "";
server.stdout.setEncoding("utf8");
server.stderr.setEncoding("utf8");
server.stdout.on("data", (chunk) => {
  serverOutput += chunk;
});
server.stderr.on("data", (chunk) => {
  serverOutput += chunk;
});

let browser;
try {
  const origin = `http://127.0.0.1:${port}`;
  await waitForServer(origin);
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  await page.goto(origin, { waitUntil: "networkidle" });

  await expectText(page, '[data-testid="policy-limit"]', "Limit: 12");
  await expectText(page, '[data-testid="policy-revision"]', "Revision: 9007199254740993");

  await page.route("**/api/policy", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        revision: "9007199254740999",
        config: { limit: 16 },
      }),
    });
  });
  await page.click("#refresh-policy");
  await expectText(page, '[data-testid="policy-limit"]', "Limit: 16");
  await expectText(page, '[data-testid="policy-revision"]', "Revision: 9007199254740999");

  await page.click("#recover-policy");
  await expectText(page, '[data-testid="policy-limit"]', "Limit: 20");
  await expectText(page, '[data-testid="policy-revision"]', "Revision: 18446744073709551615");
} catch (error) {
  if (serverOutput.length > 0) process.stderr.write(serverOutput);
  throw error;
} finally {
  await browser?.close();
  await stopServer(server);
  await clean();
}

async function run(arguments_) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(process.execPath, arguments_, {
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
    child.once("error", rejectRun);
    child.once("close", (code) => resolveRun({ code: code ?? 1, output }));
  });
}

async function availablePort() {
  const listener = createServer();
  await new Promise((resolveListen, rejectListen) => {
    listener.once("error", rejectListen);
    listener.listen(0, "127.0.0.1", resolveListen);
  });
  const address = listener.address();
  assert(address !== null && typeof address === "object");
  await new Promise((resolveClose) => listener.close(resolveClose));
  return address.port;
}

async function waitForServer(origin) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (server.exitCode !== null) {
      throw new Error(`Next browser server exited early (${server.exitCode})`);
    }
    try {
      const response = await fetch(origin);
      if (response.ok) return;
    } catch {
      // The server has not bound the port yet.
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 50));
  }
  throw new Error("timed out waiting for the Next browser fixture");
}

async function expectText(page, selector, expected) {
  await page.waitForFunction(
    ({ selector: selected, expected: text }) =>
      document.querySelector(selected)?.textContent === text,
    { selector, expected },
  );
}

async function stopServer(child) {
  if (child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([
    new Promise((resolveExit) => child.once("close", resolveExit)),
    new Promise((resolveTimeout) => setTimeout(resolveTimeout, 5_000)),
  ]);
  if (child.exitCode === null) child.kill("SIGKILL");
}

async function clean() {
  await Promise.all([
    rm(resolve(fixture, ".next"), { recursive: true, force: true }),
    rm(resolve(fixture, "next-env.d.ts"), { force: true }),
  ]);
}
