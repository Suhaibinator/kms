import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { AppProps } from "next/app";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "@/context/AuthContext";
import App from "@/pages/_app";

const mocks = vi.hoisted(() => ({
  whoami: vi.fn(),
  login: vi.fn(),
  token: null as string | null,
  replace: vi.fn(async () => true),
  clearToken: vi.fn(),
  storeIdentity: vi.fn(),
  toast: {
    error: vi.fn(),
    success: vi.fn(),
    info: vi.fn(),
    push: vi.fn(),
    dismiss: vi.fn(),
  },
}));

vi.mock("next/router", () => ({
  useRouter: () => ({
    replace: mocks.replace,
    query: {},
    isReady: true,
    pathname: "/",
    asPath: "/",
  }),
}));
// next/font/local is a build-time transform; it throws when called for real.
vi.mock("next/font/local", () => ({
  default: () => ({ variable: "font-inter-var", className: "font-inter", style: {} }),
}));
// The shell is irrelevant here and drags in the whole navigation tree.
vi.mock("@/components/AppShell", () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock("@/context/ToastContext", () => ({
  useToast: () => mocks.toast,
  ToastProvider: ({ children }: { children: React.ReactNode }) => children,
}));
// Partial mock: the provider does `err instanceof ApiError`, so ApiError and
// isAbortError have to stay the real ones.
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: { ...actual.api, whoami: mocks.whoami, login: mocks.login },
    getToken: () => mocks.token,
    setToken: (value: string) => {
      mocks.token = value;
    },
    clearToken: mocks.clearToken,
    storeIdentity: mocks.storeIdentity,
    loadIdentity: () => ({ name: "cached", kind: "admin" }),
  };
});

const { ApiError, UNAUTHORIZED_EVENT } = await import("@/lib/api");

type Auth = ReturnType<typeof useAuth>;

let latest: Auth | null = null;

function Probe() {
  const auth = useAuth();
  latest = auth;
  return (
    <div>
      <span data-testid="ready">{String(auth.ready)}</span>
      <span data-testid="authenticated">{String(auth.authenticated)}</span>
      <span data-testid="identity">{auth.identity?.name ?? "-"}</span>
      <button type="button" onClick={auth.logout}>
        Sign out
      </button>
    </div>
  );
}

function renderProvider() {
  return render(
    <AuthProvider>
      <Probe />
    </AuthProvider>,
  );
}

/** Renders the real _app tree, which is where the login redirect lives. */
function renderApp() {
  return render(<App {...({ Component: Probe, pageProps: {} } as unknown as AppProps)} />);
}

/** The Retry handler AuthContext attached to its could-not-verify toast. */
function retryAction(): () => void {
  const call = mocks.toast.error.mock.calls.at(-1) as
    | [unknown, string, { action: { onClick: () => void } }]
    | undefined;
  if (!call) throw new Error("no toast.error call to read a Retry action from");
  return call[2].action.onClick;
}

