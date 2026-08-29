import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { VALUE_EDITOR_MODE_STORAGE_KEY } from "@/components/SchemaForm";
import { ApiError, api } from "@/lib/api";
import type {
  ApplicationOverview,
  Parameter,
  ParameterMetadata,
  SecretMetadata,
} from "@/lib/types";
import ParameterDetailPage from "@/pages/parameters/detail";
import SecretDetailPage from "@/pages/secrets/detail";
import { chooseSelectOption } from "./select-test-utils";

const mocks = vi.hoisted(() => ({
  router: {
    isReady: true,
    query: {} as Record<string, string>,
    push: vi.fn(),
  },
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("next/router", () => ({ useRouter: () => mocks.router }));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));

beforeEach(() => {
  // Every parameter page looks for a pinned schema; by default there is none.
  vi.spyOn(api, "applicationOverview").mockRejectedValue(
    new ApiError("not_found", "application not found", 404),
  );
  mocks.router.isReady = true;
  mocks.router.query = {};
  mocks.router.push.mockReset();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  window.localStorage.removeItem(VALUE_EDITOR_MODE_STORAGE_KEY);
});

afterEach(() => {
  vi.restoreAllMocks();
});

const PARAMETER: Parameter = {
  env: "prod",
  app: "billing",
  key: "retries",
  value: "3",
  content_type: "integer",
  version: 2,
  metadata_json: "{}",
  created_by: "admin",
  created_at_unix_ms: 1,
  labels: { current: 2 },
};

const PARAMETER_META: ParameterMetadata = {
  env: PARAMETER.env,
  app: PARAMETER.app,
  key: PARAMETER.key,
  content_type: PARAMETER.content_type,
  metadata_json: "{}",
  created_at_unix_ms: 1,
  updated_at_unix_ms: 2,
  labels: { current: 2 },
  versions: [
    {
      version: 2,
      content_type: "integer",
      state: "enabled",
      created_by: "admin",
      created_at_unix_ms: 2,
      metadata_json: "{}",
    },
  ],
};

/** A secret whose current version (v1) is not the newest, so v2 offers Promote. */
const SECRET: SecretMetadata = {
  env: "prod",
  app: "billing",
  key: "api-key",
  content_type: "text/plain",
  client_bound: false,
  has_access_token: false,
  metadata_json: "{}",
  created_at_unix_ms: 1,
  updated_at_unix_ms: 2,
  labels: { current: 1 },
  versions: [
    {
      version: 1,
      state: "enabled",
      created_by: "admin",
      created_at_unix_ms: 1,
      destroyed_at_unix_ms: 0,
      expires_at_unix_ms: 0,
      metadata_json: "{}",
    },
    {
      version: 2,
      state: "enabled",
      created_by: "admin",
      created_at_unix_ms: 2,
      destroyed_at_unix_ms: 0,
      expires_at_unix_ms: 0,
      metadata_json: "{}",
    },
  ],
};

/** Renders the parameter detail page and opens its new-version form. */
const JSON_PARAMETER: Parameter = {
  ...PARAMETER,
  value: '{"max":3,"name":"x"}',
  content_type: "json",
};

/** billing's pinned schema describes `retries` under the alias `retries_alias`. */
const OVERVIEW = {
  application: {
    name: "billing",
    description: "",
    release_name: "billing",
    schema_id: "cfg",
    schema_version: 3,
    contract: [{ alias: "retries_alias", kind: "parameter", content_type: "json" }],
    created_by: "admin",
    created_at_unix_ms: 1,
    updated_at_unix_ms: 1,
    environment_count: 1,
  },
  status: "ready",
  findings: [],
  rows: [],
  environments: [
    {
      namespace: { env: "prod", app: "billing" },
      values: [
        {
          alias: "retries_alias",
          kind: "parameter",
          key: "retries",
          present: true,
          content_type: "json",
        },
      ],
    },
  ],
  schema_json: JSON.stringify({
    type: "object",
    properties: {
      retries_alias: {
        type: "object",
        properties: { max: { type: "integer", description: "Attempts" }, name: { type: "string" } },
      },
    },
  }),
} as unknown as ApplicationOverview;

async function openNewVersion(options?: { parameter?: Parameter; overview?: ApplicationOverview }) {
  const parameter = options?.parameter ?? PARAMETER;
  mocks.router.query = { env: parameter.env, app: parameter.app, key: parameter.key };
  vi.spyOn(api, "parameterMetadata").mockResolvedValue({
    ...PARAMETER_META,
    content_type: parameter.content_type,
  });
  vi.spyOn(api, "getParameter").mockResolvedValue({ parameter });
  const overview = vi.spyOn(api, "applicationOverview");
  if (options?.overview) overview.mockResolvedValue(options.overview);
  else overview.mockRejectedValue(new ApiError("not_found", "application not found", 404));

  render(<ParameterDetailPage />);
  fireEvent.click(await screen.findByRole("button", { name: "New version" }));
  return screen.getByRole("dialog", { name: "New parameter version" });
}

describe("detail route states", () => {
  it("shows the parameter guidance instead of an endless skeleton for a missing ref", () => {
    const metadata = vi.spyOn(api, "parameterMetadata");

    render(<ParameterDetailPage />);

    expect(screen.getByText("No parameter specified")).toBeVisible();
    expect(metadata).not.toHaveBeenCalled();
  });

  it("shows the secret guidance instead of an endless skeleton for a missing ref", () => {
    const metadata = vi.spyOn(api, "secretMetadata");

    render(<SecretDetailPage />);

    expect(screen.getByText("No secret specified")).toBeVisible();
    expect(metadata).not.toHaveBeenCalled();
  });

  it("distinguishes a parameter load failure from not found", async () => {
    mocks.router.query = { env: "prod", app: "billing", key: "currency" };
    vi.spyOn(api, "parameterMetadata").mockRejectedValue(new Error("offline"));
    vi.spyOn(api, "getParameter").mockRejectedValue(new Error("offline"));

    render(<ParameterDetailPage />);

    expect(await screen.findByRole("heading", { name: "Could not load parameter" })).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Parameter not found" })).not.toBeInTheDocument();
  });

  it("distinguishes a secret load failure from not found", async () => {
    mocks.router.query = { env: "prod", app: "billing", key: "api-key" };
    vi.spyOn(api, "secretMetadata").mockRejectedValue(new Error("offline"));

    render(<SecretDetailPage />);

    expect(await screen.findByRole("heading", { name: "Could not load secret" })).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Secret not found" })).not.toBeInTheDocument();
  });
});

describe("detail page refreshes", () => {
  it("keeps the metadata card on screen while a promote reloads the secret", async () => {
    mocks.router.query = { env: SECRET.env, app: SECRET.app, key: SECRET.key };
    let releaseReload: (() => void) | undefined;
    const secretMetadata = vi
      .spyOn(api, "secretMetadata")
      .mockResolvedValueOnce({ secret: SECRET })
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            releaseReload = () => resolve({ secret: { ...SECRET, labels: { current: 2 } } });
          }),
      );
    vi.spyOn(api, "promoteSecret").mockResolvedValue({
      current_version: 2,
      previous_version: 1,
      revision: 2,
    });

    render(<SecretDetailPage />);
    expect(await screen.findByText("Content type")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Promote" }));
    const confirm = await screen.findByRole("dialog", { name: "Promote version?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Promote" }));

    // The reload is deliberately still in flight here: the page must refresh in
    // place rather than collapse back to its loading skeleton.
    await vi.waitFor(() => expect(secretMetadata).toHaveBeenCalledTimes(2));
    expect(screen.getByText("Content type")).toBeVisible();

    releaseReload?.();
    await vi.waitFor(() => expect(screen.getByText("Content type")).toBeVisible());
  });

  it("defaults the reveal version to an enabled one", async () => {
    // `current` points at a disabled version, which the reveal select does not
    // offer — it must fall back to the newest enabled version, not render blank.
    mocks.router.query = { env: SECRET.env, app: SECRET.app, key: SECRET.key };
    vi.spyOn(api, "secretMetadata").mockResolvedValue({
      secret: {
        ...SECRET,
        labels: { current: 2 },
        versions: [SECRET.versions[0], { ...SECRET.versions[1], state: "disabled" as const }],
      },
    });

    render(<SecretDetailPage />);

    const version = await screen.findByLabelText("Version");
    expect(version).toHaveTextContent("v1");
    expect(screen.getByRole("button", { name: "Reveal secret" })).toBeEnabled();
  });

  it("pretty-prints a json parameter value", async () => {
    const raw = '{"a":1,"b":[2,3]}';
    mocks.router.query = { env: PARAMETER.env, app: PARAMETER.app, key: PARAMETER.key };
    vi.spyOn(api, "parameterMetadata").mockResolvedValue({
      ...PARAMETER_META,
      content_type: "json",
    });
    vi.spyOn(api, "getParameter").mockResolvedValue({
      parameter: { ...PARAMETER, content_type: "json", value: raw },
    });

    render(<ParameterDetailPage />);

    await screen.findByRole("button", { name: "Copy value" });
    expect(document.querySelector("pre.json-block")?.textContent).toBe(
      '{\n  "a": 1,\n  "b": [\n    2,\n    3\n  ]\n}',
    );
  });

  it("closes the delete confirmation before navigating away", async () => {
    mocks.router.query = { env: PARAMETER.env, app: PARAMETER.app, key: PARAMETER.key };
    vi.spyOn(api, "parameterMetadata").mockResolvedValue(PARAMETER_META);
    vi.spyOn(api, "getParameter").mockResolvedValue({ parameter: PARAMETER });
    vi.spyOn(api, "deleteParameter").mockResolvedValue({ revision: 3 });

    render(<ParameterDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const confirm = await screen.findByRole("dialog", { name: "Delete parameter?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete parameter" }));

    await vi.waitFor(() =>
      expect(mocks.router.push).toHaveBeenCalledWith("/parameters?env=prod&app=billing"),
    );
    // A back-navigation must not land on a confirmation for a deleted parameter.
    expect(screen.queryByRole("dialog", { name: "Delete parameter?" })).not.toBeInTheDocument();
  });
});

