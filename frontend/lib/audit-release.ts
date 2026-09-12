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

function auditMetadata(event: AuditResource): Record<string, unknown> | undefined {
  if (!event.metadata_json) return undefined;
  try {
    const metadata: unknown = JSON.parse(event.metadata_json);
    if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) return undefined;
    return metadata as Record<string, unknown>;
  } catch {
    return undefined;
  }
}

/** A positive version stored as a decimal string; anything else is "unknown". */
function decimalVersion(raw: unknown): number | undefined {
  if (typeof raw !== "string" || !/^\d+$/.test(raw)) return undefined;
  const version = Number(raw);
  return Number.isSafeInteger(version) && version > 0 ? version : undefined;
}

/**
 * The version an activation or rollback replaced. `configuration_release.activate`
 * and `.rollback` events record it as `previous_version`; a first activation
 * stores "0", which reads as undefined here because there is nothing to
 * compare against.
 */
export function auditReleasePreviousVersion(event: AuditResource): number | undefined {
  if (event.resource_type !== "configuration_release") return undefined;
  return decimalVersion(auditMetadata(event)?.previous_version);
}

export interface AuditShipVersions {
  previousVersion: number | undefined;
  releaseVersion: number;
  schemaVersion: number | undefined;
  environment: string | undefined;
  /** Not recorded by the server today; present only when a future ship event carries it. */
  releaseName: string | undefined;
}

/**
 * The activation an `application.ship` event performed, when it did
 * (`activated: "true"`). The event's resource is the application, so the
 * environment and versions live in the metadata.
 */
export function auditShipReleaseVersions(event: AuditResource): AuditShipVersions | undefined {
  if (event.resource_type !== "application") return undefined;
  const metadata = auditMetadata(event);
  if (metadata?.activated !== "true") return undefined;
  const releaseVersion = decimalVersion(metadata.release_version);
  if (releaseVersion === undefined) return undefined;
  const schema = metadata.schema_version;
  return {
    previousVersion: decimalVersion(metadata.previous_version),
    releaseVersion,
    schemaVersion: typeof schema === "string" ? parseSchemaVersion(schema) : undefined,
    environment: typeof metadata.environment === "string" ? metadata.environment : undefined,
    releaseName: typeof metadata.release_name === "string" ? metadata.release_name : undefined,
  };
}

export function auditReleaseVersion(event: AuditResource): number | undefined {
  const version = event.resource_version;
  return typeof version === "number" && Number.isSafeInteger(version) && version > 0
    ? version
    : undefined;
}
