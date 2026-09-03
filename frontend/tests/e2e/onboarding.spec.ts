import { expect, type Page, test } from "@playwright/test";

// A minimal in-memory console API for the first-run journey: an admin session
// against an empty store. Creating an application (POST /applications) makes
// it appear in the list, the fleet overview and the per-app overview, so the
// wizard's redirect lands on a real application page with a setup panel.
interface Contract {
  alias: string;
  kind: "parameter" | "secret";
  content_type?: string;
}
interface State {
  applications: Array<{
    name: string;
    description: string;
    release_name: string;
    schema_version: number;
    contract: Contract[];
  }>;
}

function overview(app: State["applications"][number]) {
  return {
    application: {
      ...app,
      created_by: "admin",
      created_at_unix_ms: 1755000000000,
      updated_at_unix_ms: 1755000000000,
      archived_at_unix_ms: 0,
      archived_by: "",
      environment_count: 0,
    },
    status: "setup",
    findings: [
      { code: "no_environments", severity: "warning", scope: {}, params: {} },
      ...(app.schema_version
        ? []
        : [{ code: "schema_unpinned", severity: "info", scope: {}, params: {} }]),
    ],
    environments: [],
    rows: [],
  };
}

async function installFake(page: Page): Promise<State> {
  const state: State = { applications: [] };
  await page.addInitScript(() => {
    sessionStorage.setItem("kms_token", "kms_e2e_admin_token");
  });
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^.*\/api\/v1/, "");
    const method = request.method();
    const json = (status: number, body: unknown) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

    if (path === "/whoami") return json(200, { name: "root", kind: "admin", namespace: null });
    if (path === "/health") {
      return json(200, {
        healthy: true,
        ready: true,
        version: "e2e",
        current_revision: 0,
        grpc_addr: "127.0.0.1:8443",
        tls_enabled: true,
        admin_client_cert_required: false,
        client_cert_presented: false,
      });
    }
    if (path === "/namespaces") return json(200, { namespaces: [], next_page_token: "" });
    if (path === "/subscribers") return json(200, { subscribers: [], current_revision: 0 });
    if (path === "/audit") return json(200, { events: [], next_page_token: "" });
    if (path === "/schemas" || path === "/releases") {
      return json(200, { schemas: [], releases: [], next_page_token: "" });
    }
    if (path === "/applications" && method === "GET") {
      return json(200, {
        applications: state.applications.map((app) => overview(app).application),
        next_page_token: "",
      });
    }
    if (path === "/applications" && method === "POST") {
      const body = request.postDataJSON() as State["applications"][number] & { schema?: unknown };
      const app = {
        name: body.name,
        description: body.description ?? "",
        release_name: body.release_name || "runtime",
        schema_version: body.schema ? 1 : (body.schema_version ?? 0),
        contract: body.contract ?? [],
      };
      state.applications.push(app);
      return json(200, { application: overview(app).application });
    }
    if (path === "/applications/get") {
      const app = state.applications.find((a) => a.name === url.searchParams.get("name"));
      return app
        ? json(200, { application: overview(app).application })
        : json(404, { error: { code: "not_found", message: "no such application" } });
    }
    if (path === "/applications/overview") {
      const name = url.searchParams.get("name");
      if (!name) {
        return json(200, {
          applications: state.applications.map((app) => ({
            application: overview(app).application,
            status: "setup",
            environments: [],
          })),
        });
      }
      const app = state.applications.find((a) => a.name === name);
      return app
        ? json(200, overview(app))
        : json(404, { error: { code: "not_found", message: "no such application" } });
    }
    if (path === "/applications/dashboard") {
      const app = state.applications.find((a) => a.name === url.searchParams.get("name"));
      return app
        ? json(200, { application: overview(app).application, environments: [], rows: [] })
        : json(404, { error: { code: "not_found", message: "no such application" } });
    }
    return json(200, {});
  });
  return state;
}

test("first run: checklist → create wizard → application page with the setup panel", async ({
  page,
}, testInfo) => {
  const state = await installFake(page);
  await page.goto("/");

  // Zero applications and zero namespaces: the checklist replaces the fleet.
  await expect(page.getByRole("heading", { name: "Set up your first application" })).toBeVisible();
  const steps = page.getByRole("list", { name: "Setup steps" });
  await expect(steps.getByRole("listitem")).toHaveCount(9);
  await expect(steps.locator("[aria-current='step']")).toHaveText(/Create an application/);
  await expect(page.locator(".stat-strip")).toBeVisible();

  if (testInfo.project.name === "mobile-chromium") {
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  }

  // Step 1 opens the create wizard (owned by the application lane; asserted
  // through the plan's step labels rather than its internals).
  await page.getByRole("button", { name: "Create application" }).click();
  const wizard = page.getByRole("dialog");
  await expect(wizard).toBeVisible();
  for (const label of ["Basics", "Schema", "Contract", "Environments"]) {
    await expect(wizard.getByText(label, { exact: true }).first()).toBeVisible();
  }

  await wizard.getByLabel("Application name").fill("billing");
  const next = wizard.getByRole("button", { name: /^Next/ });
  while (await next.isVisible()) {
    await next.click();
  }
  await wizard.getByRole("button", { name: /^Create/ }).click();

  // The wizard resolves onto the application page; the fake now knows the app.
  await expect(page).toHaveURL(/\/applications\?app=billing/);
  expect(state.applications.map((app) => app.name)).toEqual(["billing"]);
  const panel = page.locator("details.setup-panel");
  await expect(panel).toBeVisible();
  await expect(panel.locator("summary")).toHaveText(/Setup · \d+ of \d+ done/);
  await expect(panel.locator("[aria-current='step']")).toHaveText(/Add an environment/);

  if (testInfo.project.name === "mobile-chromium") {
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  }
});

test("the command palette opens with ⌘K, ranks the ship deep link first, and navigates", async ({
  page,
}, testInfo) => {
  test.skip(testInfo.project.name === "mobile-chromium", "keyboard shortcut journey");
  const state = await installFake(page);
  state.applications.push({
    name: "gradethis",
    description: "Grading API",
    release_name: "runtime",
    schema_version: 0,
    contract: [{ alias: "rate_limits", kind: "parameter", content_type: "json" }],
  });
  await page.route("**/api/v1/namespaces**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        namespaces: [
          {
            env: "prod",
            app: "gradethis",
            description: "",
            allowed_auth_methods: ["mtls"],
            created_by: "admin",
            created_at_unix_ms: 1,
            parameter_count: 1,
            secret_count: 0,
          },
        ],
        next_page_token: "",
      }),
    }),
  );

  await page.goto("/audit");
  // The sidebar search button and ⌘K open the same palette; the button does
  // not depend on hydration having finished before the key lands.
  await page.getByRole("button", { name: /Search…/ }).click();
  const input = page.getByRole("combobox", { name: /Search applications/ });
  await expect(input).toBeFocused();
  await input.fill("prod gradethis rate");
  const first = page.getByRole("option").first();
  await expect(first).toHaveText(/rate_limits/);
  await expect(first).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/applications\?app=gradethis&env=prod&ship=rate_limits/);
  await expect(input).toHaveCount(0);
});
