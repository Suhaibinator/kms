export const DESCRIPTOR_FORMAT = "kms-config-descriptor/v1" as const;
export const MAX_RELEASE_ENTRIES = 256;

export type ReloadPolicy = "hot" | "restart";

export type TypeDescriptor =
  | { readonly kind: "boolean" }
  | { readonly kind: "string" }
  | {
      readonly kind: "integer";
      readonly bits: number;
      readonly unsigned: boolean;
      readonly representation: "number" | "bigint";
    }
  | { readonly kind: "float"; readonly bits: 32 | 64 }
  | { readonly kind: "duration" }
  | { readonly kind: "bytes" }
  | { readonly kind: "nullable"; readonly value: TypeDescriptor }
  | { readonly kind: "array"; readonly element: TypeDescriptor }
  | {
      readonly kind: "fixedArray";
      readonly element: TypeDescriptor;
      readonly length: number;
    }
  | { readonly kind: "record"; readonly value: TypeDescriptor }
  | { readonly kind: "object"; readonly fields: readonly NestedFieldDescriptor[] };

export interface NestedFieldDescriptor {
  readonly property: string;
  readonly jsonName: string;
  readonly type: TypeDescriptor;
}

export interface FieldDescriptor extends NestedFieldDescriptor {
  readonly reload: ReloadPolicy;
  readonly views: readonly string[];
}

export interface GroupDescriptor {
  readonly alias: string;
  readonly fields: readonly FieldDescriptor[];
}

export interface SecretDescriptor {
  readonly property: string;
  readonly alias: string;
  readonly reload: ReloadPolicy;
  readonly views: readonly string[];
}

export interface ConfigDescriptor {
  readonly format: typeof DESCRIPTOR_FORMAT;
  readonly source: Readonly<{ module: string; type: string }>;
  readonly groups: readonly GroupDescriptor[];
  readonly secrets: readonly SecretDescriptor[];
}

const ALIAS_PATTERN = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/u;
const TYPE_NAME_PATTERN = /^[A-Za-z_$][A-Za-z0-9_$]*$/u;
const MAX_TYPE_DEPTH = 64;

/** Parse and normalize a JSON descriptor. Duplicate JSON keys are rejected. */
export function parseDescriptor(document: string): ConfigDescriptor {
  if (typeof document !== "string") throw descriptorError("descriptor must be a JSON string");
  let value: unknown;
  try {
    value = jsonNodeValue(parseStrictJson(document), "$");
  } catch (error) {
    if (error instanceof DescriptorError) throw error;
    throw descriptorError("descriptor is not valid JSON", error);
  }
  return normalizeDescriptor(value);
}

