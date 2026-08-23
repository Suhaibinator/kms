import { fireEvent, render, screen, within } from "@testing-library/react";
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
