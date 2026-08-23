import type { AppProps } from "next/app";
import localFont from "next/font/local";
import Head from "next/head";
import { useRouter } from "next/router";
import { type ReactNode, useEffect } from "react";
import AppShell from "@/components/AppShell";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { Loading } from "@/components/ui";
import { AuthProvider, useAuth } from "@/context/AuthContext";
import { ToastProvider } from "@/context/ToastContext";
import { currentPath, loginHref } from "@/lib/returnTo";
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
  const { ready, authenticated, signedOut } = useAuth();
  const router = useRouter();

  // The only place in the app that sends an unauthenticated visitor to /login.
  // AuthContext reports session state; it does not navigate. A sign-out skips
  // the returnTo round-trip — the user chose to leave.
  useEffect(() => {
    if (!ready || authenticated) return;
    void router.replace(signedOut ? "/login" : loginHref(currentPath()));
  }, [ready, authenticated, signedOut, router]);

  // `authenticated` and `ready` both start false, so the prerendered HTML is
  // still this branch — byte-identical to what the export produced before.
  if (!ready || !authenticated) {
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
    <div className={`${inter.variable} ${inter.className}`} style={{ display: "contents" }}>
      <Head>
        {/* next/font scopes its CSS variable to the class above. Expose the
            generated family at the document root as well so third-party and
            Base UI portals mounted under body inherit the exact same font. */}
        <style>{`:root { --font-inter: ${inter.style.fontFamily}; }`}</style>
        {/* No <title> here on purpose: next/head keeps the *first* title it
            collects, and _app renders above the page, so a fallback here would
            always beat the per-page one. Every page sets its own through
            PageHeader/PageTitle. */}
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="robots" content="noindex, nofollow" />
      </Head>
      <ToastProvider>
        <AuthProvider>
          {/* Tied to the route so navigating away from a crashed page clears
              the error instead of stranding the visitor on the fallback card. */}
          <ErrorBoundary resetKey={router.asPath}>
            {isPublic ? (
              <Component {...pageProps} />
            ) : (
              <Protected>
                <Component {...pageProps} />
              </Protected>
            )}
          </ErrorBoundary>
        </AuthProvider>
      </ToastProvider>
    </div>
  );
}
