import { useId, useState } from "react";
import { Modal } from "@/components/Modal";
import { Field, Input } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/AuthContext";
import type { Identity } from "@/lib/types";

/** Reauthenticate in place: page and editor state never leave memory. */
export function SessionRecovery({ identity }: { identity: Identity }) {
  const { login, logout } = useAuth();
  const formId = useId();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return (
    <Modal
      open
      title="Sign in to resume your draft"
      description="Your session ended. Your edits remain in this tab's memory; nothing has been saved by this dialog."
      dismissible={false}
      trackUnsavedWork={false}
      onClose={() => {}}
      footer={
        <>
          <Button variant="outline" disabled={busy} onClick={logout}>
            Discard draft and sign out
          </Button>
          <Button form={formId} type="submit" loading={busy}>
            Resume editing
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={async (event) => {
          event.preventDefault();
          if (busy) return;
          if (!token.trim()) {
            setError("Enter your identity token.");
            return;
          }
          setBusy(true);
          setError(null);
          try {
            await login(token.trim(), identity);
            setToken("");
          } catch (reason) {
            setError(reason instanceof Error ? reason.message : "Sign-in failed. Try again.");
          } finally {
            setBusy(false);
          }
        }}
      >
        <Field
          label={`Token for ${identity.name}`}
          error={error}
          hint="Use the same identity to resume. Secret drafts stay only in memory and will be lost if you close this tab."
        >
          <Input
            type="password"
            autoComplete="off"
            value={token}
            disabled={busy}
            onChange={(event) => setToken(event.target.value)}
          />
        </Field>
      </form>
    </Modal>
  );
}
