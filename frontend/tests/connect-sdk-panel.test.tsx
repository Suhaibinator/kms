import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ConnectSdkPanel, { ENDPOINT_STORAGE_KEY } from "@/components/onboarding/ConnectSdkPanel";
import { goSnippet, MTLS_RUNBOOK_URL, tsSnippet } from "@/lib/sdk-snippets";
import type { HealthResponse } from "@/lib/types";

const mocks = vi.hoisted(() => ({
  toast: { success: vi.fn(), info: vi.fn(), error: vi.fn() },
}));
vi.mock("@/context/ToastContext", () => ({ useToast: () => mocks.toast }));

const health: HealthResponse = {
  healthy: true,
  ready: true,
  version: "1.2.3",
  current_revision: 42,
  grpc_addr: "kms.prod.internal:8443",
  tls_enabled: true,
};

const ns = { env: "prod", app: "gradethis" };
const aliases = ["rate_limits", "database", "db_password"];

function snippet(): string {
  return (document.querySelector("pre.connect-code code") as HTMLElement).textContent ?? "";
}

describe("sdk snippets", () => {
  it("templates endpoint, namespace, release name and first alias into both languages", () => {
    const input = {
      endpoint: "kms.prod.internal:8443",
      env: "prod",
      app: "gradethis",
      releaseName: "runtime",
      alias: "rate_limits",
      tls: true,
    };
    const go = goSnippet(input);
    expect(go).toContain('Endpoint:  "kms.prod.internal:8443"');
    expect(go).toContain('Namespace: "prod/gradethis"');
    expect(go).toContain('Name: "runtime"');
    expect(go).toContain('candidate.Parameter("rate_limits")');
    expect(go).toContain("kmsclient.MTLSFromFiles(");
    expect(go).not.toContain("Insecure");

    const ts = tsSnippet(input);
    expect(ts).toContain('process.env.KMS_ENDPOINT ?? "kms.prod.internal:8443"');
    expect(ts).toContain('namespace: "prod/gradethis"');
    expect(ts).toContain('createReleaseLoader({ name: "runtime" })');
    expect(ts).toContain('snapshot.parameter("rate_limits")');
    expect(ts).toContain("mtlsFromFiles(");
  });

  it("opts into cleartext with a warning comment when TLS is off, and escapes quotes", () => {
    const input = {
      endpoint: 'weird"host:1',
      env: "dev",
      app: "a",
      releaseName: "runtime",
      alias: "",
      tls: false,
    };
    expect(goSnippet(input)).toContain("Insecure: true");
    expect(goSnippet(input)).toContain('"weird\\"host:1"');
    expect(goSnippet(input)).toContain('candidate.Parameter("alias")');
    expect(tsSnippet(input)).toContain("insecure: true");
    expect(tsSnippet(input)).not.toContain("mtlsFromFiles");
  });
});

describe("ConnectSdkPanel", () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it("shows the Go snippet by default with the health endpoint, then switches to TypeScript", () => {
    render(
      <ConnectSdkPanel namespace={ns} releaseName="runtime" aliases={aliases} health={health} />,
    );

    expect(screen.getByRole("tab", { name: "Go" })).toHaveAttribute("aria-selected", "true");
    expect(snippet()).toContain('Endpoint:  "kms.prod.internal:8443"');
    expect(snippet()).toContain('candidate.Parameter("rate_limits")');
    expect(screen.getByRole("button", { name: "Copy Go snippet" })).toBeVisible();
    // The endpoint is read-only when the server reports it.
    expect(screen.queryByLabelText("gRPC endpoint")).toBeNull();
    expect(screen.getByText("kms.prod.internal:8443")).toBeVisible();

    fireEvent.click(screen.getByRole("tab", { name: "TypeScript" }));
    expect(snippet()).toContain('createReleaseLoader({ name: "runtime" })');
    expect(screen.getByRole("button", { name: "Copy TypeScript snippet" })).toBeVisible();
  });

  it("links to identity creation with the namespace prefilled and to the mTLS runbook", () => {
    render(
      <ConnectSdkPanel namespace={ns} releaseName="runtime" aliases={aliases} health={health} />,
    );
    expect(
      screen.getByRole("link", { name: "Create identity for prod/gradethis" }),
    ).toHaveAttribute("href", "/identities?env=prod&app=gradethis&new=1");
    const runbook = screen.getByRole("link", { name: /mTLS onboarding runbook/ });
    expect(runbook).toHaveAttribute("href", MTLS_RUNBOOK_URL);
    expect(runbook.getAttribute("href")).toContain(
      "docs/operations.md#connect-a-production-application-with-mtls",
    );
    expect(runbook).toHaveAttribute("target", "_blank");
  });

  it("warns when the listener serves without TLS and switches the snippet to insecure", () => {
    render(
      <ConnectSdkPanel
        namespace={ns}
        releaseName="runtime"
        aliases={aliases}
        health={{ ...health, tls_enabled: false }}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("The gRPC listener serves without TLS.");
    expect(snippet()).toContain("Insecure: true");
    expect(snippet()).not.toContain("MTLSFromFiles");
  });

  it("does not warn while health is loading or when TLS is on", () => {
    const { rerender } = render(
      <ConnectSdkPanel namespace={ns} releaseName="runtime" aliases={aliases} health={null} />,
    );
    expect(screen.queryByRole("alert")).toBeNull();
    rerender(
      <ConnectSdkPanel namespace={ns} releaseName="runtime" aliases={aliases} health={health} />,
    );
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("lets the operator type the endpoint when health has none, remembering it per browser", () => {
    window.localStorage.setItem(ENDPOINT_STORAGE_KEY, "remembered:9443");
    render(
      <ConnectSdkPanel
        namespace={ns}
        releaseName="runtime"
        aliases={aliases}
        health={{ ...health, grpc_addr: "" }}
      />,
    );
    const input = screen.getByLabelText("gRPC endpoint") as HTMLInputElement;
    expect(input.value).toBe("remembered:9443");
    expect(snippet()).toContain('Endpoint:  "remembered:9443"');

    fireEvent.change(input, { target: { value: "typed:1234" } });
    expect(snippet()).toContain('Endpoint:  "typed:1234"');
    expect(window.localStorage.getItem(ENDPOINT_STORAGE_KEY)).toBe("typed:1234");
  });

  it("lists the three usual failures", () => {
    render(
      <ConnectSdkPanel namespace={ns} releaseName="runtime" aliases={aliases} health={health} />,
    );
    fireEvent.click(screen.getByText("Not receiving the release?"));
    expect(screen.getByText(/Identity not bound to this namespace/)).toBeVisible();
    expect(screen.getByText(/Auth method the namespace does not allow/)).toBeVisible();
    expect(screen.getByText(/Loader name differs from the release name/)).toBeVisible();
  });
});
