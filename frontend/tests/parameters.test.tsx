import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import type { Namespace, Parameter } from "@/lib/types";
import ParametersPage from "@/pages/parameters/index";
import { chooseSelectOption } from "./select-test-utils";

const mocks = vi.hoisted(() => ({
  router: {
    isReady: true,
    query: {} as Record<string, string>,
    push: vi.fn(),
    replace: vi.fn(),
  },
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("next/router", () => ({ useRouter: () => mocks.router }));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));

const NAMESPACE: Namespace = {
  env: "prod",
  app: "billing",
  description: "",
  allowed_auth_methods: ["mtls"],
  created_by: "admin",
  created_at_unix_ms: 1,
  parameter_count: 2,
  secret_count: 0,
};

function parameter(key: string): Parameter {
  return {
    env: NAMESPACE.env,
    app: NAMESPACE.app,
    key,
    value: "1",
    content_type: "integer",
    version: 1,
    metadata_json: "{}",
    created_by: "admin",
    created_at_unix_ms: 1,
    labels: { current: 1 },
  };
}

const ALPHA = parameter("alpha");
const BETA = parameter("beta");

beforeEach(() => {
  mocks.router.isReady = true;
  mocks.router.query = {};
  mocks.router.push.mockReset();
  mocks.router.replace.mockReset();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  vi.spyOn(api, "listNamespaces").mockResolvedValue({
    namespaces: [NAMESPACE],
    next_page_token: "",
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

/** Picks the fixture namespace in the filter bar once its options have arrived. */
async function chooseNamespace(): Promise<void> {
  const [app] = screen.getAllByLabelText("Application");
  await chooseSelectOption(app as HTMLElement, NAMESPACE.app);
  const [environment] = screen.getAllByLabelText("Environment");
  await chooseSelectOption(environment as HTMLElement, NAMESPACE.env);
}

describe("parameters page", () => {
  it("gives the filter and create namespace pickers distinct ids", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    render(<ParametersPage />);

    fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
    expect(await screen.findByRole("dialog", { name: "New parameter" })).toBeVisible();

    // Two pickers are mounted at once, so neither may keep the default ids —
    // duplicates make `<label for>` resolve to whichever rendered first.
    expect(document.querySelectorAll("#ns-app")).toHaveLength(0);
    expect(document.querySelectorAll("#ns-env")).toHaveLength(0);

    const apps = screen.getAllByLabelText("Application");
    expect(apps).toHaveLength(2);
    expect(new Set(apps.map((element) => element.id)).size).toBe(2);
    const envs = screen.getAllByLabelText("Environment");
    expect(new Set(envs.map((element) => element.id)).size).toBe(2);
  });

  it("reports a missing namespace inline instead of as a toast", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    const putParameter = vi.spyOn(api, "putParameter");
    render(<ParametersPage />);

    fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
    const dialog = await screen.findByRole("dialog", { name: "New parameter" });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Key" }), {
      target: { value: "rate-limit" },
    });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "100" },
    });
    fireEvent.submit(
      within(dialog).getByRole("textbox", { name: "Key" }).closest("form") as HTMLFormElement,
    );

    expect(putParameter).not.toHaveBeenCalled();
    expect(mocks.toast.error).not.toHaveBeenCalled();
    expect(within(dialog).getByText("Choose an application.")).toBeVisible();
    expect(within(dialog).getByText("Choose an environment.")).toBeVisible();
  });

  it("steps back a page after the last row on page 2 is deleted", async () => {
    const listParameters = vi
      .spyOn(api, "listParameters")
      .mockResolvedValueOnce({ parameters: [ALPHA], next_page_token: "page-2" })
      .mockResolvedValueOnce({ parameters: [BETA], next_page_token: "" })
      .mockResolvedValueOnce({ parameters: [], next_page_token: "" })
      .mockResolvedValueOnce({ parameters: [ALPHA], next_page_token: "page-2" });
    vi.spyOn(api, "deleteParameter").mockResolvedValue({ revision: 2 });

    render(<ParametersPage />);
    await chooseNamespace();
    expect(await screen.findByText(ALPHA.key)).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(await screen.findByText(BETA.key)).toBeVisible();
    expect(screen.getByText("Page 2")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    const confirm = await screen.findByRole("dialog", { name: "Delete parameter?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete parameter" }));

    // Page 2 is now empty, so staying there would be a dead end.
    expect(await screen.findByText(ALPHA.key)).toBeVisible();
    expect(screen.getByText("Page 1")).toBeVisible();
    expect(listParameters).toHaveBeenCalledTimes(4);
  });

  it("writes the chosen namespace back to the URL", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    render(<ParametersPage />);
    await chooseNamespace();

    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      { pathname: "/parameters", query: { env: NAMESPACE.env, app: NAMESPACE.app } },
      undefined,
      { shallow: true, scroll: false },
    );
  });
});
