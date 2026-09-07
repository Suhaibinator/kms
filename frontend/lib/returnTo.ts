// The ?returnTo round-trip: the guard writes where the visitor was headed, and
// the login page reads it back. Because the value survives a full page load in
// a URL anyone can edit, it is validated on both ends rather than trusted.

// The value must be a same-origin *path*, so it is resolved against a fixed
// private origin and accepted only if it stays there. A value that resolves
// anywhere else under this base ("//host", "/\\host") would leave the real
// origin too, and a constant base keeps the check identical during the static
// prerender (no window) and in tests.
const BASE_ORIGIN = "https://return-to.invalid";

// ASCII control characters (tab, CR and LF among them) are stripped by the URL
// parser before it looks for "//", and a backslash is normalised to "/", so
// "/\t/evil" and "/\\evil" both canonicalise to "//evil". A positional check
// on the raw string cannot see that; refuse the characters outright.
function hasControlOrBackslash(value: string): boolean {
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (code < 0x20 || code === 0x7f || code === 0x5c) return true;
  }
  return false;
}

/**
 * Same-origin path validation for the ?returnTo round-trip. The value is
 * parsed as a URL and accepted only when it canonicalises to a path on the
 * same origin; the canonical path + query + hash is returned, never the raw
 * input. Anything that could leave the origin — an absolute URL, a
 * protocol-relative "//host", a backslash or control-character form browsers
 * normalise to "//" — is rejected outright.
 */
export function safeReturnTo(value: string | null | undefined): string | null {
  if (!value) return null;
  if (value[0] !== "/") return null; // "http://…", "javascript:…"
  if (value[1] === "/") return null; // reject every supplied authority, including BASE_ORIGIN
  if (hasControlOrBackslash(value)) return null; // "/\t/evil", "/\\evil"
  let url: URL;
  try {
    url = new URL(value, BASE_ORIGIN);
  } catch {
    return null;
  }
  if (url.origin !== BASE_ORIGIN) return null; // "//evil" and every other escape
  const target = `${url.pathname}${url.search}${url.hash}`;
  // Dot segments can expose an authority when the returned path is parsed
  // again: /a/..//host has this origin, but its pathname is //host.
  if (target.startsWith("//")) return null;
  if (url.pathname === "/login") return null; // no loop, including query/fragment forms
  return target;
}

/** Where the browser is right now, path + query + hash, for a returnTo value. */
export function currentPath(): string {
  if (typeof window === "undefined") return "/";
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

/** "/login", or "/login?returnTo=…" when there is somewhere worth going back to. */
export function loginHref(from: string): string {
  const target = safeReturnTo(from);
  return target && target !== "/" ? `/login?returnTo=${encodeURIComponent(target)}` : "/login";
}
