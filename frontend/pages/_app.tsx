import type { AppProps } from "next/app";
import { useRouter } from "next/router";
import { useEffect, type ReactNode } from "react";
import Head from "next/head";
import { AuthProvider, useAuth } from "@/context/AuthContext";
import { ToastProvider } from "@/context/ToastContext";
import AppShell from "@/components/AppShell";
import { Loading } from "@/components/ui";
import { getToken } from "@/lib/api";
import "@/styles/globals.css";

// Routes that render without the authenticated shell.
const PUBLIC_ROUTES = new Set<string>(["/login"]);

function Protected({ children }: { children: ReactNode }) {
  const { ready } = useAuth();
  const router = useRouter();
  const hasToken = ready ? !!getToken() : true;

  useEffect(() => {
    if (ready && !getToken()) {
      void router.replace("/login");
    }
  }, [ready, router]);

  if (!ready || !hasToken) {
    return (
      <div className="auth-wrap">
        <Loading label="Checking session…" />
      </div>
    );
  }
  return <AppShell>{children}</AppShell>;
}

export default function App({ Component, pageProps }: AppProps) {
  const router = useRouter();
  const isPublic = PUBLIC_ROUTES.has(router.pathname);

  return (
    <>
      <Head>
        <title>KMS Console</title>
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="robots" content="noindex, nofollow" />
      </Head>
      <ToastProvider>
        <AuthProvider>
          {isPublic ? (
            <Component {...pageProps} />
          ) : (
            <Protected>
              <Component {...pageProps} />
            </Protected>
          )}
        </AuthProvider>
      </ToastProvider>
    </>
  );
}
