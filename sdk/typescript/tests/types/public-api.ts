import {
  // @ts-expect-error Digest helpers consume generated messages and are intentionally internal.
  deterministicReleaseDigest,
  type KmsClient,
  type KmsErrorCode,
  ParameterValue,
  type PolicySnapshot,
  type PreparedRelease,
  type PublicConfigWire,
  type ReleaseSnapshot,
  // @ts-expect-error Protocol transport types are internal and must not expose generated messages.
  type ReleaseTransport,
  type Secret,
  SecretValue,
} from "@suhaibinator/kms";
import {
  type ConfigDescriptor,
  type DefaultsArtifact,
  encodeDefaultsArtifact,
  generate,
  parseDefaultsArtifact,
  parseDescriptor,
  runDefaultsExporter,
  verifyArtifacts,
} from "@suhaibinator/kms/configgen";
import {
  type ContractEntry,
  type ManagedPreparedCandidate,
  parseDefaultsArtifact as parseRuntimeDefaultsArtifact,
  startManagedConfig,
} from "@suhaibinator/kms/configstore";
import type { NextKms } from "@suhaibinator/kms/next/server";

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

export function generateManagedArtifacts(document: string): void {
  const descriptor: ConfigDescriptor = parseDescriptor(document);
  const artifacts = generate(descriptor);
  void verifyArtifacts(
    {
      binding: "src/config.generated.ts",
      schema: "config/runtime.schema.json",
      contract: "config/runtime.contract.json",
    },
    artifacts,
  );
}

export async function exportManagedDefaults(): Promise<DefaultsArtifact> {
  type Profile = "local" | "production";
  const provider = (profile: Profile): { runtime: string } => ({
    runtime: JSON.stringify({ profile }),
  });
  await runDefaultsExporter(
    ["--profile", "local", "--output", "-"],
    provider,
    (profile, defaults) =>
      encodeDefaultsArtifact({
        profile,
        schemaSHA256: "0".repeat(64),
        contract: [{ alias: "runtime", kind: "parameter", contentType: "json" }],
        parameters: defaults,
      }),
    { stdout: () => undefined, stderr: () => undefined },
  );
  return parseDefaultsArtifact(
    encodeDefaultsArtifact({
      profile: "local",
      schemaSHA256: "0".repeat(64),
      contract: [{ alias: "runtime", kind: "parameter", contentType: "json" }],
      parameters: { runtime: "{}" },
    }),
  );
}

void parseRuntimeDefaultsArtifact;

void (undefined as ReleaseTransport | undefined);
void deterministicReleaseDigest;

export function prepare(snapshot: ReleaseSnapshot): PreparedRelease {
  snapshot.secret("secret")?.bytes();
  return { commit() {}, abort() {} };
}

const asyncCommitIsNotPrepared: PreparedRelease = {
  // @ts-expect-error release commits must be synchronous
  async commit() {},
  abort() {},
};
const asyncAbortIsNotPrepared: PreparedRelease = {
  commit() {},
  // @ts-expect-error release aborts must be synchronous
  async abort() {},
};
void [asyncCommitIsNotPrepared, asyncAbortIsNotPrepared];

const asyncPublishIsNotManaged: ManagedPreparedCandidate = {
  // @ts-expect-error managed publication must be synchronous
  async publish() {},
};
const asyncManagedAbortIsNotManaged: ManagedPreparedCandidate = {
  publish() {},
  // @ts-expect-error managed cleanup must be synchronous
  async abort() {},
};
void [asyncPublishIsNotManaged, asyncManagedAbortIsNotManaged];

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
