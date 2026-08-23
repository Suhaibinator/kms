import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(resolve(process.cwd(), "styles/globals.css"), "utf8");

describe("globals.css stays out of Tailwind's way", () => {
  // These names are Tailwind utilities. An unlayered redefinition silently wins over
  // @layer utilities, which is how `mb-16` once came to mean 16px instead of 64px.
  it.each(["mt-0", "mt-8", "mt-16", "mt-24", "mb-8", "mb-16", "text-sm", "sr-only"])(
    "does not define .%s",
    (cls) => {
      expect(css).not.toMatch(new RegExp(`^\\.${cls}\\s*[,{]`, "m"));
    },
  );

  it("pins the rem scales the px CSS assumes", () => {
    expect(css).toMatch(/--spacing:\s*4px/);
    expect(css).toMatch(/--text-sm:\s*12\.5px/);
    expect(css).toMatch(/--text-xs:\s*11\.5px/);
  });

  it("derives control height and label offset instead of hand-computing them", () => {
    expect(css).toMatch(/--control-h:\s*calc\(var\(--spacing\)/);
    expect(css).toMatch(/--control-h-sm:\s*calc\(var\(--spacing\)/);
    expect(css).toMatch(/--label-offset:\s*calc\(/);
    expect(css).not.toMatch(/margin-top:\s*1\.5rem/);
    expect(css).not.toMatch(/min-height:\s*2\.375rem/);
  });
});
