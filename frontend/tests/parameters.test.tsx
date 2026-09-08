import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ParameterManager from "@/components/parameters/ParameterManager";
import { api } from "@/lib/api";
import { lastNamespace, resetNamespaceMemory } from "@/lib/namespace-memory";
import type { Namespace, Parameter } from "@/lib/types";
import { MAX_KEY_LENGTH } from "@/lib/validation";
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
// The page asks who is signed in before it offers bulk delete.
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: { name: "root", kind: "admin", namespace: null } }),
}));

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
  resetNamespaceMemory();
  vi.spyOn(api, "listNamespaces").mockResolvedValue({
    namespaces: [NAMESPACE],
    next_page_token: "",
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

/** The Key cell of every rendered row, in the order they are rendered. */
function keyColumn(): string[] {
  return [...document.querySelectorAll('table.data tbody td[data-label="Key"]')].map(
    (cell) => cell.textContent ?? "",
  );
}

/** Picks the fixture namespace in the filter bar once its options have arrived. */
async function chooseNamespace(): Promise<void> {
  const [app] = screen.getAllByLabelText("Application");
  await chooseSelectOption(app as HTMLElement, NAMESPACE.app);
  const [environment] = screen.getAllByLabelText("Environment");
  await chooseSelectOption(environment as HTMLElement, NAMESPACE.env);
}

describe("parameters page", () => {
  it("reserves the loaded table's column count while loading", async () => {
    let settle: (page: { parameters: Parameter[]; next_page_token: string }) => void = () => {};
    vi.spyOn(api, "listParameters").mockImplementation(
      () =>
        new Promise((resolve) => {
          settle = resolve;
        }),
    );
    // Deep-linked, so the list starts loading without a trip through the picker.
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    render(<ParametersPage />);
    const skeletonColumns = await waitFor(() => {
      const count = document.querySelectorAll("thead th").length;
      expect(count).toBeGreaterThan(0);
      return count;
    });

    settle({ parameters: [ALPHA], next_page_token: "" });
    await screen.findByText("alpha");
    // The loaded header adds a select-all cell and an actions gutter; without
    // both, every column shifts the instant the rows arrive.
    expect(document.querySelectorAll("thead th")).toHaveLength(skeletonColumns);
  });

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

    // Delete lives behind the row's menu, out of reach of a stray click.
    fireEvent.click(screen.getByRole("button", { name: `More actions for ${BETA.key}` }));
    // Base UI names the popup after its trigger.
    const menu = await screen.findByRole("menu", { name: `More actions for ${BETA.key}` });
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Delete" }));
    const confirm = await screen.findByRole("dialog", { name: "Delete parameter?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete parameter" }));

    // Page 2 is now empty, so staying there would be a dead end.
    expect(await screen.findByText(ALPHA.key)).toBeVisible();
    expect(screen.getByText("Page 1")).toBeVisible();
    expect(listParameters).toHaveBeenCalledTimes(4);
  });

  it("shows the environment trail and remembers the namespace once chosen", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    render(<ParametersPage />);
    expect(screen.queryByRole("navigation", { name: "Breadcrumb" })).toBeNull();
    expect(lastNamespace()).toBeNull();

    await chooseNamespace();
    const nav = await screen.findByRole("navigation", { name: "Breadcrumb" });
    expect(within(nav).getByRole("link", { name: /billing/ })).toHaveAttribute(
      "href",
      "/applications?app=billing",
    );
    expect(nav).toHaveTextContent("prod");
    expect(lastNamespace()).toEqual({ env: NAMESPACE.env, app: NAMESPACE.app });
  });

  it("focuses the key on open, caps its length and counts down near the limit", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    render(<ParametersPage />);
    fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
    const dialog = await screen.findByRole("dialog", { name: "New parameter" });
    const key = within(dialog).getByRole("textbox", { name: "Key" });
    await waitFor(() => expect(key).toHaveFocus());
    expect(key).toHaveAttribute("maxlength", String(MAX_KEY_LENGTH));
    expect(within(dialog).queryByTestId("key-counter")).toBeNull();

    fireEvent.change(key, { target: { value: "a".repeat(210) } });
    expect(within(dialog).getByTestId("key-counter")).toHaveTextContent(`210/${MAX_KEY_LENGTH}`);
  });

  it("offers to clear a value the new content type rejects", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    render(<ParametersPage />);
    fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
    const dialog = await screen.findByRole("dialog", { name: "New parameter" });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "abc" },
    });
    await chooseSelectOption(
      within(dialog).getByRole("combobox", { name: "Content type" }),
      "integer",
    );
    expect(within(dialog).getByText(/The current value is not valid/)).toBeVisible();
    fireEvent.click(within(dialog).getByRole("button", { name: "Clear value" }));
    expect(within(dialog).getByRole("textbox", { name: "Value" })).toHaveValue("");
    expect(within(dialog).queryByText(/The current value is not valid/)).toBeNull();
  });

  it("moves focus to the first invalid field on a blocked create", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    render(<ParametersPage />);
    fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
    const dialog = await screen.findByRole("dialog", { name: "New parameter" });
    fireEvent.submit(
      within(dialog).getByRole("textbox", { name: "Key" }).closest("form") as HTMLFormElement,
    );
    // The application select is the first control flagged invalid.
    expect(within(dialog).getByRole("combobox", { name: "Application" })).toHaveFocus();
  });

  it("asks before discarding a half-typed parameter", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [], next_page_token: "" });
    render(<ParametersPage />);
    fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
    const dialog = await screen.findByRole("dialog", { name: "New parameter" });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Key" }), {
      target: { value: "rate-limit" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(
      await screen.findByRole("dialog", { name: "Discard changes?", hidden: true }),
    ).toBeInTheDocument();
  });

  it("reorders the loaded page from a column header and records the sort in the URL", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [BETA, ALPHA],
      next_page_token: "",
    });
    const { rerender } = render(<ParametersPage />);
    expect(await screen.findByText(BETA.key)).toBeVisible();
    expect(keyColumn()).toEqual(["beta", "alpha"]);

    const header = screen.getByRole("button", { name: "Key" });
    expect(header.closest("th")).toHaveAttribute("aria-sort", "none");
    fireEvent.click(header);
    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      {
        pathname: "/parameters",
        query: { env: NAMESPACE.env, app: NAMESPACE.app, sort: "key", dir: "asc" },
      },
      undefined,
      { shallow: true, scroll: false },
    );

    // The URL is the source of truth, so land the router on what the click asked for.
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, sort: "key", dir: "asc" };
    rerender(<ParametersPage />);
    expect(keyColumn()).toEqual(["alpha", "beta"]);
    expect(screen.getByRole("button", { name: "Key" }).closest("th")).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
    // Only the loaded page is ordered, and the footer says so.
    expect(screen.getByTestId("table-summary")).toHaveTextContent(
      "Showing 2 of 2 parameters · Sorts the rows loaded on this page",
    );
  });

  it("counts the rows on screen, and the filter narrowing them", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [ALPHA, BETA],
      next_page_token: "",
    });
    const { rerender } = render(<ParametersPage />);
    expect(await screen.findByText(ALPHA.key)).toBeVisible();
    const summary = screen.getByTestId("table-summary");
    expect(summary).toHaveTextContent("Showing 2 of 2 parameters");
    expect(summary).not.toHaveTextContent("filter");

    vi.mocked(api.listParameters).mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, key_prefix: "al" };
    rerender(<ParametersPage />);
    fireEvent.change(screen.getByLabelText("Key prefix"), { target: { value: "al" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() =>
      expect(screen.getByTestId("table-summary")).toHaveTextContent(
        "Showing 1 of 1 parameter · 1 filter active",
      ),
    );
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

describe("parameters list navigation", () => {
  it("keeps rows when Filter or Clear does not change the scope", async () => {
    vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    render(<ParametersPage />);
    await screen.findByText(ALPHA.key);
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByText(ALPHA.key)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(screen.getByText(ALPHA.key)).toBeVisible();
    fireEvent.change(screen.getByLabelText("Key prefix"), { target: { value: "api" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await screen.findByText(ALPHA.key);
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByText(ALPHA.key)).toBeVisible();
  });

  it("follows same-page navigation and history when query fields change or disappear", async () => {
    const list = vi
      .spyOn(api, "listParameters")
      .mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    const view = render(<ParametersPage />);
    await screen.findByText(ALPHA.key);
    mocks.router.query = { env: "dev", app: "other", key_prefix: "new" };
    view.rerender(<ParametersPage />);
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith(
        { env: "dev", app: "other" },
        "new",
        100,
        undefined,
        expect.anything(),
      ),
    );
    expect(screen.getByLabelText("Key prefix")).toHaveValue("new");
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    view.rerender(<ParametersPage />);
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith(
        { env: NAMESPACE.env, app: NAMESPACE.app },
        undefined,
        100,
        undefined,
        expect.anything(),
      ),
    );
    expect(screen.getByLabelText("Key prefix")).toHaveValue("");
    mocks.router.query = {};
    view.rerender(<ParametersPage />);
    expect(await screen.findByText("Choose an environment")).toBeVisible();
  });
});

it("uses create-only writes and retains the draft when the key already exists", async () => {
  vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
  const put = vi
    .spyOn(api, "putParameter")
    .mockRejectedValue(new Error("Parameter already exists"));
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
  render(<ParametersPage />);
  await screen.findByText(ALPHA.key);
  fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
  const dialog = await screen.findByRole("dialog", { name: "New parameter" });
  fireEvent.change(within(dialog).getByRole("textbox", { name: "Key" }), {
    target: { value: ALPHA.key },
  });
  fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
    target: { value: "replacement" },
  });
  fireEvent.click(within(dialog).getByRole("button", { name: "Save parameter" }));
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      expect.objectContaining({ key: ALPHA.key, value: "replacement", create_only: true }),
    ),
  );
  await waitFor(() => expect(mocks.toast.error).toHaveBeenCalled());
  expect(dialog).toBeVisible();
  expect(within(dialog).getByRole("textbox", { name: "Value" })).toHaveValue("replacement");
});

