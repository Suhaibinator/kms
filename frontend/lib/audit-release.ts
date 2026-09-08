import { parseSchemaVersion } from "./schema";

export interface AuditResource {
  resource_type?: string;
  resource_env?: string;
  resource_app?: string;
  resource_key?: string;
  resource_version?: number;
  metadata_json?: string;
}

/** Lifecycle metadata stores selectors as decimal strings, never inferred defaults. */
export function auditReleaseSchemaVersion(event: AuditResource): number | undefined {
  if (event.resource_type !== "configuration_release" || !event.metadata_json) return undefined;
  try {
    const metadata: unknown = JSON.parse(event.metadata_json);
    if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) return undefined;
    const raw = (metadata as Record<string, unknown>).schema_version;
    return typeof raw === "string" ? parseSchemaVersion(raw) : undefined;
  } catch {
    return undefined;
  }
}

export function auditReleaseVersion(event: AuditResource): number | undefined {
  const version = event.resource_version;
  return typeof version === "number" && Number.isSafeInteger(version) && version > 0
    ? version
    : undefined;
}
