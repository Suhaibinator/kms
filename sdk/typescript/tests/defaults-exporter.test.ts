import { chmod, mkdtemp, readFile, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import {
  encodeDefaultsArtifact,
  parseDefaultsArtifact,
  runDefaultsExporter,
} from "../src/configgen/index.js";

const schemaSHA256 = "0123456789abcdef".repeat(4);

describe("defaults exporter runner", () => {
  it("runs a synchronous provider and generated-style encoder to stdout", async () => {
    const stdout: string[] = [];
    const stderr: string[] = [];
    const code = await runDefaultsExporter(
      ["--profile", "local"],
      (profile) => ({ profile, runtime: '{"enabled":true}' }),
      (profile, defaults) => artifact(profile, defaults.runtime),
      {
        stdout: (message) => stdout.push(message),
        stderr: (message) => stderr.push(message),
      },
    );

    expect(code).toBe(0);
    expect(stderr).toEqual([]);
    expect(parseDefaultsArtifact(stdout.join(""))).toMatchObject({ profile: "local" });
  });

  it("awaits an asynchronous provider and atomically writes a private file", async () => {
    const directory = await mkdtemp(join(tmpdir(), "kms-defaults-exporter-"));
    const output = join(directory, "nested", "defaults.json");
    const code = await runDefaultsExporter(
      ["--profile=staging", `--output=${output}`],
      async () => ({ runtime: "{}" }),
      async (profile, defaults) => artifact(profile, defaults.runtime),
      { stdout: () => undefined, stderr: () => undefined },
    );

    expect(code).toBe(0);
    expect(parseDefaultsArtifact(await readFile(output, "utf8"))).toMatchObject({
      profile: "staging",
    });
    if (process.platform !== "win32") {
      expect((await stat(output)).mode & 0o777).toBe(0o600);
      await chmod(output, 0o640);
      expect(
        await runDefaultsExporter(
          ["--profile=staging", `--output=${output}`],
          () => ({ runtime: '{"updated":true}' }),
          (profile, defaults) => artifact(profile, defaults.runtime),
          { stdout: () => undefined, stderr: () => undefined },
        ),
      ).toBe(0);
      expect((await stat(output)).mode & 0o777).toBe(0o640);
    }
  });

  it("owns help and usage validation with conventional exit codes", async () => {
    const help: string[] = [];
    expect(
      await runDefaultsExporter(
        [],
        () => ({}),
        () => "",
        {
          stdout: () => undefined,
          stderr: (message) => help.push(message),
        },
      ),
    ).toBe(2);
    expect(help.join("")).toMatch(/--profile.*required/u);

    const stdout: string[] = [];
    expect(
      await runDefaultsExporter(
        ["--help"],
        () => ({}),
        () => "",
        {
          stdout: (message) => stdout.push(message),
          stderr: () => undefined,
        },
      ),
    ).toBe(0);
    expect(stdout.join("")).toMatch(/Usage: defaults-exporter/u);
  });

  it("bounds and redacts provider and encoder failures", async () => {
    const providerErrors: string[] = [];
    const providerCode = await runDefaultsExporter(
      ["--profile", "test", "--output", "-"],
      () => {
        throw new Error(`secret=${"x".repeat(2_000)}`);
      },
      () => "",
      { stdout: () => undefined, stderr: (message) => providerErrors.push(message) },
    );
    expect(providerCode).toBe(1);
    expect(providerErrors.join("")).toBe("defaults exporter: loading profile defaults failed\n");

    const encoderErrors: string[] = [];
    const encoderCode = await runDefaultsExporter(
      ["--profile", "test", "--output", "-"],
      () => ({ value: "sensitive-value" }),
      () => {
        throw new Error("sensitive-value");
      },
      { stdout: () => undefined, stderr: (message) => encoderErrors.push(message) },
    );
    expect(encoderCode).toBe(1);
    expect(encoderErrors.join("")).toBe("defaults exporter: encoding defaults artifact failed\n");

    const wrongProfileErrors: string[] = [];
    const wrongProfileCode = await runDefaultsExporter(
      ["--profile", "test"],
      () => ({ runtime: "{}" }),
      (_profile, defaults) => artifact("production", defaults.runtime),
      { stdout: () => undefined, stderr: (message) => wrongProfileErrors.push(message) },
    );
    expect(wrongProfileCode).toBe(1);
    expect(wrongProfileErrors.join("")).toBe(
      "defaults exporter: encoding defaults artifact failed\n",
    );
  });
});

function artifact(profile: string, value: string): string {
  return encodeDefaultsArtifact({
    profile,
    schemaSHA256,
    contract: [
      { alias: "runtime", kind: "parameter", contentType: "json" },
      { alias: "token", kind: "secret" },
    ],
    parameters: { runtime: value },
  });
}
