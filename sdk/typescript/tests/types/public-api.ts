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
