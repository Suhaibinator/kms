import type {
  ClientReleaseLoaderOptions,
  // @ts-expect-error Release-loader transport internals are not part of the package root.
  FetchedSecret,
  KmsClient,
  PreparedRelease,
  PublicConfigWire,
  ReleaseLoader,
  // @ts-expect-error Release-loader construction is owned by KmsClient.
  ReleaseLoaderOptions,
  ReleaseSecret,
  ReleaseSnapshot,
  // @ts-expect-error Release-loader transport internals are not part of the package root.
  ReleaseTransport,
  // @ts-expect-error Release-loader transport internals are not part of the package root.
  ReleaseWatchStream,
  ValueResolver,
} from "@suhaibinator/kms";
import {
  // @ts-expect-error Digest implementation helpers are not part of the package root.
  deterministicReleaseDigest,
  // @ts-expect-error Digest implementation helpers are not part of the package root.
  releaseDigestMatches,
  // @ts-expect-error Digest implementation helpers are not part of the package root.
  sha256Hex,
} from "@suhaibinator/kms";
import {
  type ConfigDescriptor,
  encodeDefaultsArtifact,
  generate,
  parseDefaultsArtifact,
  parseDescriptor,
  runDefaultsExporter,
  verifyArtifacts,
} from "@suhaibinator/kms/configgen";
import {
  type AppliedReport,
  type ContractEntry,
  // @ts-expect-error Internal secret-tree scanner is not a supported configstore export.
  containsSecret,
  consoleCallbacks,
  // @ts-expect-error Startup default mismatches are applied and reported; the fatal error type was removed.
  DefaultMismatchError,
  type ManagedPreparedCandidate,
  parameterHash,
  parseDefaultsArtifact as parseRuntimeDefaultsArtifact,
  // @ts-expect-error Strict JSON parse-tree primitives are internal to codecs and configgen.
  parseStrictJson,
  startManagedConfig,
  type VerifyResult,
  verifyDefaults,
} from "@suhaibinator/kms/configstore";
// @ts-expect-error Generated protocol modules are blocked by the package export map.
import type { ConfigurationRelease } from "@suhaibinator/kms/dist/generated/kms.js";
import { usePublicConfig } from "@suhaibinator/kms/next/client";
import { createNextKms } from "@suhaibinator/kms/next/server";

interface PublicPolicy {
  readonly [key: string]: number;
  readonly limit: number;
}

const contract = [
  { alias: "runtime", kind: "parameter", contentType: "application/json" },
] as const satisfies readonly ContractEntry[];

export function consumeBuiltDeclarations(
  client: KmsClient,
  loader: ReleaseLoader,
  snapshot: ReleaseSnapshot,
  wire: PublicConfigWire<PublicPolicy>,
): readonly unknown[] {
  const options: ClientReleaseLoaderOptions = { name: "runtime" };
  const secret: ReleaseSecret | undefined = snapshot.secret("secret");
  const prepared: PreparedRelease = { commit() {}, abort() {} };
  return [client, loader, snapshot, wire, options, secret, prepared];
}

export async function consumeManagedDeclarations(client: KmsClient): Promise<void> {
  const manager = await startManagedConfig(
    client,
    {
      release: "runtime",
      contract,
      ...consoleCallbacks(console),
      onApplied: (report: AppliedReport) => void report.changed(),
    },
    (): ManagedPreparedCandidate => ({ publish() {}, changed: [], groups: {} }),
  );
  manager.stop();
  await manager.wait();
}

export async function consumeVerifyDeclarations(client: KmsClient): Promise<VerifyResult> {
  await client.verifyReleaseDefaults({
    namespace: "prod/api",
    entries: [{ alias: "runtime", contentType: "json", sha256: parameterHash("json", "{}") }],
  });
  return verifyDefaults(
    client,
    { schemaSha256: "0".repeat(64), contract, groups: { runtime: "{}" } },
    { namespace: "prod/api" },
  );
}

export const adapterFactories = [createNextKms, usePublicConfig] as const;

export const internalClientHelpersArePrivate: Extract<
  keyof KmsClient,
  | "fetchParameter"
  | "fetchSecret"
  | "listParametersInNamespace"
  | "resolveResourceRef"
  | "discoverNamespace"
  | "requireNamespace"
> extends never
  ? true
  : false = true;

export const internalValueResolverHooksArePrivate: Extract<
  keyof ValueResolver,
  | "_defaultAllowedForError"
  | "_resolveRef"
  | "_registerParameter"
  | "_enqueueCallback"
  | "_dispatch"
  | "_log"
> extends never
  ? true
  : false = true;

export const publicStructuralResolverRemainsAvailable: Pick<ValueResolver, "resolveResourceRef"> =
  {};

export async function consumeConfiggenDeclarations(document: string): Promise<void> {
  const descriptor: ConfigDescriptor = parseDescriptor(document);
  await verifyArtifacts(
    {
      binding: "src/config.generated.ts",
      schema: "config/runtime.schema.json",
      contract: "config/runtime.contract.json",
    },
    generate(descriptor),
  );
}

export async function consumeDefaultsDeclarations(): Promise<void> {
  const artifact = encodeDefaultsArtifact({
    profile: "local",
    schemaSHA256: "0".repeat(64),
    contract: [{ alias: "runtime", kind: "parameter", contentType: "json" }],
    parameters: { runtime: "{}" },
  });
  parseDefaultsArtifact(artifact);
  await runDefaultsExporter(
    ["--profile", "local", "--output", "-"],
    () => ({ runtime: "{}" }),
    (profile, parameters) =>
      encodeDefaultsArtifact({
        profile,
        schemaSHA256: "0".repeat(64),
        contract: [{ alias: "runtime", kind: "parameter", contentType: "json" }],
        parameters,
      }),
    { stdout: () => undefined, stderr: () => undefined },
  );
}

void (undefined as
  | FetchedSecret
  | ReleaseLoaderOptions
  | ReleaseTransport
  | ReleaseWatchStream
  | ConfigurationRelease
  | undefined);
void deterministicReleaseDigest;
void releaseDigestMatches;
void sha256Hex;
void containsSecret;
void parseStrictJson;
void parseRuntimeDefaultsArtifact;
void DefaultMismatchError;
