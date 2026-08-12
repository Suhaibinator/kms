import { createHash } from "node:crypto";

import {
  type ConfigDescriptor,
  compareText,
  type NestedFieldDescriptor,
  normalizeDescriptor,
  normalizeModuleSpecifier,
  type TypeDescriptor,
  viewClass,
  viewMethod,
} from "./model.js";

export const MAX_SCHEMA_BYTES = 256 * 1024;
export const CONTRACT_FORMAT = "kms-config-contract/v1" as const;

export interface GenerateOptions {
  /** Import used by the generated binding for configstore runtime symbols. */
  readonly runtimeImport?: string;
  /** Import used by the generated binding for Secret and release symbols. */
  readonly coreImport?: string;
}

export interface GeneratedArtifacts {
  readonly binding: string;
  readonly schema: string;
  readonly contract: string;
  readonly schemaSHA256: string;
}

/** Generate one deterministic binding/schema/contract artifact set. */
export function generate(
  input: ConfigDescriptor | unknown,
  options: GenerateOptions = {},
): GeneratedArtifacts {
  const descriptor = normalizeDescriptor(input);
  const schema = renderSchema(descriptor);
  const byteLength = Buffer.byteLength(schema, "utf8");
  if (byteLength > MAX_SCHEMA_BYTES) {
    throw new RangeError(
      `configgen: generated schema is ${byteLength} bytes; maximum is ${MAX_SCHEMA_BYTES}`,
    );
  }
  const schemaSHA256 = createHash("sha256").update(schema, "utf8").digest("hex");
  const contract = renderContract(descriptor, schemaSHA256);
  const binding = renderBinding(descriptor, schemaSHA256, options);
  return Object.freeze({ binding, schema, contract, schemaSHA256 });
}

interface RawNumber {
  readonly raw: string;
}

type JsonValue =
  | null
  | boolean
  | string
  | number
  | RawNumber
  | readonly JsonValue[]
  | { readonly [key: string]: JsonValue };

function renderSchema(descriptor: ConfigDescriptor): string {
  const required: JsonValue[] = [];
  const properties: Record<string, JsonValue> = Object.create(null) as Record<string, JsonValue>;
  for (const group of descriptor.groups) {
    required.push(group.alias);
    const groupRequired: JsonValue[] = [];
    const groupProperties: Record<string, JsonValue> = Object.create(null) as Record<
      string,
      JsonValue
    >;
    for (const field of group.fields) {
      groupRequired.push(field.jsonName);
      groupProperties[field.jsonName] = schemaForType(field.type);
    }
    properties[group.alias] = {
      type: "object",
      additionalProperties: false,
      required: groupRequired,
      properties: groupProperties,
    };
  }
  return prettyJson({
    $schema: "https://json-schema.org/draft/2020-12/schema",
    type: "object",
    additionalProperties: false,
    required,
    properties,
  });
}

function schemaForType(type: TypeDescriptor): JsonValue {
  switch (type.kind) {
    case "boolean":
      return { type: "boolean" };
    case "string":
      return { type: "string" };
    case "integer": {
      const bits = BigInt(type.bits);
      const minimum = type.unsigned ? 0n : -(1n << (bits - 1n));
      const maximum = type.unsigned ? (1n << bits) - 1n : (1n << (bits - 1n)) - 1n;
      return { type: "integer", minimum: raw(minimum), maximum: raw(maximum) };
    }
    case "float": {
      const maximum = type.bits === 32 ? 3.4028234663852886e38 : Number.MAX_VALUE;
      return { type: "number", minimum: -maximum, maximum };
    }
    case "duration":
      return { type: "string", format: "go-duration" };
    case "bytes":
      return nullable({ type: "string", format: "kms-base64" });
    case "nullable":
      return nullable(schemaForType(type.value));
    case "array":
      return nullable({ type: "array", items: schemaForType(type.element) });
    case "fixedArray":
      return {
        type: "array",
        items: schemaForType(type.element),
        minItems: type.length,
        maxItems: type.length,
      };
    case "record":
      return nullable({ type: "object", additionalProperties: schemaForType(type.value) });
    case "object": {
      const required: JsonValue[] = [];
      const properties: Record<string, JsonValue> = Object.create(null) as Record<
        string,
        JsonValue
      >;
      for (const field of type.fields) {
        required.push(field.jsonName);
        properties[field.jsonName] = schemaForType(field.type);
      }
      return { type: "object", additionalProperties: false, required, properties };
    }
  }
}

