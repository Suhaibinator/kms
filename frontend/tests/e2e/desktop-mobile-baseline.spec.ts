import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

for (const width of [1280, 1440]) {
  for (const theme of ["light", "dark"]) {
    test(`desktop baseline ${width} ${theme}`, async ({ page }, info) => {
      test.skip(info.project.name !== "chromium");
      test.skip(
        process.platform !== "darwin",
        "Pixel baselines were captured on macOS; geometry tests run on every platform.",
      );
      await page.setViewportSize({ width, height: 960 });
      await page.addInitScript((value) => localStorage.setItem("kms-theme", value), theme);
      const state = incidentState();
      state.namespaces.dev.secrets = {};
      await mockConsole(page, state);
      await page.goto("/secrets?env=dev&app=gradethis");
      await expect(page.getByText("No secrets found")).toBeVisible();
      await page.evaluate(() => document.fonts.ready);
      await expect(page).toHaveScreenshot(`secrets-${width}-${theme}.png`, {
        animations: "disabled",
      });
    });
  }
}