it("blocks creation while a visible numeric form draft is incomplete", async () => {
  vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
  vi.spyOn(api, "applicationOverview").mockRejectedValue(new Error("No pinned schema"));
  const put = vi.spyOn(api, "putParameter").mockResolvedValue({ version: 1, revision: 1 });
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
  render(<ParametersPage />);
  await screen.findByText(ALPHA.key);
  fireEvent.click(screen.getByRole("button", { name: "New parameter" }));
  const dialog = await screen.findByRole("dialog", { name: "New parameter" });
  const key = within(dialog).getByRole("textbox", { name: "Key" });
  fireEvent.change(key, { target: { value: "new-config" } });
  await chooseSelectOption(within(dialog).getByLabelText("Content type"), "json");
  fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
    target: { value: '{"count":3}' },
  });
  fireEvent.click(within(dialog).getByRole("button", { name: "Form" }));
  const count = within(dialog).getByRole("textbox", { name: "count" });
  fireEvent.change(count, { target: { value: "4" } });
  fireEvent.change(count, { target: { value: "4e" } });
  expect(within(dialog).getByRole("button", { name: "Save parameter" })).toBeDisabled();
  fireEvent.submit(key.closest("form") as HTMLFormElement);
  expect(put).not.toHaveBeenCalled();
  fireEvent.change(count, { target: { value: "4e2" } });
  await waitFor(() =>
    expect(within(dialog).getByRole("button", { name: "Save parameter" })).toBeEnabled(),
  );
  fireEvent.submit(key.closest("form") as HTMLFormElement);
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      expect.objectContaining({ value: '{"count":400}', create_only: true }),
    ),
  );
});