/** Validate, sort, and deeply freeze a TypeScript-friendly descriptor value. */
export function normalizeDescriptor(value: unknown): ConfigDescriptor {
  const root = expectObject(value, "$", ["format", "source", "groups", "secrets"]);
  if (root.format !== DESCRIPTOR_FORMAT) {
    throw descriptorError(`$.format must equal ${JSON.stringify(DESCRIPTOR_FORMAT)}`);
  }
  const source = expectObject(root.source, "$.source", ["module", "type"]);
  const moduleName = expectModuleSpecifier(source.module, "$.source.module");
  const typeName = expectNonemptyString(source.type, "$.source.type");
  if (!TYPE_NAME_PATTERN.test(typeName)) {
    throw descriptorError("$.source.type must be a TypeScript identifier");
  }

  const rawGroups = expectArray(root.groups, "$.groups");
  const rawSecrets = expectArray(root.secrets, "$.secrets");
  if (rawGroups.length + rawSecrets.length === 0) {
    throw descriptorError("descriptor must contain at least one group or secret");
  }
  if (rawGroups.length + rawSecrets.length > MAX_RELEASE_ENTRIES) {
    throw descriptorError(
      `descriptor requires ${rawGroups.length + rawSecrets.length} release entries; maximum is ${MAX_RELEASE_ENTRIES}`,
    );
  }

  const aliases = new Map<string, string>();
  const properties = new Map<string, string>();
  const viewMethods = new Map<string, string>([
    ["config", "Snapshot.config"],
    ["release", "Snapshot.release"],
  ]);

  const groups = rawGroups.map((rawGroup, groupIndex): GroupDescriptor => {
    const path = `$.groups[${groupIndex}]`;
    const group = expectObject(rawGroup, path, ["alias", "fields"]);
    const alias = expectAlias(group.alias, `${path}.alias`);
    registerUnique(aliases, alias, path, "release alias");
    const rawFields = expectArray(group.fields, `${path}.fields`);
    if (rawFields.length === 0) throw descriptorError(`${path}.fields must not be empty`);
    const jsonNames = new Map<string, string>();
    const fields = rawFields.map((rawField, fieldIndex): FieldDescriptor => {
      const fieldPath = `${path}.fields[${fieldIndex}]`;
      const field = expectObject(rawField, fieldPath, [
        "property",
        "jsonName",
        "reload",
        "views",
        "type",
      ]);
      const property = expectAlias(field.property, `${fieldPath}.property`);
      const jsonName = expectAlias(field.jsonName, `${fieldPath}.jsonName`);
      registerUnique(properties, property, fieldPath, "root property");
      registerUnique(jsonNames, jsonName, fieldPath, `JSON name in group ${alias}`);
      const views = normalizeViews(field.views, `${fieldPath}.views`, viewMethods);
      return freeze({
        property,
        jsonName,
        reload: expectReload(field.reload, `${fieldPath}.reload`),
        views,
        type: normalizeType(field.type, `${fieldPath}.type`, 0),
      });
    });
    fields.sort((left, right) => compareText(left.jsonName, right.jsonName));
    return freeze({ alias, fields: freeze(fields) });
  });

  const secrets = rawSecrets.map((rawSecret, secretIndex): SecretDescriptor => {
    const path = `$.secrets[${secretIndex}]`;
    const secret = expectObject(rawSecret, path, ["property", "alias", "reload", "views"]);
    const property = expectAlias(secret.property, `${path}.property`);
    const alias = expectAlias(secret.alias, `${path}.alias`);
    registerUnique(properties, property, path, "root property");
    registerUnique(aliases, alias, path, "release alias");
    return freeze({
      property,
      alias,
      reload: expectReload(secret.reload, `${path}.reload`),
      views: normalizeViews(secret.views, `${path}.views`, viewMethods),
    });
  });

  groups.sort((left, right) => compareText(left.alias, right.alias));
  secrets.sort((left, right) => compareText(left.alias, right.alias));
  return freeze({
    format: DESCRIPTOR_FORMAT,
    source: freeze({ module: moduleName, type: typeName }),
    groups: freeze(groups),
    secrets: freeze(secrets),
  });
}

export class DescriptorError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(`configgen: ${message}`, options);
    this.name = "DescriptorError";
  }
}

