import { sha256Hex } from "../releases/digest.js";
import { type JsonNode, parseStrictJson } from "./strict-json.js";

const strictUtf8 = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

/**
 * Canonical byte form of one parameter value so that semantically identical
 * documents hash identically regardless of how they were written. The KMS
 * server and the Go SDK apply the same rules, so the sides never disagree on
 * formatting.
 *
 * For contentType "json" the document is parsed strictly (exactly one value,
 * no trailing data, duplicate object keys anywhere rejected, valid UTF-8
 * required) and re-encoded compactly with object keys sorted by their UTF-8
 * bytes. Number literals are preserved verbatim (1.0 and 1 remain distinct),
 * strings use the minimal JSON escaping (only ", \\, and U+0000–U+001F are
 * escaped; everything else, including U+2028, is raw UTF-8), and null, true,
 * and false are literal. Every other content type is returned byte-for-byte.
 */
export function canonicalParameterValue(
  contentType: string,
  value: string | Uint8Array,
): Uint8Array {
  if (typeof contentType !== "string") {
    throw new TypeError("configstore: canonical content type must be a string");
  }
  if (typeof value !== "string" && !(value instanceof Uint8Array)) {
    throw new TypeError("configstore: canonical value must be a string or Uint8Array");
  }
  if (contentType !== "json") {
    return typeof value === "string" ? Buffer.from(value, "utf8") : Uint8Array.from(value);
  }
  const text = decodeStrictUtf8(value);
  let node: JsonNode;
  try {
    node = parseStrictJson(text);
  } catch {
    throw new Error("configstore: canonical json: invalid document");
  }
  assertUniqueKeys(node);
  return Buffer.from(writeCanonical(node), "utf8");
}

/** Lowercase hex SHA-256 of canonicalParameterValue. */
export function parameterHash(contentType: string, value: string | Uint8Array): string {
  return sha256Hex(canonicalParameterValue(contentType, value));
}

function decodeStrictUtf8(value: string | Uint8Array): string {
  if (typeof value === "string") {
    if (!wellFormed(value)) throw new Error("configstore: canonical json: invalid UTF-8");
    return value;
  }
  try {
    return strictUtf8.decode(value);
  } catch {
    throw new Error("configstore: canonical json: invalid UTF-8");
  }
}

/** A raw lone surrogate has no UTF-8 encoding, so it is rejected like invalid input bytes. */
function wellFormed(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function assertUniqueKeys(node: JsonNode): void {
  if (node.kind === "array") {
    for (const element of node.elements) assertUniqueKeys(element);
    return;
  }
  if (node.kind !== "object") return;
  const seen = new Set<string>();
  for (const { name, value } of node.properties) {
    if (seen.has(name)) throw new Error("configstore: canonical json: duplicate object key");
    seen.add(name);
    assertUniqueKeys(value);
  }
}

function writeCanonical(node: JsonNode): string {
  switch (node.kind) {
    case "null":
      return "null";
    case "boolean":
      return node.value ? "true" : "false";
    case "number":
      return node.lexeme;
    case "string":
      return JSON.stringify(node.value);
    case "array":
      return `[${node.elements.map(writeCanonical).join(",")}]`;
    case "object": {
      const properties = [...node.properties].sort((left, right) =>
        Buffer.compare(Buffer.from(left.name, "utf8"), Buffer.from(right.name, "utf8")),
      );
      return `{${properties
        .map(({ name, value }) => `${JSON.stringify(name)}:${writeCanonical(value)}`)
        .join(",")}}`;
    }
  }
}
