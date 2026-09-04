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
  type SecretBindingCohortResult,
  type SecretBindingCohortGuardOptions,
  type SecretOptions,
  SecretValue,
  type SecretVersionMutationResult,
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
  type AppliedReport,
  type Callbacks,
  canonicalParameterValue,
  consoleCallbacks,
  type ContractEntry,
  // @ts-expect-error Startup default mismatches are applied and reported; the fatal error type was removed.
  DefaultMismatchError,
  type FieldChange,
  type ManagedPreparedCandidate,
  type MismatchSeverity,
  parameterHash,
  parseDefaultsArtifact as parseRuntimeDefaultsArtifact,
  type Phase,
  startManagedConfig,
  type VerifyResult,
  verifyDefaults,
} from "@suhaibinator/kms/configstore";
import { RateLimitedError, type VerifyReleaseDefaultsResult } from "@suhaibinator/kms";
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
  const declaration = { bindKey: "operator-owned-binding-key" } satisfies SecretOptions;
  const secret: Promise<Secret> = client.getSecret("secret", {
    secretToken: "access-token",
    bindingKey: declaration.bindKey,
  });
  const bound: Promise<SecretVersionMutationResult> = client.bindSecret("secret", {
    bindingKey: "operator-owned-binding-key",
  });
  const unbound: Promise<SecretVersionMutationResult> = client.unbindSecret("secret", {
    bindingKey: "operator-owned-binding-key",
  });
  const guards: SecretBindingCohortGuardOptions = {
    expectedRevision: 4n,
    expectedAffectedVersions: [1n, 2n],
  };
  const previewed: Promise<SecretBindingCohortResult> = client.previewSecretBindingCohort(
    "secret",
    { bindingKey: "operator-owned-binding-key" },
  );
  const rotated: Promise<SecretBindingCohortResult> = client.rotateSecretBindingKey("secret", {
    bindingKey: "operator-owned-binding-key",
    newBindingKey: "new-operator-owned-binding-key",
    ...guards,
  });
  const purged: Promise<SecretBindingCohortResult> = client.purgeSecretBindingCohort("secret", {
    bindingKey: "operator-owned-binding-key",
  });
  return [
    adapter,
    wire,
    snapshot,
    code,
    declaration,
    secret,
    bound,
    unbound,
    guards,
    previewed,
    rotated,
    purged,
  ];
}

declare const clientForRemovedPutOptions: KmsClient;
void clientForRemovedPutOptions.putSecret("secret", "plaintext", {
  // @ts-expect-error Client-bound state is derived exclusively from bindingKey.
  clientBound: true,
});
void clientForRemovedPutOptions.putSecret("secret", "plaintext", {
  // @ts-expect-error Access tokens are generated by KMS and cannot be supplied on writes.
  secretToken: "old-write-credential",
});

const managedContract = [
  { alias: "runtime", kind: "parameter", contentType: "application/json" },
] as const satisfies readonly ContractEntry[];

export async function startsManagedConfig(client: KmsClient): Promise<void> {
  const callbacks: Callbacks = consoleCallbacks(console, { component: "api" });
  const manager = await startManagedConfig(
    client,
    {
      release: "runtime",
      contract: managedContract,
      ...callbacks,
      onApplied(report: AppliedReport) {
        const phase: Phase = report.phase;
        const changes: FieldChange[] = report.changed();
        const groups: Readonly<Record<string, string>> = report.groups();
        void [phase, changes, groups, report.defaultDivergent];
      },
    },
    (): ManagedPreparedCandidate => ({
      publish() {},
      changed: [{ path: "runtime.limit", previous: 1, current: 2 }],
      groups: { runtime: "{}" },
    }),
  );
  manager.stop();
  await manager.wait();
}

void startManagedConfig(
  undefined as unknown as KmsClient,
  {
    release: "runtime",
    contract: managedContract,
    onDefaultMismatch: () => undefined,
    // @ts-expect-error startup drift is always applied and reported; the bypass flag no longer exists
    allowDefaultMismatch: true,
  },
  (): ManagedPreparedCandidate => ({ publish() {} }),
);

const onlySeverity: MismatchSeverity = "error";
// @ts-expect-error the fatal severity was removed together with startup refusal
const fatalSeverity: MismatchSeverity = "fatal";
void [onlySeverity, fatalSeverity, DefaultMismatchError];

export async function verifiesDefaults(client: KmsClient): Promise<string> {
  const wire: VerifyReleaseDefaultsResult = await client.verifyReleaseDefaults({
    namespace: "prod/api",
    entries: [{ alias: "runtime", contentType: "json", sha256: parameterHash("json", "{}") }],
  });
  const result: VerifyResult = await verifyDefaults(
    client,
    { schemaSha256: "0".repeat(64), contract: managedContract, groups: { runtime: "{}" } },
    { namespace: "prod/api" },
  );
  const canonical: Uint8Array = canonicalParameterValue("json", "{}");
  void [wire.passed(), canonical, RateLimitedError];
  return result.passed()
    ? result.report()
    : result
        .failures()
        .map((f) => f.alias)
        .join(",");
}