it("blocks new-version writes while a numeric form draft is incomplete", async () => {
  const current = { ...ALPHA, content_type: "json", value: '{"count":3}' };
  vi.spyOn(api, "getParameter").mockResolvedValue({ parameter: current });
  vi.spyOn(api, "parameterMetadata").mockResolvedValue({
    ...current,
    updated_at_unix_ms: 1,
    versions: [],
  });
  vi.spyOn(api, "applicationOverview").mockRejectedValue(new Error("No pinned schema"));
  const put = vi.spyOn(api, "putParameter").mockResolvedValue({ version: 2, revision: 2 });
  render(<ParameterManager resourceRef={ALPHA} />);
  fireEvent.click(await screen.findByRole("button", { name: "New version" }));
  const dialog = await screen.findByRole("dialog", { name: "New parameter version" });
  fireEvent.click(within(dialog).getByRole("button", { name: "Form" }));
  const count = within(dialog).getByRole("textbox", { name: "count" });
  fireEvent.change(count, { target: { value: "4" } });
  fireEvent.change(count, { target: { value: "4e" } });
  expect(within(dialog).getByRole("button", { name: "Save new version" })).toBeDisabled();
  fireEvent.submit(count.closest("form") as HTMLFormElement);
  expect(put).not.toHaveBeenCalled();
  fireEvent.change(count, { target: { value: "4e2" } });
  await waitFor(() =>
    expect(within(dialog).getByRole("button", { name: "Save new version" })).toBeEnabled(),
  );
  fireEvent.submit(count.closest("form") as HTMLFormElement);
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(expect.objectContaining({ value: '{"count":400}' })),
  );
});

it("preserves a newer filter draft when an internal URL replacement settles late", async () => {
  const list = vi
    .spyOn(api, "listParameters")
    .mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
  const view = render(<ParametersPage />);
  await screen.findByText(ALPHA.key);
  const input = screen.getByLabelText("Key prefix");
  fireEvent.change(input, { target: { value: "db" } });
  fireEvent.click(screen.getByRole("button", { name: "Filter" }));
  fireEvent.change(input, { target: { value: "db/cache" } });
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, key_prefix: "db" };
  view.rerender(<ParametersPage />);
  expect(input).toHaveValue("db/cache");
  await waitFor(() =>
    expect(list).toHaveBeenLastCalledWith(
      { env: NAMESPACE.env, app: NAMESPACE.app },
      "db",
      100,
      undefined,
      expect.anything(),
    ),
  );
  // A real navigation to another applied scope still replaces the draft.
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, key_prefix: "external" };
  view.rerender(<ParametersPage />);
  expect(input).toHaveValue("external");
});
