import { expect, test } from "@playwright/test";
import ready from "../fixtures/backend/overview-ready.json";
import { incidentState, mockConsole } from "./fakes/console-api";

test("a large contract keeps environment actions reachable and schema upgrade explicit", async ({
  page,
}, info) => {
  const state = incidentState();
  await mockConsole(page, state);
  const overview = structuredClone(ready);
  overview.environments = overview.environments.filter((e) => e.namespace.env === "dev");
  for (let i = 0; i < 64; i++)
    overview.application.contract.push({ alias: `secret_${i}`, kind: "secret", content_type: "" });
  await page.route("**/api/v1/applications/overview?**", (route) =>
    route.fulfill({ json: overview }),
  );
  await page.goto("/applications?app=gradethis");
  const definition = page.getByRole("region", { name: "Definition" });
  await expect(definition.getByText("View contract")).toBeVisible();
  await expect(definition.locator("details")).not.toHaveAttribute("open");
  await expect(page.getByRole("button", { name: "Ship to dev…" })).toBeVisible();
  const box = await definition.boundingBox();
  expect(box?.height).toBeLessThan((page.viewportSize()?.width ?? 1280) > 768 ? 330 : 540);
  await page.getByRole("button", { name: "More actions" }).click();
  await expect(page.getByRole("menuitem", { name: "Import defaults to dev…" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.screenshot({
    path: info.outputPath("compact-overview.png"),
    fullPage: true,
    animations: "disabled",
  });
  await page.getByRole("button", { name: "Upgrade schema…" }).click();
  const dialog = page.getByRole("dialog", { name: "Upgrade application schema" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveCSS("opacity", "1");
  await expect(dialog.getByLabel("Starting values")).toBeVisible();
  await expect(dialog.getByRole("region", { name: "Upgrade scope" })).toContainText(
    "Environment change:",
  );
  await page.screenshot({
    path: info.outputPath("schema-upgrade.png"),
    fullPage: true,
    animations: "disabled",
  });
});
