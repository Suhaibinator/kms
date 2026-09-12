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

/** Every `--x: <value>` declaration in one column-0 block. */
function tokens(text: string): Map<string, string> {
  const found = new Map<string, string>();
  for (const match of text.matchAll(/^\s*(--[a-z0-9-]+):\s*([^;]+);/gm)) {
    found.set(match[1] ?? "", (match[2] ?? "").trim());
  }
  return found;
}

type Rgba = { rgb: [number, number, number]; a: number };

function colour(value: string): Rgba {
  const hexMatch = value.match(/^#([0-9a-fA-F]{6})$/);
  if (hexMatch) {
    const digits = hexMatch[1] ?? "";
    return {
      rgb: [0, 2, 4].map((i) => Number.parseInt(digits.slice(i, i + 2), 16)) as [
        number,
        number,
        number,
      ],
      a: 1,
    };
  }
  const rgbaMatch = value.match(/^rgba?\(([^)]+)\)$/);
  if (!rgbaMatch) throw new Error(`not a literal colour: ${value}`);
  const parts = (rgbaMatch[1] ?? "").split(",").map((part) => Number.parseFloat(part));
  return { rgb: [parts[0] ?? 0, parts[1] ?? 0, parts[2] ?? 0], a: parts[3] ?? 1 };
}

