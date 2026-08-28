// Contract ⇄ schema derivation for the application wizard and definition
// card. Pure functions over strings and plain objects; the JSON-type ↔
// content-type mapping mirrors the Go table `jsonTypeToContentType` and is
// pinned by the shared readiness fixture.
//
// Only parameter aliases enter the validated object (internal/core/releases.go);
// secrets never appear in a schema, so every function here skips them.

import type { ApplicationContractField } from "@/lib/types";
import { type ParameterContentType, validateContract } from "@/lib/validation";

export type ContractEntry = ApplicationContractField;

export const CONTRACT_FILE_FORMAT = "kms-config-contract/v1";

export interface ParsedContractFile {
  contract: ContractEntry[];
  /** The generated artifact's hash of its sibling schema file, when present. */
  schema_sha256?: string;
  source: "envelope" | "array";
}

type Json = Record<string, unknown>;

const isObject = (value: unknown): value is Json =>
  typeof value === "object" && value !== null && !Array.isArray(value);

function projectEntry(raw: unknown, fallbackKind?: "parameter" | "secret"): ContractEntry {
  if (!isObject(raw)) throw new Error("Each contract entry must be an object.");
  const alias = typeof raw.alias === "string" ? raw.alias : "";
  const kind = typeof raw.kind === "string" ? raw.kind : (fallbackKind ?? "");
  if (kind !== "parameter" && kind !== "secret") {
    throw new Error(`Entry ${alias || "(no alias)"}: kind must be "parameter" or "secret".`);
  }
  const entry: ContractEntry = { alias, kind };
  if (kind === "parameter") {
    entry.content_type = typeof raw.content_type === "string" ? raw.content_type : "";
  }
  return entry;
}

/**
 * Accepts either the generated `kms-config-contract/v1` envelope (groups ∪
 * secrets, projected to {alias, kind, content_type}) or a bare
 * `[{alias, kind, content_type}]` array. Throws with a human message on
 * anything else, including a contract the server would refuse.
 */
export function parseContractFile(text: string): ParsedContractFile {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    throw new Error(
      `Contract file is not valid JSON: ${err instanceof Error ? err.message : String(err)}`,
    );
  }

  let contract: ContractEntry[];
  let source: ParsedContractFile["source"];
  let schemaSha: string | undefined;
  if (Array.isArray(parsed)) {
    contract = parsed.map((entry) => projectEntry(entry));
    source = "array";
  } else if (isObject(parsed) && parsed.format === CONTRACT_FILE_FORMAT) {
    const groups = Array.isArray(parsed.groups) ? parsed.groups : [];
    const secrets = Array.isArray(parsed.secrets) ? parsed.secrets : [];
    contract = [
      ...groups.map((entry) => projectEntry(entry, "parameter")),
      ...secrets.map((entry) => projectEntry(entry, "secret")),
    ];
    source = "envelope";
    if (typeof parsed.schema_sha256 === "string" && parsed.schema_sha256 !== "") {
      schemaSha = parsed.schema_sha256.toLowerCase();
    }
  } else if (isObject(parsed) && typeof parsed.format === "string") {
    throw new Error(
      `Unsupported contract format "${parsed.format}" (expected ${CONTRACT_FILE_FORMAT}).`,
    );
  } else {
    throw new Error(
      `Expected a ${CONTRACT_FILE_FORMAT} document or an array of {alias, kind, content_type} entries.`,
    );
  }

  const invalid = validateContract(contract);
  if (invalid) throw new Error(invalid);
  const result: ParsedContractFile = { contract, source };
  if (schemaSha) result.schema_sha256 = schemaSha;
  return result;
}

// --- JSON Schema type ↔ parameter content type --------------------------------

const BINARY_FORMAT = "kms-base64";

/** A property's single JSON type, or null when absent or a union. */
function singleType(property: unknown): string | null {
  if (!isObject(property)) return null;
  const type = property.type;
  if (typeof type === "string") return type;
  if (Array.isArray(type) && type.length === 1 && typeof type[0] === "string") return type[0];
  return null;
}

