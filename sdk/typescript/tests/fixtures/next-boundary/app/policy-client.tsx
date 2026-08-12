"use client";

import type { PublicConfigWire, PublicJsonObject } from "@suhaibinator/kms";
import { usePublicConfig } from "@suhaibinator/kms/next/client";

interface PublicPolicy extends PublicJsonObject {
  readonly limit: number;
}

export function PolicyClient({ initial }: { readonly initial: PublicConfigWire<PublicPolicy> }) {
  const policy = usePublicConfig(initial, {
    endpoint: "/api/policy",
    refreshOnMount: false,
    refreshOnFocus: false,
  });
  return (
    <main>
      <p data-testid="policy-limit">Limit: {policy.config.limit}</p>
      <p data-testid="policy-revision">Revision: {policy.revision.toString(10)}</p>
      <button id="refresh-policy" type="button" onClick={() => void policy.refresh()}>
        Refresh
      </button>
      <button
        id="recover-policy"
        type="button"
        onClick={() =>
          policy.applyServerResult({
            status: "policy_changed",
            current: {
              revision: "18446744073709551615",
              config: { limit: 20 },
            },
          })
        }
      >
        Recover
      </button>
    </main>
  );
}
