import type { Secret } from "../../../src/secret.js";

export interface Config {
  enabled: boolean;
  limit: number;
  epoch: bigint;
  payload: Uint8Array | null;
  labels: Record<string, string> | null;
  endpoint: {
    host: string;
    ports: number[] | null;
  };
  password: Secret;
}
