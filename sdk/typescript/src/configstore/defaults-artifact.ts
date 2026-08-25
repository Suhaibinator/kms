import type { ContractEntry, ContractKind } from "./contract.js";
import { type JsonNode, parseStrictJson } from "./strict-json.js";

export const DEFAULTS_ARTIFACT_FORMAT = "kms-config-defaults/v1" as const;
export const MAX_DEFAULTS_ARTIFACT_BYTES = 4 * 1024 * 1024;
export const MAX_DEFAULT_PARAMETER_VALUE_BYTES = 1024 * 1024;

const SCHEMA_SHA256_PATTERN = /^[0-9a-f]{64}$/u;
const ALIAS_PATTERN = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/u;

export interface DefaultsArtifactContractEntry {
  readonly alias: string;
  readonly kind: ContractKind;
  readonly contentType: string;
}

export interface DefaultsArtifactParameter {
  readonly alias: string;
  readonly contentType: string;
  /** Exact encoded value. This string is never parsed or normalized. */
  readonly value: string;
}

export interface DefaultsArtifact {
  readonly format: typeof DEFAULTS_ARTIFACT_FORMAT;
  readonly profile: string;
  readonly schemaSHA256: string;
  readonly contract: readonly DefaultsArtifactContractEntry[];
  readonly parameters: readonly DefaultsArtifactParameter[];
}

export interface EncodeDefaultsArtifactInput {
  readonly profile: string;
  readonly schemaSHA256: string;
  readonly contract: readonly ContractEntry[];
  readonly parameters: Readonly<Record<string, string>>;
}

/** A deliberately value-free validation error. */
export class DefaultsArtifactError extends Error {
  constructor(message: string) {
    super(`configstore defaults: ${message}`);
    this.name = "DefaultsArtifactError";
  }
}

/** Encode a canonical, newline-terminated defaults artifact. */
export function encodeDefaultsArtifact(input: EncodeDefaultsArtifactInput): string {
  if (!isRecord(input)) throw invalid("artifact input is invalid");
  const profile = validateDefaultsProfile(input.profile);
  const schemaSHA256 = schemaDigest(input.schemaSHA256);
  if (!Array.isArray(input.contract)) throw invalid("contract must be an array");
  if (!isRecord(input.parameters) || Array.isArray(input.parameters)) {
    throw invalid("parameters must be a string record");
  }

  const contract = normalizeContract(input.contract, false);
  const expectedParameters = new Map(
    contract
      .filter(({ kind }) => kind === "parameter")
      .map((entry) => [entry.alias, entry] as const),
  );
  const parameterAliases = Object.keys(input.parameters).sort(compareText);
  if (parameterAliases.length !== expectedParameters.size) {
    throw invalid("parameters do not exactly match the parameter contract");
  }
  const parameters: DefaultsArtifactParameter[] = [];
  for (const alias of parameterAliases) {
    const contractEntry = expectedParameters.get(alias);
    const value = input.parameters[alias];
    if (!contractEntry || typeof value !== "string") {
      throw invalid("parameters do not exactly match the parameter contract");
    }
    assertValueSize(value);
    parameters.push(
      Object.freeze({
        alias,
        contentType: contractEntry.contentType,
        value,
      }),
    );
  }

  const document = serializeArtifact({
    format: DEFAULTS_ARTIFACT_FORMAT,
    profile,
    schemaSHA256,
    contract,
    parameters,
  });
  assertArtifactSize(document);
  return document;
}

/** Strictly parse and validate a defaults artifact without exposing values in errors. */
export function parseDefaultsArtifact(document: string | Uint8Array): DefaultsArtifact {
  const source = decodeDocument(document);
  assertArtifactSize(source);
  let root: JsonNode;
  try {
    root = parseStrictJson(source);
  } catch {
    throw invalid("artifact is not valid JSON");
  }
  const rootProperties = exactObject(root, [
    "format",
    "profile",
    "schema_sha256",
    "contract",
    "parameters",
  ]);
  if (stringNode(rootProperties.get("format")) !== DEFAULTS_ARTIFACT_FORMAT) {
    throw invalid("format is unsupported");
  }
  const profile = validateDefaultsProfile(stringNode(rootProperties.get("profile")));
  const schemaSHA256 = schemaDigest(stringNode(rootProperties.get("schema_sha256")));
  const rawContract = arrayNode(rootProperties.get("contract")).map(parseContractEntry);
  const contract = normalizeContract(rawContract, true);
  const rawParameters = arrayNode(rootProperties.get("parameters")).map(parseParameterEntry);
  const parameters = normalizeParameters(rawParameters, contract);
  return Object.freeze({
    format: DEFAULTS_ARTIFACT_FORMAT,
    profile,
    schemaSHA256,
    contract,
    parameters,
  });
}

