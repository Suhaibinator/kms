"use client";

import type { PublicConfigWire, PublicJsonObject } from "@suhaibinator/kms";
import { usePublicConfig } from "@suhaibinator/kms/next/client";

interface PublicPolicy extends PublicJsonObject {
  readonly limit: number;
}

export function PolicyClient({ initial }: { readonly initial: PublicConfigWire<PublicPolicy> }) {
  const policy = usePublicConfig(initial, {
    refreshOnMount: false,
    refreshOnFocus: false,
  });
  return <p>Limit: {policy.config.limit}</p>;
}
