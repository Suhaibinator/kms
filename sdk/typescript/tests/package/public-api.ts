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
  generate,
  parseDescriptor,
  verifyArtifacts,
} from "@suhaibinator/kms/configgen";
import {
  type ContractEntry,
  // @ts-expect-error Internal secret-tree scanner is not a supported configstore export.
  containsSecret,
  type ManagedPreparedCandidate,
  // @ts-expect-error Strict JSON parse-tree primitives are internal to codecs and configgen.
  parseStrictJson,
  startManagedConfig,
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
      onDefaultMismatch: () => undefined,
    },
    (): ManagedPreparedCandidate => ({ publish() {} }),
  );
  manager.stop();
  await manager.wait();
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
