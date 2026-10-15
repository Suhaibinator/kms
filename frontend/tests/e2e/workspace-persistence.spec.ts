import { expect, test } from "@playwright/test";
import { incidentState, mockConsole } from "./fakes/console-api";

// The application and environment an operator picked follow them across the
// resource pages, including a bare arrival (typed URL, reload, a "Back to…"
// link): the page resumes the workspace by writing it into its URL.
test("keeps the selected application and environment across pages", async ({ page }) => {
  await mockConsole(page, incidentState());

  await page.goto("/secrets?env=prod&app=gradethis");
  await expect(page.getByRole("link", { name: "db_password" })).toBeVisible();

  for (const path of ["/parameters", "/secrets", "/releases"]) {
    await page.goto(path);
    await expect(page).toHaveURL(
      (url) =>
        url.pathname === path &&
        url.searchParams.get("app") === "gradethis" &&
        url.searchParams.get("env") === "prod",
    );
    await expect(page.getByRole("combobox", { name: "Application" }).first()).toHaveText(
      "gradethis",
    );
    await expect(page.getByRole("combobox", { name: "Environment" }).first()).toHaveText("prod");
  }
});
