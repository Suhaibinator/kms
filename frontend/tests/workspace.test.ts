import type { MouseEvent } from "react";
import { describe, expect, it, vi } from "vitest";
import { shouldOpenWorkspace } from "@/lib/workspace";

describe("workspace link activation", () => {
  it.each([
    {},
    { metaKey: true },
    { ctrlKey: true },
    { shiftKey: true },
    { altKey: true },
    { button: 1 },
    { defaultPrevented: true },
  ])("preserves native navigation for modified activation %j", (overrides) => {
    const event = {
      button: 0,
      currentTarget: { focus: vi.fn() },
      defaultPrevented: false,
      preventDefault: vi.fn(),
      ...overrides,
    };
    const ordinary = Object.keys(overrides).length === 0;
    expect(shouldOpenWorkspace(event as unknown as MouseEvent<HTMLElement>)).toBe(ordinary);
    expect(event.preventDefault).toHaveBeenCalledTimes(ordinary ? 1 : 0);
    expect(event.currentTarget.focus).toHaveBeenCalledTimes(ordinary ? 1 : 0);
  });
});
