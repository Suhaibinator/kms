// The ?returnTo round-trip: the guard writes where the visitor was headed, and
// the login page reads it back. Because the value survives a full page load in
// a URL anyone can edit, it is validated on both ends rather than trusted.

/**
 * Same-origin path validation for the ?returnTo round-trip. Anything that could
 * leave the origin — an absolute URL, a protocol-relative "//host", a backslash
 * form browsers normalise to "//" — is rejected outright.
 */
export function safeReturnTo(value: string | null | undefined): string | null {
  if (!value) return null;
  if (value[0] !== "/") return null; // "http://…", "javascript:…"
  if (value[1] === "/" || value[1] === "\\") return null; // "//evil", "/\evil"
  if (value === "/login" || value.startsWith("/login?")) return null; // no loop
  return value;
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
