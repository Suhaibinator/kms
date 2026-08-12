import { definePublicProjection } from "@suhaibinator/kms";
import { createNextKms } from "@suhaibinator/kms/next/server";

interface Policy {
  readonly limit: number;
}

interface PublicPolicy {
  readonly [key: string]: number;
  readonly limit: number;
}

const projection = definePublicProjection<Policy>()({
  limit: (policy) => policy.limit,
});

export const policy = createNextKms<Policy, PublicPolicy, number, readonly string[]>({
  initialize: () => ({
    source: {
      current: () => ({ revision: 9_007_199_254_740_993n, value: { limit: 12 } }),
    },
  }),
  projection,
  validate: (active, input) =>
    input >= active.limit ? { valid: true } : { valid: false, errors: ["below_limit"] },
});

export const SERVER_CREDENTIAL_SENTINEL = "KMS_SERVER_CREDENTIAL_MUST_NOT_REACH_CLIENT";
