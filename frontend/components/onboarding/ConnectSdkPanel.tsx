import { ExternalLink, ShieldAlert } from "lucide-react";
import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import type { ConnectSdkPanelProps } from "@/components/applications/contracts";
import CopyButton from "@/components/CopyButton";
import { Ident } from "@/components/Ident";
import { Field, Input } from "@/components/ui";
import { AppSelect } from "@/components/ui/app-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { links } from "@/lib/links";
import {
  ENDPOINT_PLACEHOLDER,
  goSnippet,
  MTLS_RUNBOOK_URL,
  type SnippetInput,
  tsSnippet,
} from "@/lib/sdk-snippets";
import type { AuthMethod } from "@/lib/types";

export type { ConnectSdkPanelProps };

export const ENDPOINT_STORAGE_KEY = "kms-sdk-endpoint";

function readStoredEndpoint(): string {
  try {
    return window.localStorage.getItem(ENDPOINT_STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

function storeEndpoint(value: string): void {
  try {
    if (value) window.localStorage.setItem(ENDPOINT_STORAGE_KEY, value);
    else window.localStorage.removeItem(ENDPOINT_STORAGE_KEY);
  } catch {
    /* storage unavailable; the field still works for this render */
  }
}

/** Listener wildcard addresses accept inbound traffic but cannot be dialed by a client. */
export function isWildcardEndpoint(value: string): boolean {
  const endpoint = value.trim();
  if (endpoint === "::") return true;
  const host = endpoint.startsWith("[")
    ? endpoint.slice(1, endpoint.indexOf("]"))
    : endpoint.includes(":")
      ? endpoint.slice(0, endpoint.lastIndexOf(":"))
      : endpoint;
  return host === "" || host === "0.0.0.0" || host === "::";
}

const TROUBLESHOOTING: Array<{ title: string; detail: string }> = [
  {
    title: "Identity not bound to this namespace",
    detail:
      "A client identity reads only its home namespace. Create the identity bound to this environment, or set `namespace` explicitly and grant it a policy.",
  },
  {
    title: "Auth method the namespace does not allow",
    detail:
      "Each environment lists the methods it accepts (mTLS, token). A token identity against an mTLS-only namespace is rejected at the first RPC.",
  },
  {
    title: "Loader name differs from the release name",
    detail:
      "The release loader watches one release name. It must equal the application's release name exactly, or the loader never receives an activation.",
  },
];

/**
 * Go and TypeScript release-loader snippets templated with the server's gRPC
 * address, this namespace, the release name and the first alias, plus the
 * three things that usually go wrong the first time.
 */
export default function ConnectSdkPanel({
  namespace,
  allowedAuthMethods,
  releaseName,
  aliases,
  health,
}: ConnectSdkPanelProps) {
  const serverEndpoint = health?.grpc_addr?.trim() ?? "";
  const [typed, setTyped] = useState("");
  useEffect(() => {
    setTyped(readStoredEndpoint());
  }, []);

  const commitEndpoint = () => {
    const trimmed = typed.trim();
    if (trimmed !== typed) setTyped(trimmed);
    storeEndpoint(trimmed);
  };

  const reportedEndpoint =
    serverEndpoint && !isWildcardEndpoint(serverEndpoint) ? serverEndpoint : "";
  const endpoint = typed.trim() || reportedEndpoint;
  const tls = health?.tls_enabled !== false;
  const [preferredMethod, setPreferredMethod] = useState<AuthMethod>("mtls");
  const configuredMethods: readonly AuthMethod[] = allowedAuthMethods ?? ["mtls", "token"];
  const methods = configuredMethods.filter((method) => method === "token" || tls);
  const authMethod = methods.includes(preferredMethod) ? preferredMethod : methods[0];
  const input: SnippetInput = useMemo(
    () => ({
      endpoint,
      env: namespace.env,
      app: namespace.app,
      releaseName,
      alias: aliases[0] ?? "",
      tls,
      authMethod,
    }),
    [endpoint, namespace.env, namespace.app, releaseName, aliases, tls, authMethod],
  );
  const go = useMemo(() => goSnippet(input), [input]);
  const ts = useMemo(() => tsSnippet(input), [input]);

  return (
    <section className="connect-panel" aria-labelledby="connect-sdk-title">
      <div className="connect-head">
        <h3 id="connect-sdk-title" className="section-title">
          Connect the SDK
        </h3>
        <p className="connect-sub">
          A client bound to <Ident kind="ns" value={`${namespace.env}/${namespace.app}`} /> that
          loads release <Ident kind="release" value={releaseName} />
          {aliases[0] ? (
            <>
              {" "}
              and reads <Ident kind="alias" value={aliases[0]} />
            </>
          ) : null}
          .
        </p>
      </div>

      {health && health.tls_enabled === false ? (
        <div className="connect-warning" role="alert">
          <ShieldAlert size={16} aria-hidden />
          <div>
            <strong>The gRPC listener serves without TLS.</strong> Client tokens and values travel
            in clear text. Only use this against a loopback development server; production clients
            must use TLS.
          </div>
        </div>
      ) : null}

      <Field
        label="gRPC endpoint"
        hint={
          !serverEndpoint
            ? "Enter the reachable host:port clients should dial; it is remembered in this browser."
            : isWildcardEndpoint(serverEndpoint)
              ? "The server reported a wildcard listener, which clients cannot dial. Enter a reachable host:port; it is remembered in this browser."
              : "Enter the host:port clients should dial. Leave blank to use the reported address; an override is remembered in this browser."
        }
        htmlFor="connect-endpoint"
        className="connect-endpoint"
      >
        <Input
          id="connect-endpoint"
          className="font-mono"
          placeholder={reportedEndpoint || ENDPOINT_PLACEHOLDER}
          value={typed}
          spellCheck={false}
          onChange={(event) => setTyped(event.target.value)}
          // Persist a settled value, not every keystroke: a half-typed
          // `kms.inter` must not be what the next session reloads.
          onBlur={commitEndpoint}
          onKeyDown={(event) => {
            if (event.key === "Enter") commitEndpoint();
          }}
        />
      </Field>

      {authMethod ? (
        <Field label="Authentication" hint="Use credentials accepted by this environment.">
          <AppSelect
            value={authMethod}
            onValueChange={(method) => setPreferredMethod(method as AuthMethod)}
            disabled={methods.length === 1}
            options={methods.map((method) => ({
              value: method,
              label: method === "mtls" ? "mTLS client certificate" : "Token",
            }))}
          />
        </Field>
      ) : (
        <div className="connect-warning" role="alert">
          This environment requires mTLS, but the listener has TLS disabled. Enable TLS before
          connecting an SDK.
        </div>
      )}

      {authMethod ? (
        <Tabs defaultValue="go" className="connect-tabs">
          <TabsList aria-label="SDK language">
            <TabsTrigger value="go">Go</TabsTrigger>
            <TabsTrigger value="ts">TypeScript</TabsTrigger>
          </TabsList>
          <TabsContent value="go">
            <div className="connect-snippet">
              <div className="connect-snippet-bar">
                <span className="connect-snippet-lang">Go · kmsclient.ReleaseLoader</span>
                <CopyButton value={go} label="Copy Go snippet" />
              </div>
              <pre className="connect-code">
                <code>{go}</code>
              </pre>
            </div>
          </TabsContent>
          <TabsContent value="ts">
            <div className="connect-snippet">
              <div className="connect-snippet-bar">
                <span className="connect-snippet-lang">TypeScript · @suhaibinator/kms</span>
                <CopyButton value={ts} label="Copy TypeScript snippet" />
              </div>
              <pre className="connect-code">
                <code>{ts}</code>
              </pre>
            </div>
          </TabsContent>
        </Tabs>
      ) : null}

      <div className="connect-links">
        <Link
          href={links.identities({ env: namespace.env, app: namespace.app, new: true })}
          className="connect-link"
        >
          Create identity for {namespace.env}/{namespace.app}
        </Link>
        <a
          href={MTLS_RUNBOOK_URL}
          target="_blank"
          rel="noreferrer noopener"
          className="connect-link"
        >
          mTLS onboarding runbook
          <ExternalLink size={13} aria-hidden />
        </a>
      </div>

      <details className="connect-trouble">
        <summary>Not receiving the release?</summary>
        <ul className="connect-trouble-list">
          {TROUBLESHOOTING.map((entry) => (
            <li key={entry.title}>
              <strong>{entry.title}.</strong> {entry.detail}
            </li>
          ))}
        </ul>
      </details>
    </section>
  );
}
