import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  api,
  PURGE_CLEANUP_PENDING_MESSAGE,
  PurgeCleanupPendingApiError,
  SECRET_ALREADY_EXISTS_MESSAGE,
  SECRET_OPERATION_FAILED_MESSAGE,
} from "@/lib/api";
import { datetimeLocalToUnixMs } from "@/lib/format";
import type { Namespace, SecretMetadata } from "@/lib/types";
import SecretDetailPage from "@/pages/secrets/detail";
import SecretsPage from "@/pages/secrets/index";
import NewSecretPage from "@/pages/secrets/new";
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
    info: vi.fn(),
  },
  identity: { name: "root", kind: "admin" as "admin" | "client", namespace: null },
}));

vi.mock("next/router", () => ({ useRouter: () => mocks.router }));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
// The page asks who is signed in before it offers bulk delete.
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: mocks.identity }),
}));

const NAMESPACE: Namespace = {
  env: "prod",
  app: "billing",
  description: "",
  allowed_auth_methods: ["mtls"],
  created_by: "admin",
  created_at_unix_ms: 1,
  parameter_count: 0,
  secret_count: 1,
};

const BINDING_KEY = "binding-key-current-0000000000001";
const NEW_BINDING_KEY = "binding-key-replacement-0000000001";

const SECRET: SecretMetadata = {
  env: NAMESPACE.env,
  app: NAMESPACE.app,
  key: "api-key",
  content_type: "text/plain",
  bound: false,
  has_access_token: false,
  metadata_json: "{}",
  created_at_unix_ms: 1,
  updated_at_unix_ms: 2,
  labels: { current: 1 },
  versions: [
    {
      version: 1,
      state: "enabled",
      bound: false,
      has_access_token: false,
      created_by: "admin",
      created_at_unix_ms: 1,
      destroyed_at_unix_ms: 0,
      expires_at_unix_ms: 0,
      metadata_json: "{}",
    },
  ],
};

