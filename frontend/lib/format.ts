// Presentation helpers. Timestamps arrive as Unix milliseconds (*_unix_ms).

const EMPTY = "—"; // em dash

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

export function formatRelative(ms: number | undefined | null): string {
  if (!ms || ms <= 0) return EMPTY;
  const diff = Date.now() - ms;
  const abs = Math.abs(diff);
  const sec = Math.round(abs / 1000);
  const suffix = diff >= 0 ? "ago" : "from now";
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ${suffix}`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ${suffix}`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ${suffix}`;
  const day = Math.round(hr / 24);
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
