/**
 * Text-level JSON helpers for the console's editors and viewers.
 *
 * Everything here works on the raw text rather than on `JSON.parse` output:
 * the tokenizer accepts any input (so a half-typed document still highlights),
 * the checker reports the first problem with a line and column, and the
 * formatters only ever touch whitespace — `1.0`, `1e400`, integers beyond
 * 2^53 and string escapes survive a format/minify round-trip byte for byte,
 * which `JSON.stringify(JSON.parse(text))` cannot promise.
 */

import { type JsonSchema, MAX_FORM_DEPTH } from "@/lib/schema-form";
import { byteLength } from "@/lib/validation";

export type TokenKind = "key" | "string" | "number" | "boolean" | "null" | "punct" | "ws" | "error";

/** A slice of the source text; `end` is exclusive. Tokens tile the text without gaps. */
export interface Token {
  kind: TokenKind;
  start: number;
  end: number;
}

export interface JsonProblem {
  offset: number;
  /** 1-based. */
  line: number;
  /** 1-based, in UTF-16 code units. */
  column: number;
  message: string;
}

/** Above this many bytes the editor and viewer show plain text instead of tokens. */
export const HIGHLIGHT_MAX_BYTES = 200 * 1024;
/** Above this many bytes the editor renders the highlight layer at low priority. */
export const HIGHLIGHT_DEFER_BYTES = 64 * 1024;

const NUMBER_RE = /-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/y;

const CH_TAB = 9;
const CH_LF = 10;
const CH_CR = 13;
const CH_SPACE = 32;
const CH_QUOTE = 34;
const CH_COMMA = 44;
const CH_MINUS = 45;
const CH_0 = 48;
const CH_9 = 57;
const CH_COLON = 58;
const CH_LBRACKET = 91;
const CH_BACKSLASH = 92;
const CH_RBRACKET = 93;
const CH_LBRACE = 123;
const CH_RBRACE = 125;

function isHorizontalWs(c: number): boolean {
  return c === CH_SPACE || c === CH_TAB || c === CH_CR;
}

function isPunct(c: number): boolean {
  return (
    c === CH_LBRACE ||
    c === CH_RBRACE ||
    c === CH_LBRACKET ||
    c === CH_RBRACKET ||
    c === CH_COMMA ||
    c === CH_COLON
  );
}

/** Characters that end a bare word (number, literal, or garbage). */
function isStop(c: number): boolean {
  return Number.isNaN(c) || c === CH_LF || isHorizontalWs(c) || c === CH_QUOTE || isPunct(c);
}

/**
 * Splits `text` into tokens that tile it exactly. Never throws and never
 * stalls: every step consumes at least one code unit. A string never spans a
 * line break (an unterminated one runs to the end of its line as an `error`),
 * which is what lets the highlighter work line by line.
 */
export function tokenizeJson(text: string): Token[] {
  const tokens: Token[] = [];
  const n = text.length;
  let i = 0;
  const push = (kind: TokenKind, start: number, end: number) => {
    tokens.push({ kind, start, end });
  };
  while (i < n) {
    const c = text.charCodeAt(i);
    if (c === CH_LF) {
      push("ws", i, i + 1);
      i += 1;
      continue;
    }
    if (isHorizontalWs(c)) {
      let j = i + 1;
      while (j < n && isHorizontalWs(text.charCodeAt(j))) j += 1;
      push("ws", i, j);
      i = j;
      continue;
    }
    if (c === CH_QUOTE) {
      let j = i + 1;
      let closed = false;
      while (j < n) {
        const d = text.charCodeAt(j);
        if (d === CH_BACKSLASH) {
          j += text.charCodeAt(j + 1) === CH_LF ? 1 : 2;
          continue;
        }
        if (d === CH_LF) break;
        j += 1;
        if (d === CH_QUOTE) {
          closed = true;
          break;
        }
      }
      if (j > n) j = n;
      push(closed ? "string" : "error", i, j);
      i = j;
      continue;
    }
    if (isPunct(c)) {
      if (c === CH_COLON) {
        // A string followed by a colon is a property name.
        const candidate = findPreviousSolid(tokens, tokens.length - 1);
        if (candidate && candidate.kind === "string") candidate.kind = "key";
      }
      push("punct", i, i + 1);
      i += 1;
      continue;
    }
    if (c === CH_MINUS || (c >= CH_0 && c <= CH_9)) {
      NUMBER_RE.lastIndex = i;
      const match = NUMBER_RE.exec(text);
      if (match && isStop(text.charCodeAt(i + match[0].length))) {
        push("number", i, i + match[0].length);
        i += match[0].length;
        continue;
      }
    } else if (
      (text.startsWith("true", i) && isStop(text.charCodeAt(i + 4))) ||
      (text.startsWith("null", i) && isStop(text.charCodeAt(i + 4)))
    ) {
      push(text.charCodeAt(i) === 116 ? "boolean" : "null", i, i + 4);
      i += 4;
      continue;
    } else if (text.startsWith("false", i) && isStop(text.charCodeAt(i + 5))) {
      push("boolean", i, i + 5);
      i += 5;
      continue;
    }
    // Anything else: a bare word up to the next delimiter.
    let j = i + 1;
    while (j < n && !isStop(text.charCodeAt(j))) j += 1;
    push("error", i, j);
    i = j;
  }
  return tokens;
}

