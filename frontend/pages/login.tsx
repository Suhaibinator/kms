import { useRouter } from "next/router";
import { useEffect, useState } from "react";
import { ClientCertificatePanel } from "@/components/ClientCertificatePanel";
import { LogoMark } from "@/components/LogoMark";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { Field, Input, PageTitle } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import { ApiError, getToken } from "@/lib/api";
import { useQueryParam } from "@/lib/hooks";
import { safeReturnTo } from "@/lib/returnTo";

export default function LoginPage() {
  const router = useRouter();
  const { login, ready, verificationPending, retryVerification } = useAuth();
  const toast = useToast();
  const [token, setTokenValue] = useState("");
  const [busy, setBusy] = useState(false);
  // Turns the required-token message on only once the user has actually tried
  // to submit, so an untouched form does not open with an error on it.
  const [submitted, setSubmitted] = useState(false);
  // Why the last attempt failed, shown under the field; cleared on edit.
  const [authError, setAuthError] = useState<string | null>(null);
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
      if (err instanceof Error && err.name === "SessionVerificationError") {
        setAuthError(
          "Signed in, but the console could not load your access scope. Retry the check.",
        );
        return;
      }
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
        const message = "Sign-in failed. Check your credentials and try again.";
        setAuthError(message);
        toast.error(new Error(message), "Sign-in failed");
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
          Paste an admin or client identity token to sign in. Tokens are stored only for this
          browser session.
        </p>

        <form onSubmit={onSubmit}>
          {/* field-reserve: the card is vertically centred, so without the
              reserved message line the input jumped up 10.9px and the submit
              button down 10.9px the moment the validation message appeared. */}
          <Field
            className="field-reserve"
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

          {/* `loading` also carries aria-busy and the disabled state, which the
              hand-rolled label swap did not. */}
          <Button type="submit" className="w-full" loading={busy}>
            Sign in
          </Button>
          {verificationPending ? (
            <Button
              type="button"
              variant="outline"
              className="w-full mt-2"
              onClick={retryVerification}
            >
              Retry access scope check
            </Button>
          ) : null}
        </form>
        <ClientCertificatePanel />
        <div className="auth-foot">
          <span>Appearance</span>
          <ThemeSwitch />
        </div>
      </div>
    </main>
  );
}
