import {
  type KmsClient,
  type KmsErrorCode,
  ParameterValue,
  type PolicySnapshot,
  type PreparedRelease,
  type PublicConfigWire,
  type ReleaseSnapshot,
  type Secret,
  SecretValue,
} from "@suhaibinator/kms";
import type { NextKms } from "@suhaibinator/kms/next/server";
import {
  type ContractEntry,
  type ManagedPreparedCandidate,
  startManagedConfig,
} from "@suhaibinator/kms/configstore";

// @ts-expect-error Protocol transport types are internal and must not expose generated messages.
import type { ReleaseTransport } from "@suhaibinator/kms";
// @ts-expect-error Digest helpers consume generated messages and are intentionally internal.
import { deterministicReleaseDigest } from "@suhaibinator/kms";

interface Policy {
  readonly limit: number;
}

interface PublicPolicy {
  readonly [key: string]: number;
  readonly limit: number;
}

export const values = {
  secret: new SecretValue("secret"),
  parameter: new ParameterValue("parameter"),
};

void (undefined as ReleaseTransport | undefined);
void deterministicReleaseDigest;

export function prepare(snapshot: ReleaseSnapshot): PreparedRelease {
  snapshot.secret("secret")?.bytes();
  return { commit() {}, abort() {} };
}

export function acceptsPublicApi(
  client: KmsClient,
  adapter: NextKms<Policy, PublicPolicy, string, string>,
  wire: PublicConfigWire<PublicPolicy>,
  snapshot: PolicySnapshot<Policy>,
  code: KmsErrorCode,
): readonly unknown[] {
  const secret: Promise<Secret> = client.getSecret("secret");
  return [adapter, wire, snapshot, code, secret];
}

const managedContract = [
  { alias: "runtime", kind: "parameter", contentType: "application/json" },
] as const satisfies readonly ContractEntry[];

export async function startsManagedConfig(client: KmsClient): Promise<void> {
  const manager = await startManagedConfig(
    client,
    {
      release: "runtime",
      contract: managedContract,
      onDefaultMismatch: () => undefined,
    },
    (): ManagedPreparedCandidate => ({ publish() {} }),
  );
  manager.stop();
  await manager.wait();
}
