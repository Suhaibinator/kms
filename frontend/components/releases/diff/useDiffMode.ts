import { useEffect, useState } from "react";

/** How a row's whole value is read: the field list, a unified line diff or two columns. */
export type DiffMode = "fields" | "unified" | "split";

const MODE_KEY = "kms-release-diff-mode";

/** Stored values from the first release of the view (`structural` / `side`) map onto the new names. */
function migrate(stored: string | null): DiffMode | null {
  switch (stored) {
    case "fields":
    case "unified":
    case "split":
      return stored;
    case "structural":
      return "fields";
    case "side":
      return "split";
    default:
      return null;
  }
}

/**
 * The value view persists per browser (not in the URL: the URL says what is
 * compared, this says how the reader reads). A legacy or unknown stored value
 * migrates once; a blocked store falls back to the field list.
 */
export function useDiffMode(): [DiffMode, (mode: DiffMode) => void] {
  const [mode, setMode] = useState<DiffMode>("fields");
  useEffect(() => {
    try {
      const stored = window.localStorage.getItem(MODE_KEY);
      const next = migrate(stored);
      if (next === null) return;
      setMode(next);
      if (next !== stored) window.localStorage.setItem(MODE_KEY, next);
    } catch {
      // Private mode or blocked storage: keep the default.
    }
  }, []);
  const update = (next: DiffMode) => {
    setMode(next);
    try {
      window.localStorage.setItem(MODE_KEY, next);
    } catch {
      // Same: the choice just does not persist.
    }
  };
  return [mode, update];
}
