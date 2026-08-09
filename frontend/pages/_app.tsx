import type { AppProps } from "next/app";
import localFont from "next/font/local";
import Head from "next/head";
import { useRouter } from "next/router";
import { type ReactNode, useEffect } from "react";
import AppShell from "@/components/AppShell";
import { Loading } from "@/components/ui";
import { AuthProvider, useAuth } from "@/context/AuthContext";
import { ToastProvider } from "@/context/ToastContext";
import { getToken } from "@/lib/api";
import "@/styles/globals.css";

// Self-hosted so the static export stays hermetic: no build-time fetch from
// Google Fonts and no runtime request to a third-party origin (which the
// server's `default-src 'self'` CSP would block anyway). Latin subset only —
// the system stack in --sans covers anything outside it.
const inter = localFont({
  src: "../fonts/Inter-Variable-latin.woff2",
  weight: "400 700",
  style: "normal",
  display: "swap",
  variable: "--font-inter",
  fallback: [
    "-apple-system",
    "BlinkMacSystemFont",
    "Segoe UI",
    "Roboto",
    "Helvetica",
    "Arial",
    "sans-serif",
  ],
});

// Routes that render without the authenticated shell. /404 is included so an
// unknown URL shows the not-found page itself rather than bouncing through the
// session check to the login screen.
const PUBLIC_ROUTES = new Set<string>(["/login", "/404"]);

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
        {/* Titles the pre-auth moment (and therefore the prerendered HTML,
            which always exports in this state). It unmounts once the page
            renders, so the page's own title is free to take over — next/head
            keeps the first title currently mounted. */}
        <Head>
          <title>KMS Console</title>
        </Head>
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
    // `display: contents` puts --font-inter in scope for the whole tree
    // without introducing a box that could affect layout.
    <div className={`${inter.variable} ${inter.className}`} style={{ display: "contents" }}>
      <Head>
        {/* No <title> here on purpose: next/head keeps the *first* title it
            collects, and _app renders above the page, so a fallback here would
            always beat the per-page one. Every page sets its own through
            PageHeader/PageTitle. */}
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
    </div>
  );
}
