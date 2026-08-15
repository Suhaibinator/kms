import type { Secret } from "../../../src/secret.js";

export interface Config {
  enabled: boolean;
  limit: number;
  epoch: bigint;
  payload: Uint8Array | null;
  labels: Record<string, string> | null;
  endpoint: {
    host: string;
    ports: readonly number[] | null;
    zones: readonly [string, string];
  };
  password: Secret;
}

export interface GroupsOnly {
  name: string;
}

export interface SecretsOnly {
  token: Secret;
}
