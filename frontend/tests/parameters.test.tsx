import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ParameterManager from "@/components/parameters/ParameterManager";
import { api } from "@/lib/api";
import { lastNamespace, resetNamespaceMemory } from "@/lib/namespace-memory";
import type { Namespace, Parameter } from "@/lib/types";
import { SEARCH_RESULT_LIMIT } from "@/lib/key-search";
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

  it("counts the rows on screen, and the search narrowing them", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [ALPHA, BETA],
      next_page_token: "",
    });
    render(<ParametersPage />);
    expect(await screen.findByText(ALPHA.key)).toBeVisible();
    const summary = screen.getByTestId("table-summary");
    expect(summary).toHaveTextContent("Showing 2 of 2 parameters");
    expect(summary).not.toHaveTextContent("filter");

    fireEvent.change(screen.getByLabelText("Find parameter"), { target: { value: "al" } });
    await waitFor(() =>
      expect(screen.getByTestId("table-summary")).toHaveTextContent(
        "Showing 1 of 1 parameter · 1 filter active",
      ),
    );
    expect(keyColumn()).toEqual([ALPHA.key]);
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
  it("keeps rows when a search starts and ends", async () => {
    const list = vi
      .spyOn(api, "listParameters")
      .mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    render(<ParametersPage />);
    await screen.findByText(ALPHA.key);
    expect(list).toHaveBeenCalledTimes(1);

    const input = screen.getByLabelText("Find parameter");
    fireEvent.change(input, { target: { value: "alp" } });
    // One extra call, and it is the index walk: page size 1000, no prefix.
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    expect(list).toHaveBeenLastCalledWith(
      { env: NAMESPACE.env, app: NAMESPACE.app },
      undefined,
      1000,
      undefined,
      expect.anything(),
    );
    // The key is on screen with the typed characters marked, so it is no
    // longer one text node.
    await waitFor(() => expect(keyColumn()).toEqual([ALPHA.key]));
    expect(document.querySelector(".cell-path mark")).toHaveTextContent("alp");

    // Emptying the box returns to the browse page already in hand.
    fireEvent.change(input, { target: { value: "" } });
    await waitFor(() =>
      expect(screen.getByTestId("table-summary")).not.toHaveTextContent("filter"),
    );
    expect(screen.getByText(ALPHA.key)).toBeVisible();
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("follows same-page navigation and history when query fields change or disappear", async () => {
    const list = vi
      .spyOn(api, "listParameters")
      .mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    const view = render(<ParametersPage />);
    await screen.findByText(ALPHA.key);
    // A `?q=` link searches the new namespace instead of browsing it.
    mocks.router.query = { env: "dev", app: "other", q: "new" };
    view.rerender(<ParametersPage />);
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith(
        { env: "dev", app: "other" },
        undefined,
        1000,
        undefined,
        expect.anything(),
      ),
    );
    expect(screen.getByLabelText("Find parameter")).toHaveValue("new");
    mocks.router.query = { env: "dev", app: "other" };
    view.rerender(<ParametersPage />);
    await waitFor(() =>
      expect(list).toHaveBeenLastCalledWith(
        { env: "dev", app: "other" },
        undefined,
        100,
        undefined,
        expect.anything(),
      ),
    );
    expect(screen.getByLabelText("Find parameter")).toHaveValue("");
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

it("preserves a newer search draft when an internal URL replacement settles late", async () => {
  vi.spyOn(api, "listParameters").mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
  const view = render(<ParametersPage />);
  await screen.findByText(ALPHA.key);
  const input = screen.getByLabelText("Find parameter");
  fireEvent.change(input, { target: { value: "db" } });
  await waitFor(() =>
    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      {
        pathname: "/parameters",
        query: { env: NAMESPACE.env, app: NAMESPACE.app, q: "db" },
      },
      undefined,
      { shallow: true, scroll: false },
    ),
  );
  // The next keystroke lands before that replacement makes it back into
  // router.query; the draft must survive it.
  fireEvent.change(input, { target: { value: "db/cache" } });
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, q: "db" };
  view.rerender(<ParametersPage />);
  expect(input).toHaveValue("db/cache");

  // A real navigation to another applied scope still replaces the draft.
  mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, q: "external" };
  view.rerender(<ParametersPage />);
  expect(input).toHaveValue("external");
});

