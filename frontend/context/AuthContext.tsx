import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
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
import { rememberNamespace } from "@/lib/namespace-memory";
import type { Identity } from "@/lib/types";
import { confirmDiscardWork, discardUnsavedWork, hasUnsavedWork } from "@/lib/unsaved-work";

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
  /** Login succeeded, but the identity scope has not yet been verified. */
  verificationPending: boolean;
  verifying: boolean;
  retryVerification: () => void;
  login: (token: string, expectedIdentity?: Identity) => Promise<Identity>;
  logout: () => void;
}

interface Session {
  authenticated: boolean;
  signedOut: boolean;
}

const AuthContext = createContext<AuthState | null>(null);

function sameIdentityScope(a: Identity, b: Identity): boolean {
  return (
    a.name === b.name &&
    a.kind === b.kind &&
    (a.namespace?.env ?? "") === (b.namespace?.env ?? "") &&
    (a.namespace?.app ?? "") === (b.namespace?.app ?? "")
  );
}

export class SessionVerificationError extends Error {
  constructor() {
    super("Signed in, but the console could not load this identity's access scope.");
    this.name = "SessionVerificationError";
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const toast = useToast();
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [ready, setReady] = useState(false);
  const [session, setSession] = useState<Session>({ authenticated: false, signedOut: false });
  // Bumped by the "Retry" action on the could-not-verify toast, which re-runs
  // the restore effect below.
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);
  const pendingLoginVerification = useRef(false);
  const [verificationPending, setVerificationPending] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const identityRef = useRef<Identity | null>(null);
  identityRef.current = identity;
  const recoveryIdentity = useRef<Identity | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` is never read inside the effect; it exists purely so that bumping it re-runs the session check, which is what the toast's Retry action does.
  useEffect(() => {
    const token = getToken();
    const requestGeneration = ++generation.current;
    const controller = new AbortController();
    let cancelled = false;

    async function restoreSession() {
      if (!token) {
        setSession({ authenticated: false, signedOut: false });
        setReady(true);
        return;
      }

      setVerifying(true);
      try {
        const current = await api.whoami({ signal: controller.signal });
        if (cancelled || generation.current !== requestGeneration || getToken() !== token) return;
        const verifiedIdentity: Identity = {
          name: current.name,
          kind: current.kind,
          namespace: current.namespace,
          auth_method: current.auth_method,
        };
        if (
          recoveryIdentity.current &&
          !sameIdentityScope(verifiedIdentity, recoveryIdentity.current)
        ) {
          clearToken();
          setIdentity(recoveryIdentity.current);
          setSession({ authenticated: false, signedOut: false });
          pendingLoginVerification.current = false;
          setVerificationPending(false);
          toast.error(
            new Error("Access scope changed. Discard the draft and sign in again."),
            "Could not resume draft",
          );
          return;
        }
        storeIdentity(verifiedIdentity);
        setIdentity(verifiedIdentity);
        setSession({ authenticated: true, signedOut: false });
        pendingLoginVerification.current = false;
        setVerificationPending(false);
        recoveryIdentity.current = null;
      } catch (err) {
        if (
          cancelled ||
          isAbortError(err) ||
          generation.current !== requestGeneration ||
          getToken() !== token
        )
          return;
        const rejected = err instanceof ApiError && (err.status === 401 || err.status === 403);
        if (rejected) {
          // The server actively refused this token: it is not a session any
          // more, so drop it along with the cached identity.
          clearToken();
          rememberNamespace(null);
          setIdentity(recoveryIdentity.current);
          setSession({ authenticated: false, signedOut: false });
          pendingLoginVerification.current = false;
          setVerificationPending(false);
          return;
        }
        // A network blip or a timeout says nothing about the token. Dropping it
        // here logged people out silently every time the API hiccuped.
        const cachedIdentity = pendingLoginVerification.current ? null : loadIdentity();
        if (cachedIdentity) {
          setIdentity(cachedIdentity);
        } else {
          pendingLoginVerification.current = true;
          setVerificationPending(true);
          setIdentity(recoveryIdentity.current);
        }
        setSession({ authenticated: true, signedOut: false });
        toast.error(err, "Could not verify your session", {
          id: "session-check",
          action: { label: "Retry", onClick: () => setAttempt((n) => n + 1) },
        });
      } finally {
        if (!cancelled && generation.current === requestGeneration) {
          setReady(!pendingLoginVerification.current);
          setVerifying(false);
        }
      }
    }

    void restoreSession();

    // apiFetch has already cleared the token by the time this fires; all that
    // is left is to report the session as gone. Redirecting is `Protected`'s job.
    const onUnauthorized = () => {
      generation.current += 1;
      pendingLoginVerification.current = false;
      setVerificationPending(false);
      setVerifying(false);
      rememberNamespace(null);
      // Keep only the already verified display identity while an in-memory
      // draft is being recovered. Credentials have already been cleared.
      recoveryIdentity.current = hasUnsavedWork() ? identityRef.current : null;
      setIdentity(recoveryIdentity.current);
      setSession({ authenticated: false, signedOut: false });
      setReady(true);
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => {
      cancelled = true;
      controller.abort();
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    };
  }, [toast, attempt]);

  const login = useCallback(
    async (token: string, expectedIdentity?: Identity): Promise<Identity> => {
      const loginGeneration = ++generation.current;
      // The form owns this request's busy state. A superseded restore cannot
      // clear its own flag in finally once this generation takes over.
      setVerifying(false);
      // Validate before persisting: login sends the token in the body, not the header.
      let res: Awaited<ReturnType<typeof api.login>>;
      try {
        res = await api.login(token);
      } catch (error) {
        if (generation.current === loginGeneration) {
          // This attempt invalidated an in-flight restore. Resume checking the
          // previous stored session, or settle as signed out if there was none.
          if (getToken()) setAttempt((n) => n + 1);
          else setReady(true);
        }
        throw error;
      }
      if (generation.current !== loginGeneration)
        throw new DOMException("Superseded", "AbortError");
      if (
        expectedIdentity &&
        (res.identity.name !== expectedIdentity.name || res.identity.kind !== expectedIdentity.kind)
      ) {
        throw new Error(
          `Sign in as ${expectedIdentity.name} to resume this draft, or discard it to change identities.`,
        );
      }
      recoveryIdentity.current = expectedIdentity ?? null;
      // Namespace memory belongs to the previous identity until the new session
      // establishes its own scope.
      rememberNamespace(null);
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
        if (generation.current !== loginGeneration || getToken() !== token) {
          throw new DOMException("Superseded", "AbortError");
        }
        id = { name: me.name, kind: me.kind, namespace: me.namespace, auth_method: me.auth_method };
      } catch (err) {
        if (isAbortError(err)) throw err;
        if (!getToken()) throw err; // whoami 401'd and cleared it: not a real session
        if (generation.current !== loginGeneration || getToken() !== token) {
          throw new DOMException("Superseded", "AbortError");
        }
        pendingLoginVerification.current = true;
        setVerificationPending(true);
        setIdentity(expectedIdentity ?? id);
        setSession({ authenticated: true, signedOut: false });
        setReady(false);
        toast.error(err, "Could not load access scope", {
          id: "session-check",
          action: { label: "Retry", onClick: () => setAttempt((n) => n + 1) },
        });
        throw new SessionVerificationError();
      }
      pendingLoginVerification.current = false;
      setVerificationPending(false);
      if (expectedIdentity && !sameIdentityScope(id, expectedIdentity)) {
        clearToken();
        setIdentity(expectedIdentity);
        setSession({ authenticated: false, signedOut: false });
        setReady(true);
        throw new Error(
          "This identity's access scope changed. Discard the draft and sign in again.",
        );
      }
      storeIdentity(id);
      recoveryIdentity.current = null;
      setIdentity(id);
      setSession({ authenticated: true, signedOut: false });
      setReady(true);
      return id;
    },
    [toast],
  );

  const logout = useCallback(() => {
    if (!confirmDiscardWork()) return;
    discardUnsavedWork();
    recoveryIdentity.current = null;
    generation.current += 1;
    pendingLoginVerification.current = false;
    setVerificationPending(false);
    setVerifying(false);
    clearToken();
    rememberNamespace(null);
    setIdentity(null);
    setSession({ authenticated: false, signedOut: true });
    setReady(true);
  }, []);

  const value = useMemo<AuthState>(
    () => ({
      identity,
      ready,
      authenticated: session.authenticated,
      signedOut: session.signedOut,
      verificationPending,
      verifying,
      retryVerification: () => setAttempt((n) => n + 1),
      login,
      logout,
    }),
    [identity, ready, session, verificationPending, verifying, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