/** sRGB relative luminance, per WCAG 2.1. */
function luminance(rgb: [number, number, number]): number {
  const [r, g, b] = rgb.map((channel) => {
    const c = channel / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  }) as [number, number, number];
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(ink: Rgba, ground: [number, number, number]): number {
  const inkLum = luminance(ink.rgb);
  const groundLum = luminance(ground);
  return (Math.max(inkLum, groundLum) + 0.05) / (Math.min(inkLum, groundLum) + 0.05);
}

/** A translucent fill composited over an opaque ground. */
function composite(fill: Rgba, ground: [number, number, number]): [number, number, number] {
  return fill.rgb.map((c, i) => c * fill.a + (ground[i] ?? 0) * (1 - fill.a)) as [
    number,
    number,
    number,
  ];
}

/** Semantic inks that are read as text on their own `-soft` wash. `--accent` is
 *  excluded on purpose: it is a hover *surface*, not an ink. */
const INK_PAIRS = [
  "success",
  "warning",
  "danger",
  "ident-app",
  "ident-env",
  "ident-ns",
  "ident-alias",
  "ident-key",
  "ident-release",
  "ident-schema",
  "ident-version",
  "ident-revision",
  "ident-identity",
  "ident-instance",
];

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

  // A `--text-*` name Tailwind also ships keeps its `--text-*--line-height`
  // companion when we override only the size, so the utility carries a ratio
  // the matching `font-size: var(--text-*)` rule does not. --text-lg/-xl are
  // ours by value but Tailwind's by name, so they are pinned to `inherit` and
  // mean a font size only. --text-2xs/-md/-touch are names Tailwind does not
  // ship: no companion is generated, and adding one would break the same rule.
  it("keeps every type token this file added to a font size alone", () => {
    // Comment prose names these tokens too; only declarations count.
    const declared = new Set(
      [...css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/^\s*(--text-[\w-]+):\s*([^;]+);/gm)].map(
        (m) => `${m[1]}=${(m[2] ?? "").trim()}`,
      ),
    );
    for (const name of ["lg", "xl"]) {
      expect(declared).toContain(`--text-${name}--line-height=inherit`);
    }
    for (const name of ["2xs", "md", "touch"]) {
      expect([...declared].some((d) => d.startsWith(`--text-${name}=`))).toBe(true);
      expect([...declared].some((d) => d.startsWith(`--text-${name}--line-height=`))).toBe(false);
    }
    // The three that deliberately keep Tailwind's ratio, because live utility
    // uses and .field-reserve's `line-height: var(--text-sm--line-height)`
    // resolve them. Pinning these would move 230 call sites at once.
    for (const name of ["sm", "xs", "base"]) {
      expect([...declared].some((d) => d.startsWith(`--text-${name}--line-height=`))).toBe(false);
    }
  });

  // --text-base is 14px because `text-base` is a live utility on Input,
  // Textarea, CardTitle, FieldLegend and three dialog titles, all of which
  // resolved 1rem against this root before it was named. Its line-height
  // ratio is unitless, so changing the size moves the line box too.
  it("keeps --text-base on the root font size the text-base utility assumes", () => {
    expect(css).toMatch(/--text-base:\s*14px/);
    expect(css).toMatch(/font-size:\s*14px;[\s\S]{0,40}line-height:\s*1\.5/);
  });

  // Column-0 openers are a file's top-level blocks. Anything that is not a
  // token block or a layer is a rule that would silently outrank every
  // utility, so the list of allowed openers is closed on purpose — and it
  // applies to every feature sheet, not just globals.css.
  it.each(sheets.map((sheet) => [sheet.file, sheet.css] as const))(
    "%s keeps every rule inside a Tailwind layer",
    (file, source) => {
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
        // The four feature sheets, in a layer above components so they win the
        // ties they look like they should win, and below utilities so they
        // still lose to one.
        "@layer features",
        // Mobile constraints override utility-authored footprints within the same layer.
        "@layer utilities",
      ]);
      expect(openers.filter((o) => !allowed.has(o))).toEqual([]);
      expect(openers).toContain(file === "globals.css" ? "@layer components" : "@layer features");
    },
  );

  // The `features` layer only outranks `components` because globals.css names
  // the order before its first @import. Lose that line, or let a feature sheet
  // fall back into `components`, and applications.css silently stops winning
  // the ties the contract-editor rules depend on.
  it("orders the feature layer between components and utilities", () => {
    // Before the first @import, or the browser has already fixed an order.
    const order = css.slice(0, css.indexOf('@import "tailwindcss";'));
    expect(order).toContain("@layer theme, base, components, features, utilities;");
    for (const sheet of sheets.filter((s) => s.file !== "globals.css")) {
      expect(sheet.css).not.toContain("@layer components {");
    }
  });

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
      "./release-diff.css",
    ]);
    for (const file of [
      "applications.css",
      "ship.css",
      "onboarding.css",
      "palette.css",
      "release-diff.css",
    ]) {
      expect(sheets.map((sheet) => sheet.file)).toContain(file);
    }
  });

  // Feature sheets read tokens only: a literal colour would render one theme's
  // ink on the other theme's ground. (globals.css owns the literals.)
  it.each(
    sheets.filter((sheet) => sheet.file !== "globals.css").map((s) => [s.file, s.css] as const),
  )("%s takes every colour from a token", (_file, source) => {
    expect(source.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []).toEqual([]);
    expect(source.match(/\b(?:rgba?|hsla?|color-mix)\(/g) ?? []).toEqual([]);
  });

  it("defines each simple class selector in exactly one sheet", () => {
    // A recipe defined twice (once per lane) silently drifts; a shared one
    // belongs in globals.css. Only bare `.name {` openers count — compound
    // selectors are legitimately repeated for overrides.
    const owners = new Map<string, string[]>();
    for (const sheet of sheets) {
      for (const match of sheet.css.matchAll(/^ {2}\.([a-z][\w-]*) \{$/gm)) {
        const name = match[1] ?? "";
        owners.set(name, [...(owners.get(name) ?? []), sheet.file]);
      }
    }
    const shared = [...owners.entries()].filter(([, files]) => new Set(files).size > 1);
    expect(shared).toEqual([]);
    expect(owners.get("menu-popup")).toEqual(["globals.css"]);
    expect(owners.get("menu-item")).toEqual(["globals.css"]);
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

  // The colour system was tuned against white and the tinted grounds were never
  // re-checked: a badge, a chip and a panel all paint their ink on their own
  // `-soft` wash, which lifts the ground's luminance and costs 0.3-0.6 of the
  // ratio, and two audits running found the same class of failure. The
  // arithmetic is cheap and it is the only thing that keeps this fixed.
  it.each([
    ["light", ":root"],
    ["dark", ".dark"],
  ])("keeps every %s ink readable on its own soft wash", (theme, selector) => {
    const light = tokens(block(css, ":root"));
    const declared =
      theme === "light" ? light : new Map([...light, ...tokens(block(css, selector))]);
    const grounds = (["--bg", "--surface", "--surface-2"] as const).map((name) => {
      const value = declared.get(name);
      if (!value) throw new Error(`${selector} has no ${name}`);
      return colour(value).rgb;
    });
    const failures: string[] = [];
    for (const pair of INK_PAIRS) {
      const ink = declared.get(`--${pair}`);
      const soft = declared.get(`--${pair}-soft`);
      if (!ink || !soft) throw new Error(`${selector} is missing --${pair} or --${pair}-soft`);
      for (const [index, ground] of grounds.entries()) {
        const ratio = contrast(colour(ink), composite(colour(soft), ground));
        const on = ["--bg", "--surface", "--surface-2"][index];
        if (ratio < 4.5) failures.push(`--${pair} on ${on}: ${ratio.toFixed(2)}`);
      }
    }
    expect(failures).toEqual([]);
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
