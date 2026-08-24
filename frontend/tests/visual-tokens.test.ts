import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const stylesDir = resolve(process.cwd(), "styles");
const css = readFileSync(resolve(stylesDir, "globals.css"), "utf8");
const sheets = readdirSync(stylesDir)
  .filter((file) => file.endsWith(".css"))
  .sort()
  .map((file) => ({ file, css: readFileSync(resolve(stylesDir, file), "utf8") }));

/** The text of one column-0 `{` block, e.g. `:root { … }`. */
function block(source: string, opener: string): string {
  const start = source.indexOf(`\n${opener} {\n`);
  if (start === -1) return "";
  const end = source.indexOf("\n}\n", start);
  return source.slice(start, end);
}

const tokenNames = (text: string): string[] =>
  [...text.matchAll(/^\s*(--ident-[a-z-]+):/gm)].map((match) => match[1] ?? "");

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

  // Column-0 openers are a file's top-level blocks. Anything that is not a
  // token block or a layer is a rule that would silently outrank every
  // utility, so the list of allowed openers is closed on purpose — and it
  // applies to every feature sheet, not just globals.css.
  it.each(sheets.map((sheet) => [sheet.file, sheet.css] as const))(
    "%s keeps every rule inside a Tailwind layer",
    (_file, source) => {
      const openers = source
        .split("\n")
        .filter((line) => /^\S.*\{\s*$/.test(line))
        .map((line) => line.replace(/\s*\{\s*$/, ""));
      const allowed = new Set([
        ":root",
        ".dark",
        "@theme",
        "@theme inline",
        "@layer base",
        "@layer components",
      ]);
      expect(openers.filter((o) => !allowed.has(o))).toEqual([]);
      expect(openers).toContain("@layer components");
    },
  );

  it("imports every feature sheet right after the framework imports", () => {
    const imports = [...css.matchAll(/^@import "([^"]+)";/gm)].map((match) => match[1]);
    expect(imports).toEqual([
      "tailwindcss",
      "tw-animate-css",
      "shadcn/tailwind.css",
      "./applications.css",
      "./ship.css",
      "./onboarding.css",
      "./palette.css",
    ]);
    for (const file of ["applications.css", "ship.css", "onboarding.css", "palette.css"]) {
      expect(sheets.map((sheet) => sheet.file)).toContain(file);
    }
  });

  it("defines every --ident-* token on both :root and .dark", () => {
    const light = tokenNames(block(css, ":root"));
    const dark = tokenNames(block(css, ".dark"));
    expect(light.length).toBeGreaterThan(0);
    expect([...dark].sort()).toEqual([...light].sort());
    // One foreground and one -soft background per kind, no strays.
    for (const kind of [
      "app",
      "env",
      "ns",
      "alias",
      "key",
      "release",
      "schema",
      "version",
      "revision",
      "identity",
      "instance",
    ]) {
      expect(light).toContain(`--ident-${kind}`);
      expect(light).toContain(`--ident-${kind}-soft`);
    }
    expect(light).toHaveLength(22);
  });

  it("derives the --space-* scale from --spacing", () => {
    expect(css).toMatch(/--space-4:\s*calc\(var\(--spacing\) \* 4\)/);
    expect(css).not.toMatch(/--space-\d+:\s*\d+px/);
  });

  it("derives control height and label offset instead of hand-computing them", () => {
    expect(css).toMatch(/--control-h:\s*calc\(var\(--spacing\)/);
    expect(css).toMatch(/--control-h-sm:\s*calc\(var\(--spacing\)/);
    expect(css).toMatch(/--label-offset:\s*calc\(/);
    expect(css).not.toMatch(/margin-top:\s*1\.5rem/);
    expect(css).not.toMatch(/min-height:\s*2\.375rem/);
  });
});
