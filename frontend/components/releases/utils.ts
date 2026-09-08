import { displayPath } from "@/lib/format";
import { schemaVersionError } from "@/lib/schema";
import type { ConfigurationRelease, CreateReleaseRequest } from "@/lib/types";
import {
  validateAlias,
  validateKey,
  validateMetadataJson,
  validateReleaseName,
} from "@/lib/validation";

export function releaseKey(release: {
  name: string;
  version: number;
  schema_version?: number;
}): string {
  return release.schema_version === undefined
    ? `${release.name}@${release.version}`
    : `${release.name}@${release.schema_version}:${release.version}`;
}

/** Inverse of releaseKey: `runtime@12` → {name, version}; null when malformed. */
export function parseReleaseKey(
  key: string,
): { name: string; version: number; schema_version?: number } | null {
  const at = key.lastIndexOf("@");
  if (at <= 0 || at === key.length - 1) return null;
  const name = key.slice(0, at);
  const suffix = key.slice(at + 1);
  const parts = suffix.split(":");
  if (parts.length > 2 || parts.some((part) => !/^\d+$/.test(part))) return null;
  const schema_version = parts.length === 2 ? Number(parts[0]) : undefined;
  const version = Number(parts.at(-1));
  if (!Number.isSafeInteger(version) || version < 1) return null;
  if (schema_version !== undefined && (!Number.isSafeInteger(schema_version) || schema_version < 0))
    return null;
  return { name, version, schema_version };
}

export function refText(entry: ConfigurationRelease["entries"][number]): string {
  const namespace = entry.ref.namespace;
  return displayPath({ env: namespace.env, app: namespace.app, key: entry.ref.key });
}

export function releaseDefinitionError(definition: string): string | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(definition);
  } catch (error) {
    return `Definition must be valid JSON: ${error instanceof Error ? error.message : String(error)}`;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return "Definition must be a JSON object with a name and an entries array.";
  }
  const draft = parsed as Record<string, unknown>;
  if (typeof draft.name !== "string" || draft.name === "" || !Array.isArray(draft.entries)) {
    return "Definition requires name and entries.";
  }
  if (draft.schema_version !== undefined) {
    const schemaError = schemaVersionError(draft.schema_version);
    if (schemaError) return schemaError;
  }
  const nameError = validateReleaseName(draft.name);
  if (nameError) return nameError;
  for (const entry of draft.entries as unknown[]) {
    const record = (entry ?? {}) as Record<string, unknown>;
    const alias = typeof record.alias === "string" ? record.alias : "";
    const aliasError = validateAlias(alias);
    if (aliasError) return aliasError;
    if (record.kind !== "parameter" && record.kind !== "secret") {
      return `Entry ${alias} kind must be parameter or secret.`;
    }
    const ref = (record.ref ?? {}) as Record<string, unknown>;
    const keyError = validateKey(typeof ref.key === "string" ? ref.key : "");
    if (keyError) return `Entry ${alias} needs a resource key: ${keyError}`;
  }
  if (typeof draft.metadata_json === "string") return validateMetadataJson(draft.metadata_json);
  return null;
}

export function parseReleaseDefinition(definition: string): CreateReleaseRequest {
  const error = releaseDefinitionError(definition);
  if (error) throw new Error(error);
  const parsed = JSON.parse(definition) as CreateReleaseRequest;
  return {
    namespace: parsed.namespace,
    name: parsed.name,
    schema_version: parsed.schema_version,
    entries: parsed.entries,
    metadata_json: parsed.metadata_json ?? "{}",
  };
}
