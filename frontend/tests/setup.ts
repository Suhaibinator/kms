import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// One unmount between every test in every file. Without it a leaked render
// makes the next test's `screen` queries ambiguous in a way that only shows up
// when files run in a particular order.
afterEach(cleanup);

// Node >= 22 ships an experimental global `localStorage` that shadows
// happy-dom's; without a --localstorage-file it is a method-less stub, so
// `localStorage.getItem` is undefined in tests. Install a plain in-memory
// Storage when that is the case (sessionStorage is unaffected).
if (typeof window.localStorage?.getItem !== "function") {
  const backing = new Map<string, string>();
  const memoryStorage: Storage = {
    get length() {
      return backing.size;
    },
    key: (index) => Array.from(backing.keys())[index] ?? null,
    getItem: (key) => backing.get(key) ?? null,
    setItem: (key, value) => {
      backing.set(key, String(value));
    },
    removeItem: (key) => {
      backing.delete(key);
    },
    clear: () => backing.clear(),
  };
  for (const target of [window, globalThis]) {
    Object.defineProperty(target, "localStorage", { value: memoryStorage, configurable: true });
  }
}