/**
 * Mirrors Go's `jsonTypeToContentType`: object|array → json, string → string
 * (format kms-base64 → binary), integer → integer, number → float,
 * boolean → boolean; a union or absent type → json.
 */
export function jsonTypeToContentType(property: unknown): ParameterContentType {
  const type = singleType(property);
  switch (type) {
    case "object":
    case "array":
      return "json";
    case "string":
      return isObject(property) && property.format === BINARY_FORMAT ? "binary" : "string";
    case "integer":
      return "integer";
    case "number":
      return "float";
    case "boolean":
      return "boolean";
    default:
      return "json";
  }
}

/** True when the property pins a type at all (so a mismatch is meaningful). */
function isDefinitive(property: unknown): boolean {
  return singleType(property) !== null;
}

/** The reverse table: json → {} (anything), others → {type} (+ format for binary). */
export function contentTypeToSchema(contentType: string | undefined): Json {
  switch (contentType) {
    case "string":
      return { type: "string" };
    case "binary":
      return { type: "string", format: BINARY_FORMAT };
    case "integer":
      return { type: "integer" };
    case "float":
      return { type: "number" };
    case "boolean":
      return { type: "boolean" };
    default:
      return {};
  }
}

// --- Schema parsing --------------------------------------------------------------

const DIALECT_2020_12 = /^https?:\/\/json-schema\.org\/draft\/2020-12\/schema#?$/;

export const SCHEMA_2020_12 = "https://json-schema.org/draft/2020-12/schema";

interface ParsedSchema {
  root: Json;
  properties: Record<string, unknown>;
  required: string[];
  additionalPropertiesFalse: boolean;
}

type SchemaParse =
  | { ok: true; schema: ParsedSchema }
  | { ok: false; code: "schema_unparseable" | "unsupported_dialect"; detail: string };

function parseSchema(schemaJson: string): SchemaParse {
  let parsed: unknown;
  try {
    parsed = JSON.parse(schemaJson);
  } catch (err) {
    return {
      ok: false,
      code: "schema_unparseable",
      detail: `Schema is not valid JSON: ${err instanceof Error ? err.message : String(err)}`,
    };
  }
  if (!isObject(parsed)) {
    return { ok: false, code: "schema_unparseable", detail: "Schema must be a JSON object." };
  }
  const dialect = parsed.$schema;
  if (dialect !== undefined && !(typeof dialect === "string" && DIALECT_2020_12.test(dialect))) {
    return {
      ok: false,
      code: "unsupported_dialect",
      detail: `Only JSON Schema 2020-12 is supported (found $schema ${JSON.stringify(dialect)}).`,
    };
  }
  const properties = isObject(parsed.properties) ? parsed.properties : {};
  const required = Array.isArray(parsed.required)
    ? parsed.required.filter((name): name is string => typeof name === "string")
    : [];
  return {
    ok: true,
    schema: {
      root: parsed,
      properties,
      required,
      additionalPropertiesFalse: parsed.additionalProperties === false,
    },
  };
}

// --- Derivation -----------------------------------------------------------------

export interface DerivedContract {
  contract: ContractEntry[];
  /** Things the derivation decided for the user, worth showing. */
  notes: string[];
}

/**
 * Builds a contract from a schema's top-level properties. Existing secret
 * entries are kept (they are never in the schema) and an existing parameter's
 * content type is kept when the schema does not pin a type. Existing
 * parameters the schema no longer mentions are dropped with a note.
 */
