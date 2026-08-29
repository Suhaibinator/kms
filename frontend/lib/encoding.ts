// Base64 helpers used for secret values on the wire (value_base64).
//
// SECURITY: these functions handle plaintext secret material. Never log,
// stringify into errors, or otherwise persist their inputs/outputs. Callers
// keep decoded values in ephemeral React state only.

import { validateParameterValue, validateValueSize } from "@/lib/validation";

export function utf8ToBase64(input: string): string {
  const bytes = new TextEncoder().encode(input);
  let binary = "";
  for (let i = 0; i < bytes.length; i += 1) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

export function base64ToUtf8(b64: string): string {
  return new TextDecoder("utf-8", { fatal: false }).decode(base64ToBytes(b64));
}

export function base64ByteLength(b64: string): number {
  try {
    return atob(b64).length;
  } catch {
    return 0;
  }
}

// True when the bytes look like valid, printable UTF-8 text (no NUL / control
// bytes other than tab/newline/carriage-return). Used only to decide how to
// present a revealed value; never leaks the value itself.
export function looksLikeText(b64: string): boolean {
  try {
    const bytes = base64ToBytes(b64);
    for (let i = 0; i < bytes.length; i += 1) {
      const c = bytes[i];
      if (c === 9 || c === 10 || c === 13) continue;
      if (c < 32) return false;
    }
    const decoded = new TextDecoder("utf-8", { fatal: true });
    decoded.decode(bytes);
    return true;
  } catch {
    return false;
  }
}

/** Content types offered for a secret; anything else is typed in by hand. */
export const SECRET_CONTENT_TYPES = [
  "text/plain",
  "application/json",
  "application/x-pem-file",
  "application/pkcs8",
  "application/x-pkcs12",
  "application/octet-stream",
  "text/x-yaml",
] as const;

export type GeneratedEncoding = "base64url" | "hex";

/** Standard base64 without padding, using the URL-safe alphabet. */
function toBase64Url(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 1) binary += String.fromCharCode(bytes[i]);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function toHex(bytes: Uint8Array): string {
  let out = "";
  for (let i = 0; i < bytes.length; i += 1) out += bytes[i].toString(16).padStart(2, "0");
  return out;
}

/**
 * A fresh random token from the browser's CSPRNG: `bytes` of entropy, spelled
 * as base64url or hex. Never logged; the caller places it in the value field.
 */
export function generateSecretValue(bytes: 32 | 64, encoding: GeneratedEncoding): string {
  const buffer = new Uint8Array(bytes);
  crypto.getRandomValues(buffer);
  return encoding === "hex" ? toHex(buffer) : toBase64Url(buffer);
}

/** The bytes sent for a secret value: typed text is encoded, pasted base64 goes through untouched. */
export function secretValueBase64(value: string, alreadyBase64: boolean): string {
  return alreadyBase64 ? value.replace(/[\r\n]/g, "") : utf8ToBase64(value);
}

/**
 * The secret value rule: required, capped, and — when passed through as
 * base64 — a legal standard-base64 string. The message never quotes the value.
 */
export function validateSecretValue(value: string, alreadyBase64: boolean): string | null {
  if (!value) return "A secret value is required.";
  const size = validateValueSize(value);
  if (size) return size;
  return alreadyBase64 ? validateParameterValue(value, "binary") : null;
}