beforeEach(() => {
  mocks.router.isReady = true;
  mocks.router.query = {};
  mocks.router.push.mockReset();
  mocks.router.replace.mockReset();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
  mocks.toast.info.mockReset();
  mocks.identity = { name: "root", kind: "admin", namespace: null };
  vi.spyOn(api, "listNamespaces").mockResolvedValue({
    namespaces: [NAMESPACE],
    next_page_token: "",
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

/** Picks the fixture namespace once its options have arrived. */
async function chooseNamespace(): Promise<void> {
  const app = screen.getByLabelText("Application");
  await waitFor(() => expect(app).toBeEnabled());
  await chooseSelectOption(app, NAMESPACE.app);
  const environment = screen.getByLabelText("Environment");
  await chooseSelectOption(environment, NAMESPACE.env);
}

/** The Key cell of every rendered row, in the order they are rendered. */
function keyColumn(): string[] {
  return [...document.querySelectorAll('table.data tbody td[data-label="Key"]')].map(
    (cell) => cell.textContent ?? "",
  );
}

/** Renders the secret detail page and opens its new-version form. */
async function openNewVersion(secret: SecretMetadata = SECRET): Promise<HTMLElement> {
  mocks.router.query = { env: secret.env, app: secret.app, key: secret.key };
  vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret });

  render(<SecretDetailPage />);
  fireEvent.click(await screen.findByRole("button", { name: "New version" }));
  return screen.getByRole("dialog", { name: "New secret version" });
}

describe("secret list filter validation", () => {
  it("blocks a key prefix the list API would reject", async () => {
    const listSecrets = vi
      .spyOn(api, "listSecrets")
      .mockResolvedValue({ secrets: [], next_page_token: "" });
    render(<SecretsPage />);
    await chooseNamespace();
    const prefix = screen.getByLabelText("Key prefix");

    // A trailing slash is what the placeholder suggests, and the server's
    // key rule rejects it — but not until the operator has left the field.
    fireEvent.change(prefix, { target: { value: "billing/" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.blur(prefix);
    expect(screen.getByText("Key must not start or end with '/'.")).toBeVisible();
    expect(prefix).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("button", { name: "Filter" })).toBeDisabled();

    listSecrets.mockClear();
    fireEvent.submit(prefix.closest("form") as HTMLFormElement);
    expect(listSecrets).not.toHaveBeenCalled();

    // Dropping the slash makes the same prefix legal again — and the same
    // submit now reaches the API, so the block above was the validator's doing.
    fireEvent.change(prefix, { target: { value: "billing" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Filter" })).toBeEnabled();

    fireEvent.submit(prefix.closest("form") as HTMLFormElement);
    await vi.waitFor(() => expect(listSecrets).toHaveBeenCalled());
    expect(listSecrets.mock.lastCall?.[1]).toBe("billing");
  });

  it("clears the rows it was showing when the next load fails", async () => {
    const listSecrets = vi
      .spyOn(api, "listSecrets")
      .mockResolvedValue({ secrets: [SECRET], next_page_token: "" });
    render(<SecretsPage />);
    await chooseNamespace();
    expect(await screen.findByText(SECRET.key)).toBeVisible();

    // A failed reload must not leave the previous result on screen: those rows
    // would read as the current filter's answer.
    listSecrets.mockRejectedValue(new Error("offline"));
    const prefix = screen.getByLabelText("Key prefix");
    fireEvent.change(prefix, { target: { value: "billing" } });
    fireEvent.submit(prefix.closest("form") as HTMLFormElement);

    expect(await screen.findByText("No secrets found")).toBeVisible();
    expect(screen.queryByText(SECRET.key)).not.toBeInTheDocument();
    expect(mocks.toast.error).toHaveBeenCalled();
  });

  it("writes the namespace and prefix back to the URL", async () => {
    vi.spyOn(api, "listSecrets").mockResolvedValue({ secrets: [], next_page_token: "" });
    render(<SecretsPage />);
    await chooseNamespace();

    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      { pathname: "/secrets", query: { env: NAMESPACE.env, app: NAMESPACE.app } },
      undefined,
      { shallow: true, scroll: false },
    );

    const prefix = screen.getByLabelText("Key prefix");
    fireEvent.change(prefix, { target: { value: "billing" } });
    fireEvent.submit(prefix.closest("form") as HTMLFormElement);
    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      { pathname: "/secrets", query: { key_prefix: "billing" } },
      undefined,
      { shallow: true, scroll: false },
    );

    // An empty value drops the key rather than writing `key_prefix=`.
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      { pathname: "/secrets", query: {} },
      undefined,
      { shallow: true, scroll: false },
    );
  });

  it("treats an empty prefix as the whole namespace", async () => {
    vi.spyOn(api, "listSecrets").mockResolvedValue({ secrets: [], next_page_token: "" });
    render(<SecretsPage />);
    await chooseNamespace();
    const prefix = screen.getByLabelText("Key prefix");

    fireEvent.focus(prefix);
    fireEvent.blur(prefix);

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Filter" })).toBeEnabled();
  });

  it("reorders the loaded page from a column header and records the sort in the URL", async () => {
    const older: SecretMetadata = { ...SECRET, key: "zeta", updated_at_unix_ms: 9 };
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    vi.spyOn(api, "listSecrets").mockResolvedValue({
      secrets: [older, SECRET],
      next_page_token: "",
    });
    const { rerender } = render(<SecretsPage />);
    expect(await screen.findByText("zeta")).toBeVisible();
    expect(keyColumn()).toEqual(["zeta", "api-key"]);
    expect(screen.getByTestId("table-summary")).toHaveTextContent("Showing 2 of 2 secrets");

    fireEvent.click(screen.getByRole("button", { name: "Key" }));
    expect(mocks.router.replace).toHaveBeenLastCalledWith(
      {
        pathname: "/secrets",
        query: { env: NAMESPACE.env, app: NAMESPACE.app, sort: "key", dir: "asc" },
      },
      undefined,
      { shallow: true, scroll: false },
    );

    // The URL is the source of truth, so land the router on what the click asked for.
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app, sort: "key", dir: "asc" };
    rerender(<SecretsPage />);
    expect(keyColumn()).toEqual(["api-key", "zeta"]);
    expect(screen.getByRole("button", { name: "Key" }).closest("th")).toHaveAttribute(
      "aria-sort",
      "ascending",
    );
  });
});

describe("secret workspace navigation", () => {
  it("opens an ordinary key activation in place and preserves the detail href", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    vi.spyOn(api, "listSecrets").mockResolvedValue({ secrets: [SECRET], next_page_token: "" });
    vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret: SECRET });

    render(<SecretsPage />);
    const key = await screen.findByRole("link", { name: SECRET.key });
    expect(key).toHaveAttribute(
      "href",
      `/secrets/detail?env=${NAMESPACE.env}&app=${NAMESPACE.app}&key=${SECRET.key}`,
    );

    fireEvent.click(key);
    const workspace = await screen.findByRole("dialog", {
      name: `/${NAMESPACE.env}/${NAMESPACE.app}/${SECRET.key}`,
    });
    expect(within(workspace).getByRole("tab", { name: "Overview" })).toBeVisible();
    expect(within(workspace).getByRole("tab", { name: "Versions" })).toBeVisible();
    expect(mocks.router.push).not.toHaveBeenCalled();
  });

  it("does not intercept modified clicks", async () => {
    mocks.router.query = { env: NAMESPACE.env, app: NAMESPACE.app };
    vi.spyOn(api, "listSecrets").mockResolvedValue({ secrets: [SECRET], next_page_token: "" });
    const metadata = vi.spyOn(api, "secretMetadata");

    render(<SecretsPage />);
    const key = await screen.findByRole("link", { name: SECRET.key });
    fireEvent.click(key, { ctrlKey: true });

    expect(metadata).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("new secret validation", () => {
  /** Renders the form with a namespace chosen and every other field valid, so
   *  the field under test is the only thing that can block a submit. */
  async function renderReadyForm(): Promise<void> {
    render(<NewSecretPage />);
    await chooseNamespace();
    fireEvent.change(screen.getByLabelText("Key"), { target: { value: "api-key" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Value" }), {
      target: { value: "a value" },
    });
  }

  it("reports missing required fields inline, not as toasts", async () => {
    const createSecret = vi.spyOn(api, "createSecret");
    render(<NewSecretPage />);

    const form = screen
      .getByRole("button", { name: "Create secret" })
      .closest("form") as HTMLFormElement;
    fireEvent.submit(form);

    expect(createSecret).not.toHaveBeenCalled();
    expect(mocks.toast.error).not.toHaveBeenCalled();
    expect(screen.getByText("Choose an application.")).toBeVisible();
    expect(screen.getByText("Choose an environment.")).toBeVisible();
    expect(screen.getByText("Key is required.")).toBeVisible();
    expect(screen.getByText("A secret value is required.")).toBeVisible();
  });

  it("blocks an expiry that is already in the past", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockResolvedValue({ version: 1, revision: 1 });
    await renderReadyForm();
    const expires = screen.getByLabelText("Expires at");

    // The `min` bound is a convenience; the inline check is what blocks submit.
    expect(expires).toHaveAttribute("min");

    fireEvent.change(expires, { target: { value: "2000-01-01T00:00" } });
    fireEvent.blur(expires);
    expect(screen.getByText("Expiry must be in the future.")).toBeVisible();

    fireEvent.submit(expires.closest("form") as HTMLFormElement);
    expect(createSecret).not.toHaveBeenCalled();
  });

  it("blocks a create on a key the API would reject", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockResolvedValue({ version: 1, revision: 1 });
    await renderReadyForm();
    const key = screen.getByLabelText("Key");

    fireEvent.change(key, { target: { value: "Billing/API-Key" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.blur(key);
    expect(screen.getByText(/must use lowercase letters/)).toBeVisible();
    expect(key).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("button", { name: "Create secret" })).toBeDisabled();

    fireEvent.submit(key.closest("form") as HTMLFormElement);
    expect(createSecret).not.toHaveBeenCalled();

    // The same submit goes through once the key is legal, so nothing but the
    // validator was holding it back.
    fireEvent.change(key, { target: { value: "billing/api-key" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create secret" })).toBeEnabled();

    fireEvent.submit(key.closest("form") as HTMLFormElement);
    await vi.waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
  });

  it("reports an oversize value by size alone, never echoing the value", async () => {
    const createSecret = vi.spyOn(api, "createSecret");
    await renderReadyForm();
    const value = screen.getByRole("textbox", { name: "Value" });

    // A distinctive marker: if the plaintext ever reached a message or a toast,
    // it would show up here.
    const marker = "canary-plaintext-marker";
    fireEvent.change(value, { target: { value: marker + "x".repeat(3 << 19) } });
    fireEvent.blur(value);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("over the 1.0 MiB limit");
    expect(alert.textContent).not.toContain(marker);
    expect(screen.getByRole("button", { name: "Create secret" })).toBeDisabled();

    fireEvent.submit(value.closest("form") as HTMLFormElement);
    expect(createSecret).not.toHaveBeenCalled();
    for (const call of mocks.toast.error.mock.calls) {
      expect(JSON.stringify(call)).not.toContain(marker);
    }
  });

  it("blocks a create on metadata that is not a JSON object", async () => {
    const createSecret = vi.spyOn(api, "createSecret");
    await renderReadyForm();
    const metadata = screen.getByLabelText("Metadata JSON");

    fireEvent.change(metadata, { target: { value: '["owner"]' } });
    fireEvent.blur(metadata);
    expect(screen.getByText("Metadata must be a JSON object.")).toBeVisible();

    fireEvent.submit(metadata.closest("form") as HTMLFormElement);
    expect(createSecret).not.toHaveBeenCalled();
  });

  it("sends an opaque binding key without coupling it to access-token generation", async () => {
    let finish: (value: { version: number; revision: number }) => void = () => undefined;
    const createSecret = vi.spyOn(api, "createSecret").mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    await renderReadyForm();

    fireEvent.click(
      screen.getByRole("checkbox", { name: /Bind this version to an application key/ }),
    );
    const input = screen.getByLabelText("Binding key");
    const opaqueKey = `  ${"k".repeat(30)}`;
    fireEvent.change(input, { target: { value: opaqueKey } });
    fireEvent.submit(input.closest("form") as HTMLFormElement);

    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret.mock.calls[0][0]).toMatchObject({
      binding_key: opaqueKey,
      generate_access_token: false,
    });
    expect(createSecret.mock.calls[0][0]).not.toHaveProperty("client_bound");
    expect(createSecret.mock.calls[0][0]).not.toHaveProperty("secret_token");
    expect(input).toHaveValue("");
    await act(async () => finish({ version: 1, revision: 1 }));
  });

  it("creates only a new key and keeps the form usable after an existing-secret conflict", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockRejectedValue(new ApiError("already_exists", SECRET_OPERATION_FAILED_MESSAGE, 409));
    await renderReadyForm();

    fireEvent.click(screen.getByRole("button", { name: "Create secret" }));

    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret.mock.calls[0][0]).toMatchObject({ create_only: true });
    await waitFor(() =>
      expect(mocks.toast.error).toHaveBeenCalledWith(
        SECRET_ALREADY_EXISTS_MESSAGE,
        "Secret already exists",
      ),
    );
    expect(mocks.router.push).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Key")).toHaveValue("api-key");
    expect(screen.getByRole("textbox", { name: "Value" })).toHaveValue("a value");
    expect(screen.getByRole("button", { name: "Create secret" })).toBeEnabled();
  });
});

describe("new secret version validation", () => {
  it("blocks a save on metadata that is not a JSON object", async () => {
    const createSecret = vi.spyOn(api, "createSecret");
    const dialog = await openNewVersion();
    const metadata = within(dialog).getByRole("textbox", { name: "Metadata JSON" });

    // The prefilled "{}" is not a mistake the operator has made yet.
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();

    // A valid value, so only the metadata can block the save.
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "a value" },
    });
    fireEvent.change(metadata, { target: { value: "42" } });
    fireEvent.blur(metadata);
    expect(within(dialog).getByText("Metadata must be a JSON object.")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Save new version" }));
    expect(createSecret).not.toHaveBeenCalled();
  });

  it("confirms token rotation, carries expiry, and holds the replacement token", async () => {
    const protectedSecret: SecretMetadata = {
      ...SECRET,
      has_access_token: true,
      versions: SECRET.versions.map((version) => ({ ...version, has_access_token: true })),
    };
    mocks.router.query = {
      env: protectedSecret.env,
      app: protectedSecret.app,
      key: protectedSecret.key,
    };
    const metadata = vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret: protectedSecret });
    const createSecret = vi.spyOn(api, "createSecret").mockResolvedValue({
      version: 2,
      revision: 3,
      access_token: "kmss_replacement",
    });

    render(<SecretDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: "New version" }));
    const dialog = screen.getByRole("dialog", { name: "New secret version" });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "replacement value" },
    });
    fireEvent.click(within(dialog).getByText("Advanced options"));
    const expires = within(dialog).getByLabelText("Expires at");
    fireEvent.change(expires, { target: { value: "2099-01-02T03:04" } });
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Rotate access token/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Create version & rotate token" }));

    expect(createSecret).not.toHaveBeenCalled();
    const confirm = screen.getByRole("dialog", { name: "Rotate access token?" });
    expect(confirm).toHaveTextContent("current token");
    fireEvent.click(within(confirm).getByRole("button", { name: "Create version & rotate token" }));

    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret).toHaveBeenCalledWith(
      expect.objectContaining({
        generate_access_token: true,
        expires_at_unix_ms: datetimeLocalToUnixMs("2099-01-02T03:04"),
      }),
    );
    const token = await screen.findByRole("dialog", { name: "Save this access token now" });
    expect(token).toHaveTextContent("kmss_replacement");
    expect(within(token).queryByRole("button", { name: "Dismiss dialog" })).toBeNull();
    fireEvent.click(within(token).getByRole("button", { name: "I've saved it — continue" }));
    await waitFor(() => expect(metadata).toHaveBeenCalledTimes(2));
  });
});

