import { displayPath } from "@/lib/format";
import type { ConfigurationRelease, CreateReleaseRequest } from "@/lib/types";
import {
  validateAlias,
  validateKey,
  validateMetadataJson,
  validateReleaseName,
} from "@/lib/validation";

export function releaseKey(release: ConfigurationRelease): string {
  return `${release.name}@${release.version}`;
}

/** Inverse of releaseKey: `runtime@12` → {name, version}; null when malformed. */
export function parseReleaseKey(key: string): { name: string; version: number } | null {
  const at = key.lastIndexOf("@");
  if (at <= 0 || at === key.length - 1) return null;
  const name = key.slice(0, at);
  const digits = key.slice(at + 1);
  if (!/^\d+$/.test(digits)) return null;
  const version = Number(digits);
  if (!Number.isSafeInteger(version) || version < 1) return null;
  return { name, version };
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
  const parsed = JSON.parse(definition) as CreateReleaseRequest;
  return {
    namespace: parsed.namespace,
    name: parsed.name,
    schema_version: parsed.schema_version ? Number(parsed.schema_version) : undefined,
    entries: parsed.entries,
    metadata_json: parsed.metadata_json ?? "{}",
  };
}
