import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ConnectionResponse } from "@/lib/types";
import LoginPage from "@/pages/login";

const mocks = vi.hoisted(() => ({
  query: {} as Record<string, string>,
  replace: vi.fn(async () => true),
  login: vi.fn(),
  connection: vi.fn(),
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
    api: { ...actual.api, connection: mocks.connection },
  };
});

const { ApiError } = await import("@/lib/api");

function connection(overrides: Partial<ConnectionResponse> = {}): ConnectionResponse {
  return { tls_enabled: true, client_certificate: null, ...overrides };
}
const certificate = {
  identity_uri: "kms://identity/admin",
  fingerprint_sha256: "a".repeat(64),
  not_after: "2027-01-01T00:00:00Z",
};

describe("LoginPage", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.token = null;
    mocks.replace.mockClear();
    mocks.login.mockReset();
    mocks.connection.mockReset();
    mocks.connection.mockResolvedValue(connection());
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

  it("keeps the submit label and marks the button busy while signing in", async () => {
    let release!: (identity: { name: string; kind: string }) => void;
    mocks.login.mockReturnValue(
      new Promise((resolve) => {
        release = resolve;
      }),
    );

    render(<LoginPage />);
    submit("kms_admin_token");

    // The label no longer swaps to "Signing in…": the Button's own loading
    // state overlays the spinner and carries aria-busy and disabled, so the
    // box keeps its width. (Spinner contributes "Loading" to the name.)
    const button = screen.getByRole("button", { name: /Sign in$/ });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toHaveTextContent("Sign in");
    expect(screen.queryByText("Signing in…")).toBeNull();

    release({ name: "admin", kind: "admin" });
    await waitFor(() => expect(mocks.replace).toHaveBeenCalled());
  });

  it.each(["//evil.com", "/a/..//evil.example", "/%2e//evil.example"])(
    "ignores an unsafe returnTo %s",
    async (returnTo) => {
      // The parameter round-trips through a URL the visitor can edit, so an
      // off-origin value is dropped rather than followed.
      mocks.query = { returnTo };
      mocks.login.mockResolvedValue({ name: "admin", kind: "admin" });

      render(<LoginPage />);
      submit("kms_admin_token");

      await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/"));
    },
  );

  it("ignores a returnTo that smuggles a second origin behind a control character", async () => {
    // "/%09/evil.example" decodes to "/\t/evil.example"; the URL parser drops
    // the tab and a naive slash check would hand "//evil.example" to the router.
    mocks.query = { returnTo: "/\t/evil.example" };
    mocks.login.mockResolvedValue({ name: "admin", kind: "admin" });

    render(<LoginPage />);
    submit("kms_admin_token");

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/"));
    expect(mocks.replace).not.toHaveBeenCalledWith(expect.stringContaining("evil.example"));
  });

  it("reports a rejected token as a sign-in failure", async () => {
    const error = new ApiError("invalid_credentials", "token not recognised", 401);
    mocks.login.mockRejectedValue(error);

    render(<LoginPage />);
    submit("bad-token");

    await waitFor(() =>
      expect(mocks.toast.error).toHaveBeenCalledWith(expect.any(Error), "Sign-in failed"),
    );
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

    await waitFor(() =>
      expect(mocks.toast.error).toHaveBeenCalledWith(expect.any(Error), "Sign-in failed"),
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Sign-in failed. Check your credentials and try again.",
    );
    expect(screen.getByLabelText("Identity token")).toHaveAttribute("aria-invalid", "true");

    fireEvent.change(screen.getByLabelText("Identity token"), { target: { value: "x" } });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it.each([null, certificate])(
    "keeps credential errors generic with certificate %j",
    async (cert) => {
      mocks.connection.mockResolvedValue(connection({ client_certificate: cert }));
      mocks.login.mockRejectedValue(new ApiError("invalid_credentials", "internal reason", 401));
      render(<LoginPage />);
      submit("bad-token");
      expect(await screen.findByRole("alert")).toHaveTextContent(
        "Sign-in failed. Check your credentials and try again.",
      );
      expect(screen.queryByText(/internal reason|Admin sign-in needs/)).toBeNull();
    },
  );

  it("shows certificate details and copies the fingerprint", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mocks.connection.mockResolvedValue(connection({ client_certificate: certificate }));
    render(<LoginPage />);
    expect(await screen.findByText(certificate.identity_uri)).toBeVisible();
    const disclosure = screen.getByText("Certificate details").closest("details");
    expect(disclosure).not.toHaveAttribute("open");
    fireEvent.click(screen.getByText("Certificate details"));
    fireEvent.click(screen.getByRole("button", { name: "Copy fingerprint" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(certificate.fingerprint_sha256));
  });

  it.each([
    [connection(), "No client certificate received"],
    [connection({ tls_enabled: false }), "This request reached the server without TLS."],
    [
      connection({ client_certificate: { ...certificate, identity_uri: null } }),
      "No unambiguous KMS identity URI.",
    ],
  ])("describes transport facts", async (data, message) => {
    mocks.connection.mockResolvedValue(data);
    render(<LoginPage />);
    expect(await screen.findByText(String(message), { exact: false })).toBeVisible();
  });

  it("clears stale details on refresh and keeps sign-in usable after failure", async () => {
    mocks.connection
      .mockResolvedValueOnce(connection({ client_certificate: certificate }))
      .mockRejectedValueOnce(new Error("offline"));
    mocks.login.mockResolvedValue({ name: "admin", kind: "admin" });
    render(<LoginPage />);
    await screen.findByText(certificate.identity_uri);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(screen.queryByText(certificate.identity_uri)).toBeNull();
    expect(await screen.findByText("Could not check the client certificate.")).toBeVisible();
    expect(screen.queryByText("No client certificate received")).toBeNull();
    submit("token");
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/"));
  });

  it("skips the form when a session is already stored", async () => {
    mocks.token = "already-signed-in";
    mocks.query = { returnTo: "/audit" };

    render(<LoginPage />);

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/audit"));
  });
});