describe("protected secret reveal", () => {
  it("bypasses the exact-version access token and sends only the transient binding key", async () => {
    const protectedSecret: SecretMetadata = {
      ...SECRET,
      bound: true,
      has_access_token: true,
      versions: [{ ...SECRET.versions[0], bound: true, has_access_token: true }],
    };
    mocks.router.query = {
      env: protectedSecret.env,
      app: protectedSecret.app,
      key: protectedSecret.key,
    };
    vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret: protectedSecret });
    const revealResponse = {
      env: protectedSecret.env,
      app: protectedSecret.app,
      key: protectedSecret.key,
      version: 1,
      value_base64: "dmFsdWU=",
      content_type: "text/plain",
    };
    let finishReveal: (response: typeof revealResponse) => void = () => undefined;
    const reveal = vi.spyOn(api, "revealSecret").mockImplementation(
      () =>
        new Promise((resolve) => {
          finishReveal = resolve;
        }),
    );

    render(<SecretDetailPage />);
    const revealButton = await screen.findByRole("button", { name: "Reveal secret" });
    const revealNotice = screen.getByText(/Revealing decrypts the selected version/);
    expect(revealNotice).toHaveTextContent("A binding key, when required");
    expect(revealNotice).not.toHaveTextContent("access token");
    fireEvent.click(revealButton);
    let dialog = screen.getByRole("dialog", { name: "Reveal secret value?" });
    let bindingInput = within(dialog).getByLabelText("Binding key");
    const confirm = within(dialog).getByRole("button", { name: "Reveal" });
    expect(within(dialog).queryByLabelText("Access token")).not.toBeInTheDocument();
    expect(bindingInput).toHaveAttribute("type", "password");
    expect(confirm).toBeDisabled();

    fireEvent.change(bindingInput, { target: { value: "discarded-binding-key" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    fireEvent.click(screen.getByRole("button", { name: "Reveal secret" }));
    dialog = screen.getByRole("dialog", { name: "Reveal secret value?" });
    bindingInput = within(dialog).getByLabelText("Binding key");
    expect(within(dialog).queryByLabelText("Access token")).not.toBeInTheDocument();
    expect(bindingInput).toHaveValue("");

    fireEvent.change(bindingInput, { target: { value: "binding-key-for-version-00000001" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Reveal" }));

    await waitFor(() =>
      expect(reveal).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "api-key" },
        1,
        "",
        "binding-key-for-version-00000001",
        { signal: expect.any(AbortSignal) },
      ),
    );
    expect(bindingInput).toHaveValue("");
    await act(async () => finishReveal(revealResponse));
    expect(mocks.toast.success).toHaveBeenCalledWith(
      "Revealed version 1",
      "Recorded in the audit log.",
    );
  });

  it.each([
    ["environment", { env: "staging" }],
    ["application", { app: "other-app" }],
    ["key", { key: "other-secret" }],
    ["version", { version: 2 }],
  ])("rejects a reveal response with a mismatched %s", async (_field, patch) => {
    mocks.router.query = { env: SECRET.env, app: SECRET.app, key: SECRET.key };
    vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret: SECRET });
    vi.spyOn(api, "revealSecret").mockResolvedValue({
      env: SECRET.env,
      app: SECRET.app,
      key: SECRET.key,
      version: 1,
      value_base64: "d3Jvbmctc2VjcmV0LXJlc3BvbnNl",
      content_type: "text/plain",
      ...patch,
    });

    render(<SecretDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Reveal secret" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Reveal" }));

    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledTimes(1));
    expect(mocks.toast.error.mock.calls[0][0]).toEqual(
      new Error("Reveal response did not match the requested secret version."),
    );
    expect(mocks.toast.success).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Show value" })).not.toBeInTheDocument();
  });

  it("does not offer administrator reveal controls to a client identity", async () => {
    const protectedSecret: SecretMetadata = {
      ...SECRET,
      bound: true,
      versions: [{ ...SECRET.versions[0], bound: true }],
    };
    mocks.identity = { name: "app", kind: "client", namespace: null };
    mocks.router.query = {
      env: protectedSecret.env,
      app: protectedSecret.app,
      key: protectedSecret.key,
    };
    vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret: protectedSecret });
    const reveal = vi.spyOn(api, "revealSecret");

    render(<SecretDetailPage />);

    expect(
      await screen.findByText(/Secret values can be revealed only by an administrator/),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "Reveal secret" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reveal" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Binding key")).not.toBeInTheDocument();
    expect(reveal).not.toHaveBeenCalled();
  });

  it("discards an in-flight reveal and closes its dialog when the active secret changes", async () => {
    const firstSecret: SecretMetadata = {
      ...SECRET,
      bound: true,
      has_access_token: true,
      versions: [{ ...SECRET.versions[0], bound: true, has_access_token: true }],
    };
    const nextSecret: SecretMetadata = {
      ...firstSecret,
      key: "replacement-key",
    };
    mocks.router.query = { env: firstSecret.env, app: firstSecret.app, key: firstSecret.key };
    vi.spyOn(api, "secretMetadata").mockImplementation(async (ref) => ({
      secret: ref.key === nextSecret.key ? nextSecret : firstSecret,
    }));

    const staleResponse = {
      env: firstSecret.env,
      app: firstSecret.app,
      key: firstSecret.key,
      version: 1,
      value_base64: "c3RhbGUgcGxhaW50ZXh0",
      content_type: "text/plain",
    };
    let finishReveal: (response: typeof staleResponse) => void = () => undefined;
    vi.spyOn(api, "revealSecret").mockImplementation(
      () =>
        new Promise((resolve) => {
          finishReveal = resolve;
        }),
    );

    const view = render(<SecretDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Reveal secret" }));
    const dialog = screen.getByRole("dialog", { name: "Reveal secret value?" });
    expect(within(dialog).queryByLabelText("Access token")).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Binding key"), {
      target: { value: "binding-key-for-version-00000001" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Reveal" }));

    mocks.router.query = { env: nextSecret.env, app: nextSecret.app, key: nextSecret.key };
    view.rerender(<SecretDetailPage />);
    await waitFor(() =>
      expect(api.secretMetadata).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "replacement-key" },
        { signal: expect.any(AbortSignal) },
      ),
    );
    await screen.findByRole("button", { name: "Reveal secret" });
    expect(screen.queryByRole("dialog", { name: "Reveal secret value?" })).not.toBeInTheDocument();

    await act(async () => finishReveal(staleResponse));
    expect(screen.queryByText("stale plaintext")).not.toBeInTheDocument();
    expect(mocks.toast.success).not.toHaveBeenCalled();
  });
});