function nullable(schema: JsonValue): JsonValue {
  return { anyOf: [schema, { type: "null" }] };
}

function raw(value: bigint): RawNumber {
  return Object.freeze({ raw: value.toString() });
}

function renderContract(descriptor: ConfigDescriptor, schemaSHA256: string): string {
  const groups = descriptor.groups.map((group) => ({
    alias: group.alias,
    kind: "parameter",
    content_type: "json",
    fields: group.fields.map((field) => field.jsonName),
  }));
  const fields = descriptor.groups.flatMap((group) =>
    group.fields.map((field) => ({
      group: group.alias,
      json_name: field.jsonName,
      ts_name: field.property,
      ts_path: field.property,
      reload: field.reload,
      encoding: encodingForType(field.type),
      views: field.views,
    })),
  );
  const secrets = descriptor.secrets.map((secret) => ({
    alias: secret.alias,
    kind: "secret",
    ts_name: secret.property,
    ts_path: secret.property,
    reload: secret.reload,
    encoding: "secret",
    views: secret.views,
  }));
  const views = collectViews(descriptor).map((view) => ({
    name: view.name,
    method: viewMethod(view.name),
    fields: view.fields.map((field) => field.canonicalName),
  }));
  return prettyJson({
    format: CONTRACT_FORMAT,
    source: {
      language: "typescript",
      module: descriptor.source.module,
      type: descriptor.source.type,
    },
    schema_sha256: schemaSHA256,
    groups,
    fields,
    secrets,
    views,
  });
}

interface ViewField {
  readonly property: string;
  readonly canonicalName: string;
  readonly secret: boolean;
}

interface ViewDescription {
  readonly name: string;
  readonly className: string;
  readonly fields: readonly ViewField[];
}

function collectViews(descriptor: ConfigDescriptor): readonly ViewDescription[] {
  const byName = new Map<string, ViewField[]>();
  const append = (viewName: string, field: ViewField): void => {
    const fields = byName.get(viewName) ?? [];
    fields.push(field);
    byName.set(viewName, fields);
  };
  for (const group of descriptor.groups) {
    for (const field of group.fields) {
      for (const name of field.views) {
        append(name, {
          property: field.property,
          canonicalName: `${group.alias}.${field.jsonName}`,
          secret: false,
        });
      }
    }
  }
  for (const secret of descriptor.secrets) {
    for (const name of secret.views) {
      append(name, {
        property: secret.property,
        canonicalName: secret.alias,
        secret: true,
      });
    }
  }
  return [...byName]
    .sort(([left], [right]) => compareText(left, right))
    .map(([name, fields]) => ({
      name,
      className: viewClass(name),
      fields: Object.freeze(
        fields.sort((left, right) => compareText(left.canonicalName, right.canonicalName)),
      ),
    }));
}

function encodingForType(type: TypeDescriptor): string {
  switch (type.kind) {
    case "boolean":
    case "string":
      return type.kind;
    case "integer":
      return `${type.unsigned ? "uint" : "int"}${type.bits}`;
    case "float":
      return `float${type.bits}`;
    case "duration":
      return "go-duration";
    case "bytes":
      return "nullable-base64";
    case "nullable":
      return `nullable-${encodingForType(type.value)}`;
    case "array":
      return "nullable-array";
    case "fixedArray":
      return "array";
    case "record":
      return "nullable-string-map";
    case "object":
      return "object";
  }
}

