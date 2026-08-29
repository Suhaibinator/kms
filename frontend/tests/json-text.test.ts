import { describe, expect, it } from "vitest";
import {
  canonicalParameterValue,
  checkJson,
  formatJson,
  inferSchema,
  jsonEquivalent,
  lineCount,
  minifyJson,
  offsetToLineColumn,
  splitLines,
  tokenizeJson,
  valuesEquivalent,
} from "@/lib/json-text";

function kinds(text: string): string[] {
  return tokenizeJson(text)
    .filter((token) => token.kind !== "ws")
    .map((token) => `${token.kind}:${text.slice(token.start, token.end)}`);
}

function tiles(text: string): boolean {
  let at = 0;
  for (const token of tokenizeJson(text)) {
    if (token.start !== at || token.end <= token.start) return false;
    at = token.end;
  }
  return at === text.length;
}

describe("tokenizeJson", () => {
  it("tags keys, strings, numbers, literals and punctuation", () => {
    expect(kinds('{"a": "b", "n": -1.5e3, "t": true, "z": null}')).toEqual([
      "punct:{",
      'key:"a"',
      "punct::",
      'string:"b"',
      "punct:,",
      'key:"n"',
      "punct::",
      "number:-1.5e3",
      "punct:,",
      'key:"t"',
      "punct::",
      "boolean:true",
      "punct:,",
      'key:"z"',
      "punct::",
      "null:null",
      "punct:}",
    ]);
  });

  it("recognises a key even when whitespace or a line break precedes the colon", () => {
    expect(kinds('{"a" : 1}')[1]).toBe('key:"a"');
    expect(kinds('{"a"\n:1}')[1]).toBe('key:"a"');
    // A string value followed by a colon on the next line is still a value.
    expect(kinds('["a"\n:1]')[1]).toBe('key:"a"');
  });

  it("keeps escapes and unicode inside one string token", () => {
    expect(kinds('"a\\"b\\\\c\\u00e9é"')).toEqual(['string:"a\\"b\\\\c\\u00e9é"']);
  });

  it("marks an unterminated string as an error that stops at the line break", () => {
    expect(kinds('{"a": "oops\n"b": 1}')).toEqual([
      "punct:{",
      'key:"a"',
      "punct::",
      'error:"oops',
      'key:"b"',
      "punct::",
      "number:1",
      "punct:}",
    ]);
  });

  it("rejects sloppy numbers and bare words as errors", () => {
    expect(kinds("[01, +1, .5, 1., NaN, truex, -]")).toEqual([
      "punct:[",
      "error:01",
      "punct:,",
      "error:+1",
      "punct:,",
      "error:.5",
      "punct:,",
      "error:1.",
      "punct:,",
      "error:NaN",
      "punct:,",
      "error:truex",
      "punct:,",
      "error:-",
      "punct:]",
    ]);
  });

  it("tiles any input exactly, including garbage and lone surrogates", () => {
    const samples = [
      "",
      "\n",
      "{",
      '"',
      '"\\',
      " {}",
      '\ud800"x',
      "\\\\\\",
      '{"a":[1,2,{"b":null}]}\n\n',
      Array.from({ length: 500 }, (_, i) => String.fromCharCode(i)).join(""),
    ];
    for (const sample of samples) expect(tiles(sample)).toBe(true);
  });

  it("splits tokens per logical line", () => {
    const text = '{\n  "a": 1,\n  "b": "x"\n}';
    const lines = splitLines(text, tokenizeJson(text));
    expect(lines).toHaveLength(4);
    expect(lines[1].map((token) => text.slice(token.start, token.end))).toEqual([
      "  ",
      '"a"',
      ":",
      " ",
      "1",
      ",",
    ]);
    expect(lineCount(text)).toBe(4);
  });
});

describe("checkJson", () => {
  it("accepts well-formed documents", () => {
    for (const text of [
      "{}",
      "[]",
      " 1 ",
      '"s"',
      "null",
      '{"a":{"b":[1,[2,{"c":false}]]}}',
      '{\n  "a": 1\n}\n',
    ]) {
      expect(checkJson(text)).toBeNull();
    }
  });

  it("reports positions and friendly messages", () => {
    expect(checkJson('{\n  "a": 1,\n}')).toEqual({
      offset: 12,
      line: 3,
      column: 1,
      message: 'Trailing comma before "}"',
    });
    expect(checkJson("[1,]")?.message).toBe('Trailing comma before "]"');
    expect(checkJson('{"a" 1}')?.message).toBe('Expected ":" after the property name');
    expect(checkJson("{a: 1}")?.message).toBe('Unexpected "a"');
    expect(checkJson('{"a": "x')?.message).toBe("Unterminated string");
    expect(checkJson('{"a": 1')).toMatchObject({ offset: 7, message: "Unexpected end of input" });
    expect(checkJson("")).toMatchObject({ offset: 0, message: "Expected a JSON value" });
    expect(checkJson("   ")).toMatchObject({ message: "Expected a JSON value" });
    expect(checkJson("{} x")?.message).toBe("Unexpected content after the JSON value");
    expect(checkJson('{"a": 1 "b": 2}')?.message).toBe('Expected "," or "}", got string');
    expect(checkJson("[1 2]")?.message).toBe('Expected "," or "]", got number');
    expect(checkJson("[01]")?.message).toBe('Unexpected "01"');
    expect(checkJson("[}")?.message).toBe('Expected a value, got "}"');
  });

  it("survives absurd nesting without recursion", () => {
    const deep = "[".repeat(100000);
    expect(checkJson(deep)?.message).toBe("Unexpected end of input");
    expect(checkJson(`${"[".repeat(50000)}${"]".repeat(50000)}`)).toBeNull();
  });
});

