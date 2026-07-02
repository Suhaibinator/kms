// Base64 helpers used for secret values on the wire (value_base64).
//
// SECURITY: these functions handle plaintext secret material. Never log,
// stringify into errors, or otherwise persist their inputs/outputs. Callers
// keep decoded values in ephemeral React state only.

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
