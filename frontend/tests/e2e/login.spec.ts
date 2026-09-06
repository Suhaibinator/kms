import { expect, test, type Page } from "@playwright/test";

async function mockAuthenticatedConsole(page: Page, namespaces: Record<string, unknown>[] = []) {
  await page.addInitScript(() => {
    sessionStorage.setItem("kms_token", "kms_e2e_token");
  });
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const payload = path.endsWith("/whoami")
      ? { name: "e2e-client", kind: "client", namespace: { env: "prod", app: "billing" } }
      : path.endsWith("/health")
        ? {
            healthy: true,
            ready: true,
            version: "e2e",
            current_revision: 0,
            grpc_addr: "127.0.0.1:8443",
            tls_enabled: true,
            admin_client_cert_required: false,
            client_cert_presented: false,
          }
        : path.endsWith("/namespaces")
          ? { namespaces, next_page_token: "" }
          : path.endsWith("/subscribers")
            ? { subscribers: [], current_revision: 0 }
            : path.endsWith("/audit")
              ? { events: [], next_page_token: "" }
              : {};
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
}

test("login exposes a labelled identity-token form with the intended font", async ({ page }) => {
  await page.goto("/login");

  await expect(page.getByRole("heading", { name: "KMS Console", level: 1 })).toBeVisible();
  await expect(page.getByLabel("Identity token")).toHaveAttribute("type", "password");
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();

  const fontFamily = await page
    .getByRole("heading", { name: "KMS Console", level: 1 })
    .evaluate((heading) => getComputedStyle(heading).fontFamily);
  expect(fontFamily.toLowerCase()).not.toContain("times");
});

test("shows neutral certificate diagnostics and generic login failures", async ({
  page,
}, testInfo) => {
  const identity = `kms://identity/${"long-admin-name-".repeat(12)}`;
  await page.route("**/api/v1/auth/connection", (route) =>
    route.fulfill({
      json: {
        tls_enabled: true,
        client_certificate: {
          identity_uri: identity,
          fingerprint_sha256: "ab".repeat(32),
          not_after: "2027-01-01T00:00:00Z",
        },
      },
    }),
  );
  await page.route("**/api/v1/auth/login", (route) =>
    route.fulfill({
      status: 401,
      json: { error: { code: "unauthenticated", message: "invalid credentials" } },
    }),
  );
  await page.goto("/login");
  await expect(page.getByText(identity, { exact: true })).toBeVisible();
  await page.getByText("Certificate details", { exact: true }).click();
  await expect(page.getByRole("button", { name: "Copy fingerprint" })).toBeVisible();
  await page.getByLabel("Identity token").fill("bad-token");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.locator("[data-slot=field-error]")).toContainText(
    "Sign-in failed. Check your credentials and try again.",
  );
  for (const theme of ["light", "dark"]) {
    await page.evaluate(
      (value) => document.documentElement.classList.toggle("dark", value === "dark"),
      theme,
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath(`certificate-${theme}.png`),
      fullPage: true,
    });
  }
  await page.route("**/api/v1/auth/connection", (route) =>
    route.fulfill({ json: { tls_enabled: true, client_certificate: null } }),
  );
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByText("No client certificate received", { exact: true })).toBeVisible();
  await expect(page.getByText(identity, { exact: true })).toHaveCount(0);
});

test("portalled dropdowns and filter controls stay visually consistent", async ({ page }) => {
  await mockAuthenticatedConsole(page, [
    {
      app: "billing",
      env: "prod",
      description: "",
      allowed_auth_methods: ["mtls"],
      created_by: "admin",
      created_at_unix_ms: 1,
      parameter_count: 0,
      secret_count: 0,
    },
  ]);
  await page.goto("/parameters");

  const trigger = page.getByRole("combobox", { name: "Application" });
  await expect(trigger).toBeVisible();
  const triggerFont = await trigger.evaluate((element) => getComputedStyle(element).fontFamily);

  await trigger.click();
  const option = page.getByRole("option", { name: "billing" });
  await expect(option).toBeVisible();
  const optionFont = await option.evaluate((element) => getComputedStyle(element).fontFamily);

  expect(optionFont).toBe(triggerFont);

  await page.keyboard.press("Escape");
  const controls = [
    trigger,
    page.getByRole("combobox", { name: "Environment" }),
    page.getByRole("textbox", { name: "Key prefix" }),
    page.getByRole("button", { name: "Filter" }),
    page.getByRole("button", { name: "Clear" }),
  ];
  const boxes = await Promise.all(controls.map((control) => control.boundingBox()));
  expect(boxes.every(Boolean)).toBe(true);
  const visibleBoxes = boxes.filter((box) => box !== null);

  if ((page.viewportSize()?.width ?? 1280) <= 768) {
    expect(
      Math.max(...visibleBoxes.map((box) => box.width)) -
        Math.min(...visibleBoxes.map((box) => box.width)),
    ).toBeLessThanOrEqual(1);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  } else {
    expect(
      Math.max(...visibleBoxes.map((box) => box.y)) - Math.min(...visibleBoxes.map((box) => box.y)),
    ).toBeLessThanOrEqual(1);
    expect(
      Math.max(...visibleBoxes.map((box) => box.height)) -
        Math.min(...visibleBoxes.map((box) => box.height)),
    ).toBeLessThanOrEqual(1);
  }
});

test("mobile navigation is isolated, focus-managed, and capability-aware", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 1280) > 768, "mobile-only drawer behavior");

  await mockAuthenticatedConsole(page);

  await page.goto("/");
  const toggle = page.getByRole("button", { name: "Open navigation" });
  await expect(toggle).toBeVisible();

  const drawer = page.getByRole("dialog", { name: "Primary navigation" });
  const shell = page.locator(".app-shell");
  await expect(drawer).toHaveCount(0);

  await toggle.click();
  await expect(drawer).toBeVisible();

  const shellFont = await shell.evaluate((element) => getComputedStyle(element).fontFamily);
  const drawerFont = await drawer.evaluate((element) => getComputedStyle(element).fontFamily);
  expect(drawerFont).toBe(shellFont);

  // The drawer is a modal portalled outside the shell, so the whole shell —
  // topbar, desktop sidebar, and main — leaves the accessibility tree while it
  // is open. Asserting the shell is hidden rather than that `main` is inert
  // keeps this tied to the behaviour instead of the drawer's implementation.
  await expect(shell).toHaveAttribute("aria-hidden", "true");
  // The desktop sidebar renders the same links, so a single reachable
  // "Overview" link proves the background really is isolated.
  await expect(page.getByRole("link", { name: "Overview" })).toHaveCount(1);
  await expect(drawer.getByRole("link", { name: "Overview" })).toBeVisible();

  // Focus is pulled into the drawer rather than left behind on the trigger.
  await expect(drawer.locator(":focus")).toHaveCount(1);

  // A non-admin client never sees admin-only destinations.
  await expect(page.getByRole("link", { name: "Policies" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Identities" })).toHaveCount(0);

  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);
  await expect(toggle).toBeFocused();
  await expect(shell).not.toHaveAttribute("aria-hidden", "true");
});