function renderBinding(
  descriptor: ConfigDescriptor,
  schemaSHA256: string,
  options: GenerateOptions,
): string {
  const runtimeImport = normalizeModuleSpecifier(
    options.runtimeImport ?? "@suhaibinator/kms/configstore",
    "runtimeImport",
  );
  const coreImport = normalizeModuleSpecifier(
    options.coreImport ?? "@suhaibinator/kms",
    "coreImport",
  );
  const root = "RootConfig";
  const hasGroups = descriptor.groups.length > 0;
  const hasSecrets = descriptor.secrets.length > 0;
  const hasRestartFields = descriptor.groups.some((groupDescriptor) =>
    groupDescriptor.fields.some((fieldDescriptor) => fieldDescriptor.reload === "restart"),
  );
  const hasRestartSecrets = descriptor.secrets.some((secret) => secret.reload === "restart");
  const lines: string[] = [];
  const line = (text = ""): void => {
    lines.push(text);
  };

  line("// Code generated by kms-config-gen-ts; DO NOT EDIT.");
  line();
  if (hasGroups) {
    line('import { isDeepStrictEqual } from "node:util";');
    line();
  }
  if (hasSecrets) line(`import { Secret } from ${quote(coreImport)};`);
  line(`import type { ReleaseSnapshot } from ${quote(coreImport)};`);
  line("import {");
  const runtimeValues = [
    "CandidateError",
    "cloneConfig",
    "immutableSnapshot",
    "ReleaseIdentity",
    "startManagedConfig",
  ];
  if (hasGroups) {
    runtimeValues.push("codecs", "decodeGroup", "encodeGroup", "field", "group", "rejectDecode");
  }
  for (const name of runtimeValues) {
    line(`  ${name},`);
  }
  line(`} from ${quote(runtimeImport)};`);
  line("import type {");
  const runtimeTypes = [
    "ConfigSnapshot",
    "ContractEntry",
    "FieldDifference",
    "ManagedConfigManager",
    "ManagedConfigOptions",
    "ManagedPreparedCandidate",
    "ManagedReleaseClient",
  ];
  if (hasGroups) runtimeTypes.push("GroupCodec", "ValueCodec");
  for (const name of runtimeTypes) {
    line(`  ${name},`);
  }
  line(`} from ${quote(runtimeImport)};`);
  line(
    `import type { ${descriptor.source.type} as ${root} } from ${quote(descriptor.source.module)};`,
  );
  line();
  line(`export const schemaSHA256 = ${quote(schemaSHA256)};`);
  line();
  line("export const generatedContract = Object.freeze([");
  for (const groupDescriptor of descriptor.groups) {
    line(
      `  Object.freeze({ alias: ${quote(groupDescriptor.alias)}, kind: "parameter", contentType: "json" }),`,
    );
  }
  for (const secret of descriptor.secrets) {
    line(`  Object.freeze({ alias: ${quote(secret.alias)}, kind: "secret" }),`);
  }
  line("]) satisfies readonly ContractEntry[];");
  line();

  const rootProperties = [
    ...descriptor.groups.flatMap((groupDescriptor) =>
      groupDescriptor.fields.map((fieldDescriptor) => fieldDescriptor.property),
    ),
    ...descriptor.secrets.map((secret) => secret.property),
  ];
  line(`assertRootCoverage<${root}, ${rootProperties.map(quote).join(" | ")}>();`);
  line();

  for (const [index, groupDescriptor] of descriptor.groups.entries()) {
    const typeName = `Group${index}`;
    line(
      `type ${typeName} = Pick<${root}, ${groupDescriptor.fields.map((field) => quote(field.property)).join(" | ")}>;`,
    );
    for (const [fieldIndex, fieldDescriptor] of groupDescriptor.fields.entries()) {
      const context = `${typeName}[${quote(fieldDescriptor.property)}]`;
      line(
        `const valueCodec${index}_${fieldIndex}: ValueCodec<${context}> = ${renderCodec(fieldDescriptor.type, context)};`,
      );
    }
    const groupProperties = groupDescriptor.fields.map((fieldDescriptor) =>
      quote(fieldDescriptor.property),
    );
    line(
      `const groupCodec${index}: GroupCodec<${typeName}> = exactObject<${typeName}, ${groupProperties.join(" | ")}>(group<${typeName}>([`,
    );
    for (const [fieldIndex, fieldDescriptor] of groupDescriptor.fields.entries()) {
      line(
        `  field<${typeName}, ${quote(fieldDescriptor.property)}>(${quote(fieldDescriptor.jsonName)}, ${quote(fieldDescriptor.property)}, valueCodec${index}_${fieldIndex}),`,
      );
    }
    line("]));");
    line();
  }

  line("export const groupCodecs = Object.freeze({");
  for (const [index, groupDescriptor] of descriptor.groups.entries()) {
    line(`  ${quote(groupDescriptor.alias)}: groupCodec${index},`);
  }
  line("});");
  line();
  line(
    `export function encodeParameterGroups(config: ${root}): Readonly<Record<string, string>> {`,
  );
  if (hasGroups) line("  const source = cloneConfig(config);");
  else line("  void config;");
  line("  return Object.freeze({");
  for (const [index, groupDescriptor] of descriptor.groups.entries()) {
    line(`    ${quote(groupDescriptor.alias)}: encodeGroup(source, groupCodec${index}),`);
  }
  line("  });");
  line("}");
  line();

  for (const view of collectViews(descriptor)) {
    line(`export class ${view.className} {`);
    line(`  readonly #snapshot: ConfigSnapshot<${root}>;`);
    line();
    line(`  constructor(snapshot: ConfigSnapshot<${root}>) {`);
    line("    this.#snapshot = snapshot;");
    line("    Object.freeze(this);");
    line("  }");
    for (const fieldDescriptor of view.fields) {
      line();
      line(
        `  get [${quote(fieldDescriptor.property)}](): ${root}[${quote(fieldDescriptor.property)}] {`,
      );
      line(`    return this.#snapshot.get(${quote(fieldDescriptor.property)});`);
      line("  }");
    }
    line("}");
    line();
  }

  line("export class Snapshot {");
  line(`  readonly #snapshot: ConfigSnapshot<${root}>;`);
  line();
  line(`  constructor(snapshot: ConfigSnapshot<${root}>) {`);
  line("    this.#snapshot = snapshot;");
  line("    Object.freeze(this);");
  line("  }");
  line();
  line("  get release(): ReleaseIdentity {");
  line("    return this.#snapshot.release;");
  line("  }");
  line();
  line(`  config(): ${root} {`);
  line("    return this.#snapshot.config();");
  line("  }");
  for (const view of collectViews(descriptor)) {
    line();
    line(`  ${viewMethod(view.name)}(): ${view.className} {`);
    line(`    return new ${view.className}(this.#snapshot);`);
    line("  }");
  }
  line("}");
  line();

  line(`export type ValidateConfig = (config: ${root}) => void | Promise<void>;`);
  line(`export type StartOptions = Omit<ManagedConfigOptions, "contract">;`);
  line();
  line("export class Store {");
  line(`  readonly #defaults: ConfigSnapshot<${root}>;`);
  line("  readonly #validate: ValidateConfig;");
  line(`  #active: ConfigSnapshot<${root}> | undefined;`);
  line("  #started = false;");
  line();
  line(`  constructor(defaults: ${root}, validate: ValidateConfig) {`);
  line(
    '    if (typeof validate !== "function") throw new TypeError("generated config store: validate callback is required");',
  );
  line("    const copiedDefaults = writableClone(defaults);");
  for (const secret of descriptor.secrets) {
    line(
      `    assertZeroSecret(copiedDefaults[${quote(secret.property)}], ${quote(secret.alias)});`,
    );
  }
  line("    this.#defaults = immutableSnapshot(copiedDefaults);");
  line("    this.#validate = validate;");
  line("  }");
  line();
  line("  async start(");
  line("    client: ManagedReleaseClient,");
  line("    options: StartOptions,");
  line("    signal?: AbortSignal,");
  line("  ): Promise<ManagedConfigManager> {");
  line(
    '    if (this.#started) throw new Error("generated config store: Store.start may only be called once");',
  );
  line("    this.#started = true;");
  line("    return startManagedConfig(");
  line("      client,");
  line("      { ...options, contract: generatedContract },");
  line("      (snapshot, candidateSignal) => this.#prepare(snapshot, candidateSignal),");
  line("      signal,");
  line("    );");
  line("  }");
  line();
  line("  current(): Snapshot {");
  line("    const active = this.#active;");
  line(
    '    if (!active) throw new Error("generated config store: configuration is not initialized");',
  );
  line("    return new Snapshot(active);");
  line("  }");
  line();
  line(
    "  async #prepare(snapshot: ReleaseSnapshot, signal: AbortSignal): Promise<ManagedPreparedCandidate> {",
  );
  line("    throwIfAborted(signal);");
  line("    const candidate = writableClone(this.#defaults.config());");
  for (const [index, groupDescriptor] of descriptor.groups.entries()) {
    line(`    const parameter${index} = snapshot.parameter(${quote(groupDescriptor.alias)});`);
    line(
      `    if (!parameter${index} || parameter${index}.entry.alias !== ${quote(groupDescriptor.alias)} || parameter${index}.entry.kind !== "parameter" || parameter${index}.entry.contentType !== "json") throw contractMismatch();`,
    );
    line(`    let decoded${index}: Group${index};`);
    line("    try {");
    line(`      decoded${index} = decodeGroup(parameter${index}.value(), groupCodec${index});`);
    line("    } catch (cause) {");
    line(`      throw rejectDecode(${quote(groupDescriptor.alias)}, cause);`);
    line("    }");
    for (const fieldDescriptor of groupDescriptor.fields) {
      line(
        `    setProperty(candidate, ${quote(fieldDescriptor.property)}, decoded${index}[${quote(fieldDescriptor.property)}]);`,
      );
    }
  }
  for (const [index, secret] of descriptor.secrets.entries()) {
    line(`    const secret${index} = snapshot.secret(${quote(secret.alias)});`);
    line(
      `    if (!secret${index} || secret${index}.entry.alias !== ${quote(secret.alias)} || secret${index}.entry.kind !== "secret") throw contractMismatch();`,
    );
    line(`    const secretValue${index} = new Secret(secret${index}.bytes(), {`);
    line(`      path: secret${index}.entry.path,`);
    line(`      version: secret${index}.entry.version,`);
    line(`      contentType: secret${index}.entry.contentType,`);
    line("    });");
    line(`    setProperty(candidate, ${quote(secret.property)}, secretValue${index});`);
  }
  line("    throwIfAborted(signal);");
  line("    const effectiveDefaults = writableClone(this.#defaults.config());");
  for (const [index, secret] of descriptor.secrets.entries()) {
    line(
      `    setProperty(effectiveDefaults, ${quote(secret.property)}, secretValue${index}.clone());`,
    );
  }
  line(`    let validatedCandidate: ${root};`);
  if (hasGroups) line(`    let validatedDefaults: ${root};`);
  line("    try {");
  line("      await this.#validate(candidate);");
  line("      throwIfAborted(signal);");
  line("      await this.#validate(effectiveDefaults);");
  line("      throwIfAborted(signal);");
  for (const secret of descriptor.secrets) {
    line(`      assertSecret(candidate[${quote(secret.property)}], ${quote(secret.alias)});`);
    line(
      `      assertSecret(effectiveDefaults[${quote(secret.property)}], ${quote(secret.alias)});`,
    );
  }
  line("      validatedCandidate = writableClone(candidate);");
  if (hasGroups) line("      validatedDefaults = writableClone(effectiveDefaults);");
  line("    } catch (cause) {");
  line("      if (signal.aborted) throw abortReason(signal);");
  line('      throw new CandidateError("config_validation_failed", cause);');
  line("    }");
  line("    throwIfAborted(signal);");
  line("    const defaultDifferences: FieldDifference[] = [];");
  for (const [groupIndex, groupDescriptor] of descriptor.groups.entries()) {
    for (const [fieldIndex, fieldDescriptor] of groupDescriptor.fields.entries()) {
      line(
        `    appendDifference(defaultDifferences, ${quote(`${groupDescriptor.alias}.${fieldDescriptor.jsonName}`)}, valueCodec${groupIndex}_${fieldIndex}, validatedDefaults[${quote(fieldDescriptor.property)}], validatedCandidate[${quote(fieldDescriptor.property)}]);`,
      );
    }
  }
  line("    const restartRequiredFields: string[] = [];");
  line("    const active = this.#active;");
  line("    if (active) {");
  for (const [groupIndex, groupDescriptor] of descriptor.groups.entries()) {
    for (const [fieldIndex, fieldDescriptor] of groupDescriptor.fields.entries()) {
      if (fieldDescriptor.reload === "restart") {
        line(
          `      appendRestart(restartRequiredFields, ${quote(`${groupDescriptor.alias}.${fieldDescriptor.jsonName}`)}, valueCodec${groupIndex}_${fieldIndex}, active.get(${quote(fieldDescriptor.property)}), validatedCandidate[${quote(fieldDescriptor.property)}]);`,
        );
      }
    }
  }
  for (const secret of descriptor.secrets) {
    if (secret.reload === "restart") {
      line(
        `      if (!sameSecretIdentity(active.get(${quote(secret.property)}), validatedCandidate[${quote(secret.property)}])) restartRequiredFields.push(${quote(secret.alias)});`,
      );
    }
  }
  line("    }");
  line("    restartRequiredFields.sort();");
  line(
    "    const prepared = immutableSnapshot(validatedCandidate, ReleaseIdentity.from(snapshot));",
  );
  line("    return {");
  line("      defaultDifferences,");
  line("      restartRequiredFields,");
  line("      publish: () => {");
  line("        this.#active = prepared;");
  line("      },");
  line("      abort: () => undefined,");
  line("    };");
  line("  }");
  line("}");
  line();

  line("function assertRootCoverage<T extends object, K extends PropertyKey>(");
  line(
    "  ...missing: IsUnion<T> extends true ? [never] : [Exclude<keyof T, K>, Exclude<K, keyof T>] extends [never, never] ? [] : [never]",
  );
  line("): void {");
  line("  void missing;");
  line("}");
  line();
  if (hasGroups) {
    line("function exactObject<T extends object, K extends keyof T & string>(");
    line(
      "  codec: IsUnion<T> extends true ? never : Exclude<keyof T, K> extends never ? GroupCodec<T> : never,",
    );
    line("): GroupCodec<T> {");
    line("  return codec;");
    line("}");
    line();
  }
  line("type IsUnion<T, Whole = T> = T extends unknown");
  line("  ? [Whole] extends [T]");
  line("    ? false");
  line("    : true");
  line("  : never;");
  line();

  line(`function writableClone(value: ${root}): ${root} {`);
  line("  const source = cloneConfig(value);");
  line("  const target = Object.create(Object.getPrototypeOf(source)) as object;");
  line("  for (const key of Reflect.ownKeys(source)) {");
  line("    const descriptor = Object.getOwnPropertyDescriptor(source, key);");
  line('    if (!descriptor || !("value" in descriptor)) {');
  line(
    '      throw new TypeError("generated config store: accessor properties are not supported");',
  );
  line("    }");
  line(
    "    Object.defineProperty(target, key, { ...descriptor, writable: true, configurable: true });",
  );
  line("  }");
  line(`  return target as ${root};`);
  line("}");
  line();
  line(
    "function setProperty<T extends object, K extends keyof T>(target: T, key: K, value: T[K]): void {",
  );
  line("  const current = Object.getOwnPropertyDescriptor(target, key);");
  line("  Object.defineProperty(target, key, {");
  line("    value: cloneConfig(value),");
  line("    enumerable: current?.enumerable ?? true,");
  line("    writable: true,");
  line("    configurable: true,");
  line("  });");
  line("}");
  line();
  if (hasGroups) {
    line('type EncodedNode = ReturnType<ValueCodec<unknown>["encodeNode"]>;');
    line();
    line("function appendDifference<T>(");
    line("  result: FieldDifference[],");
    line("  path: string,");
    line("  codec: ValueCodec<T>,");
    line("  expected: T,");
    line("  actual: T,");
    line("): void {");
    line("  if (!sameEncoded(codec, expected, actual)) result.push({ path, expected, actual });");
    line("}");
    line();
    if (hasRestartFields) {
      line(
        "function appendRestart<T>(result: string[], path: string, codec: ValueCodec<T>, active: T, candidate: T): void {",
      );
      line("  if (!sameEncoded(codec, active, candidate)) result.push(path);");
      line("}");
      line();
    }
    line("function sameEncoded<T>(codec: ValueCodec<T>, left: T, right: T): boolean {");
    line("  try {");
    line(
      '    return isDeepStrictEqual(canonicalNode(codec.encodeNode(left, "$")), canonicalNode(codec.encodeNode(right, "$")));',
    );
    line("  } catch (cause) {");
    line('    throw new CandidateError("config_validation_failed", cause);');
    line("  }");
    line("}");
    line();
    line("function canonicalNode(node: EncodedNode): EncodedNode {");
    line('  if (node.kind === "array") {');
    line('    return { kind: "array", elements: node.elements.map(canonicalNode) };');
    line("  }");
    line('  if (node.kind === "object") {');
    line("    return {");
    line('      kind: "object",');
    line("      properties: node.properties");
    line("        .map(({ name, value }) => ({ name, value: canonicalNode(value) }))");
    line(
      "        .sort((left, right) => left.name < right.name ? -1 : left.name > right.name ? 1 : 0),",
    );
    line("    };");
    line("  }");
    line("  return node;");
    line("}");
    line();
  }
  if (hasRestartSecrets) {
    line("function sameSecretIdentity(left: unknown, right: unknown): boolean {");
    line("  return (");
    line("    left instanceof Secret &&");
    line("    right instanceof Secret &&");
    line("    left.path === right.path &&");
    line("    left.version === right.version");
    line("  );");
    line("}");
    line();
  }
  if (hasSecrets) {
    line("function assertSecret(value: unknown, alias: string): asserts value is Secret {");
    line("  if (!(value instanceof Secret)) {");
    line(
      '    throw new TypeError("generated config store: validated secret " + alias + " must remain a Secret");',
    );
    line("  }");
    line("}");
    line();
    line("function assertZeroSecret(value: unknown, alias: string): void {");
    line("  if (");
    line("    !(value instanceof Secret) ||");
    line("    !value.isEmpty ||");
    line('    value.path !== "" ||');
    line("    value.version !== 0n ||");
    line('    value.contentType !== ""');
    line("  ) {");
    line(
      '    throw new TypeError("generated config store: secret default " + alias + " must be the zero Secret");',
    );
    line("  }");
    line("}");
    line();
  }
  line("function contractMismatch(): CandidateError {");
  line("  return new CandidateError(");
  line('    "config_contract_mismatch",');
  line('    new Error("generated config store: resolved release does not match contract"),');
  line("  );");
  line("}");
  line();
  line("function throwIfAborted(signal: AbortSignal): void {");
  line("  if (signal.aborted) throw abortReason(signal);");
  line("}");
  line();
  line("function abortReason(signal: AbortSignal): unknown {");
  line('  return signal.reason ?? new DOMException("Aborted", "AbortError");');
  line("}");

  return `${lines.join("\n")}\n`;
}