describe("new secret value tools", () => {
  async function renderReadyForm(): Promise<void> {
    render(<NewSecretPage />);
    await chooseNamespace();
    fireEvent.change(screen.getByLabelText("Key"), { target: { value: "api-key" } });
  }

  it("edits metadata in a JSON editor and puts Cancel before Create", async () => {
    await renderReadyForm();
    const metadata = screen.getByRole("textbox", { name: "Metadata JSON" });
    expect(metadata.tagName).toBe("TEXTAREA");
    expect(screen.getByRole("button", { name: "Format" })).toBeInTheDocument();

    const cancel = screen.getByRole("link", { name: "Cancel" });
    const create = screen.getByRole("button", { name: "Create secret" });
    expect(cancel.compareDocumentPosition(create) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("masks the value until revealed", async () => {
    await renderReadyForm();
    const value = screen.getByRole("textbox", { name: "Value" });
    expect(value).toHaveAttribute("data-masked", "true");
    fireEvent.click(screen.getByRole("button", { name: "Show value" }));
    expect(value).toHaveAttribute("data-masked", "false");
    fireEvent.click(screen.getByRole("button", { name: "Hide value" }));
    expect(value).toHaveAttribute("data-masked", "true");
  });

  it("generates a random value and reveals it", async () => {
    await renderReadyForm();
    fireEvent.click(screen.getByRole("button", { name: "Generate…" }));
    const menu = await screen.findByRole("menu", { name: "Generate…" });
    fireEvent.click(within(menu).getByRole("menuitem", { name: "32 bytes, hex" }));
    const value = screen.getByRole("textbox", { name: "Value" }) as HTMLTextAreaElement;
    expect(value.value).toMatch(/^[0-9a-f]{64}$/);
    expect(value).toHaveAttribute("data-masked", "false");
  });

  it("passes an already-base64 value through untouched", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockResolvedValue({ version: 1, revision: 1 });
    await renderReadyForm();
    const value = screen.getByRole("textbox", { name: "Value" });
    fireEvent.click(screen.getByRole("checkbox", { name: "Value is already base64" }));

    fireEvent.change(value, { target: { value: "not base64!" } });
    fireEvent.blur(value);
    expect(screen.getByRole("alert")).toHaveTextContent("standard base64");

    fireEvent.change(value, { target: { value: "aGk=\n" } });
    fireEvent.submit(value.closest("form") as HTMLFormElement);
    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret.mock.calls[0][0]).toMatchObject({ value_base64: "aGk=" });
  });

  it("offers a content-type list with a free-text escape", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockResolvedValue({ version: 1, revision: 1 });
    await renderReadyForm();
    fireEvent.change(screen.getByRole("textbox", { name: "Value" }), {
      target: { value: "a value" },
    });
    const type = screen.getByRole("combobox", { name: "Content type" });
    expect(type).toHaveTextContent("text/plain");
    await chooseSelectOption(type, "Other…");
    fireEvent.change(screen.getByRole("textbox", { name: "Custom content type" }), {
      target: { value: "application/x-foo" },
    });
    fireEvent.submit(type.closest("form") as HTMLFormElement);
    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret.mock.calls[0][0]).toMatchObject({ content_type: "application/x-foo" });
  });
});

