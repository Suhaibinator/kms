// SDK snippets for the Connect SDK panel, templated from docs/sdk-go.md
// (ReleaseLoader) and sdk/typescript/examples/release.ts. Pure string
// builders so the panel and its tests share one source.

export interface SnippetInput {
  /** host:port of the gRPC listener (health.grpc_addr). */
  endpoint: string;
  env: string;
  app: string;
  releaseName: string;
  /** The first contract alias; the snippet reads it. Empty when the contract is empty. */
  alias: string;
  /** false when health reports `tls_enabled: false`; the snippet then opts into cleartext. */
  tls: boolean;
}

export const MTLS_RUNBOOK_URL =
  "https://github.com/Suhaibinator/kms/blob/main/docs/operations.md#connect-a-production-application-with-mtls";

export const ENDPOINT_PLACEHOLDER = "kms.internal:8443";

// Go string literals: escape backslashes and quotes; identifiers here are
// validated names, but the endpoint is operator-typed.
function goString(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tsString(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function identifier(alias: string): string {
  const camel = alias.replace(/[^A-Za-z0-9]+(.)?/g, (_, c: string | undefined) =>
    c ? c.toUpperCase() : "",
  );
  return /^[A-Za-z]/.test(camel) ? camel : `v${camel}`;
}

export function goSnippet(input: SnippetInput): string {
  const endpoint = input.endpoint || ENDPOINT_PLACEHOLDER;
  const alias = input.alias || "alias";
  const name = identifier(alias);
  const transport = input.tls
    ? `    TLS: kmsclient.MTLSFromFiles(
        os.Getenv("KMS_CLIENT_CERT_FILE"),
        os.Getenv("KMS_CLIENT_KEY_FILE"),
        os.Getenv("KMS_CA_FILE"),
    ),`
    : `    // The server reports TLS disabled: cleartext is for a loopback dev server only.
    Insecure: true,
    Token:    os.Getenv("KMS_TOKEN"),`;
  return `client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint:  ${goString(endpoint)},
${transport}
    Namespace: ${goString(`${input.env}/${input.app}`)}, // optional when the identity is bound to it
})
if err != nil {
    return err
}
defer client.Close()

loader, err := kmsclient.NewReleaseLoader(client, kmsclient.ReleaseLoaderConfig{
    Name: ${goString(input.releaseName)}, // must equal the application's release name
})
if err != nil {
    return err
}

err = loader.Run(ctx, func(ctx context.Context, candidate kmsclient.ReleaseSnapshot) (
    kmsclient.PreparedRelease, error,
) {
    ${name}, ok := candidate.Parameter(${goString(alias)})
    if !ok {
        return nil, errors.New(${goString(`${alias} alias is missing`)})
    }
    // Decode and validate here; commit atomically in the returned PreparedRelease.
    return prepareRuntime(ctx, ${name}.Value())
})`;
}

export function tsSnippet(input: SnippetInput): string {
  const endpoint = input.endpoint || ENDPOINT_PLACEHOLDER;
  const alias = input.alias || "alias";
  const name = identifier(alias);
  const credentials = input.tls
    ? `  credentials: mtlsFromFiles(
    process.env.KMS_CLIENT_CERT_FILE!,
    process.env.KMS_CLIENT_KEY_FILE!,
    process.env.KMS_CA_FILE!,
  ),`
    : `  // The server reports TLS disabled: cleartext is for a loopback dev server only.
  insecure: true,
  token: process.env.KMS_TOKEN,`;
  const imports = input.tls
    ? 'import { ClassifiedReleaseError, createClient, mtlsFromFiles } from "@suhaibinator/kms";'
    : 'import { ClassifiedReleaseError, createClient } from "@suhaibinator/kms";';
  return `${imports}

const client = createClient({
  endpoint: process.env.KMS_ENDPOINT ?? ${tsString(endpoint)},
${credentials}
  namespace: ${tsString(`${input.env}/${input.app}`)}, // optional when the identity is bound to it
});
const loader = await client.createReleaseLoader({ name: ${tsString(input.releaseName)} });

await loader.run((snapshot) => {
  const ${name} = snapshot.parameter(${tsString(alias)})?.value();
  if (${name} === undefined) throw new ClassifiedReleaseError("config_validation_failed");
  // Parse and validate here; keep commit synchronous and infallible.
  return { commit() {}, abort() {} };
});`;
}
