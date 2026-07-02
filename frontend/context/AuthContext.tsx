import { useRouter } from "next/router";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  api,
  clearToken,
  getToken,
  loadIdentity,
  setToken,
  storeIdentity,
  UNAUTHORIZED_EVENT,
} from "@/lib/api";
import type { Identity } from "@/lib/types";

interface AuthState {
  identity: Identity | null;
  // ready flips true once we have read sessionStorage on the client, so guards
  // do not redirect during the first render before hydration completes.
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
    if (token) {
      setIdentity(loadIdentity());
    }
    setReady(true);

    const onUnauthorized = () => {
      setIdentity(null);
      if (window.location.pathname !== "/login") {
        void router.replace("/login");
      }
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    // router identity is stable enough; we only want this wired once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
