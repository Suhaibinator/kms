import { useRouter } from "next/router";
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { api, clearToken, getToken, setToken, storeIdentity, UNAUTHORIZED_EVENT } from "@/lib/api";
import type { Identity } from "@/lib/types";

interface AuthState {
  identity: Identity | null;
  // ready flips true once a stored session has been revalidated, so guards do
  // not redirect during hydration or while whoami is still pending.
  ready: boolean;
  login: (token: string) => Promise<Identity>;
  logout: () => void;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const router = useRouter();
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    const token = getToken();
    const controller = new AbortController();
    let cancelled = false;

    async function restoreSession() {
      if (!token) {
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
        };
        storeIdentity(verifiedIdentity);
        setIdentity(verifiedIdentity);
      } catch {
        if (cancelled) return;
        // A restored session is not usable unless the server accepts it. This
        // also removes stale cached identity data alongside the token.
        clearToken();
        setIdentity(null);
      } finally {
        if (!cancelled) setReady(true);
      }
    }

    void restoreSession();

    const onUnauthorized = () => {
      setIdentity(null);
      if (window.location.pathname !== "/login") {
        void router.replace("/login");
      }
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => {
      cancelled = true;
      controller.abort();
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    };
  }, [router]);

  const login = useCallback(async (token: string): Promise<Identity> => {
    // Validate first (login sends the token in the body, not the header), and
    // only persist it once the server accepts it.
    const res = await api.login(token);
    const id: Identity = { name: res.identity.name, kind: res.identity.kind };
    setToken(token);
    storeIdentity(id);
    setIdentity(id);
    return id;
  }, []);

  const logout = useCallback(() => {
    clearToken();
    setIdentity(null);
    void router.replace("/login");
  }, [router]);

  const value = useMemo<AuthState>(
    () => ({ identity, ready, login, logout }),
    [identity, ready, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
