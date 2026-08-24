import { describe, expect, it } from "vitest";
import { parseReleaseKey, releaseKey } from "@/components/releases/utils";

describe("parseReleaseKey", () => {
  it("splits name@version and rejects malformed keys", () => {
    expect(parseReleaseKey("runtime@12")).toEqual({ name: "runtime", version: 12 });
    expect(parseReleaseKey("run@time@3")).toEqual({ name: "run@time", version: 3 });
    for (const bad of [
      "",
      "runtime",
      "@12",
      "runtime@",
      "runtime@v1",
      "runtime@0",
      "runtime@1.5",
      "runtime@-1",
    ]) {
      expect(parseReleaseKey(bad)).toBeNull();
    }
  });

  it("round-trips releaseKey", () => {
    const release = { name: "runtime", version: 7 } as Parameters<typeof releaseKey>[0];
    expect(parseReleaseKey(releaseKey(release))).toEqual({ name: "runtime", version: 7 });
  });
});