export function deriveContractFromSchema(
  schemaJson: string,
  existing: readonly ContractEntry[] = [],
): DerivedContract {
  const notes: string[] = [];
  const parsed = parseSchema(schemaJson);
  if (!parsed.ok) {
    return { contract: [...existing], notes: [parsed.detail] };
  }
  const { properties, required } = parsed.schema;
  const existingByAlias = new Map(existing.map((entry) => [entry.alias, entry]));
  const contract: ContractEntry[] = [];

  for (const [alias, property] of Object.entries(properties)) {
    const current = existingByAlias.get(alias);
    if (current?.kind === "secret") {
      notes.push(
        `\`${alias}\` is a secret in the contract but also a schema property; kept as a secret (secrets are never validated by the schema).`,
      );
      contract.push({ ...current });
      continue;
    }
    let contentType: string = jsonTypeToContentType(property);
    if (!isDefinitive(property)) {
      if (current?.content_type) {
        contentType = current.content_type;
        notes.push(
          `\`${alias}\` has no single JSON type in the schema; kept its existing content type ${contentType}.`,
        );
      } else {
        notes.push(`\`${alias}\` has no single JSON type in the schema; defaulted to json.`);
      }
    } else if (current?.content_type && current.content_type !== contentType) {
      notes.push(
        `\`${alias}\` changed from ${current.content_type} to ${contentType} to match the schema.`,
      );
    }
    contract.push({ alias, kind: "parameter", content_type: contentType });
  }

  for (const entry of existing) {
    if (entry.kind === "secret" && !(entry.alias in properties)) {
      contract.push({ ...entry });
    } else if (entry.kind === "parameter" && !(entry.alias in properties)) {
      notes.push(`\`${entry.alias}\` is not a schema property and was dropped.`);
    }
  }

  for (const name of required) {
    if (!(name in properties)) {
      notes.push(`The schema requires \`${name}\` but defines no such property.`);
    }
  }
  if (Object.keys(properties).length === 0) {
    notes.push("The schema defines no top-level properties.");
  }

  return { contract, notes };
}

export interface DerivedSchema {
  schemaJson: string;
  notes: string[];
}

/**
 * Builds a 2020-12 schema from the contract's parameter aliases: json → {},
 * others → {type}; every parameter alias is required and
 * additionalProperties is false. When an existing schema is given, each
 * property's body is merged (constraints, descriptions and nested shapes
 * survive) and its other top-level keywords are carried over.
 */
export function deriveSchemaFromContract(
  contract: readonly ContractEntry[],
  existingSchemaJson?: string | null,
): DerivedSchema {
  const notes: string[] = [];
  let existing: ParsedSchema | null = null;
  if (existingSchemaJson) {
    const parsed = parseSchema(existingSchemaJson);
    if (parsed.ok) existing = parsed.schema;
    else notes.push(`${parsed.detail} Starting from an empty schema.`);
  }

  const parameters = contract.filter((entry) => entry.kind === "parameter");
  const properties: Json = {};
  for (const entry of parameters) {
    const derived = contentTypeToSchema(entry.content_type);
    const previous = existing?.properties[entry.alias];
    if (isObject(previous)) {
      const merged: Json = { ...previous, ...derived };
      // A former binary property keeps `format: kms-base64` only while it is
      // still binary; otherwise the leftover format would flip it back.
      if (entry.content_type !== "binary" && merged.format === BINARY_FORMAT) {
        delete merged.format;
      }
      properties[entry.alias] = merged;
    } else {
      properties[entry.alias] = derived;
    }
  }

  if (existing) {
    for (const name of Object.keys(existing.properties)) {
      if (!(name in properties)) {
        notes.push(`Schema property \`${name}\` is not a contract parameter and was dropped.`);
      }
    }
  }
  if (parameters.length === 0) notes.push("The contract has no parameter aliases.");

  const carried: Json = {};
  if (existing) {
    for (const [key, value] of Object.entries(existing.root)) {
      if (!["$schema", "type", "properties", "required", "additionalProperties"].includes(key)) {
        carried[key] = value;
      }
    }
  }
  const schema: Json = {
    $schema: SCHEMA_2020_12,
    ...carried,
    type: "object",
    properties,
    required: parameters.map((entry) => entry.alias),
    additionalProperties: false,
  };
  return { schemaJson: JSON.stringify(schema, null, 2), notes };
}

// --- Alignment -----------------------------------------------------------------

export type AlignmentIssueCode =
  | "missing_in_contract"
  | "missing_in_schema"
  | "required_missing"
  | "content_type_mismatch"
  | "schema_unparseable"
  | "unsupported_dialect";

export interface AlignmentIssue {
  code: AlignmentIssueCode;
  alias?: string;
  severity: "error" | "warning";
  detail: string;
}

