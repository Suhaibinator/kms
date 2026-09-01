import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { useToast } from "@/context/ToastContext";
import {
  ApiError,
  api,
  clearToken,
  getToken,
  isAbortError,
  loadIdentity,
  setToken,
  storeIdentity,
  UNAUTHORIZED_EVENT,
} from "@/lib/api";
import type { Identity } from "@/lib/types";

interface AuthState {
  identity: Identity | null;
  // ready flips true once a stored session has been revalidated, so guards do
  // not redirect during hydration or while whoami is still pending.
  ready: boolean;
  /** A token is believed valid. The guard, not this provider, acts on it. */
  authenticated: boolean;
  /**
   * The session ended because the user pressed Sign out, so the guard sends
   * them to a bare /login rather than round-tripping back to where they were.
   */
  signedOut: boolean;
  login: (token: string) => Promise<Identity>;
  logout: () => void;
}

interface Session {
  authenticated: boolean;
  signedOut: boolean;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const toast = useToast();
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [ready, setReady] = useState(false);
  const [session, setSession] = useState<Session>({ authenticated: false, signedOut: false });
  // Bumped by the "Retry" action on the could-not-verify toast, which re-runs
  // the restore effect below.
  const [attempt, setAttempt] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` is never read inside the effect; it exists purely so that bumping it re-runs the session check, which is what the toast's Retry action does.
  useEffect(() => {
    const token = getToken();
    const controller = new AbortController();
    let cancelled = false;

    async function restoreSession() {
      if (!token) {
        setSession({ authenticated: false, signedOut: false });
        setReady(true);
        return;
      }

      try {
        const current = await api.whoami({ signal: controller.signal });
        if (cancelled) return;
        const verifiedIdentity: Identity = {
          name: current.name,
          kind: current.kind,
          namespace: current.namespace,
          auth_method: current.auth_method,
        };
        storeIdentity(verifiedIdentity);
        setIdentity(verifiedIdentity);
        setSession({ authenticated: true, signedOut: false });
      } catch (err) {
        if (cancelled || isAbortError(err)) return;
        const rejected = err instanceof ApiError && (err.status === 401 || err.status === 403);
        if (rejected) {
          // The server actively refused this token: it is not a session any
          // more, so drop it along with the cached identity.
          clearToken();
          setIdentity(null);
          setSession({ authenticated: false, signedOut: false });
          return;
        }
        // A network blip or a timeout says nothing about the token. Dropping it
        // here logged people out silently every time the API hiccuped.
        setIdentity(loadIdentity());
        setSession({ authenticated: true, signedOut: false });
        toast.error(err, "Could not verify your session", {
          id: "session-check",
          action: { label: "Retry", onClick: () => setAttempt((n) => n + 1) },
        });
      } finally {
        if (!cancelled) setReady(true);
      }
    }

    void restoreSession();

    // apiFetch has already cleared the token by the time this fires; all that
    // is left is to report the session as gone. Redirecting is `Protected`'s job.
    const onUnauthorized = () => {
      setIdentity(null);
      setSession({ authenticated: false, signedOut: false });
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => {
      cancelled = true;
      controller.abort();
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    };
  }, [toast, attempt]);

  const login = useCallback(async (token: string): Promise<Identity> => {
    // Validate before persisting: login sends the token in the body, not the header.
    const res = await api.login(token);
    setToken(token);
    // Seed from the login response so the identity still records how it
    // authenticated (token, or cert + token) if the whoami below fails.
    let id: Identity = {
      name: res.identity.name,
      kind: res.identity.kind,
      auth_method: res.auth_method,
    };
    try {
      // /auth/login omits the bound namespace, which the shell needs to scope
      // navigation. A failure here is not fatal — the next load fills it in.
      const me = await api.whoami();
      id = { name: me.name, kind: me.kind, namespace: me.namespace, auth_method: me.auth_method };
    } catch (err) {
      if (!getToken()) throw err; // whoami 401'd and cleared it: not a real session
    }
    storeIdentity(id);
    setIdentity(id);
    setSession({ authenticated: true, signedOut: false });
    return id;
  }, []);

  const logout = useCallback(() => {
    clearToken();
    setIdentity(null);
    setSession({ authenticated: false, signedOut: true });
  }, []);

  const value = useMemo<AuthState>(
    () => ({
      identity,
      ready,
      authenticated: session.authenticated,
      signedOut: session.signedOut,
      login,
      logout,
    }),
    [identity, ready, session, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
