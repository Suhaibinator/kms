import type {
  ClientReleaseLoaderOptions,
  KmsClient,
  PreparedRelease,
  PublicConfigWire,
  ReleaseLoader,
  ReleaseSecret,
  ReleaseSnapshot,
  // @ts-expect-error Low-level protocol transport types are not part of the package root.
  ReleaseTransport,
} from "@suhaibinator/kms";
import {
  type ConfigDescriptor,
  generate,
  parseDescriptor,
  verifyArtifacts,
} from "@suhaibinator/kms/configgen";
import {
  type ContractEntry,
  type ManagedPreparedCandidate,
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

void (undefined as ReleaseTransport | ConfigurationRelease | undefined);
