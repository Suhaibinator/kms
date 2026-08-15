export interface ZipTextEntry {
  name: string;
  content: string;
  /** Unix permission bits recorded in the central directory. Defaults to 0600. */
  mode?: number;
}

const encoder = new TextEncoder();

const CRC32_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let value = 0; value < table.length; value += 1) {
    let crc = value;
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc & 1) !== 0 ? 0xedb88320 ^ (crc >>> 1) : crc >>> 1;
    }
    table[value] = crc >>> 0;
  }
  return table;
})();

function crc32(bytes: Uint8Array): number {
  let crc = 0xffffffff;
  for (const byte of bytes) crc = CRC32_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function uint16(value: number): Uint8Array {
  const bytes = new Uint8Array(2);
  new DataView(bytes.buffer).setUint16(0, value, true);
  return bytes;
}

function uint32(value: number): Uint8Array {
  const bytes = new Uint8Array(4);
  new DataView(bytes.buffer).setUint32(0, value, true);
  return bytes;
}

function join(parts: readonly Uint8Array[]): Uint8Array {
  const length = parts.reduce((total, part) => total + part.byteLength, 0);
  const result = new Uint8Array(length);
  let offset = 0;
  for (const part of parts) {
    result.set(part, offset);
    offset += part.byteLength;
  }
  return result;
}

function encodeFilename(name: string): Uint8Array {
  const encoded = encoder.encode(name);
  if (
    !name ||
    name === "." ||
    name === ".." ||
    name.includes("\0") ||
    name.includes("/") ||
    name.includes("\\") ||
    encoded.byteLength > 0xffff
  ) {
    throw new Error("ZIP entry names must be simple filenames.");
  }
  return encoded;
}

// Builds a standards-compatible ZIP using the "stored" method (no
// compression). Credential PEM is small, and avoiding compression keeps the
// one-click bundle dependency-free and deterministic.
export function createStoredZip(entries: readonly ZipTextEntry[]): Blob {
  if (entries.length === 0 || entries.length > 0xffff) {
    throw new Error("ZIP must contain between 1 and 65,535 entries.");
  }

  const localParts: Uint8Array[] = [];
  const centralParts: Uint8Array[] = [];
  const names = new Set<string>();
  let localOffset = 0;

  for (const entry of entries) {
    const name = encodeFilename(entry.name);
    if (names.has(entry.name)) throw new Error(`Duplicate ZIP entry: ${entry.name}`);
    names.add(entry.name);

    const content = encoder.encode(entry.content);
    if (content.byteLength > 0xffffffff) throw new Error("ZIP entry is too large.");
    const mode = entry.mode ?? 0o600;
    if (!Number.isInteger(mode) || mode < 0 || mode > 0o777) {
      throw new Error("ZIP entry mode must contain Unix permission bits only.");
    }
    const checksum = crc32(content);
    const utf8Flag = 0x0800;
    const minimumDosDate = 33; // 1980-01-01; deterministic and accepted by ZIP readers.
    const unixRegularFileMode = 0o100000 | mode;
    const unixExternalAttributes = (unixRegularFileMode << 16) >>> 0;

    const local = join([
      uint32(0x04034b50),
      uint16(20),
      uint16(utf8Flag),
      uint16(0),
      uint16(0),
      uint16(minimumDosDate),
      uint32(checksum),
      uint32(content.byteLength),
      uint32(content.byteLength),
      uint16(name.byteLength),
      uint16(0),
      name,
      content,
    ]);
    localParts.push(local);

    centralParts.push(
      join([
        uint32(0x02014b50),
        uint16((3 << 8) | 20), // ZIP 2.0, created on Unix.
        uint16(20),
        uint16(utf8Flag),
        uint16(0),
        uint16(0),
        uint16(minimumDosDate),
        uint32(checksum),
        uint32(content.byteLength),
        uint32(content.byteLength),
        uint16(name.byteLength),
        uint16(0),
        uint16(0),
        uint16(0),
        uint16(0),
        uint32(unixExternalAttributes),
        uint32(localOffset),
        name,
      ]),
    );
    localOffset += local.byteLength;
  }

  const localDirectory = join(localParts);
  const centralDirectory = join(centralParts);
  const end = join([
    uint32(0x06054b50),
    uint16(0),
    uint16(0),
    uint16(entries.length),
    uint16(entries.length),
    uint32(centralDirectory.byteLength),
    uint32(localDirectory.byteLength),
    uint16(0),
  ]);
  const archive = join([localDirectory, centralDirectory, end]);
  const buffer = archive.buffer.slice(
    archive.byteOffset,
    archive.byteOffset + archive.byteLength,
  ) as ArrayBuffer;
  return new Blob([buffer], { type: "application/zip" });
}