function findPreviousSolid(tokens: Token[], from: number): Token | null {
  for (let k = from; k >= 0; k -= 1) {
    if (tokens[k].kind !== "ws") return tokens[k];
  }
  return null;
}

/** Tokens grouped per logical line; the line-break tokens themselves are dropped. */
export function splitLines(text: string, tokens: Token[]): Token[][] {
  const lines: Token[][] = [[]];
  for (const token of tokens) {
    if (
      token.kind === "ws" &&
      token.end - token.start === 1 &&
      text.charCodeAt(token.start) === CH_LF
    ) {
      lines.push([]);
    } else {
      lines[lines.length - 1].push(token);
    }
  }
  return lines;
}

export function offsetToLineColumn(text: string, offset: number): { line: number; column: number } {
  const at = Math.max(0, Math.min(offset, text.length));
  let line = 1;
  let lineStart = 0;
  for (let i = 0; i < at; i += 1) {
    if (text.charCodeAt(i) === CH_LF) {
      line += 1;
      lineStart = i + 1;
    }
  }
  return { line, column: at - lineStart + 1 };
}

export function lineCount(text: string): number {
  let count = 1;
  for (let i = 0; i < text.length; i += 1) {
    if (text.charCodeAt(i) === CH_LF) count += 1;
  }
  return count;
}

type Expect = "value" | "key" | "colon" | "more" | "end";

function describe(text: string, token: Token): string {
  if (token.kind === "error") {
    const raw = text.slice(token.start, token.end);
    if (raw.startsWith('"')) return "Unterminated string";
    const shown = raw.length > 24 ? `${raw.slice(0, 24)}…` : raw;
    return `Unexpected "${shown}"`;
  }
  if (token.kind === "punct") return `Unexpected "${text[token.start]}"`;
  return `Unexpected ${token.kind === "key" ? "string" : token.kind}`;
}

/**
 * Reports the first grammar problem, or null when `text` is a well-formed JSON
 * document. Iterative, so a pathologically deep document cannot overflow the
 * stack. Numbers and literals are checked as strictly as the server does
 * (`01`, `+1`, `.5`, `NaN` are all rejected).
 */
export function checkJson(text: string): JsonProblem | null {
  const tokens = tokenizeJson(text);
  const stack: Array<"object" | "array"> = [];
  let expect: Expect = "value";
  let afterComma = false;
  const problem = (offset: number, message: string): JsonProblem => ({
    offset,
    ...offsetToLineColumn(text, offset),
    message,
  });
  const afterValue = (): Expect => (stack.length === 0 ? "end" : "more");
  for (const token of tokens) {
    if (token.kind === "ws") continue;
    const c = text.charCodeAt(token.start);
    if (expect === "end") {
      return problem(token.start, "Unexpected content after the JSON value");
    }
    if (token.kind === "error") return problem(token.start, describe(text, token));
    switch (expect) {
      case "value": {
        if (token.kind === "punct") {
          if (c === CH_LBRACE) {
            stack.push("object");
            expect = "key";
            afterComma = false;
            continue;
          }
          if (c === CH_LBRACKET) {
            stack.push("array");
            expect = "value";
            afterComma = false;
            continue;
          }
          if (c === CH_RBRACKET && stack[stack.length - 1] === "array") {
            if (afterComma) return problem(token.start, 'Trailing comma before "]"');
            stack.pop();
            expect = afterValue();
            afterComma = false;
            continue;
          }
          return problem(token.start, `Expected a value, got "${text[token.start]}"`);
        }
        expect = afterValue();
        afterComma = false;
        continue;
      }
      case "key": {
        if (token.kind === "punct" && c === CH_RBRACE) {
          if (afterComma) return problem(token.start, 'Trailing comma before "}"');
          stack.pop();
          expect = afterValue();
          afterComma = false;
          continue;
        }
        if (token.kind === "key" || token.kind === "string") {
          expect = "colon";
          afterComma = false;
          continue;
        }
        return problem(token.start, "Expected a property name in double quotes");
      }
      case "colon": {
        if (token.kind === "punct" && c === CH_COLON) {
          expect = "value";
          continue;
        }
        return problem(token.start, 'Expected ":" after the property name');
      }
      case "more": {
        const top = stack[stack.length - 1];
        if (token.kind === "punct") {
          if (c === CH_COMMA) {
            expect = top === "object" ? "key" : "value";
            afterComma = true;
            continue;
          }
          if ((c === CH_RBRACE && top === "object") || (c === CH_RBRACKET && top === "array")) {
            stack.pop();
            expect = afterValue();
            continue;
          }
        }
        return problem(
          token.start,
          `Expected "," or "${top === "object" ? "}" : "]"}", got ${describe(text, token).replace(/^Unexpected /, "")}`,
        );
      }
    }
  }
  if (expect === "end") return null;
  const message =
    expect === "value" && stack.length === 0 ? "Expected a JSON value" : "Unexpected end of input";
  return problem(text.length, message);
}