function normalizeType(value: unknown, path: string, depth: number): TypeDescriptor {
  if (depth > MAX_TYPE_DEPTH) throw descriptorError(`${path} exceeds maximum nesting depth`);
  const raw = expectObjectWithKind(value, path);
  switch (raw.kind) {
    case "boolean":
    case "string":
    case "duration":
    case "bytes":
      assertKeys(raw, path, ["kind"]);
      return freeze({ kind: raw.kind });
    case "integer": {
      assertKeys(raw, path, ["kind", "bits", "unsigned", "representation"]);
      const bits = raw.bits === undefined ? 32 : expectInteger(raw.bits, `${path}.bits`);
      if (bits < 1 || bits > 64) throw descriptorError(`${path}.bits must be from 1 through 64`);
      const unsigned =
        raw.unsigned === undefined ? false : expectBoolean(raw.unsigned, `${path}.unsigned`);
      const representation: "number" | "bigint" =
        raw.representation === undefined
          ? bits > 53
            ? "bigint"
            : "number"
          : raw.representation === "number" || raw.representation === "bigint"
            ? raw.representation
            : (() => {
                throw descriptorError(`${path}.representation must be number or bigint`);
              })();
      if (representation === "number" && bits > 53) {
        throw descriptorError(`${path} integers wider than 53 bits require bigint representation`);
      }
      return freeze({ kind: "integer", bits, unsigned, representation });
    }
    case "float": {
      assertKeys(raw, path, ["kind", "bits"]);
      const rawBits = raw.bits === undefined ? 64 : expectInteger(raw.bits, `${path}.bits`);
      if (rawBits !== 32 && rawBits !== 64) throw descriptorError(`${path}.bits must be 32 or 64`);
      const bits: 32 | 64 = rawBits;
      return freeze({ kind: "float", bits });
    }
    case "nullable":
      assertKeys(raw, path, ["kind", "value"]);
      if (raw.value === undefined) throw descriptorError(`${path}.value is required`);
      return freeze({
        kind: "nullable",
        value: normalizeType(raw.value, `${path}.value`, depth + 1),
      });
    case "array":
      assertKeys(raw, path, ["kind", "element"]);
      if (raw.element === undefined) throw descriptorError(`${path}.element is required`);
      return freeze({
        kind: "array",
        element: normalizeType(raw.element, `${path}.element`, depth + 1),
      });
    case "fixedArray": {
      assertKeys(raw, path, ["kind", "element", "length"]);
      if (raw.element === undefined) throw descriptorError(`${path}.element is required`);
      const length = expectInteger(raw.length, `${path}.length`);
      if (length < 0 || length > 1_000_000) {
        throw descriptorError(`${path}.length must be from 0 through 1000000`);
      }
      return freeze({
        kind: "fixedArray",
        element: normalizeType(raw.element, `${path}.element`, depth + 1),
        length,
      });
    }
    case "record":
      assertKeys(raw, path, ["kind", "value"]);
      if (raw.value === undefined) throw descriptorError(`${path}.value is required`);
      return freeze({
        kind: "record",
        value: normalizeType(raw.value, `${path}.value`, depth + 1),
      });
    case "object": {
      assertKeys(raw, path, ["kind", "fields"]);
      const fields = expectArray(raw.fields, `${path}.fields`);
      const properties = new Map<string, string>();
      const jsonNames = new Map<string, string>();
      const normalized = fields.map((value, index): NestedFieldDescriptor => {
        const fieldPath = `${path}.fields[${index}]`;
        const field = expectObject(value, fieldPath, ["property", "jsonName", "type"]);
        const property = expectAlias(field.property, `${fieldPath}.property`);
        const jsonName = expectAlias(field.jsonName, `${fieldPath}.jsonName`);
        registerUnique(properties, property, fieldPath, "object property");
        registerUnique(jsonNames, jsonName, fieldPath, "object JSON name");
        return freeze({
          property,
          jsonName,
          type: normalizeType(field.type, `${fieldPath}.type`, depth + 1),
        });
      });
      normalized.sort((left, right) => compareText(left.jsonName, right.jsonName));
      return freeze({ kind: "object", fields: freeze(normalized) });
    }
    default:
      throw descriptorError(`${path}.kind is unsupported`);
  }
}

function normalizeViews(
  value: unknown,
  path: string,
  methods: Map<string, string>,
): readonly string[] {
  const raw = expectArray(value, path);
  if (raw.length === 0) throw descriptorError(`${path} must not be empty`);
  const seen = new Set<string>();
  const result = raw.map((item, index) => {
    const name = expectAlias(item, `${path}[${index}]`);
    if (seen.has(name))
      throw descriptorError(`${path} contains duplicate view ${JSON.stringify(name)}`);
    seen.add(name);
    const method = viewMethod(name);
    const previous = methods.get(method);
    if (previous && previous !== name) {
      throw descriptorError(
        `view ${JSON.stringify(name)} generates method ${method}, which collides with ${previous}`,
      );
    }
    methods.set(method, name);
    return name;
  });
  result.sort(compareText);
  return freeze(result);
}

export function viewMethod(name: string): string {
  const parts = name.split(/[-_]/u).filter(Boolean);
  const [first = "view", ...rest] = parts;
  return `${first.toLowerCase()}${rest.map(capitalize).join("")}`;
}

export function viewClass(name: string): string {
  return `${name.split(/[-_]/u).filter(Boolean).map(capitalize).join("")}View`;
}

function capitalize(value: string): string {
  return value.length === 0 ? value : `${value[0]?.toUpperCase()}${value.slice(1)}`;
}

