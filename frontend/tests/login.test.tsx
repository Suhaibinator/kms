import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import LoginPage from "@/pages/login";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  replace: vi.fn(async () => true),
  login: vi.fn(),
  token: null as string | null,
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    query: mocks.query,
    isReady: true,
    pathname: "/login",
    replace: mocks.replace,
  }),
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));
vi.mock("@/context/AuthContext", () => ({
  useAuth: () => ({ login: mocks.login, ready: true }),
}));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return { ...actual, getToken: () => mocks.token };
});

const { ApiError } = await import("@/lib/api");

describe("LoginPage", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.token = null;
    mocks.replace.mockClear();
    mocks.login.mockReset();
    mocks.toast.success.mockClear();
    mocks.toast.error.mockClear();
  });

  function submit(token: string) {
    fireEvent.change(screen.getByLabelText("Identity token"), { target: { value: token } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  }

  it("puts the cursor in the token field so the page is usable from the keyboard", () => {
    render(<LoginPage />);

    expect(screen.getByLabelText("Identity token")).toHaveFocus();
  });

  it("reports an empty token inline rather than as a toast", () => {
    render(<LoginPage />);
    // No error before the first attempt: an untouched form is not a failure.
    expect(screen.queryByRole("alert")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(screen.getByRole("alert")).toHaveTextContent("Enter a token to continue.");
    expect(screen.getByLabelText("Identity token")).toHaveAttribute("aria-invalid", "true");
    expect(mocks.toast.error).not.toHaveBeenCalled();
    expect(mocks.login).not.toHaveBeenCalled();
  });

  it("returns the visitor to the page the guard bounced them from", async () => {
    mocks.query = { returnTo: "/secrets?env=prod&app=x" };
    mocks.login.mockResolvedValue({ name: "admin", kind: "admin" });

    render(<LoginPage />);
    submit("kms_admin_token");

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/secrets?env=prod&app=x"));
    expect(mocks.toast.success).toHaveBeenCalledWith("Signed in", "Welcome, admin");
  });

  it("ignores a returnTo that would leave the origin", async () => {
    // The parameter round-trips through a URL the visitor can edit, so an
    // off-origin value is dropped rather than followed.
    mocks.query = { returnTo: "//evil.com" };
    mocks.login.mockResolvedValue({ name: "admin", kind: "admin" });

    render(<LoginPage />);
    submit("kms_admin_token");

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/"));
  });

  it("reports a rejected token as a sign-in failure", async () => {
    const error = new ApiError("invalid_credentials", "token not recognised", 401);
    mocks.login.mockRejectedValue(error);

    render(<LoginPage />);
    submit("bad-token");

    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledWith(error, "Sign-in failed"));
    expect(mocks.replace).not.toHaveBeenCalled();
  });

  it("skips the form when a session is already stored", async () => {
    mocks.token = "already-signed-in";
    mocks.query = { returnTo: "/audit" };

    render(<LoginPage />);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/audit"));
  });
});
