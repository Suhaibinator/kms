import { useRouter } from "next/router";
import { useEffect, useState } from "react";
import { LogoMark } from "@/components/LogoMark";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { Field, Input, PageTitle, Spinner } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import { api, ApiError, getToken } from "@/lib/api";
import { useQueryParam } from "@/lib/hooks";
import { safeReturnTo } from "@/lib/returnTo";
import type { HealthResponse } from "@/lib/types";

export default function LoginPage() {
  const router = useRouter();
  const { login, ready } = useAuth();
  const toast = useToast();
  const [token, setTokenValue] = useState("");
  const [busy, setBusy] = useState(false);
  // Turns the required-token message on only once the user has actually tried
  // to submit, so an untouched form does not open with an error on it.
  const [submitted, setSubmitted] = useState(false);
  // Why the last attempt failed, shown under the field; cleared on edit.
  const [authError, setAuthError] = useState<string | null>(null);
  // Unauthenticated server posture, used only to explain the client
  // certificate. Null until it loads, and null forever if it cannot: health is
  // advisory here, never a precondition for using the form.
  const [health, setHealth] = useState<HealthResponse | null>(null);
  // Where the guard wanted the visitor to go before it bounced them here.
  // Re-validated on read: the value survived a page load inside an editable URL.
  const { value: returnTo } = useQueryParam("returnTo");
  const destination = safeReturnTo(returnTo) ?? "/";

  // If already authenticated, skip the login screen.
  useEffect(() => {
    if (ready && getToken()) {
      void router.replace(destination);
    }
  }, [ready, router, destination]);

  // `/health` is unauthenticated, so this reveals nothing about the token: it
  // reports what the *server* requires and what *this connection* presented.
  useEffect(() => {
    const controller = new AbortController();
    api
      .health({ signal: controller.signal })
      .then((value) => {
        if (!controller.signal.aborted) setHealth(value);
      })
      .catch(() => {
        // A server that is down, sealed, or behind a proxy that drops /health
        // must not take the sign-in form with it.
      });
    return () => controller.abort();
  }, []);

  // The server asks admins for a client certificate and this browser sent
  // none — so an admin token cannot possibly succeed here. Client identity
  // tokens are unaffected, which is why this is a notice and not a blocker.
  const certMissing = health?.admin_client_cert_required === true && !health.client_cert_presented;

  // A certificate was presented, but presenting one is not the same as it being
  // accepted for the identity behind the token: it may be revoked, replaced by
  // a newer enrolment, or issued for someone else entirely.
  const certPresented =
    health?.admin_client_cert_required === true && health.client_cert_presented === true;

  const missingToken = submitted && !token.trim();

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitted(true);
    setAuthError(null);
    const trimmed = token.trim();
    // An empty field is a form error, not an incident: it belongs under the
    // input, not in a toast the user has to dismiss.
    if (!trimmed) return;
    setBusy(true);
    try {
      const identity = await login(trimmed);
      // Clear the field so the token does not linger in the DOM.
      setTokenValue("");
      toast.success("Signed in", `Welcome, ${identity.name}`);
      await router.replace(destination);
    } catch (err) {
      const status = err instanceof ApiError ? err.status : 0;
      const code = err instanceof ApiError ? err.code : "";
      if (status === 503 || (status !== 401 && code === "unavailable" && status !== 0)) {
        // The server answers but is not serving yet: still starting, or the
        // store is sealed until the master key is supplied.
        setAuthError("The server is starting or sealed. Try again in a moment.");
        toast.error(
          new Error("The server is starting or sealed. Try again in a moment."),
          "Server unavailable",
        );
      } else if (status === 401 || code === "invalid_credentials") {
        // The server answers every bad credential the same way and never says
        // which one failed, so the certificate — absent or rejected — can only
        // ever be named as a possibility, never as a diagnosis.
        let message = "That token was not recognised. Check it was copied completely.";
        if (certMissing) {
          message =
            "That token was not recognised — or it belongs to an administrator and this browser presented no client certificate.";
        } else if (certPresented) {
          message =
            "That token was not recognised — or the client certificate this browser presented is not valid for that administrator (revoked, replaced, or issued for another identity).";
        }
        setAuthError(message);
        toast.error(err, "Sign-in failed");
      } else {
        toast.error(err, "Sign-in failed");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="auth-wrap">
      <PageTitle title="Sign in" />
      <div className="auth-card">
        <div className="auth-brand">
          <LogoMark />
          <div>
            <h1 className="auth-title">KMS Console</h1>
            <div className="faint text-sm">Parameter &amp; Secret Store</div>
          </div>
        </div>

        <p className="muted text-sm mb-4">
          Paste an admin or client identity token to sign in. Administrators may also need a client
          certificate. Tokens are stored only for this browser session.
        </p>

        {certMissing ? (
          // `status`, not `alert`: nothing has failed yet, and the form is
          // still usable for client identity tokens.
          <div className="warn-panel mb-4" role="status">
            <strong>Admin sign-in needs a client certificate.</strong> This server requires
            administrators to present a client certificate, and your browser did not present one on
            this connection. Ask an operator to run{" "}
            <span className="mono">parameter-store admin-cert issue NAME --out DIR</span>, import
            the certificate (PKCS#12) into your browser, then reload. Client identity tokens still
            sign in without a certificate.
          </div>
        ) : null}

        <form onSubmit={onSubmit}>
          <Field
            label="Identity token"
            error={missingToken ? "Enter a token to continue." : authError}
            hint={
              <>
                Minted by <span className="mono">parameter-store init --admin</span>,{" "}
                <span className="mono">create-admin</span>, or the identities API.
              </>
            }
          >
            <Input
              id="token"
              className="font-mono"
              type="password"
              autoComplete="off"
              autoFocus
              spellCheck={false}
              placeholder="paste token…"
              value={token}
              onChange={(e) => {
                setTokenValue(e.target.value);
                setAuthError(null);
              }}
            />
          </Field>

          <Button type="submit" className="w-full" disabled={busy}>
            {busy ? <Spinner /> : null}
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </form>
        <div className="auth-foot">
          <span>Appearance</span>
          <ThemeSwitch />
        </div>
      </div>
    </main>
  );
}
