import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { HealthResponse } from "@/lib/types";
import LoginPage from "@/pages/login";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  replace: vi.fn(async () => true),
  login: vi.fn(),
  health: vi.fn(),
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
// Partial mock: `ApiError` must stay the real class so `instanceof` in the page
// still recognises the errors these tests throw.
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    getToken: () => mocks.token,
    api: { ...actual.api, health: mocks.health },
  };
});

const { ApiError } = await import("@/lib/api");

/** A server that does not ask admins for a client certificate. */
function health(overrides: Partial<HealthResponse> = {}): HealthResponse {
  return {
    healthy: true,
    ready: true,
    version: "test",
    current_revision: 0,
    grpc_addr: "127.0.0.1:8443",
    tls_enabled: true,
    admin_client_cert_required: false,
    client_cert_presented: false,
    ...overrides,
  };
}

const CERT_NOTICE = /Admin sign-in needs a client certificate/;

describe("LoginPage", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.token = null;
    mocks.replace.mockClear();
    mocks.login.mockReset();
    mocks.health.mockReset();
    mocks.health.mockResolvedValue(health());
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

  it("explains a starting or sealed server on 503 instead of blaming the token", async () => {
    const error = new ApiError("unavailable", "store sealed", 503);
    mocks.login.mockRejectedValue(error);

    render(<LoginPage />);
    submit("kms_admin_token");

    await waitFor(() =>
      expect(mocks.toast.error).toHaveBeenCalledWith(expect.any(Error), "Server unavailable"),
    );
    const reported = mocks.toast.error.mock.calls[0]?.[0] as Error;
    expect(reported.message).toBe("The server is starting or sealed. Try again in a moment.");
    expect(screen.getByRole("alert")).toHaveTextContent("The server is starting or sealed.");
    expect(mocks.replace).not.toHaveBeenCalled();
  });

  it("marks an unrecognised token inline on 401 and clears it on edit", async () => {
    const error = new ApiError("invalid_credentials", "token not recognised", 401);
    mocks.login.mockRejectedValue(error);

    render(<LoginPage />);
    submit("bad-token");

    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledWith(error, "Sign-in failed"));
    expect(screen.getByRole("alert")).toHaveTextContent("That token was not recognised.");
    expect(screen.getByLabelText("Identity token")).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(screen.getByLabelText("Identity token"), { target: { value: "x" } });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("warns that admin sign-in needs a client certificate when required and none was presented", async () => {
    mocks.health.mockResolvedValue(
      health({ admin_client_cert_required: true, client_cert_presented: false }),
    );

    render(<LoginPage />);

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(CERT_NOTICE);
    expect(notice).toHaveTextContent("parameter-store admin-cert issue NAME --out DIR");
    expect(notice).toHaveTextContent(/Client identity tokens still sign in without a certificate/);
    // The requirement only affects admins, so the form stays live for everyone else.
    expect(screen.getByLabelText("Identity token")).toBeEnabled();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
    // Nothing has failed yet: a notice must not announce itself as an error.
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("stays quiet when the browser presented a certificate", async () => {
    mocks.health.mockResolvedValue(
      health({ admin_client_cert_required: true, client_cert_presented: true }),
    );

    render(<LoginPage />);

    await waitFor(() => expect(mocks.health).toHaveBeenCalled());
    expect(screen.queryByText(CERT_NOTICE)).toBeNull();
  });

  it("stays quiet when the requirement is off", async () => {
    mocks.health.mockResolvedValue(
      health({ admin_client_cert_required: false, client_cert_presented: false }),
    );

    render(<LoginPage />);

    await waitFor(() => expect(mocks.health).toHaveBeenCalled());
    expect(screen.queryByText(CERT_NOTICE)).toBeNull();
  });

  it("keeps the form usable when health cannot be loaded", async () => {
    // Health is advisory. A sealed, starting or proxied server that cannot
    // answer it must not cost the visitor the sign-in form.
    mocks.health.mockRejectedValue(new Error("connection refused"));
    mocks.login.mockResolvedValue({ name: "admin", kind: "admin" });

    render(<LoginPage />);

    await waitFor(() => expect(mocks.health).toHaveBeenCalled());
    expect(screen.queryByText(CERT_NOTICE)).toBeNull();
    expect(screen.getByLabelText("Identity token")).toBeEnabled();
    expect(mocks.toast.error).not.toHaveBeenCalled();

    submit("kms_admin_token");
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/"));
  });

  it("mentions the certificate in the 401 message when one is required but missing", async () => {
    mocks.health.mockResolvedValue(
      health({ admin_client_cert_required: true, client_cert_presented: false }),
    );
    mocks.login.mockRejectedValue(new ApiError("invalid_credentials", "nope", 401));

    render(<LoginPage />);
    await screen.findByRole("status");
    submit("kms_admin_token");

    // The server answers every bad credential identically, so the copy offers
    // the certificate as a possibility rather than a diagnosis.
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "That token was not recognised — or it belongs to an administrator and this browser presented no client certificate.",
    );
  });

  it("skips the form when a session is already stored", async () => {
    mocks.token = "already-signed-in";
    mocks.query = { returnTo: "/audit" };

    render(<LoginPage />);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/audit"));
  });
});
