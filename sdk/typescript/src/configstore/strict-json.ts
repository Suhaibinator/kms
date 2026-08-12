/** A lossless JSON syntax tree used by the managed configuration decoder. */
export type JsonNode =
  | { readonly kind: "null" }
  | { readonly kind: "boolean"; readonly value: boolean }
  | { readonly kind: "string"; readonly value: string }
  | { readonly kind: "number"; readonly lexeme: string }
  | { readonly kind: "array"; readonly elements: readonly JsonNode[] }
  | { readonly kind: "object"; readonly properties: readonly JsonProperty[] };

export interface JsonProperty {
  readonly name: string;
  readonly value: JsonNode;
}

const MAX_JSON_DEPTH = 1_000;

/** Deliberately does not contain a source offset, token, property, or value. */
export class InvalidJsonDocumentError extends Error {
  constructor() {
    super("configstore: invalid JSON document");
    this.name = "InvalidJsonDocumentError";
  }
}

/**
 * Parse a single JSON document while retaining property order, duplicate
 * properties, and the exact spelling of every JSON number.
 */
export function parseStrictJson(document: string): JsonNode {
  if (typeof document !== "string") throw new InvalidJsonDocumentError();
  try {
    const parser = new JsonParser(document);
    const result = parser.parseValue(0);
    parser.skipWhitespace();
    if (!parser.atEnd()) throw new InvalidJsonDocumentError();
    return result;
  } catch (error) {
    if (error instanceof InvalidJsonDocumentError) throw error;
    throw new InvalidJsonDocumentError();
  }
}

/** Serialize the lossless tree. Object properties remain ordered and duplicated. */
export function stringifyJsonNode(node: JsonNode): string {
  switch (node.kind) {
    case "null":
      return "null";
    case "boolean":
      return node.value ? "true" : "false";
    case "string":
      return JSON.stringify(node.value);
    case "number":
      return node.lexeme;
    case "array":
      return `[${node.elements.map(stringifyJsonNode).join(",")}]`;
    case "object":
      return `{${node.properties
        .map(({ name, value }) => `${JSON.stringify(name)}:${stringifyJsonNode(value)}`)
        .join(",")}}`;
  }
}

class JsonParser {
  readonly #source: string;
  #offset = 0;

  constructor(source: string) {
    this.#source = source;
  }

  atEnd(): boolean {
    return this.#offset === this.#source.length;
  }

  skipWhitespace(): void {
    while (this.#offset < this.#source.length) {
      const code = this.#source.charCodeAt(this.#offset);
      if (code !== 0x20 && code !== 0x09 && code !== 0x0a && code !== 0x0d) return;
      this.#offset += 1;
    }
  }

  parseValue(depth: number): JsonNode {
    if (depth > MAX_JSON_DEPTH) throw new InvalidJsonDocumentError();
    this.skipWhitespace();
    const character = this.#source[this.#offset];
    switch (character) {
      case "n":
        this.#consumeLiteral("null");
        return { kind: "null" };
      case "t":
        this.#consumeLiteral("true");
        return { kind: "boolean", value: true };
      case "f":
        this.#consumeLiteral("false");
        return { kind: "boolean", value: false };
      case '"':
        return { kind: "string", value: this.#parseString() };
      case "[":
        return this.#parseArray(depth);
      case "{":
        return this.#parseObject(depth);
      default:
        if (character === "-" || isDigit(character)) return this.#parseNumber();
        throw new InvalidJsonDocumentError();
    }
  }

  #consumeLiteral(literal: string): void {
    if (this.#source.slice(this.#offset, this.#offset + literal.length) !== literal) {
      throw new InvalidJsonDocumentError();
    }
    this.#offset += literal.length;
  }

