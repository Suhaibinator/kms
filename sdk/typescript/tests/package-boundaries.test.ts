import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

interface ConditionalExport {
  readonly types?: string;
  readonly node?: string;
  readonly default?: string;
}

interface PackageManifest {
  readonly type?: string;
  readonly engines?: Readonly<Record<string, string>>;
  readonly files?: readonly string[];
  readonly sideEffects?: boolean | readonly string[];
  readonly exports?: Readonly<Record<string, string | ConditionalExport>>;
  readonly publishConfig?: Readonly<Record<string, boolean | string>>;
}

function source(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), "utf8");
}

describe("published trust boundaries", () => {
  const manifest = JSON.parse(source("../package.json")) as PackageManifest;

  it("poisons every server entry outside Node while leaving only next/client browser-safe", () => {
    const exports = manifest.exports ?? {};
    expect(exports["."]).toEqual({
      types: "./dist/index.d.ts",
      node: "./dist/index.js",
      default: "./dist/unsupported-browser.js",
    });
    expect(exports["./configstore"]).toEqual({
      types: "./dist/configstore/index.d.ts",
      node: "./dist/configstore/index.js",
      default: "./dist/unsupported-browser.js",
    });
    expect(exports["./next/server"]).toEqual({
      types: "./dist/next/server.d.ts",
      node: "./dist/next/server.js",
      default: "./dist/unsupported-browser.js",
    });
    expect(exports["./next/client"]).toEqual({
      types: "./dist/next/client.d.ts",
      default: "./dist/next/client.js",
    });
  });

  it("keeps browser and server markers effective through packaging", () => {
    expect(source("../src/next/client.tsx").startsWith('"use client";')).toBe(true);
    expect(source("../src/next/server.ts").startsWith('import "server-only";')).toBe(true);
    expect(source("../src/unsupported-browser.ts")).toContain("Node.js-only SDK");
    expect(manifest.sideEffects).toEqual(
      expect.arrayContaining([
        "./dist/next/client.js",
        "./dist/next/server.js",
        "./dist/unsupported-browser.js",
      ]),
    );
  });

  it("ships the documented Node-only package contract", () => {
    expect(manifest.type).toBe("module");
    expect(manifest.engines?.node).toBe(">=22");
    expect(manifest.files).toEqual(
      expect.arrayContaining(["dist", "README.md", "SECURITY.md", "CHANGELOG.md", "LICENSE"]),
    );
    expect(manifest.publishConfig).toEqual({ access: "public", provenance: true });
  });
});