describe("new secret version dialog", () => {
  it("states which version becomes current and focuses the value", async () => {
    const dialog = await openNewVersion();
    expect(dialog).toHaveAccessibleDescription("Saving creates v2 and makes it current.");
    expect(within(dialog).getByTestId("version-transition")).toHaveTextContent("v1 → v2");
    await waitFor(() =>
      expect(within(dialog).getByRole("textbox", { name: "Value" })).toHaveFocus(),
    );
    // Metadata uses the same editor as the parameter forms.
    expect(within(dialog).getByRole("button", { name: "Format" })).toBeInTheDocument();
  });

  it("asks before discarding a typed value", async () => {
    const dialog = await openNewVersion();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "a value" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(
      await screen.findByRole("dialog", { name: "Discard changes?", hidden: true }),
    ).toBeInTheDocument();
  });

  it("sends a pasted base64 value as-is", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockResolvedValue({ version: 2, revision: 3 });
    const dialog = await openNewVersion();
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Value is already base64" }));
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "AQID" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save new version" }));
    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret.mock.calls[0][0]).toMatchObject({ value_base64: "AQID" });
  });

  it("chooses binding protection independently for the new version", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockResolvedValue({ version: 2, revision: 3 });
    const dialog = await openNewVersion();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "next value" },
    });
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Bind only this new version/ }));
    const key = within(dialog).getByLabelText("Binding key");
    fireEvent.change(key, { target: { value: BINDING_KEY } });
    fireEvent.click(screen.getByRole("button", { name: "Save new version" }));

    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret.mock.calls[0][0]).toMatchObject({
      binding_key: BINDING_KEY,
      generate_access_token: false,
    });
    expect(createSecret.mock.calls[0][0]).not.toHaveProperty("client_bound");
    expect(createSecret.mock.calls[0][0]).not.toHaveProperty("secret_token");
    expect(key).toHaveValue("");
  });

  it("preserves binding protection by default when the current version is bound", async () => {
    const createSecret = vi
      .spyOn(api, "createSecret")
      .mockResolvedValue({ version: 2, revision: 3 });
    const boundSecret: SecretMetadata = {
      ...SECRET,
      bound: true,
      versions: [{ ...SECRET.versions[0], bound: true }],
    };
    const dialog = await openNewVersion(boundSecret);

    expect(
      within(dialog).getByRole("checkbox", { name: /Bind only this new version/ }),
    ).toBeChecked();
    expect(within(dialog).getByText("Advanced options").closest("details")).toHaveAttribute("open");
    expect(within(dialog).getByLabelText("Binding key")).toBeVisible();
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Value" }), {
      target: { value: "next value" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save new version" }));

    expect(createSecret).not.toHaveBeenCalled();
    expect(within(dialog).getByLabelText("Binding key")).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Binding key must be at least 32 UTF-8 bytes.",
    );

    fireEvent.change(within(dialog).getByLabelText("Binding key"), {
      target: { value: BINDING_KEY },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save new version" }));

    await waitFor(() => expect(createSecret).toHaveBeenCalledTimes(1));
    expect(createSecret.mock.calls[0][0]).toMatchObject({ binding_key: BINDING_KEY });
  });
});