describe("parameters search", () => {
  const DEEP = parameter("billing/timeout");

  /** A list mock that pages only when the index asks for 1,000 at a time. */
  function pagedList(first: Parameter[], second: Parameter[]) {
    return vi
      .spyOn(api, "listParameters")
      .mockImplementation(async (_ns, _prefix, pageSize, pageToken) => {
        if (pageSize !== 1000) return { parameters: first, next_page_token: "" };
        return pageToken
          ? { parameters: second, next_page_token: "" }
          : { parameters: first, next_page_token: "page-2" };
      });
  }

  it("finds a key the browse page never loaded", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    pagedList([ALPHA], [DEEP]);
    render(<ParametersPage />);
    await screen.findByText(ALPHA.key);

    // `timeout` is nowhere near the front of `billing/timeout`, and the row
    // only exists on the second index page.
    fireEvent.change(screen.getByLabelText("Find parameter"), { target: { value: "timeout" } });
    await waitFor(() => expect(keyColumn()).toEqual([DEEP.key]));
    expect(document.querySelector(".cell-path mark")).toHaveTextContent("timeout");
  });

  it("shows a snippet when only the value matched", async () => {
    const gateway: Parameter = {
      ...parameter("gateway"),
      value: "https://payments.example.test/v2/charge",
      content_type: "string",
    };
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [ALPHA, gateway],
      next_page_token: "",
    });
    render(<ParametersPage />);
    await screen.findByText(ALPHA.key);

    fireEvent.change(screen.getByLabelText("Find parameter"), { target: { value: "payments" } });
    // The snippet lives in the Key cell, under the key itself.
    await waitFor(() => expect(keyColumn()).toHaveLength(1));
    expect(keyColumn()[0]).toMatch(/^gateway/);
    const snippet = document.querySelector(".search-snippet");
    expect(snippet).not.toBeNull();
    expect(snippet).toHaveTextContent("payments");
    // The key carried no match, so nothing in it is marked.
    expect(document.querySelector(".cell-path mark")).toBeNull();
  });

  it("renders a ?q= deep link without flashing the browse table", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, q: "beta" };
    const list = vi
      .spyOn(api, "listParameters")
      .mockResolvedValue({ parameters: [ALPHA, BETA], next_page_token: "" });
    render(<ParametersPage />);
    // The skeleton holds the page until the index answers; the unfiltered
    // browse table never appears.
    expect(keyColumn()).toEqual([]);
    await waitFor(() => expect(keyColumn()).toEqual([BETA.key]));
    expect(list).toHaveBeenCalledTimes(1);
    expect(list.mock.calls[0]?.[2]).toBe(1000);
    // No pager while searching: the matches are the whole answer.
    expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
    expect(screen.getByTestId("table-summary")).toHaveTextContent(
      "Showing 1 of 1 parameter · 1 filter active",
    );
  });

  it("drops a deleted row from the matches", async () => {
    const alt = parameter("alt");
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, q: "al" };
    const list = vi
      .spyOn(api, "listParameters")
      .mockResolvedValue({ parameters: [ALPHA, alt], next_page_token: "" });
    vi.spyOn(api, "deleteParameter").mockResolvedValue({ revision: 2 });
    render(<ParametersPage />);
    await waitFor(() => expect(keyColumn()).toEqual([ALPHA.key, alt.key]));

    list.mockResolvedValue({ parameters: [alt], next_page_token: "" });
    fireEvent.click(screen.getByRole("button", { name: `More actions for ${ALPHA.key}` }));
    const menu = await screen.findByRole("menu", { name: `More actions for ${ALPHA.key}` });
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Delete" }));
    const confirm = await screen.findByRole("dialog", { name: "Delete parameter?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete parameter" }));

    // The index is re-walked, so the deleted key leaves the results.
    await waitFor(() => expect(keyColumn()).toEqual([alt.key]));
  });
});

describe("parameters search failures and totals", () => {
  it("clears a legacy ?key_prefix= link instead of refilling the box", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, key_prefix: "db" };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [ALPHA, parameter("db/host")],
      next_page_token: "",
    });
    render(<ParametersPage />);
    const input = screen.getByLabelText("Find parameter");
    await waitFor(() => expect(input).toHaveValue("db"));

    fireEvent.change(input, { target: { value: "" } });
    // Both keys leave the URL. Dropping only `q` would let the seeding effect
    // fall back to `key_prefix` and type the old filter back in.
    await waitFor(() =>
      expect(mocks.router.replace).toHaveBeenLastCalledWith(
        { pathname: "/parameters", query: { env: NAMESPACE.env, app: NAMESPACE.app } },
        undefined,
        { shallow: true, scroll: false },
      ),
    );
    // The page re-reads the settled URL; the box must stay empty.
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    await waitFor(() => expect(keyColumn()).toEqual([ALPHA.key, "db/host"]));
    expect(screen.getByLabelText("Find parameter")).toHaveValue("");
  });

  it("counts every match, not just the ones on screen", async () => {
    // One more than the result limit, so the cut is real and the hint honest.
    const many = Array.from({ length: SEARCH_RESULT_LIMIT + 1 }, (_, i) =>
      parameter(`token-${String(i).padStart(4, "0")}`),
    );
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, q: "token" };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: many,
      next_page_token: "",
    });
    render(<ParametersPage />);
    await waitFor(() => expect(keyColumn()).toHaveLength(SEARCH_RESULT_LIMIT));

    const summary = screen.getByTestId("table-summary");
    expect(summary).toHaveTextContent(
      `Showing ${SEARCH_RESULT_LIMIT} of ${SEARCH_RESULT_LIMIT + 1} parameters`,
    );
    expect(summary).toHaveTextContent(`Showing the best ${SEARCH_RESULT_LIMIT} matches`);
  });

  it("does not claim results were cut when they were not", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, q: "alpha" };
    vi.spyOn(api, "listParameters").mockResolvedValue({
      parameters: [ALPHA, BETA],
      next_page_token: "",
    });
    render(<ParametersPage />);
    await waitFor(() => expect(keyColumn()).toEqual([ALPHA.key]));
    const summary = screen.getByTestId("table-summary");
    expect(summary).toHaveTextContent("Showing 1 of 1 parameter");
    expect(summary).not.toHaveTextContent("keep typing");
  });

  it("offers a retry when the index could not be loaded", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, q: "alpha" };
    const list = vi.spyOn(api, "listParameters").mockRejectedValue(new Error("offline"));
    render(<ParametersPage />);

    // A failed walk is not an answer: never the "no matches" empty state.
    expect(await screen.findByText("Search failed")).toBeVisible();
    expect(screen.queryByText(/No parameters match/)).toBeNull();
    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalled());

    list.mockResolvedValue({ parameters: [ALPHA], next_page_token: "" });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(keyColumn()).toEqual([ALPHA.key]));
  });
});
