import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import type { Parameter, ParameterMetadata } from "@/lib/types";
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
  mocks.router.isReady = true;
  mocks.router.query = {};
  mocks.router.push.mockReset();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
});

afterEach(() => {
  cleanup();
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

/** Renders the parameter detail page and opens its new-version form. */
async function openNewVersion(): Promise<HTMLElement> {
  mocks.router.query = { env: PARAMETER.env, app: PARAMETER.app, key: PARAMETER.key };
  vi.spyOn(api, "parameterMetadata").mockResolvedValue(PARAMETER_META);
  vi.spyOn(api, "getParameter").mockResolvedValue({ parameter: PARAMETER });

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
    const metadata = within(dialog).getByRole("textbox", { name: "Metadata JSON" });

    fireEvent.change(metadata, { target: { value: '["owner"]' } });
    fireEvent.blur(metadata);
    expect(within(dialog).getByText("Metadata must be a JSON object.")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Save new version" }));
    expect(putParameter).not.toHaveBeenCalled();
  });
});
