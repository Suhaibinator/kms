import { useRouter } from "next/router";
import { useEffect, useState } from "react";
import { LogoMark } from "@/components/LogoMark";
import { Field, Input, PageTitle, Spinner } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import { getToken } from "@/lib/api";
import { useQueryParam } from "@/lib/hooks";
import { safeReturnTo } from "@/lib/returnTo";

export default function LoginPage() {
  const router = useRouter();
  const { login, ready } = useAuth();
  const toast = useToast();
  const [token, setTokenValue] = useState("");
  const [busy, setBusy] = useState(false);
  // Turns the required-token message on only once the user has actually tried
  // to submit, so an untouched form does not open with an error on it.
  const [submitted, setSubmitted] = useState(false);
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
      toast.error(err, "Sign-in failed");
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

        <p className="muted text-sm mb-16">
          Paste an admin or client identity token to sign in. Tokens are stored only for this
          browser session.
        </p>

        <form onSubmit={onSubmit}>
          <Field
            label="Identity token"
            error={missingToken ? "Enter a token to continue." : null}
            hint={
              <>
                Minted by <span className="mono">parameter-store create-admin</span> or the
                identities API.
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
              onChange={(e) => setTokenValue(e.target.value)}
            />
          </Field>

          <Button type="submit" className="w-full" disabled={busy}>
            {busy ? <Spinner /> : null}
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </div>
    </main>
  );
}
