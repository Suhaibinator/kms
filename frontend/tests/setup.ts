import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// One unmount between every test in every file. Without it a leaked render
// makes the next test's `screen` queries ambiguous in a way that only shows up
// when files run in a particular order.
afterEach(cleanup);