describe("new parameter version validation", () => {
  it("re-checks the value against the content type the form will send", async () => {
    const putParameter = vi.spyOn(api, "putParameter");
    const dialog = await openNewVersion();
    const value = within(dialog).getByRole("textbox", { name: "Value" });
    const save = screen.getByRole("button", { name: "Save new version" });

    // The prefilled value is not a mistake the operator has made yet.
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.change(value, { target: { value: "3.5" } });
    fireEvent.blur(value);
    expect(within(dialog).getByText("Value must be a whole number.")).toBeVisible();
    expect(value).toHaveAttribute("aria-invalid", "true");
    expect(save).toBeDisabled();

    // Widening the content type makes the same text legal again.
    await chooseSelectOption(
      within(dialog).getByRole("combobox", { name: "Content type" }),
      "float",
    );
    expect(within(dialog).queryByText("Value must be a whole number.")).not.toBeInTheDocument();
    expect(save).toBeEnabled();
    expect(putParameter).not.toHaveBeenCalled();
  });

  it("blocks a save on metadata that is not a JSON object", async () => {
    const putParameter = vi.spyOn(api, "putParameter");
    const dialog = await openNewVersion();
    // Metadata is almost always `{}`, so it sits behind a disclosure.
    const disclosure = within(dialog).getByRole("button", { name: /^Metadata JSON/ });
    expect(disclosure).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(disclosure);
    const metadata = within(dialog).getByRole("textbox", { name: "Metadata JSON" });

    fireEvent.change(metadata, { target: { value: '["owner"]' } });
    fireEvent.blur(metadata);
    expect(within(dialog).getByText("Metadata must be a JSON object.")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Save new version" }));
    expect(putParameter).not.toHaveBeenCalled();
  });
});

describe("new parameter version editor", () => {
  it("looks the schema up for the parameter's environment and opens on its fields", async () => {
    const dialog = await openNewVersion({ parameter: JSON_PARAMETER, overview: OVERVIEW });
    expect(api.applicationOverview).toHaveBeenCalledWith("billing", ["prod"], expect.anything());

    // The alias came from the overview's resolved key, not from the key itself.
    expect(within(dialog).getByText("cfg@3")).toBeVisible();
    expect(within(dialog).getByText("retries_alias")).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Form" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    const max = within(dialog).getByRole("textbox", { name: "max" });
    expect(max).toHaveValue("3");
    expect(max).toHaveAccessibleDescription("Attempts");
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("saves the minified JSON edited through the form", async () => {
    const putParameter = vi
      .spyOn(api, "putParameter")
      .mockResolvedValue({ version: 3, revision: 9 });
    const dialog = await openNewVersion({ parameter: JSON_PARAMETER, overview: OVERVIEW });
    const save = screen.getByRole("button", { name: "Save new version" });
    expect(save).toBeDisabled();
    expect(within(dialog).getByTestId("version-unchanged")).toHaveTextContent(
      "Nothing changed since v2.",
    );

    fireEvent.change(within(dialog).getByRole("textbox", { name: "max" }), {
      target: { value: "5" },
    });
    expect(within(dialog).queryByTestId("version-unchanged")).toBeNull();
    expect(save).toBeEnabled();
    fireEvent.click(save);
    await waitFor(() => expect(putParameter).toHaveBeenCalledTimes(1));
    expect(putParameter.mock.calls[0][0]).toMatchObject({
      env: "prod",
      app: "billing",
      key: "retries",
      value: '{"max":5,"name":"x"}',
      content_type: "json",
      metadata_json: "{}",
    });
  });

  it("falls back to a pretty-printed JSON editor when the namespace has no application", async () => {
    const dialog = await openNewVersion({ parameter: JSON_PARAMETER });
    const value = within(dialog).getByRole("textbox", { name: "Value" });
    expect(value).toHaveValue('{\n  "max": 3,\n  "name": "x"\n}');
    // The shape of the value still offers a form, one click away.
    expect(within(dialog).getByRole("button", { name: "JSON" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(within(dialog).getByRole("button", { name: "Form" })).toBeEnabled();
    expect(within(dialog).queryByText("cfg@3")).toBeNull();
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("saves from the JSON editor on Ctrl+Enter, minified", async () => {
    const putParameter = vi
      .spyOn(api, "putParameter")
      .mockResolvedValue({ version: 3, revision: 9 });
    const dialog = await openNewVersion({ parameter: JSON_PARAMETER });
    const value = within(dialog).getByRole("textbox", { name: "Value" });
    fireEvent.change(value, { target: { value: '{\n  "max": 4\n}' } });
    fireEvent.keyDown(value, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(putParameter).toHaveBeenCalledTimes(1));
    expect(putParameter.mock.calls[0][0]).toMatchObject({ value: '{"max":4}' });
  });

  it("treats a whitespace-only difference as unchanged", async () => {
    const dialog = await openNewVersion({ parameter: JSON_PARAMETER });
    const value = within(dialog).getByRole("textbox", { name: "Value" });
    fireEvent.change(value, { target: { value: '{"max": 3, "name": "x"}' } });
    expect(within(dialog).getByTestId("version-unchanged")).toBeVisible();
    expect(screen.getByRole("button", { name: "Save new version" })).toBeDisabled();
  });
});

describe("detail page navigation", () => {
  it("shows a breadcrumb trail on the parameter and secret pages", async () => {
    mocks.router.query = { env: PARAMETER.env, app: PARAMETER.app, key: PARAMETER.key };
    vi.spyOn(api, "parameterMetadata").mockResolvedValue(PARAMETER_META);
    vi.spyOn(api, "getParameter").mockResolvedValue({ parameter: PARAMETER });
    const { unmount } = render(<ParameterDetailPage />);
    await screen.findByRole("button", { name: "Copy value" });
    const nav = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(nav).getByRole("link", { name: "Parameters" })).toHaveAttribute(
      "href",
      "/parameters?env=prod&app=billing",
    );
    expect(within(nav).getByRole("link", { name: /billing/ })).toHaveAttribute(
      "href",
      "/applications?app=billing",
    );
    expect(nav).toHaveTextContent("retries");
    unmount();

    mocks.router.query = { env: SECRET.env, app: SECRET.app, key: SECRET.key };
    vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret: SECRET });
    render(<SecretDetailPage />);
    await screen.findByText("Content type");
    const secretNav = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(secretNav).getByRole("link", { name: "Secrets" })).toHaveAttribute(
      "href",
      "/secrets?env=prod&app=billing",
    );
    expect(secretNav).toHaveTextContent("api-key");
  });
});

describe("parameter version history", () => {
  const OLDER = { ...PARAMETER, value: "2", version: 1 };
  const HISTORY: ParameterMetadata = {
    ...PARAMETER_META,
    versions: [
      PARAMETER_META.versions[0],
      { ...PARAMETER_META.versions[0], version: 1, created_at_unix_ms: 1 },
    ],
  };

  function renderHistory() {
    mocks.router.query = { env: PARAMETER.env, app: PARAMETER.app, key: PARAMETER.key };
    vi.spyOn(api, "parameterMetadata").mockResolvedValue(HISTORY);
    vi.spyOn(api, "getParameter").mockImplementation(async (_ref, version) => ({
      parameter: version === 1 ? OLDER : PARAMETER,
    }));
    render(<ParameterDetailPage />);
    return screen.findByRole("button", { name: "Copy value" });
  }

  function row(version: number): HTMLElement {
    const cell = screen.getByText(`v${version}`, { selector: "td div" });
    const tr = cell.closest("tr");
    if (!tr) throw new Error(`row v${version} not found`);
    return tr;
  }

  it("marks the current version and diffs a viewed version against it", async () => {
    await renderHistory();
    expect(within(row(2)).getByText("current")).toBeVisible();
    expect(within(row(1)).queryByText("current")).toBeNull();

    fireEvent.click(within(row(1)).getByRole("button", { name: "View value" }));
    const panel = await screen.findByTestId("version-panel");
    expect(panel).toHaveTextContent("v1");
    expect(panel.querySelector(".json-block")?.textContent).toBe("2");
    expect(within(row(1)).getByText("viewing")).toBeVisible();

    fireEvent.click(within(panel).getByRole("button", { name: "Compare with current" }));
    const diff = await within(panel).findByTestId("json-diff");
    expect(diff).toHaveTextContent("+1 −1");
    expect(diff.querySelector('[data-op="del"].json-diff-text')?.textContent).toBe("2");
    expect(diff.querySelector('[data-op="add"].json-diff-text')?.textContent).toBe("3");
    expect(within(row(2)).getByText("comparing")).toBeVisible();

    fireEvent.click(within(panel).getByRole("button", { name: "Close compare" }));
    expect(within(panel).queryByTestId("json-diff")).toBeNull();
    expect(panel.querySelector(".json-block")?.textContent).toBe("2");
  });

  it("restores an older version by prefilling the new-version dialog", async () => {
    const putParameter = vi
      .spyOn(api, "putParameter")
      .mockResolvedValue({ version: 3, revision: 9 });
    await renderHistory();
    expect(within(row(2)).queryByRole("button", { name: /Restore/ })).toBeNull();

    fireEvent.click(within(row(1)).getByRole("button", { name: "Restore v1" }));
    const dialog = await screen.findByRole("dialog", { name: "New parameter version" });
    expect(within(dialog).getByTestId("restore-note")).toHaveTextContent("Prefilled from v1");
    expect(within(dialog).getByRole("textbox", { name: "Value" })).toHaveValue("2");

    const save = screen.getByRole("button", { name: "Save new version" });
    expect(save).toBeEnabled();
    fireEvent.click(save);
    await waitFor(() => expect(putParameter).toHaveBeenCalledTimes(1));
    expect(putParameter.mock.calls[0][0]).toMatchObject({ value: "2", content_type: "integer" });
  });
});

describe("new parameter version dialog", () => {
  it("puts the content type above the value and focuses the value on open", async () => {
    const dialog = await openNewVersion();
    const type = within(dialog).getByRole("combobox", { name: "Content type" });
    const value = within(dialog).getByRole("textbox", { name: "Value" });
    expect(type.compareDocumentPosition(value) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await waitFor(() => expect(value).toHaveFocus());
    expect(dialog).toHaveAccessibleDescription("Saving creates v3 and makes it current.");
  });

  it("shows what changed against the current version", async () => {
    const dialog = await openNewVersion({ parameter: JSON_PARAMETER });
    expect(within(dialog).queryByRole("button", { name: /Show changes/ })).toBeNull();

    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: '{"max":4,"name":"x"}' },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Show changes since v2" }));
    const diff = within(dialog).getByTestId("json-diff");
    expect(diff).toHaveTextContent("+1 −1");
    expect(diff.querySelector('[data-op="add"].json-diff-text')?.textContent).toBe('  "max": 4,');
  });

  it("asks before discarding an edit, from Cancel and from the close button", async () => {
    const dialog = await openNewVersion();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "4" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    const confirm = await screen.findByRole("dialog", { name: "Discard changes?", hidden: true });
    fireEvent.click(within(confirm).getByRole("button", { name: "Keep editing", hidden: true }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Discard changes?", hidden: true })).toBeNull(),
    );
    expect(screen.getByRole("dialog", { name: "New parameter version" })).toBeInTheDocument();

    fireEvent.click(
      within(screen.getByRole("dialog", { name: "New parameter version" })).getByRole("button", {
        name: "Dismiss dialog",
        hidden: true,
      }),
    );
    const again = await screen.findByRole("dialog", { name: "Discard changes?", hidden: true });
    fireEvent.click(within(again).getByRole("button", { name: "Discard", hidden: true }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "New parameter version" })).toBeNull(),
    );
  });

  it("closes without asking when nothing changed", async () => {
    const dialog = await openNewVersion();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "New parameter version" })).toBeNull(),
    );
    expect(screen.queryByRole("dialog", { name: "Discard changes?", hidden: true })).toBeNull();
  });

  it("moves focus to the invalid value on a blocked save", async () => {
    const putParameter = vi.spyOn(api, "putParameter");
    const dialog = await openNewVersion();
    const value = within(dialog).getByRole("textbox", { name: "Value" });
    fireEvent.change(value, { target: { value: "3.5" } });
    // Not touched yet, so nothing is shown and Save is still live.
    within(dialog).getByRole("combobox", { name: "Content type" }).focus();
    const save = screen.getByRole("button", { name: "Save new version" });
    expect(save).toBeEnabled();
    fireEvent.click(save);
    expect(within(dialog).getByText("Value must be a whole number.")).toBeVisible();
    expect(value).toHaveFocus();
    expect(putParameter).not.toHaveBeenCalled();
  });

  it("offers a schema that arrives after the operator has started typing", async () => {
    let release: (() => void) | undefined;
    const dialog = await openNewVersion({
      parameter: JSON_PARAMETER,
      overview: new Promise((resolve) => {
        release = () => resolve(OVERVIEW);
      }) as unknown as ApplicationOverview,
    });
    expect(within(dialog).getByText(/Looking for a schema/)).toBeInTheDocument();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: '{"max":4,"name":"x"}' },
    });

    release?.();
    const adopt = await within(dialog).findByRole("button", { name: "Use schema form" });
    // The late schema did not swap the editor out from under the edit…
    expect(within(dialog).getByRole("textbox", { name: "Value" })).toHaveValue(
      '{"max":4,"name":"x"}',
    );
    fireEvent.click(adopt);
    // …but one click adopts it, keeping the edited value.
    expect(await within(dialog).findByRole("textbox", { name: "max" })).toHaveValue("4");
    expect(within(dialog).getByText("cfg@3")).toBeVisible();
  });
});