function reemit(text: string, indent: string | null): string | null {
  if (checkJson(text) !== null) return null;
  const tokens = tokenizeJson(text).filter((token) => token.kind !== "ws");
  const out: string[] = [];
  let depth = 0;
  const pad = (level: number) => (indent === null ? "" : `\n${indent.repeat(level)}`);
  for (let k = 0; k < tokens.length; k += 1) {
    const token = tokens[k];
    const raw = text.slice(token.start, token.end);
    if (token.kind !== "punct") {
      out.push(raw);
      continue;
    }
    const c = text.charCodeAt(token.start);
    if (c === CH_LBRACE || c === CH_LBRACKET) {
      const next = tokens[k + 1];
      const closer = c === CH_LBRACE ? CH_RBRACE : CH_RBRACKET;
      if (next && next.kind === "punct" && text.charCodeAt(next.start) === closer) {
        out.push(raw, text.slice(next.start, next.end));
        k += 1;
        continue;
      }
      depth += 1;
      out.push(raw, pad(depth));
      continue;
    }
    if (c === CH_RBRACE || c === CH_RBRACKET) {
      depth -= 1;
      out.push(pad(depth), raw);
      continue;
    }
    if (c === CH_COMMA) {
      out.push(",", pad(depth));
      continue;
    }
    out.push(indent === null ? ":" : ": ");
  }
  return out.join("");
}

/** Two-space pretty print, or null when `text` is not well-formed JSON. Only whitespace changes. */
export function formatJson(text: string): string | null {
  return reemit(text, "  ");
}

/** Whitespace-free rendering, or null when `text` is not well-formed JSON. Only whitespace changes. */
export function minifyJson(text: string): string | null {
  return reemit(text, null);
}

/** True when both texts are well-formed JSON that differ only in whitespace. */
export function jsonEquivalent(a: string, b: string): boolean {
  const left = minifyJson(a);
  if (left === null) return false;
  const right = minifyJson(b);
  return right !== null && left === right;
}

/** The bytes the console sends for a value: JSON is stored minified, everything else verbatim. */
export function canonicalParameterValue(value: string, contentType: string): string {
  if (contentType !== "json") return value;
  return minifyJson(value) ?? value;
}

/** Whether saving `next` over `current` would change the stored value. */
export function valuesEquivalent(next: string, current: string, contentType: string): boolean {
  if (next === current) return true;
  return contentType === "json" && jsonEquivalent(next, current);
}

export function overHighlightCap(text: string): boolean {
  return byteLength(text) > HIGHLIGHT_MAX_BYTES;
}

function inferNode(value: unknown, depth: number): JsonSchema {
  if (typeof value === "string") return { type: "string" };
  if (typeof value === "boolean") return { type: "boolean" };
  if (typeof value === "number") {
    return { type: Number.isInteger(value) ? "integer" : "number" };
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return {};
    const kinds = new Set(value.map((item) => typeof item));
    const kind = kinds.size === 1 ? [...kinds][0] : null;
    if (kind === "string" || kind === "boolean") return { type: "array", items: { type: kind } };
    if (kind === "number") {
      const integer = value.every((item) => Number.isInteger(item));
      return { type: "array", items: { type: integer ? "integer" : "number" } };
    }
    return {};
  }
  if (value !== null && typeof value === "object" && depth < MAX_FORM_DEPTH) {
    const properties: Record<string, JsonSchema> = {};
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      properties[key] = inferNode(child, depth + 1);
    }
    return { type: "object", properties, additionalProperties: true };
  }
  return {};
}

/**
 * A schema shaped like `value`, so the field editor can render a document
 * that has no pinned schema. Nothing is required and no descriptions or
 * bounds exist; mixed lists and `null` become raw-JSON fields. Null when the
 * root is not an object.
 */
export function inferSchema(value: unknown): JsonSchema | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  return inferNode(value, 0);
}
