import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
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
  parameter_count: 0,
  secret_count: 1,
};

const SECRET: SecretMetadata = {
  env: NAMESPACE.env,
  app: NAMESPACE.app,
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
  ],
};

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

/** Picks the fixture namespace once its options have arrived. */
async function chooseNamespace(): Promise<void> {
  const app = screen.getByLabelText("Application");
  await chooseSelectOption(app, NAMESPACE.app);
  const environment = screen.getByLabelText("Environment");
  await chooseSelectOption(environment, NAMESPACE.env);
}

/** Renders the secret detail page and opens its new-version form. */
async function openNewVersion(): Promise<HTMLElement> {
  mocks.router.query = { env: SECRET.env, app: SECRET.app, key: SECRET.key };
  vi.spyOn(api, "secretMetadata").mockResolvedValue({ secret: SECRET });

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
});
