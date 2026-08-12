import { createHash, timingSafeEqual } from "node:crypto";
import {
  type ConfigurationRelease,
  type ConfigurationReleaseEntry,
  ConfigurationRelease as ConfigurationReleaseMessage,
} from "../generated/kms.js";

/** SHA-256 of bytes as lowercase hexadecimal. */
export function sha256Hex(value: Uint8Array | string): string {
  return createHash("sha256").update(value).digest("hex");
}

/**
 * Mirrors the server's deterministic immutable protobuf projection. Allocated
 * release versions, timestamps, creator and the digest itself are excluded.
 */
export function deterministicReleaseDigest(release: ConfigurationRelease): string {
  if (!release.namespace) throw new TypeError("release namespace is required");

  const entries = [...release.entries].sort((left, right) =>
    Buffer.compare(Buffer.from(left.alias), Buffer.from(right.alias)),
  );
  const projectedEntries = entries.map(projectEntry);
  const projection = ConfigurationReleaseMessage.create({
    namespace: { env: release.namespace.env, app: release.namespace.app },
    name: release.name,
    version: 0n,
    schemaId: release.schemaId,
    schemaVersion: release.schemaVersion,
    entries: projectedEntries,
    digest: "",
    metadataJson: release.metadataJson,
    createdBy: "",
    createdAtUnixMs: 0n,
  });
  return sha256Hex(ConfigurationReleaseMessage.encode(projection).finish());
}

export function releaseDigestMatches(release: ConfigurationRelease): boolean {
  const claimed = decodeSha256(release.digest);
  if (!claimed) return false;
  const calculated = Buffer.from(deterministicReleaseDigest(release), "hex");
  return timingSafeEqual(claimed, calculated);
}

function projectEntry(entry: ConfigurationReleaseEntry): ConfigurationReleaseEntry {
  if (!entry.ref?.namespace) {
    throw new TypeError(`release entry ${JSON.stringify(entry.alias)} has no resource namespace`);
  }
  return {
    alias: entry.alias,
    kind: entry.kind,
    ref: {
      namespace: { env: entry.ref.namespace.env, app: entry.ref.namespace.app },
      key: entry.ref.key,
    },
    version: entry.version,
    contentType: entry.contentType,
    metadataJson: entry.metadataJson,
    parameterDigest: entry.parameterDigest,
    clientBound: entry.clientBound,
    hasAccessToken: entry.hasAccessToken,
  };
}

function decodeSha256(value: string): Buffer | undefined {
  if (!/^[0-9a-f]{64}$/iu.test(value)) return undefined;
  return Buffer.from(value, "hex");
}