function renderCodec(type: TypeDescriptor, context: string): string {
  switch (type.kind) {
    case "boolean":
      return "codecs.boolean";
    case "string":
      return "codecs.string";
    case "integer": {
      const factory = type.representation === "bigint" ? "bigint" : "int";
      return `codecs.${factory}({ bits: ${type.bits}, unsigned: ${type.unsigned} })`;
    }
    case "float":
      return `codecs.float({ bits: ${type.bits} })`;
    case "duration":
      return "codecs.duration";
    case "bytes":
      return "codecs.bytes";
    case "nullable":
      return `codecs.nullable(${renderCodec(type.value, `Exclude<${context}, null>`)})`;
    case "array":
      return `codecs.array<NonNullable<${context}>[number], NonNullable<${context}>>(${renderCodec(type.element, `NonNullable<${context}>[number]`)})`;
    case "fixedArray":
      return `codecs.fixedArray<${context}[number], ${type.length}, ${context}>(${renderCodec(type.element, `${context}[number]`)}, ${type.length})`;
    case "record":
      return `codecs.record<NonNullable<${context}>[string], NonNullable<${context}>>(${renderCodec(type.value, `NonNullable<${context}>[string]`)})`;
    case "object":
      return renderObjectCodec(type.fields, context);
  }
}

