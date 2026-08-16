import { Button } from "@/components/ui/button";
import { LogoMark } from "@/components/LogoMark";
import { useRouter } from "next/router";
import { useEffect, useState } from "react";
import { Input, PageTitle, Spinner } from "@/components/ui";
import { useAuth } from "@/context/AuthContext";
import { useToast } from "@/context/ToastContext";
import { getToken } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const { login, ready } = useAuth();
  const toast = useToast();
  const [token, setTokenValue] = useState("");
  const [busy, setBusy] = useState(false);

  // If already authenticated, skip the login screen.
  useEffect(() => {
    if (ready && getToken()) {
      void router.replace("/");
    }
  }, [ready, router]);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = token.trim();
    if (!trimmed) {
      toast.error(new Error("Enter a token to continue."), "Missing token");
      return;
    }
    setBusy(true);
    try {
      const identity = await login(trimmed);
      // Clear the field so the token does not linger in the DOM.
      setTokenValue("");
      toast.success("Signed in", `Welcome, ${identity.name}`);
      await router.replace("/");
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
          <div className="field">
            <label className="field-label" htmlFor="token">
              Identity token
            </label>
            <Input
              id="token"
              className="font-mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder="paste token…"
              value={token}
              onChange={(e) => setTokenValue(e.target.value)}
              aria-describedby="token-hint"
            />
            <div className="field-hint" id="token-hint">
              Minted by <span className="mono">parameter-store create-admin</span> or the identities
              API.
            </div>
          </div>

          <Button type="submit" className="w-full" disabled={busy}>
            {busy ? <Spinner /> : null}
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </div>
    </main>
  );
}