describe("AuthProvider session restore", () => {
  beforeEach(() => {
    mocks.token = null;
    mocks.whoami.mockReset();
    mocks.login.mockReset();
    mocks.replace.mockClear();
    mocks.clearToken.mockClear();
    mocks.clearToken.mockImplementation(() => {
      mocks.token = null;
    });
    mocks.storeIdentity.mockClear();
    for (const fn of Object.values(mocks.toast)) fn.mockClear();
    latest = null;
    window.history.replaceState(null, "", "/");
  });

  it("keeps the session when the network, not the server, is the problem", async () => {
    // Dropping the token on a timeout logged people out every time the API
    // hiccuped, with no explanation.
    mocks.token = "stored-token";
    mocks.whoami.mockRejectedValue(new ApiError("unavailable", "offline", 0));

    renderProvider();

    await waitFor(() => expect(screen.getByTestId("ready")).toHaveTextContent("true"));
    expect(mocks.clearToken).not.toHaveBeenCalled();
    expect(mocks.token).toBe("stored-token");
    expect(screen.getByTestId("authenticated")).toHaveTextContent("true");
    expect(screen.getByTestId("identity")).toHaveTextContent("cached");
    expect(mocks.toast.error).toHaveBeenCalledTimes(1);
    expect(mocks.toast.error.mock.calls[0][1]).toBe("Could not verify your session");
    expect(mocks.replace).not.toHaveBeenCalled();
  });

  it.each([401, 403])("drops a token the server refuses with %i", async (status) => {
    mocks.token = "stored-token";
    mocks.whoami.mockRejectedValue(new ApiError("unauthenticated", "no", status));

    renderProvider();

    await waitFor(() => expect(screen.getByTestId("ready")).toHaveTextContent("true"));
    expect(mocks.clearToken).toHaveBeenCalled();
    expect(screen.getByTestId("identity")).toHaveTextContent("-");
    expect(screen.getByTestId("authenticated")).toHaveTextContent("false");
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("treats its own unmount abort as a non-event", async () => {
    mocks.token = "stored-token";
    mocks.whoami.mockRejectedValue(new DOMException("Aborted", "AbortError"));

    renderProvider();

    await waitFor(() => expect(mocks.whoami).toHaveBeenCalled());
    expect(mocks.clearToken).not.toHaveBeenCalled();
    expect(mocks.toast.error).not.toHaveBeenCalled();
  });

  it("re-checks the session when the toast's Retry is pressed", async () => {
    mocks.token = "stored-token";
    mocks.whoami.mockRejectedValueOnce(new ApiError("unavailable", "offline", 0));

    renderProvider();
    await waitFor(() => expect(mocks.toast.error).toHaveBeenCalledTimes(1));

    mocks.whoami.mockResolvedValue({
      name: "admin",
      kind: "admin",
      namespace: { env: "prod", app: "billing" },
    });
    const retry = retryAction();
    act(() => retry());

    await waitFor(() => expect(screen.getByTestId("identity")).toHaveTextContent("admin"));
    expect(mocks.toast.error).toHaveBeenCalledTimes(1);
    expect(mocks.clearToken).not.toHaveBeenCalled();
  });

  it("fills in the bound namespace that /auth/login leaves out", async () => {
    mocks.login.mockResolvedValue({ identity: { name: "a", kind: "admin" } });
    mocks.whoami.mockResolvedValue({
      name: "a",
      kind: "admin",
      namespace: { env: "prod", app: "billing" },
    });

    renderProvider();
    await waitFor(() => expect(screen.getByTestId("ready")).toHaveTextContent("true"));

    let identity: Awaited<ReturnType<Auth["login"]>> | undefined;
    await act(async () => {
      identity = await latest?.login("tok");
    });

    expect(identity).toMatchObject({ name: "a", namespace: { env: "prod", app: "billing" } });
    expect(screen.getByTestId("authenticated")).toHaveTextContent("true");
  });

  it("still signs in when the follow-up whoami fails for a non-auth reason", async () => {
    mocks.login.mockResolvedValue({ identity: { name: "a", kind: "admin" } });
    mocks.whoami.mockRejectedValue(new ApiError("unavailable", "offline", 0));

    renderProvider();
    await waitFor(() => expect(screen.getByTestId("ready")).toHaveTextContent("true"));

    let identity: Awaited<ReturnType<Auth["login"]>> | undefined;
    await act(async () => {
      identity = await latest?.login("tok");
    });

    // The token survived, so the partial identity is a real session; the next
    // page load fills in the namespace.
    expect(identity).toMatchObject({ name: "a", kind: "admin" });
    expect(identity).not.toHaveProperty("namespace.env");
    expect(mocks.storeIdentity).toHaveBeenCalledWith({ name: "a", kind: "admin" });
    expect(screen.getByTestId("identity")).toHaveTextContent("a");
  });

  it("keeps the login response's auth method when whoami cannot confirm it", async () => {
    // An admin signing in with a client certificate plus a token: /auth/login
    // already reported how it was accepted, so a failed whoami does not lose it.
    mocks.login.mockResolvedValue({ identity: { name: "a", kind: "admin" }, auth_method: "mtls" });
    mocks.whoami.mockRejectedValue(new ApiError("unavailable", "offline", 0));

    renderProvider();
    await waitFor(() => expect(screen.getByTestId("ready")).toHaveTextContent("true"));

    let identity: Awaited<ReturnType<Auth["login"]>> | undefined;
    await act(async () => {
      identity = await latest?.login("tok");
    });

    expect(identity).toMatchObject({ name: "a", kind: "admin", auth_method: "mtls" });
  });
});

describe("Protected redirects", () => {
  beforeEach(() => {
    mocks.token = null;
    mocks.whoami.mockReset();
    mocks.login.mockReset();
    mocks.replace.mockClear();
    mocks.clearToken.mockClear();
    mocks.clearToken.mockImplementation(() => {
      mocks.token = null;
    });
    for (const fn of Object.values(mocks.toast)) fn.mockClear();
    window.history.replaceState(null, "", "/");
  });

  it("sends an unauthenticated visitor to login with somewhere to come back to", async () => {
    window.history.replaceState(null, "", "/secrets?env=prod");

    renderApp();

    await waitFor(() =>
      expect(mocks.replace).toHaveBeenCalledWith("/login?returnTo=%2Fsecrets%3Fenv%3Dprod"),
    );
  });

  it("redirects exactly once when a token dies mid-session", async () => {
    // Every in-flight request raises the event; only one navigation may result.
    mocks.token = "stored-token";
    mocks.whoami.mockResolvedValue({ name: "admin", kind: "admin" });

    renderApp();
    await waitFor(() => expect(screen.getByTestId("authenticated")).toHaveTextContent("true"));

    act(() => {
      mocks.token = null;
      window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
      window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    });

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledTimes(1));
    expect(mocks.replace).toHaveBeenCalledWith("/login");
  });

  it("skips the returnTo round-trip after a deliberate sign-out", async () => {
    window.history.replaceState(null, "", "/secrets?env=prod");
    mocks.token = "stored-token";
    mocks.whoami.mockResolvedValue({ name: "admin", kind: "admin" });

    renderApp();
    await waitFor(() => expect(screen.getByTestId("authenticated")).toHaveTextContent("true"));

    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith("/login"));
    expect(mocks.replace).toHaveBeenCalledTimes(1);
  });
});
