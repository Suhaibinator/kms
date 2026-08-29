// Presentation helpers. Timestamps arrive as Unix milliseconds (*_unix_ms).

const EMPTY = "—"; // em dash

// The one place the frontend renders the legacy `/env/app/key` display form.
// The wire protocol never carries it; this is for humans (breadcrumbs, audit
// rows, confirmation copy) only.
export function displayPath(ref: { env: string; app: string; key: string }): string {
  return `/${ref.env}/${ref.app}/${ref.key}`;
}

// A namespace shown compactly, e.g. "prod/gradethis".
export function displayNamespace(ns: { env: string; app: string }): string {
  return `${ns.env}/${ns.app}`;
}

export function displayAuditResource(event: {
  resource_env?: string;
  resource_app?: string;
  resource_key?: string;
}): string | null {
  if (event.resource_env && event.resource_app && event.resource_key) {
    return displayPath({
      env: event.resource_env,
      app: event.resource_app,
      key: event.resource_key,
    });
  }
  if (event.resource_env && event.resource_app) {
    return displayNamespace({ env: event.resource_env, app: event.resource_app });
  }
  return null;
}

export function formatUnixMs(ms: number | undefined | null): string {
  if (!ms || ms <= 0) return EMPTY;
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return EMPTY;
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// `now` lets a ticking clock (lib/useNow.ts) drive re-renders; it defaults to
// the wall clock so one-off callers need not pass it.
export function formatRelative(ms: number | undefined | null, now: number = Date.now()): string {
  if (!ms || ms <= 0) return EMPTY;
  const diff = now - ms;
  const abs = Math.abs(diff);
  // Floor at every unit so 90s reads "1m", not "2m", and 45m never becomes "1h".
  const sec = Math.floor(abs / 1000);
  const suffix = diff >= 0 ? "ago" : "from now";
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ${suffix}`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ${suffix}`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ${suffix}`;
  const day = Math.floor(hr / 24);
  return `${day}d ${suffix}`;
}

// Convert a datetime-local input value (local time) to Unix ms, or undefined.
export function datetimeLocalToUnixMs(value: string): number | undefined {
  if (!value) return undefined;
  const ms = new Date(value).getTime();
  return Number.isNaN(ms) ? undefined : ms;
}

// Pretty-print a metadata_json string; returns the raw string if not JSON.
export function prettyJson(raw: string | undefined | null): string {
  if (!raw) return "";
  try {
    const parsed = JSON.parse(raw);
    return JSON.stringify(parsed, null, 2);
  } catch {
    return raw;
  }
}

// True when a metadata_json string carries no meaningful content.
export function isEmptyJson(raw: string | undefined | null): boolean {
  if (!raw) return true;
  const t = raw.trim();
  return t === "" || t === "{}" || t === "null";
}

export function labelEntries(
  labels: Record<string, number> | undefined | null,
): Array<[string, number]> {
  if (!labels) return [];
  return Object.entries(labels).sort((a, b) => a[0].localeCompare(b[0]));
}