function jsonNodeValue(node: JsonNode, path: string): unknown {
  switch (node.kind) {
    case "null":
      return null;
    case "boolean":
    case "string":
      return node.value;
    case "number": {
      const value = Number(node.lexeme);
      if (!Number.isFinite(value)) throw descriptorError(`${path} contains a non-finite number`);
      return value;
    }
    case "array":
      return node.elements.map((item, index) => jsonNodeValue(item, `${path}[${index}]`));
    case "object": {
      const result: Record<string, unknown> = Object.create(null) as Record<string, unknown>;
      for (const property of node.properties) {
        if (Object.hasOwn(result, property.name)) {
          throw descriptorError(
            `${path} contains duplicate JSON property ${JSON.stringify(property.name)}`,
          );
        }
        result[property.name] = jsonNodeValue(property.value, `${path}.${property.name}`);
      }
      return result;
    }
  }
}

function expectObjectWithKind(
  value: unknown,
  path: string,
): Record<string, unknown> & { kind: string } {
  const object = expectObject(value, path);
  if (typeof object.kind !== "string" || object.kind.length === 0) {
    throw descriptorError(`${path}.kind is required`);
  }
  return object as Record<string, unknown> & { kind: string };
}

function expectObject(
  value: unknown,
  path: string,
  allowed?: readonly string[],
): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw descriptorError(`${path} must be an object`);
  }
  const result = value as Record<string, unknown>;
  if (allowed) assertKeys(result, path, allowed);
  return result;
}

function assertKeys(
  value: Record<string, unknown>,
  path: string,
  allowed: readonly string[],
): void {
  const expected = new Set(allowed);
  for (const key of Object.keys(value)) {
    if (!expected.has(key))
      throw descriptorError(`${path} contains unknown property ${JSON.stringify(key)}`);
  }
}

function expectArray(value: unknown, path: string): unknown[] {
  if (!Array.isArray(value)) throw descriptorError(`${path} must be an array`);
  return value;
}

function expectNonemptyString(value: unknown, path: string): string {
  if (typeof value !== "string" || value.trim() !== value || value.length === 0) {
    throw descriptorError(`${path} must be a non-empty canonical string`);
  }
  return value;
}

function expectModuleSpecifier(value: unknown, path: string): string {
  const result = expectNonemptyString(value, path);
  if (
    result.startsWith("/") ||
    result.startsWith("\\") ||
    result.toLowerCase().startsWith("file:") ||
    /^[A-Za-z]:[\\/]/u.test(result) ||
    result.includes("\\") ||
    [...result].some((character) => character.charCodeAt(0) < 0x20)
  ) {
    throw descriptorError(`${path} must be a portable module specifier, not a physical path`);
  }
  return result;
}

function expectAlias(value: unknown, path: string): string {
  const result = expectNonemptyString(value, path);
  if (!ALIAS_PATTERN.test(result)) throw descriptorError(`${path} is not a canonical name`);
  return result;
}

function expectInteger(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) {
    throw descriptorError(`${path} must be a safe integer`);
  }
  return value;
}

function expectBoolean(value: unknown, path: string): boolean {
  if (typeof value !== "boolean") throw descriptorError(`${path} must be a boolean`);
  return value;
}

function expectReload(value: unknown, path: string): ReloadPolicy {
  if (value !== "hot" && value !== "restart") {
    throw descriptorError(`${path} must be hot or restart`);
  }
  return value;
}

function registerUnique(
  seen: Map<string, string>,
  value: string,
  path: string,
  description: string,
): void {
  const previous = seen.get(value);
  if (previous) {
    throw descriptorError(
      `${description} ${JSON.stringify(value)} is used by both ${previous} and ${path}`,
    );
  }
  seen.set(value, path);
}

function descriptorError(message: string, cause?: unknown): DescriptorError {
  return new DescriptorError(message, cause === undefined ? undefined : { cause });
}

function freeze<T>(value: T): Readonly<T> {
  return Object.freeze(value);
}

/** Locale-independent UTF-16 code-unit order used by every artifact. */
export function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

import { type JsonNode, parseStrictJson } from "../configstore/strict-json.js";