export interface AlignmentResult {
  /** True only when there are no issues at all, warnings included. */
  aligned: boolean;
  issues: AlignmentIssue[];
}

/**
 * Compares the contract's parameter aliases with the schema's top-level
 * properties. Secrets never count. A parameter missing from the schema is an
 * error under `additionalProperties: false` (the release would fail
 * validation) and a warning otherwise. No schema → nothing to check.
 */
export function checkContractAlignment(
  contract: readonly ContractEntry[],
  schemaJson: string | null | undefined,
): AlignmentResult {
  if (!schemaJson) return { aligned: true, issues: [] };
  const parsed = parseSchema(schemaJson);
  if (!parsed.ok) {
    return {
      aligned: false,
      issues: [{ code: parsed.code, severity: "error", detail: parsed.detail }],
    };
  }
  const { properties, required, additionalPropertiesFalse } = parsed.schema;
  const issues: AlignmentIssue[] = [];
  const parameters = contract.filter((entry) => entry.kind === "parameter");
  const secretAliases = new Set(
    contract.filter((entry) => entry.kind === "secret").map((entry) => entry.alias),
  );
  const parameterAliases = new Set(parameters.map((entry) => entry.alias));
  const requiredSet = new Set(required);

  for (const entry of parameters) {
    const property = properties[entry.alias];
    if (property === undefined) {
      issues.push({
        code: "missing_in_schema",
        alias: entry.alias,
        severity: additionalPropertiesFalse ? "error" : "warning",
        detail: additionalPropertiesFalse
          ? `\`${entry.alias}\` is not a schema property and additionalProperties is false, so validation rejects it.`
          : `\`${entry.alias}\` is not a schema property, so its value is never validated.`,
      });
      continue;
    }
    if (!requiredSet.has(entry.alias)) {
      issues.push({
        code: "required_missing",
        alias: entry.alias,
        severity: "warning",
        detail: `\`${entry.alias}\` is a schema property but not required; a release missing it would still validate.`,
      });
    }
    if (isDefinitive(property)) {
      const expected = jsonTypeToContentType(property);
      if (entry.content_type !== expected) {
        issues.push({
          code: "content_type_mismatch",
          alias: entry.alias,
          severity: "warning",
          detail: `\`${entry.alias}\` is ${entry.content_type || "untyped"} in the contract but the schema type maps to ${expected}.`,
        });
      }
    }
  }

  for (const name of Object.keys(properties)) {
    if (parameterAliases.has(name) || secretAliases.has(name)) continue;
    issues.push({
      code: "missing_in_contract",
      alias: name,
      severity: requiredSet.has(name) ? "error" : "warning",
      detail: requiredSet.has(name)
        ? `Schema requires \`${name}\` but the contract has no such alias; every release fails validation.`
        : `Schema property \`${name}\` has no contract alias.`,
    });
  }

  return { aligned: issues.length === 0, issues };
}

// --- Hashing ----------------------------------------------------------------------

/** SHA-256 of the UTF-8 bytes of `text`, lower-case hex — Web Crypto only. */
export async function sha256Hex(text: string): Promise<string> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    throw new Error("SHA-256 is unavailable: this page is not in a secure context.");
  }
  const digest = await subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** SHA-256 of JSON with insignificant whitespace removed, matching KMS schema registration. */
export async function schemaSha256Hex(text: string): Promise<string> {
  // Validate first. The lexical compaction below deliberately preserves number
  // spellings that JSON.parse/JSON.stringify could round in JavaScript.
  JSON.parse(text);
  let compact = "";
  let inString = false;
  let escaped = false;
  for (const character of text) {
    if (inString) {
      compact += character;
      if (escaped) {
        escaped = false;
      } else if (character === "\\") {
        escaped = true;
      } else if (character === '"') {
        inString = false;
      }
      continue;
    }
    if (character === '"') {
      inString = true;
      compact += character;
    } else if (!/\s/u.test(character)) {
      compact += character;
    }
  }
  return sha256Hex(compact);
}