describe("formatJson / minifyJson", () => {
  it("matches JSON.stringify layout for canonical input", () => {
    const samples = [
      '{"a":1,"b":{"c":[1,2,3],"d":{}},"e":[],"f":[{"g":null}],"h":"x"}',
      "[]",
      "{}",
      "[[[]]]",
      '"s"',
      "42",
    ];
    for (const sample of samples) {
      expect(formatJson(sample)).toBe(JSON.stringify(JSON.parse(sample), null, 2));
      expect(minifyJson(sample)).toBe(JSON.stringify(JSON.parse(sample)));
    }
  });

  it("only changes whitespace", () => {
    const text = '{"big":12345678901234567890,"f":1.0,"e":1e400,"s":"\\u00e9\\n","u":"é"}';
    expect(minifyJson(formatJson(text) ?? "")).toBe(text);
    expect(formatJson(text)).toContain("12345678901234567890");
    expect(formatJson(text)).toContain("1.0");
    expect(formatJson(text)).toContain("1e400");
    expect(formatJson(text)).toContain('"\\u00e9\\n"');
  });

  it("returns null for malformed input", () => {
    expect(formatJson("{")).toBeNull();
    expect(minifyJson("[1,]")).toBeNull();
    expect(formatJson("")).toBeNull();
  });
});

describe("equivalence and canonical values", () => {
  it("compares JSON by tokens", () => {
    expect(jsonEquivalent('{"a": 1}', '{"a":1}')).toBe(true);
    expect(jsonEquivalent('{"a": 1.0}', '{"a":1}')).toBe(false);
    expect(jsonEquivalent("{", "{")).toBe(false);
  });

  it("minifies json values and leaves everything else alone", () => {
    expect(canonicalParameterValue('{\n  "a": 1\n}', "json")).toBe('{"a":1}');
    expect(canonicalParameterValue("{ broken", "json")).toBe("{ broken");
    expect(canonicalParameterValue("  hello  ", "string")).toBe("  hello  ");
    expect(canonicalParameterValue("3", "integer")).toBe("3");
  });

  it("detects an unchanged value per content type", () => {
    expect(valuesEquivalent('{"a": 1}', '{"a":1}', "json")).toBe(true);
    expect(valuesEquivalent('{"a": 2}', '{"a":1}', "json")).toBe(false);
    expect(valuesEquivalent("3", "3", "integer")).toBe(true);
    expect(valuesEquivalent("3 ", "3", "integer")).toBe(false);
  });

  it("maps offsets to 1-based lines and columns", () => {
    expect(offsetToLineColumn("ab\ncd", 0)).toEqual({ line: 1, column: 1 });
    expect(offsetToLineColumn("ab\ncd", 3)).toEqual({ line: 2, column: 1 });
    expect(offsetToLineColumn("ab\ncd", 4)).toEqual({ line: 2, column: 2 });
    expect(offsetToLineColumn("ab\ncd", 99)).toEqual({ line: 2, column: 3 });
  });
});

describe("inferSchema", () => {
  it("builds a form-friendly schema from a value", () => {
    expect(
      inferSchema({
        n: 1,
        f: 1.5,
        s: "x",
        b: true,
        list: ["a", "b"],
        nums: [1, 2.5],
        mixed: [1, "a"],
        empty: [],
        nothing: null,
        nested: { deep: { flag: false } },
      }),
    ).toEqual({
      type: "object",
      additionalProperties: true,
      properties: {
        n: { type: "integer" },
        f: { type: "number" },
        s: { type: "string" },
        b: { type: "boolean" },
        list: { type: "array", items: { type: "string" } },
        nums: { type: "array", items: { type: "number" } },
        mixed: {},
        empty: {},
        nothing: {},
        nested: {
          type: "object",
          additionalProperties: true,
          properties: {
            deep: {
              type: "object",
              additionalProperties: true,
              properties: { flag: { type: "boolean" } },
            },
          },
        },
      },
    });
  });

  it("returns null unless the root is an object", () => {
    expect(inferSchema([1])).toBeNull();
    expect(inferSchema("x")).toBeNull();
    expect(inferSchema(null)).toBeNull();
  });
});