describe("binding-key version actions", () => {
  function renderDetail(secret: SecretMetadata) {
    mocks.router.query = { env: secret.env, app: secret.app, key: secret.key };
    vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret });
    return render(<SecretDetailPage />);
  }

  function renderBoundDetail() {
    return renderDetail({
      ...SECRET,
      bound: true,
      versions: [{ ...SECRET.versions[0], bound: true }],
    });
  }

  async function submitPurgeAfterPreview(): Promise<void> {
    fireEvent.click(
      await screen.findByRole("button", { name: "Purge cohort containing version 1" }),
    );
    let dialog = screen.getByRole("dialog", { name: "Purge cohort · v1" });
    fireEvent.change(within(dialog).getByLabelText("Current binding key"), {
      target: { value: BINDING_KEY },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Preview cohort" }));
    await screen.findByTestId("binding-cohort-versions");
    dialog = screen.getByRole("dialog", { name: "Purge cohort · v1" });
    fireEvent.change(within(dialog).getByLabelText("Current binding key"), {
      target: { value: BINDING_KEY },
    });
    fireEvent.change(within(dialog).getByLabelText(/Type PURGE/), {
      target: { value: "PURGE" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Purge versions" }));
  }

  it("binds and unbinds current into a new version without a cohort preview", async () => {
    const bind = vi.spyOn(api, "bindSecret").mockResolvedValue({
      current_version: 2,
      previous_version: 1,
      revision: 10,
    });
    const preview = vi.spyOn(api, "previewSecretBindingCohort");
    const view = renderDetail(SECRET);
    fireEvent.click(await screen.findByRole("button", { name: "Bind" }));
    let dialog = screen.getByRole("dialog", { name: "Bind · v1" });
    const key = within(dialog).getByLabelText("New binding key");
    fireEvent.change(key, { target: { value: BINDING_KEY } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Bind" }));

    await waitFor(() =>
      expect(bind).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "api-key" },
        1,
        BINDING_KEY,
        { signal: expect.any(AbortSignal) },
      ),
    );
    expect(key).toHaveValue("");
    expect(preview).not.toHaveBeenCalled();

    // Re-render a bound row to exercise the inverse exact-version operation.
    view.unmount();
    const unbind = vi.spyOn(api, "unbindSecret").mockResolvedValue({
      current_version: 2,
      previous_version: 1,
      revision: 11,
    });
    renderBoundDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Unbind" }));
    dialog = screen.getByRole("dialog", { name: "Unbind · v1" });
    fireEvent.change(within(dialog).getByLabelText("Current binding key"), {
      target: { value: BINDING_KEY },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Unbind" }));
    await waitFor(() => expect(unbind).toHaveBeenCalledTimes(1));
  });

  it("rotates only current into one new version with its current-version CAS", async () => {
    const preview = vi.spyOn(api, "previewSecretBindingCohort");
    const rotate = vi.spyOn(api, "rotateSecretBindingKey").mockResolvedValue({
      current_version: 2,
      previous_version: 1,
      revision: 42,
    });
    renderBoundDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Rotate key" }));
    const dialog = screen.getByRole("dialog", { name: "Rotate binding key · v1" });
    const current = within(dialog).getByLabelText("Current binding key");
    const replacement = within(dialog).getByLabelText("New binding key");
    const confirmation = within(dialog).getByLabelText("Confirm new binding key");
    fireEvent.change(current, { target: { value: BINDING_KEY } });
    fireEvent.change(replacement, { target: { value: NEW_BINDING_KEY } });
    fireEvent.change(confirmation, { target: { value: NEW_BINDING_KEY } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rotate binding key" }));

    await waitFor(() =>
      expect(rotate).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "api-key" },
        1,
        BINDING_KEY,
        NEW_BINDING_KEY,
        { signal: expect.any(AbortSignal) },
      ),
    );
    expect(preview).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("dialog", { name: "Rotate binding key · v1" }),
    ).not.toBeInTheDocument();
  });

  it("keeps no-op rotation diagnostics sanitized after ordered server validation", async () => {
    const rotate = vi
      .spyOn(api, "rotateSecretBindingKey")
      .mockRejectedValue(new ApiError("invalid_argument", SECRET_OPERATION_FAILED_MESSAGE, 400));
    renderBoundDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Rotate key" }));
    const dialog = screen.getByRole("dialog", { name: "Rotate binding key · v1" });
    fireEvent.change(within(dialog).getByLabelText("Current binding key"), {
      target: { value: BINDING_KEY },
    });
    fireEvent.change(within(dialog).getByLabelText("New binding key"), {
      target: { value: BINDING_KEY },
    });
    fireEvent.change(within(dialog).getByLabelText("Confirm new binding key"), {
      target: { value: BINDING_KEY },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rotate binding key" }));

    await waitFor(() =>
      expect(rotate).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "api-key" },
        1,
        BINDING_KEY,
        BINDING_KEY,
        { signal: expect.any(AbortSignal) },
      ),
    );
    await waitFor(() =>
      expect(mocks.toast.error).toHaveBeenCalledWith(
        expect.objectContaining({
          code: "invalid_argument",
          message: SECRET_OPERATION_FAILED_MESSAGE,
        }),
        "Rotate binding key failed",
      ),
    );
  });

  it("keeps transitions on current while retaining cohort purge for historical bound versions", async () => {
    renderDetail({
      ...SECRET,
      bound: false,
      labels: { current: 2, previous: 1 },
      versions: [
        { ...SECRET.versions[0], bound: true },
        { ...SECRET.versions[0], version: 2, created_at_unix_ms: 2 },
      ],
    });

    const historicalRow = (await screen.findByText("v1")).closest("tr");
    const currentRow = screen
      .getAllByText("v2")
      .map((element) => element.closest("tr"))
      .find((row): row is HTMLTableRowElement => row !== null);
    expect(historicalRow).not.toBeNull();
    expect(currentRow).toBeDefined();
    expect(
      within(historicalRow as HTMLElement).queryByRole("button", { name: "Unbind" }),
    ).toBeNull();
    expect(
      within(historicalRow as HTMLElement).queryByRole("button", { name: "Rotate key" }),
    ).toBeNull();
    expect(
      within(historicalRow as HTMLElement).getByRole("button", {
        name: "Purge cohort containing version 1",
      }),
    ).toBeVisible();
    expect(
      within(historicalRow as HTMLElement).getByRole("button", { name: "Destroy version 1" }),
    ).toBeVisible();
    expect(within(currentRow as HTMLElement).getByRole("button", { name: "Bind" })).toBeVisible();
  });

  it("previews and purges every unbound version with exact guards", async () => {
    const preview = vi.spyOn(api, "previewSecretUnboundVersions").mockResolvedValue({
      affected_versions: [1, 3],
      revision: 60,
    });
    const purge = vi.spyOn(api, "purgeSecretUnboundVersions").mockResolvedValue({
      affected_versions: [1, 3],
      revision: 61,
    });
    renderDetail(SECRET);
    fireEvent.click(await screen.findByRole("button", { name: "Purge unbound versions" }));
    let dialog = screen.getByRole("dialog", { name: "Purge unbound versions" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Preview unbound versions" }));
    await waitFor(() =>
      expect(preview).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "api-key" },
        { signal: expect.any(AbortSignal) },
      ),
    );
    dialog = screen.getByRole("dialog", { name: "Purge unbound versions" });
    expect(within(dialog).getByTestId("unbound-purge-versions")).toHaveTextContent("v1");
    expect(within(dialog).getByTestId("unbound-purge-versions")).toHaveTextContent("v3");
    fireEvent.change(within(dialog).getByLabelText(/Type PURGE/), {
      target: { value: "PURGE" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Purge versions" }));
    await waitFor(() =>
      expect(purge).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "api-key" },
        60,
        [1, 3],
        { signal: expect.any(AbortSignal) },
      ),
    );
  });

  it("previews and purges the exact cohort only for an administrator", async () => {
    vi.spyOn(api, "previewSecretBindingCohort").mockResolvedValue({
      anchor_version: 1,
      affected_versions: [1],
      revision: 50,
    });
    const purge = vi.spyOn(api, "purgeSecretBindingCohort").mockResolvedValue({
      anchor_version: 1,
      affected_versions: [1],
      revision: 51,
    });
    renderBoundDetail();
    await submitPurgeAfterPreview();

    await waitFor(() =>
      expect(purge).toHaveBeenCalledWith(
        { env: "prod", app: "billing", key: "api-key" },
        1,
        BINDING_KEY,
        50,
        [1],
        { signal: expect.any(AbortSignal) },
      ),
    );
  });

  it("treats the API-minted cleanup-pending result as a committed purge", async () => {
    vi.spyOn(api, "previewSecretBindingCohort").mockResolvedValue({
      anchor_version: 1,
      affected_versions: [1],
      revision: 50,
    });
    vi.spyOn(api, "purgeSecretBindingCohort").mockRejectedValue(new PurgeCleanupPendingApiError());
    renderBoundDetail();

    await submitPurgeAfterPreview();

    await waitFor(() =>
      expect(mocks.toast.info).toHaveBeenCalledWith(
        "Purge committed",
        "Database artifact cleanup is pending. Do not retry with the binding key; restart the service to complete cleanup.",
        { duration: 12_000 },
      ),
    );
    expect(mocks.toast.error).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "Purge cohort · v1" })).not.toBeInTheDocument();
    await waitFor(() => expect(api.secretMetadata).toHaveBeenCalledTimes(2));
  });

  it("treats unbound cleanup-pending as committed without retrying the preview", async () => {
    vi.spyOn(api, "previewSecretUnboundVersions").mockResolvedValue({
      affected_versions: [1],
      revision: 50,
    });
    vi.spyOn(api, "purgeSecretUnboundVersions").mockRejectedValue(
      new PurgeCleanupPendingApiError(),
    );
    renderDetail(SECRET);

    fireEvent.click(await screen.findByRole("button", { name: "Purge unbound versions" }));
    let dialog = screen.getByRole("dialog", { name: "Purge unbound versions" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Preview unbound versions" }));
    await screen.findByTestId("unbound-purge-versions");
    dialog = screen.getByRole("dialog", { name: "Purge unbound versions" });
    fireEvent.change(within(dialog).getByLabelText(/Type PURGE/), {
      target: { value: "PURGE" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Purge versions" }));

    await waitFor(() =>
      expect(mocks.toast.info).toHaveBeenCalledWith(
        "Purge committed",
        "Database artifact cleanup is pending. Do not retry the purge; restart the service to complete cleanup.",
        { duration: 12_000 },
      ),
    );
    expect(mocks.toast.error).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("dialog", { name: "Purge unbound versions" }),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(api.secretMetadata).toHaveBeenCalledTimes(2));
  });

  it.each([
    [
      "an exact-looking plain API error",
      () => new ApiError("purge_cleanup_pending", PURGE_CLEANUP_PENDING_MESSAGE, 503),
    ],
    [
      "the wrong HTTP status",
      () => new ApiError("purge_cleanup_pending", PURGE_CLEANUP_PENDING_MESSAGE, 500),
    ],
    ["the wrong code", () => new ApiError("unavailable", PURGE_CLEANUP_PENDING_MESSAGE, 503)],
    [
      "a near-match message",
      () =>
        new ApiError(
          "purge_cleanup_pending",
          `${PURGE_CLEANUP_PENDING_MESSAGE}! ${BINDING_KEY}`,
          503,
        ),
    ],
  ])("does not treat %s as a committed purge", async (_case, makeError) => {
    vi.spyOn(api, "previewSecretBindingCohort").mockResolvedValue({
      anchor_version: 1,
      affected_versions: [1],
      revision: 50,
    });
    vi.spyOn(api, "purgeSecretBindingCohort").mockRejectedValue(makeError());
    renderBoundDetail();

    await submitPurgeAfterPreview();

    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledTimes(1));
    expect(mocks.toast.info).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Purge cohort · v1" })).toBeVisible();
    expect(api.secretMetadata).toHaveBeenCalledTimes(1);
  });

  it("does not offer cohort purge to a client identity", async () => {
    mocks.identity = { name: "app", kind: "client", namespace: null };
    renderDetail({
      ...SECRET,
      bound: true,
      labels: { current: 2, previous: 1 },
      versions: [
        { ...SECRET.versions[0], bound: false },
        { ...SECRET.versions[0], version: 2, bound: true, created_at_unix_ms: 2 },
      ],
    });
    expect(await screen.findByRole("button", { name: "Rotate key" })).toBeVisible();
    expect(
      screen.queryByRole("button", { name: /Purge cohort containing version/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Purge unbound versions" }),
    ).not.toBeInTheDocument();
  });
});
