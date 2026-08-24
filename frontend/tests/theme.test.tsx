import { act, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import {
  applyResolvedTheme,
  readThemePreference,
  resolveTheme,
  THEME_BOOT_SCRIPT,
  THEME_STORAGE_KEY,
  ThemeProvider,
  useTheme,
} from "@/lib/theme";

function Probe() {
  const { preference, resolved } = useTheme();
  return (
    <output data-testid="probe">
      {preference}/{resolved}
    </output>
  );
}

describe("theme resolution", () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.classList.remove("dark");
  });

  it("follows the OS only when the preference is system", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("light", true)).toBe("light");
    expect(resolveTheme("dark", false)).toBe("dark");
  });

  it("treats an unknown stored value as system", () => {
    window.localStorage.setItem(THEME_STORAGE_KEY, "sepia");
    expect(readThemePreference()).toBe("system");
    window.localStorage.setItem(THEME_STORAGE_KEY, "dark");
    expect(readThemePreference()).toBe("dark");
  });

  it("applies the class and the browser chrome colour together", () => {
    const meta = document.createElement("meta");
    meta.setAttribute("name", "theme-color");
    document.head.appendChild(meta);
    applyResolvedTheme("dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(meta.getAttribute("content")).toBe("#0b0e14");
    applyResolvedTheme("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(meta.getAttribute("content")).toBe("#f4f6f9");
    meta.remove();
  });

  it("boot script agrees with the module on key and colours", () => {
    expect(THEME_BOOT_SCRIPT).toContain(`"${THEME_STORAGE_KEY}"`);
    expect(THEME_BOOT_SCRIPT).toContain('"#0b0e14"');
    expect(THEME_BOOT_SCRIPT).toContain('"#f4f6f9"');
    // Plain ES5 that never throws: it runs before any bundle exists.
    expect(THEME_BOOT_SCRIPT).toMatch(/^\(function\(\)\{try\{.*\}catch\(e\)\{\}\}\)\(\);$/);
    expect(() => new Function(THEME_BOOT_SCRIPT)).not.toThrow();
  });

  it("useTheme works without a provider", () => {
    render(<Probe />);
    expect(screen.getByTestId("probe")).toHaveTextContent("system/light");
  });
});

describe("ThemeProvider + ThemeSwitch", () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.classList.remove("dark");
  });

  it("restores a stored preference and lets the switch change it", async () => {
    window.localStorage.setItem(THEME_STORAGE_KEY, "dark");
    render(
      <ThemeProvider>
        <ThemeSwitch />
        <Probe />
      </ThemeProvider>,
    );
    await act(async () => {});
    expect(screen.getByTestId("probe")).toHaveTextContent("dark/dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(screen.getByRole("radio", { name: "Dark" })).toBeChecked();

    await act(async () => {
      screen.getByRole("radio", { name: "Light" }).click();
    });
    expect(screen.getByTestId("probe")).toHaveTextContent("light/light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");

    await act(async () => {
      screen.getByRole("radio", { name: "Match system" }).click();
    });
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBeNull();
    expect(screen.getByRole("radio", { name: "Match system" })).toBeChecked();
  });
});
