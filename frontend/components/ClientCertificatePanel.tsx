import { useEffect, useState } from "react";
import CopyButton from "@/components/CopyButton";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { ConnectionResponse } from "@/lib/types";

export function ClientCertificatePanel() {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<
    { kind: "loading" | "error" } | { kind: "ready"; data: ConnectionResponse }
  >({ kind: "loading" });

  // Refresh intentionally reruns this effect even though request inputs are unchanged.
  // biome-ignore lint/correctness/useExhaustiveDependencies: attempt is the explicit refresh trigger.
  useEffect(() => {
    const controller = new AbortController();
    api.connection({ signal: controller.signal }).then(
      (data) => {
        if (!controller.signal.aborted) setState({ kind: "ready", data });
      },
      () => {
        if (!controller.signal.aborted) setState({ kind: "error" });
      },
    );
    return () => controller.abort();
  }, [attempt]);

  const data = state.kind === "ready" ? state.data : null;
  const cert = data?.client_certificate;
  return (
    <section
      aria-label="Client certificate"
      className="mt-5 border-t pt-4 text-sm [overflow-wrap:anywhere]"
    >
      <div className="flex items-center justify-between gap-2 mb-2">
        <h2 className="font-medium">Client certificate</h2>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={state.kind === "loading"}
          onClick={() => {
            setState({ kind: "loading" });
            setAttempt((value) => value + 1);
          }}
        >
          Refresh
        </Button>
      </div>
      <div role="status" className="muted">
        {state.kind === "loading"
          ? "Checking client certificate…"
          : state.kind === "error"
            ? "Could not check the client certificate."
            : !data?.tls_enabled
              ? "Client certificate information unavailable. This request reached the server without TLS."
              : cert
                ? "Client certificate received"
                : "No client certificate received"}
      </div>
      {cert ? (
        <>
          <p className="mt-2">
            Identity in certificate:{" "}
            <span className="mono">{cert.identity_uri ?? "No unambiguous KMS identity URI."}</span>
          </p>
          <details className="mt-3">
            <summary className="cursor-pointer">Certificate details</summary>
            <dl className="mt-2 space-y-2">
              <div>
                <dt className="muted">SHA-256 fingerprint</dt>
                <dd className="mono">{cert.fingerprint_sha256}</dd>
              </div>
              <div>
                <dt className="muted">Expires (UTC)</dt>
                <dd>
                  <time dateTime={cert.not_after}>{cert.not_after}</time>
                </dd>
              </div>
            </dl>
            <div className="mt-2">
              <CopyButton value={cert.fingerprint_sha256} label="Copy fingerprint" />
            </div>
          </details>
          <p className="muted mt-3">
            This is the certificate your browser presented on the latest connection check. It does
            not confirm account access.
          </p>
        </>
      ) : null}
    </section>
  );
}
