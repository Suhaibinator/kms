import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ParameterManager from "@/components/parameters/ParameterManager";
import SecretManager from "@/components/secrets/SecretManager";
import { api } from "@/lib/api";

const mocks = vi.hoisted(() => ({
  router: {
    isReady: true,
    query: { env: "prod", app: "billing", key: "alpha" } as Record<string, string>,
    push: vi.fn(),
    replace: vi.fn(),
  },
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("next/router", () => ({ useRouter: () => mocks.router }));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ identity: { name: "root", kind: "admin", namespace: null } }),
}));

const ref = { env: "prod", app: "billing", key: "alpha" };

function secret(key: string) {
  return {
    ...ref,
    key,
    content_type: "text/plain",
    bound: false,
    metadata_json: "{}",
    created_at_unix_ms: 1,
    updated_at_unix_ms: 1,
    labels: { current: 1 },
    versions: [
      {
        version: 1,
        state: "enabled" as const,
        bound: false,
        created_by: "admin",
        created_at_unix_ms: 1,
        destroyed_at_unix_ms: 0,
        expires_at_unix_ms: 0,
        metadata_json: "{}",
      },
      {
        version: 2,
        state: "disabled" as const,
        bound: false,
        created_by: "admin",
        created_at_unix_ms: 1,
        destroyed_at_unix_ms: 0,
        expires_at_unix_ms: 0,
        metadata_json: "{}",
      },
      {
        version: 3,
        state: "enabled" as const,
        bound: false,
        created_by: "admin",
        created_at_unix_ms: 1,
        destroyed_at_unix_ms: 0,
        expires_at_unix_ms: 0,
        metadata_json: "{}",
      },
    ],
  };
}

beforeEach(() => {
  mocks.router.query = { ...ref };
  mocks.router.push.mockReset();
  mocks.router.replace.mockReset();
  mocks.toast.error.mockReset();
  mocks.toast.success.mockReset();
});

afterEach(() => vi.restoreAllMocks());

describe("resource detail scope changes", () => {
  it("closes a parameter draft before it can save against the history-selected resource", async () => {
    vi.spyOn(api, "parameterMetadata").mockImplementation(async (resource) => ({
      ...resource,
      content_type: "string",
      metadata_json: "{}",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 1,
      labels: { current: 1 },
      versions: [],
    }));
    vi.spyOn(api, "getParameter").mockImplementation(async (resource) => ({
      parameter: {
        ...resource,
        value: "original",
        content_type: "string",
        version: 1,
        metadata_json: "{}",
        created_by: "admin",
        created_at_unix_ms: 1,
        labels: { current: 1 },
      },
    }));
    const put = vi.spyOn(api, "putParameter").mockResolvedValue({ version: 2, revision: 2 });
    const view = render(<ParameterManager />);
    fireEvent.click(await screen.findByRole("button", { name: "New version" }));
    fireEvent.change(within(screen.getByRole("dialog")).getByLabelText("Value"), {
      target: { value: "draft for alpha" },
    });

    mocks.router.query = { ...ref, key: "beta" };
    view.rerender(<ParameterManager />);

    expect(screen.queryByRole("dialog", { name: "New parameter version" })).not.toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();
  });

  it.each([
    ["Disable", "disableSecret"],
    ["Enable", "disableSecret"],
    ["Promote", "promoteSecret"],
    ["Destroy version 1", "destroySecret"],
    ["Delete", "deleteSecret"],
  ] as const)("does not carry a %s confirmation to another secret", async (action, apiName) => {
    vi.spyOn(api, "secretMetadata").mockImplementation(async (resource) => ({
      secret: secret(resource.key),
    }));
    const operation = vi.spyOn(api, apiName).mockResolvedValue({} as never);
    const view = render(<SecretManager />);
    const trigger =
      action === "Disable"
        ? (await screen.findAllByRole("button", { name: action }))[0]
        : await screen.findByRole("button", { name: action });
    fireEvent.click(trigger);
    await screen.findByRole("dialog");

    mocks.router.query = { ...ref, key: "beta" };
    view.rerender(<SecretManager />);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(operation).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByText("beta")).toBeVisible());
  });
});