function normalizeContract(
  entries: readonly ContractEntry[],
  requireSorted: boolean,
): readonly DefaultsArtifactContractEntry[] {
  const normalized: DefaultsArtifactContractEntry[] = [];
  if (entries.length === 0) throw invalid("contract must not be empty");
  const aliases = new Set<string>();
  let previous = "";
  for (const entry of entries) {
    if (!isRecord(entry)) throw invalid("contract entry is invalid");
    const alias = validAlias(entry.alias);
    if (aliases.has(alias)) throw invalid("contract contains a duplicate alias");
    if (requireSorted && normalized.length > 0 && compareText(previous, alias) >= 0) {
      throw invalid("contract is not sorted by alias");
    }
    aliases.add(alias);
    previous = alias;
    const kind = contractKind(entry.kind);
    const contentType = entry.contentType ?? "";
    if (
      typeof contentType !== "string" ||
      hasEdgeGoWhitespace(contentType) ||
      hasUnpairedSurrogate(contentType) ||
      (kind === "parameter" && contentType.length === 0)
    ) {
      throw invalid("contract content type is invalid");
    }
    normalized.push(Object.freeze({ alias, kind, contentType }));
  }
  if (!requireSorted) normalized.sort((left, right) => compareText(left.alias, right.alias));
  return Object.freeze(normalized);
}

function normalizeParameters(
  entries: readonly DefaultsArtifactParameter[],
  contract: readonly DefaultsArtifactContractEntry[],
): readonly DefaultsArtifactParameter[] {
  const expected = new Map(
    contract
      .filter(({ kind }) => kind === "parameter")
      .map((entry) => [entry.alias, entry] as const),
  );
  if (entries.length !== expected.size) {
    throw invalid("parameters do not exactly match the parameter contract");
  }
  const seen = new Set<string>();
  let previous = "";
  const normalized: DefaultsArtifactParameter[] = [];
  for (const entry of entries) {
    const alias = validAlias(entry.alias);
    if (seen.has(alias)) throw invalid("parameters contain a duplicate alias");
    if (normalized.length > 0 && compareText(previous, alias) >= 0) {
      throw invalid("parameters are not sorted by alias");
    }
    seen.add(alias);
    previous = alias;
    const wanted = expected.get(alias);
    if (!wanted || entry.contentType !== wanted.contentType) {
      throw invalid("parameters do not exactly match the parameter contract");
    }
    assertValueSize(entry.value);
    normalized.push(Object.freeze({ ...entry }));
  }
  return Object.freeze(normalized);
}

function parseContractEntry(node: JsonNode): ContractEntry {
  const properties = exactObject(node, ["alias", "kind", "content_type"]);
  return {
    alias: stringNode(properties.get("alias")),
    kind: contractKind(stringNode(properties.get("kind"))),
    contentType: stringNode(properties.get("content_type")),
  };
}

function parseParameterEntry(node: JsonNode): DefaultsArtifactParameter {
  const properties = exactObject(node, ["alias", "content_type", "value"]);
  return {
    alias: stringNode(properties.get("alias")),
    contentType: stringNode(properties.get("content_type")),
    value: stringNode(properties.get("value")),
  };
}

function serializeArtifact(artifact: DefaultsArtifact): string {
  const contract = artifact.contract.map(({ alias, kind, contentType }) => ({
    alias,
    kind,
    content_type: contentType,
  }));
  const parameters = artifact.parameters.map(({ alias, contentType, value }) => ({
    alias,
    content_type: contentType,
    value,
  }));
  const encoded = JSON.stringify({
    format: artifact.format,
    profile: artifact.profile,
    schema_sha256: artifact.schemaSHA256,
    contract,
    parameters,
  });
  // Go's encoding/json always escapes line and paragraph separators, even
  // with HTML escaping disabled. JSON.stringify leaves them literal.
  return `${encoded.replace(/\u2028/gu, "\\u2028").replace(/\u2029/gu, "\\u2029")}\n`;
}

