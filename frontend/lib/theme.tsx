import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";

export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

export const THEME_STORAGE_KEY = "kms-theme";
const DARK_QUERY = "(prefers-color-scheme: dark)";
// Mirrors --bg in styles/globals.css for each theme; browsers paint this
// behind the page and in the tab strip, so it has to agree with the stylesheet.
export const THEME_COLOR: Record<ResolvedTheme, string> = {
  light: "#f4f6f9",
  dark: "#0b0e14",
};

export function isThemePreference(value: unknown): value is ThemePreference {
  return value === "system" || value === "light" || value === "dark";
}

export function readThemePreference(): ThemePreference {
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
    return isThemePreference(stored) ? stored : "system";
  } catch {
    return "system";
  }
}

export function resolveTheme(preference: ThemePreference, prefersDark: boolean): ResolvedTheme {
  if (preference === "system") return prefersDark ? "dark" : "light";
  return preference;
}

function systemPrefersDark(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia(DARK_QUERY).matches
    : false;
}

/** Flips the `.dark` class the stylesheet keys on, and the browser chrome colour. */
export function applyResolvedTheme(theme: ResolvedTheme): void {
  const root = document.documentElement;
  root.classList.toggle("dark", theme === "dark");
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", THEME_COLOR[theme]);
}

/**
 * Runs inline in <head> before the first paint so a stored preference never
 * flashes the other theme. Kept as plain ES5 and self-contained on purpose: it
 * must not depend on any bundle. Keep the key and colours in sync with the
 * constants above.
 */
export const THEME_BOOT_SCRIPT = `(function(){try{var p=window.localStorage.getItem(${JSON.stringify(
  THEME_STORAGE_KEY,
)});var d=p==="dark"||(p!=="light"&&window.matchMedia("${DARK_QUERY}").matches);document.documentElement.classList.toggle("dark",d);var m=document.querySelector('meta[name="theme-color"]');if(m)m.setAttribute("content",d?${JSON.stringify(
  THEME_COLOR.dark,
)}:${JSON.stringify(THEME_COLOR.light)})}catch(e){}})();`;

interface ThemeState {
  preference: ThemePreference;
  resolved: ResolvedTheme;
  setPreference: (preference: ThemePreference) => void;
}

// A provider-less default keeps components renderable in isolation (tests,
// storybook-style probes) without every one of them mocking the context.
const FALLBACK: ThemeState = { preference: "system", resolved: "light", setPreference: () => {} };
const ThemeContext = createContext<ThemeState | null>(null);

export function ThemeProvider({ children }: { children: ReactNode }) {
  // Both start at the server-rendered defaults so hydration matches; the boot
  // script has already put the right class on <html>, and the effect below
  // brings React's copy of the state in line right after mount.
  const [preference, setPreferenceState] = useState<ThemePreference>("system");
  const [resolved, setResolved] = useState<ResolvedTheme>("light");

  useEffect(() => {
    setPreferenceState(readThemePreference());
  }, []);

  useEffect(() => {
    const sync = () => {
      const next = resolveTheme(preference, systemPrefersDark());
      setResolved(next);
      applyResolvedTheme(next);
    };
    sync();
    if (preference !== "system" || typeof window.matchMedia !== "function") return;
    // Only "system" follows the OS; an explicit choice stays put.
    const query = window.matchMedia(DARK_QUERY);
    query.addEventListener("change", sync);
    return () => query.removeEventListener("change", sync);
  }, [preference]);

  const setPreference = useCallback((next: ThemePreference) => {
    setPreferenceState(next);
    try {
      if (next === "system") window.localStorage.removeItem(THEME_STORAGE_KEY);
      else window.localStorage.setItem(THEME_STORAGE_KEY, next);
    } catch {
      /* storage unavailable; the choice still applies for this page */
    }
  }, []);

  const value = useMemo<ThemeState>(
    () => ({ preference, resolved, setPreference }),
    [preference, resolved, setPreference],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeState {
  return useContext(ThemeContext) ?? FALLBACK;
}