function renderObjectCodec(fields: readonly NestedFieldDescriptor[], context: string): string {
  if (fields.length === 0) return `exactObject<${context}, never>(codecs.object<${context}>([]))`;
  const rendered = fields.map(
    (fieldDescriptor) =>
      `field<${context}, ${quote(fieldDescriptor.property)}>(${quote(fieldDescriptor.jsonName)}, ${quote(fieldDescriptor.property)}, ${renderCodec(fieldDescriptor.type, `${context}[${quote(fieldDescriptor.property)}]`)})`,
  );
  const properties = fields.map((fieldDescriptor) => quote(fieldDescriptor.property)).join(" | ");
  return `exactObject<${context}, ${properties}>(codecs.object<${context}>([${rendered.join(", ")}]))`;
}

function quote(value: string): string {
  return JSON.stringify(value);
}

function prettyJson(value: JsonValue): string {
  return `${renderJson(value, 0)}\n`;
}

function renderJson(value: JsonValue, depth: number): string {
  if (value === null || typeof value === "boolean" || typeof value === "number") {
    return JSON.stringify(value);
  }
  if (typeof value === "string") return JSON.stringify(value);
  if (isRawNumber(value)) return value.raw;
  const indent = "  ".repeat(depth);
  const childIndent = "  ".repeat(depth + 1);
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]";
    return `[\n${value.map((item) => `${childIndent}${renderJson(item, depth + 1)}`).join(",\n")}\n${indent}]`;
  }
  const entries = Object.entries(value);
  if (entries.length === 0) return "{}";
  return `{\n${entries
    .map(([key, item]) => `${childIndent}${JSON.stringify(key)}: ${renderJson(item, depth + 1)}`)
    .join(",\n")}\n${indent}}`;
}

function isRawNumber(value: object): value is RawNumber {
  return Object.keys(value).length === 1 && typeof (value as { raw?: unknown }).raw === "string";
}

export const internal = Object.freeze({ encodingForType, renderCodec });
