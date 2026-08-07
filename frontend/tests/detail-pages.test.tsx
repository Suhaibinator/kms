import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import ParameterDetailPage from "@/pages/parameters/detail";
import SecretDetailPage from "@/pages/secrets/detail";

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
  vi.restoreAllMocks();
});

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
