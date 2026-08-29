import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { formatRelative } from "@/lib/format";

const NOW = Date.UTC(2026, 7, 22, 12, 0, 0);
const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

describe("formatRelative", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("floors at every unit instead of rounding up", () => {
    expect(formatRelative(NOW - 90 * SECOND)).toBe("1m ago");
    expect(formatRelative(NOW - 45 * MINUTE)).toBe("45m ago");
    expect(formatRelative(NOW - 23.9 * HOUR)).toBe("23h ago");
    expect(formatRelative(NOW - 1.9 * DAY)).toBe("1d ago");
    expect(formatRelative(NOW - 59 * SECOND)).toBe("59s ago");
  });

  it("treats very recent timestamps as just now", () => {
    expect(formatRelative(NOW)).toBe("just now");
    expect(formatRelative(NOW - 4 * SECOND)).toBe("just now");
    expect(formatRelative(NOW - 5 * SECOND)).toBe("5s ago");
  });

  it("describes future timestamps as from now", () => {
    expect(formatRelative(NOW + 90 * SECOND)).toBe("1m from now");
    expect(formatRelative(NOW + 3 * DAY)).toBe("3d from now");
  });

  it("measures against an explicit clock when one is given", () => {
    const later = NOW + 10 * MINUTE;
    expect(formatRelative(NOW - MINUTE, later)).toBe("11m ago");
    expect(formatRelative(NOW - MINUTE)).toBe("1m ago");
  });

  it("renders an em dash for missing timestamps", () => {
    expect(formatRelative(0)).toBe("—");
    expect(formatRelative(null)).toBe("—");
    expect(formatRelative(undefined)).toBe("—");
    expect(formatRelative(-1)).toBe("—");
  });
});