function exactObject(node: JsonNode, names: readonly string[]): ReadonlyMap<string, JsonNode> {
  if (node.kind !== "object") throw invalid("artifact structure is invalid");
  const allowed = new Set(names);
  const result = new Map<string, JsonNode>();
  for (const property of node.properties) {
    if (!allowed.has(property.name)) throw invalid("artifact contains an unknown property");
    if (result.has(property.name)) throw invalid("artifact contains a duplicate property");
    result.set(property.name, property.value);
  }
  if (result.size !== names.length) throw invalid("artifact is missing a required property");
  return result;
}

function arrayNode(node: JsonNode | undefined): readonly JsonNode[] {
  if (node?.kind !== "array") throw invalid("artifact structure is invalid");
  return node.elements;
}

function stringNode(node: JsonNode | undefined): string {
  if (node?.kind !== "string") throw invalid("artifact structure is invalid");
  return node.value;
}

function schemaDigest(value: unknown): string {
  if (typeof value !== "string" || !SCHEMA_SHA256_PATTERN.test(value)) {
    throw invalid("schema SHA-256 is invalid");
  }
  return value;
}

/** @internal Shared with the exporter so invalid profiles never reach providers. */
export function validateDefaultsProfile(value: unknown): string {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    hasEdgeGoWhitespace(value) ||
    hasUnpairedSurrogate(value)
  ) {
    throw invalid("profile is invalid");
  }
  return value;
}

function hasEdgeGoWhitespace(value: string): boolean {
  return isGoWhitespace(value.charCodeAt(0)) || isGoWhitespace(value.charCodeAt(value.length - 1));
}

function isGoWhitespace(code: number): boolean {
  return (
    (code >= 0x0009 && code <= 0x000d) ||
    code === 0x0020 ||
    code === 0x0085 ||
    code === 0x00a0 ||
    code === 0x1680 ||
    (code >= 0x2000 && code <= 0x200a) ||
    code === 0x2028 ||
    code === 0x2029 ||
    code === 0x202f ||
    code === 0x205f ||
    code === 0x3000
  );
}

function validAlias(value: unknown): string {
  if (typeof value !== "string" || !ALIAS_PATTERN.test(value)) {
    throw invalid("contract alias is invalid");
  }
  return value;
}

function contractKind(value: unknown): ContractKind {
  if (value !== "parameter" && value !== "secret") {
    throw invalid("contract kind is invalid");
  }
  return value;
}

function assertValueSize(value: string): void {
  if (hasUnpairedSurrogate(value)) throw invalid("parameter value is not valid UTF-8");
  if (Buffer.byteLength(value, "utf8") > MAX_DEFAULT_PARAMETER_VALUE_BYTES) {
    throw invalid("parameter value exceeds the maximum size");
  }
}

function assertArtifactSize(document: string): void {
  if (Buffer.byteLength(document, "utf8") > MAX_DEFAULTS_ARTIFACT_BYTES) {
    throw invalid("artifact exceeds the maximum size");
  }
}

function decodeDocument(document: string | Uint8Array): string {
  if (typeof document === "string") {
    if (hasUnpairedSurrogate(document)) throw invalid("artifact must be UTF-8 JSON");
    return document;
  }
  if (!(document instanceof Uint8Array)) throw invalid("artifact must be UTF-8 JSON");
  if (document.byteLength > MAX_DEFAULTS_ARTIFACT_BYTES) {
    throw invalid("artifact exceeds the maximum size");
  }
  try {
    // Preserving a leading BOM makes it invalid JSON, matching Go's decoder.
    return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(document);
  } catch {
    throw invalid("artifact must be UTF-8 JSON");
  }
}

function hasUnpairedSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function invalid(message: string): DefaultsArtifactError {
  return new DefaultsArtifactError(message);
}