  #parseArray(depth: number): JsonNode {
    this.#offset += 1;
    const elements: JsonNode[] = [];
    this.skipWhitespace();
    if (this.#source[this.#offset] === "]") {
      this.#offset += 1;
      return { kind: "array", elements };
    }
    while (true) {
      elements.push(this.parseValue(depth + 1));
      this.skipWhitespace();
      const separator = this.#source[this.#offset];
      this.#offset += 1;
      if (separator === "]") return { kind: "array", elements };
      if (separator !== ",") throw new InvalidJsonDocumentError();
    }
  }

  #parseObject(depth: number): JsonNode {
    this.#offset += 1;
    const properties: JsonProperty[] = [];
    this.skipWhitespace();
    if (this.#source[this.#offset] === "}") {
      this.#offset += 1;
      return { kind: "object", properties };
    }
    while (true) {
      this.skipWhitespace();
      if (this.#source[this.#offset] !== '"') throw new InvalidJsonDocumentError();
      const name = this.#parseString();
      this.skipWhitespace();
      if (this.#source[this.#offset] !== ":") throw new InvalidJsonDocumentError();
      this.#offset += 1;
      properties.push({ name, value: this.parseValue(depth + 1) });
      this.skipWhitespace();
      const separator = this.#source[this.#offset];
      this.#offset += 1;
      if (separator === "}") return { kind: "object", properties };
      if (separator !== ",") throw new InvalidJsonDocumentError();
    }
  }

  #parseString(): string {
    const start = this.#offset;
    this.#offset += 1;
    while (this.#offset < this.#source.length) {
      const code = this.#source.charCodeAt(this.#offset);
      if (code === 0x22) {
        this.#offset += 1;
        const encoded = this.#source.slice(start, this.#offset);
        const decoded: unknown = JSON.parse(encoded);
        if (typeof decoded !== "string") throw new InvalidJsonDocumentError();
        // Go's encoding/json replaces escaped unmatched UTF-16 surrogates
        // with U+FFFD. JSON.parse preserves the unmatched code unit.
        return normalizeUnpairedSurrogates(decoded);
      }
      if (code < 0x20) throw new InvalidJsonDocumentError();
      if (code === 0x5c) {
        this.#offset += 1;
        const escapeCode = this.#source[this.#offset];
        if (escapeCode === "u") {
          for (let index = 1; index <= 4; index += 1) {
            if (!isHex(this.#source[this.#offset + index])) throw new InvalidJsonDocumentError();
          }
          this.#offset += 5;
          continue;
        }
        if (!escapeCode || !'"\\/bfnrt'.includes(escapeCode)) {
          throw new InvalidJsonDocumentError();
        }
        this.#offset += 1;
        continue;
      }
      this.#offset += 1;
    }
    throw new InvalidJsonDocumentError();
  }

  #parseNumber(): JsonNode {
    const start = this.#offset;
    if (this.#source[this.#offset] === "-") this.#offset += 1;
    if (this.#source[this.#offset] === "0") {
      this.#offset += 1;
      if (isDigit(this.#source[this.#offset])) throw new InvalidJsonDocumentError();
    } else {
      if (!isNonzeroDigit(this.#source[this.#offset])) throw new InvalidJsonDocumentError();
      while (isDigit(this.#source[this.#offset])) this.#offset += 1;
    }
    if (this.#source[this.#offset] === ".") {
      this.#offset += 1;
      if (!isDigit(this.#source[this.#offset])) throw new InvalidJsonDocumentError();
      while (isDigit(this.#source[this.#offset])) this.#offset += 1;
    }
    const exponentMarker = this.#source[this.#offset];
    if (exponentMarker === "e" || exponentMarker === "E") {
      this.#offset += 1;
      const sign = this.#source[this.#offset];
      if (sign === "+" || sign === "-") this.#offset += 1;
      if (!isDigit(this.#source[this.#offset])) throw new InvalidJsonDocumentError();
      while (isDigit(this.#source[this.#offset])) this.#offset += 1;
    }
    return { kind: "number", lexeme: this.#source.slice(start, this.#offset) };
  }
}

function isDigit(character: string | undefined): boolean {
  return character !== undefined && character >= "0" && character <= "9";
}

function isNonzeroDigit(character: string | undefined): boolean {
  return character !== undefined && character >= "1" && character <= "9";
}

function isHex(character: string | undefined): boolean {
  return (
    character !== undefined &&
    ((character >= "0" && character <= "9") ||
      (character >= "a" && character <= "f") ||
      (character >= "A" && character <= "F"))
  );
}

function normalizeUnpairedSurrogates(value: string): string {
  let result = "";
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        result += `${value[index] ?? ""}${value[index + 1] ?? ""}`;
        index += 1;
      } else {
        result += "\ufffd";
      }
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      result += "\ufffd";
    } else {
      result += value[index] ?? "";
    }
  }
  return result;
}
