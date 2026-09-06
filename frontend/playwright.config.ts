import { defineConfig, devices } from "@playwright/test";

const port = process.env.KMS_E2E_PORT ?? "32189";
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  outputDir: process.env.KMS_E2E_OUTPUT_DIR ?? "test-results",
  testDir: "./tests/e2e",
  testMatch: "**/*.spec.ts",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL,
    trace: "on-first-retry",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile-chromium", use: { ...devices["Pixel 7"] } },
    { name: "mobile-webkit", use: { ...devices["iPhone 13"] } },
  ],
  webServer: {
    env: { KMS_NEXT_DIST_DIR: process.env.KMS_E2E_DIST_DIR ?? ".next" },
    command: `npm run dev -- --hostname 127.0.0.1 --port ${port}`,
    url: `${baseURL}/login`,
    reuseExistingServer: process.env.KMS_E2E_REUSE_SERVER === "1",
    timeout: 120_000,
  },
});
